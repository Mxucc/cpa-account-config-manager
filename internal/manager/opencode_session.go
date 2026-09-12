package manager

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

// OpenCode Go asks clients to send a stable per-conversation identifier in
// x-opencode-session so the gateway can keep routing and prompt caching keyed
// to one conversation. The router derives that identifier from hints the
// caller already carries; when no hint exists it falls back to a salted digest
// of the conversation prefix, so prompt text never leaves the process.
//
// The router deliberately contains no logging calls: session values and the
// salt are private correlation material.
const (
	openCodeSessionHeader   = "x-opencode-session"
	openCodeClientHeader    = "x-opencode-client"
	openCodeSessionSaltFile = "opencode-session-salt"

	openCodeClientHeaderValue       = "cli"
	openCodeSessionValuePrefix      = "oc-"
	openCodeSessionMaxValueBytes    = 128
	openCodeSessionMaxTargets       = 4096
	openCodeSessionMaxDistinct      = 1024
	openCodeSessionSaltBytes        = 32
	openCodeSessionSaltMinBytes     = 16
	openCodeSessionSeedMaxChars     = 4096
	openCodeSessionMaxSaltFileBytes = 256
)

// openCodeNativeSessionHeaders are client session headers that OpenCode Go
// recognizes natively. Translating the first one found keeps routing and prompt
// caching keyed to the same conversation the client already tracks.
var openCodeNativeSessionHeaders = []string{
	"x-session-id",
	"x-claude-session-id",
	"x-claude-code-session-id",
	"session-id",
	"x-codex-session-id",
	"conversation-id",
	"x-conversation-id",
}

var openCodeSessionBodyKeys = []string{"prompt_cache_key", "session_id", "conversation_id"}

// OpenCodeSessionSnapshot is the public, secret-free view of the router.
type OpenCodeSessionSnapshot struct {
	Enabled           bool      `json:"enabled"`
	SaltReady         bool      `json:"salt_ready"`
	TargetModels      []string  `json:"target_models,omitempty"`
	TargetAuthIndexes int       `json:"target_auth_indexes"`
	InjectedRequests  int64     `json:"injected_requests"`
	DistinctSessions  int64     `json:"distinct_sessions"`
	LastInjectedAt    time.Time `json:"last_injected_at,omitempty"`
	// Attribution breakdown: how each injected request was recognised, and why
	// other requests were skipped.
	AttributedByAuthIndex  int64 `json:"attributed_by_auth_index"`
	AttributedByModel      int64 `json:"attributed_by_model"`
	SkippedOtherChannel    int64 `json:"skipped_other_channel"`
	SkippedCodexRequests   int64 `json:"skipped_codex_requests"`
	SkippedUntargetedModel int64 `json:"skipped_untargeted_model"`
}

// OpenCodeSessionRouter injects x-opencode-session into requests that CPA routed
// to an OpenCode channel.
//
// Attribution is deliberately credential-based rather than model-based: Zen
// resells vendor model ids such as gpt-5.5, so a model name alone cannot prove a
// request belongs to OpenCode and injecting there would add OpenCode headers to
// unrelated Codex, Claude or OpenAI traffic. The router therefore only acts when
// the request metadata names a CPA auth index that the plugin recorded for a
// channel whose base URL is an OpenCode gateway.
type OpenCodeSessionRouter struct {
	mu          sync.Mutex
	enabled     bool
	targets     map[string]struct{}
	authIndexes map[string]struct{}
	salt        []byte
	injected    int64
	// Attribution counters explain why a request was or was not given a session
	// header, which is what an operator needs when an upstream reports a missing
	// x-opencode-session.
	attributedByAuthIndex int64
	attributedByModel     int64
	skippedOtherChannel   int64
	skippedCodexRequest   int64
	skippedUntargeted     int64
	distinct              map[[sha256.Size]byte]struct{}
	lastInjected          time.Time
	storageErr            string
}

// NewOpenCodeSessionRouter creates a disabled router without a salt. Configure
// must load or create the salt before requests can be intercepted.
func NewOpenCodeSessionRouter() *OpenCodeSessionRouter {
	return &OpenCodeSessionRouter{
		targets:     map[string]struct{}{},
		authIndexes: map[string]struct{}{},
		distinct:    map[[sha256.Size]byte]struct{}{},
	}
}

// Configure loads the persisted session salt from the normalized data
// directory, or creates it atomically with 0600 permissions. The salt is never
// logged; a failed configure leaves the router without a usable salt so it
// fails closed.
func (r *OpenCodeSessionRouter) Configure(config Config) {
	if r == nil {
		return
	}
	config = normalizeConfig(config)
	saltPath := filepath.Join(config.DataDir, openCodeSessionSaltFile)
	salt, errLoad := loadOrCreateOpenCodeSessionSalt(saltPath)
	r.mu.Lock()
	defer r.mu.Unlock()
	if errLoad != nil {
		r.salt = nil
		r.storageErr = errLoad.Error()
		return
	}
	r.salt = salt
	r.storageErr = ""
}

// SetEnabled toggles injection without touching the persisted salt.
func (r *OpenCodeSessionRouter) SetEnabled(enabled bool) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.enabled = enabled
	r.mu.Unlock()
}

// Enabled reports the configured switch state.
func (r *OpenCodeSessionRouter) Enabled() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.enabled
}

// SetTargets replaces the target model set with normalized, deduplicated model
// identifiers. The set is bounded so a bad configuration cannot grow memory
// without limit.
func (r *OpenCodeSessionRouter) SetTargets(models []string) {
	if r == nil {
		return
	}
	targets := make(map[string]struct{}, len(models))
	for _, model := range models {
		normalized := normalizeOpenCodeSessionModel(model)
		if normalized == "" {
			continue
		}
		if _, exists := targets[normalized]; exists {
			continue
		}
		if len(targets) >= openCodeSessionMaxTargets {
			break
		}
		targets[normalized] = struct{}{}
	}
	r.mu.Lock()
	r.targets = targets
	r.mu.Unlock()
}

// SetAuthIndexes replaces the authoritative allow-list of CPA auth indexes that
// belong to OpenCode channels. Indexes are assigned per channel key entry by CPA
// and can be regenerated when a channel is edited, so the caller refreshes them.
func (r *OpenCodeSessionRouter) SetAuthIndexes(indexes []string) {
	if r == nil {
		return
	}
	allowed := make(map[string]struct{}, len(indexes))
	for _, index := range indexes {
		normalized := normalizeOpenCodeSessionAuthIndex(index)
		if normalized == "" {
			continue
		}
		if len(allowed) >= openCodeSessionMaxTargets {
			break
		}
		allowed[normalized] = struct{}{}
	}
	r.mu.Lock()
	r.authIndexes = allowed
	r.mu.Unlock()
}

// RequestInterceptionActive implements the request transformer gate. Injection
// needs a loaded salt and at least one known OpenCode model; the channel
// allow-list refines attribution but must never disable injection on its own,
// because CPA does not always report an auth index for an API-key channel.
func (r *OpenCodeSessionRouter) RequestInterceptionActive() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.enabled && len(r.salt) > 0 && (len(r.targets) > 0 || len(r.authIndexes) > 0)
}

// RequestInterceptionAcceptsFormat accepts every source format: the header is
// format-agnostic, and targeting already restricts the transform to OpenCode
// Go models.
func (r *OpenCodeSessionRouter) RequestInterceptionAcceptsFormat(string) bool {
	return r != nil
}

// InterceptRequest injects the resolved conversation session when the request is
// attributed to OpenCode and the router is active.
//
// Attribution has two layers. When CPA reports an auth index and the plugin has
// recorded which indexes belong to OpenCode channels, that mapping is
// authoritative. When it does not (older hosts, or a channel the plugin could not
// map), the request is attributed by model: the model must be published by
// OpenCode and the request must not be Codex traffic, which is the only family
// whose model ids overlap with the OpenCode catalog.
func (r *OpenCodeSessionRouter) InterceptRequest(request cpaapi.RequestInterceptRequest) (cpaapi.RequestInterceptResponse, bool) {
	if r == nil {
		return cpaapi.RequestInterceptResponse{}, false
	}
	authIndex := normalizeOpenCodeSessionAuthIndex(openCodeSessionAuthIndexFromMetadata(request.Metadata))
	model := normalizeOpenCodeSessionModel(firstNonEmpty(request.Model, request.RequestedModel))
	r.mu.Lock()
	enabled, salt := r.enabled, r.salt
	_, targeted := r.targets[model]
	_, allowedIndex := r.authIndexes[authIndex]
	channelListKnown := len(r.authIndexes) > 0
	r.mu.Unlock()

	switch {
	case !enabled || len(salt) == 0:
		return cpaapi.RequestInterceptResponse{}, false
	case authIndex != "" && channelListKnown && !allowedIndex:
		// The request provably belongs to a different channel.
		r.recordSkip("other_channel")
		return cpaapi.RequestInterceptResponse{}, false
	case authIndex != "" && channelListKnown && allowedIndex:
		// Authoritative: an OpenCode channel credential was selected.
		r.recordAttribution("auth_index")
	case isCodexFamilyRequest(request):
		r.recordSkip("codex_request")
		return cpaapi.RequestInterceptResponse{}, false
	case !targeted:
		r.recordSkip("untargeted_model")
		return cpaapi.RequestInterceptResponse{}, false
	default:
		r.recordAttribution("model")
	}

	value := resolveOpenCodeSessionValue(request.Headers, request.Body, salt)
	if value == "" {
		return cpaapi.RequestInterceptResponse{}, false
	}

	headers := request.Headers.Clone()
	if headers == nil {
		headers = http.Header{}
	}
	headers.Set(openCodeSessionHeader, value)
	headers.Set(openCodeClientHeader, openCodeClientHeaderValue)
	r.recordInjection(value)
	return cpaapi.RequestInterceptResponse{Headers: headers}, true
}

// Snapshot returns the current public state, including the approximate capped
// distinct-session count.
func (r *OpenCodeSessionRouter) Snapshot() OpenCodeSessionSnapshot {
	if r == nil {
		return OpenCodeSessionSnapshot{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	models := make([]string, 0, len(r.targets))
	for model := range r.targets {
		models = append(models, model)
	}
	sort.Strings(models)
	return OpenCodeSessionSnapshot{
		Enabled:                r.enabled,
		SaltReady:              len(r.salt) > 0,
		TargetModels:           models,
		TargetAuthIndexes:      len(r.authIndexes),
		InjectedRequests:       r.injected,
		DistinctSessions:       int64(len(r.distinct)),
		LastInjectedAt:         r.lastInjected,
		AttributedByAuthIndex:  r.attributedByAuthIndex,
		AttributedByModel:      r.attributedByModel,
		SkippedOtherChannel:    r.skippedOtherChannel,
		SkippedCodexRequests:   r.skippedCodexRequest,
		SkippedUntargetedModel: r.skippedUntargeted,
	}
}

// recordAttribution counts which signal attributed one injected request.
func (r *OpenCodeSessionRouter) recordAttribution(source string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	if source == "auth_index" {
		r.attributedByAuthIndex++
	} else {
		r.attributedByModel++
	}
	r.mu.Unlock()
}

// recordSkip counts why a request was left untouched.
func (r *OpenCodeSessionRouter) recordSkip(reason string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	switch reason {
	case "other_channel":
		r.skippedOtherChannel++
	case "codex_request":
		r.skippedCodexRequest++
	default:
		r.skippedUntargeted++
	}
	r.mu.Unlock()
}

func (r *OpenCodeSessionRouter) recordInjection(value string) {
	digest := sha256.Sum256([]byte(value))
	r.mu.Lock()
	defer r.mu.Unlock()
	r.injected++
	// Remember only digests, never raw session values, and stop counting once
	// the hard cap is reached so memory stays bounded.
	if r.distinct == nil {
		r.distinct = map[[sha256.Size]byte]struct{}{}
	}
	if _, seen := r.distinct[digest]; !seen && len(r.distinct) < openCodeSessionMaxDistinct {
		r.distinct[digest] = struct{}{}
	}
	r.lastInjected = time.Now().UTC()
}

// resolveOpenCodeSessionValue prefers the strongest conversation hint the
// caller already sent and only falls back to a salted digest when nothing
// usable is present.
func resolveOpenCodeSessionValue(headers http.Header, body []byte, salt []byte) string {
	// (a) A client-provided OpenCode session is authoritative; never replace it.
	if value := validOpenCodeSessionValue(headerValue(headers, openCodeSessionHeader)); value != "" {
		return value
	}
	// (b) Translate the first recognized native client session header.
	for _, name := range openCodeNativeSessionHeaders {
		if value := validOpenCodeSessionValue(headerValue(headers, name)); value != "" {
			return value
		}
	}
	// (c) and (d) Inspect the body defensively; non-JSON bodies simply produce
	// no session.
	decoded, errDecode := decodeJSONObjectBody(body)
	if errDecode != nil {
		return ""
	}
	if value := openCodeSessionBodyValue(decoded); value != "" {
		return value
	}
	return openCodeSessionFallbackValue(salt, decoded)
}

// openCodeSessionBodyValue looks for a client session identifier at the JSON
// top level first, then inside a metadata object.
func openCodeSessionBodyValue(decoded map[string]any) string {
	if decoded == nil {
		return ""
	}
	for _, key := range openCodeSessionBodyKeys {
		if value, ok := decoded[key].(string); ok {
			if valid := validOpenCodeSessionValue(value); valid != "" {
				return valid
			}
		}
	}
	metadata, ok := decoded["metadata"].(map[string]any)
	if !ok {
		return ""
	}
	for _, key := range openCodeSessionBodyKeys {
		if value, ok := metadata[key].(string); ok {
			if valid := validOpenCodeSessionValue(value); valid != "" {
				return valid
			}
		}
	}
	return ""
}

// openCodeSessionFallbackValue derives a stable, opaque conversation id with
// HMAC-SHA256 over the router salt and a canonical seed. No message text and
// no salt byte ever leaves the process.
func openCodeSessionFallbackValue(salt []byte, decoded map[string]any) string {
	seed := openCodeSessionSeed(decoded)
	if seed == "" {
		return ""
	}
	mac := hmac.New(sha256.New, salt)
	mac.Write([]byte(seed))
	digest := mac.Sum(nil)
	return openCodeSessionValuePrefix + hex.EncodeToString(digest[:16])
}

// openCodeSessionSeed builds a canonical prefix from the system/instructions
// text and the first user message text. The same conversation prefix therefore
// maps to the same id across turns, while later turns and unrelated content do
// not change the value.
func openCodeSessionSeed(decoded map[string]any) string {
	if decoded == nil {
		return ""
	}
	system := truncateOpenCodeSessionSeed(openCodeSessionSystemText(decoded))
	user := truncateOpenCodeSessionSeed(openCodeSessionUserText(decoded))
	if system == "" && user == "" {
		return ""
	}
	return "system:" + system + "\x00user:" + user
}

func openCodeSessionSystemText(decoded map[string]any) string {
	for _, key := range []string{"system", "instructions"} {
		if value, exists := decoded[key]; exists {
			if text := openCodeMessageText(value); strings.TrimSpace(text) != "" {
				return text
			}
		}
	}
	return openCodeMessagesText(decoded, "system", "developer")
}

func openCodeSessionUserText(decoded map[string]any) string {
	if text := openCodeMessagesText(decoded, "user"); strings.TrimSpace(text) != "" {
		return text
	}
	return openCodeInputText(decoded["input"])
}

// openCodeMessagesText extracts the first message with one of the requested
// roles from an OpenAI/Anthropic-ish messages array.
func openCodeMessagesText(decoded map[string]any, roles ...string) string {
	items, ok := decoded["messages"].([]any)
	if !ok {
		return ""
	}
	for _, item := range items {
		message, ok := item.(map[string]any)
		if !ok {
			continue
		}
		role, _ := message["role"].(string)
		if !openCodeSessionRoleMatches(role, roles) {
			continue
		}
		if text := openCodeMessageText(message["content"]); strings.TrimSpace(text) != "" {
			return text
		}
	}
	return ""
}

// openCodeInputText extracts text from the Responses API input field, which may
// be a plain string, a content object, or a message list.
func openCodeInputText(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case map[string]any:
		return openCodeMessageText(typed["content"])
	case []any:
		for _, item := range typed {
			message, ok := item.(map[string]any)
			if !ok {
				continue
			}
			role, _ := message["role"].(string)
			if role != "" && !strings.EqualFold(strings.TrimSpace(role), "user") {
				continue
			}
			if text := openCodeMessageText(message["content"]); strings.TrimSpace(text) != "" {
				return text
			}
		}
		for _, item := range typed {
			if text := openCodeMessageText(item); strings.TrimSpace(text) != "" {
				return text
			}
		}
	}
	return ""
}

// openCodeMessageText collects text from a message content value while skipping
// non-text parts such as images or tool calls.
func openCodeMessageText(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := openCodeBlockText(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	case map[string]any:
		return openCodeBlockText(typed)
	}
	return ""
}

func openCodeBlockText(value any) string {
	block, ok := value.(map[string]any)
	if !ok {
		text, _ := value.(string)
		return text
	}
	if content, exists := block["content"]; exists {
		if text := openCodeMessageText(content); strings.TrimSpace(text) != "" {
			return text
		}
	}
	kind, _ := block["type"].(string)
	if kind != "" && !strings.Contains(strings.ToLower(kind), "text") {
		return ""
	}
	text, _ := block["text"].(string)
	return text
}

func openCodeSessionRoleMatches(role string, roles []string) bool {
	role = strings.ToLower(strings.TrimSpace(role))
	for _, want := range roles {
		if role == want {
			return true
		}
	}
	return false
}

func truncateOpenCodeSessionSeed(text string) string {
	text = strings.TrimSpace(text)
	if len(text) <= openCodeSessionSeedMaxChars {
		return text
	}
	runes := []rune(text)
	if len(runes) <= openCodeSessionSeedMaxChars {
		return text
	}
	return string(runes[:openCodeSessionSeedMaxChars])
}

// validOpenCodeSessionValue accepts only bounded printable ASCII without
// whitespace, protecting the outbound header from injection and abuse.
func validOpenCodeSessionValue(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" || len(value) > openCodeSessionMaxValueBytes {
		return ""
	}
	for i := 0; i < len(value); i++ {
		if value[i] < 0x21 || value[i] > 0x7e {
			return ""
		}
	}
	return value
}

// openCodeSessionAuthIndexFromMetadata reads the selected-auth fields CPA
// supplies for API-key provider channels. An empty result means the request
// cannot be attributed, and the router must not touch it.
func openCodeSessionAuthIndexFromMetadata(metadata map[string]any) string {
	for _, key := range []string{"selected_auth_index", "selected_auth_id", "auth_index", "auth_id"} {
		if value, ok := metadata[key].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func normalizeOpenCodeSessionAuthIndex(index string) string {
	return strings.ToLower(strings.TrimSpace(index))
}

func normalizeOpenCodeSessionModel(model string) string {
	return strings.ToLower(strings.TrimSpace(model))
}

// loadOrCreateOpenCodeSessionSalt reuses a valid persisted salt and otherwise
// creates one. The salt is stored hex-encoded in a private, atomically written
// file.
func loadOrCreateOpenCodeSessionSalt(path string) ([]byte, error) {
	if salt, ok := loadOpenCodeSessionSalt(path); ok {
		if info, errStat := os.Stat(path); errStat == nil && info.Mode().Perm()&0o077 != 0 {
			_ = os.Chmod(path, 0o600)
		}
		return salt, nil
	}
	salt := make([]byte, openCodeSessionSaltBytes)
	if _, errGenerate := rand.Read(salt); errGenerate != nil {
		return nil, fmt.Errorf("generate opencode session salt: %w", errGenerate)
	}
	if errMkdir := os.MkdirAll(filepath.Dir(path), 0o700); errMkdir != nil {
		return nil, fmt.Errorf("create opencode session directory: %w", errMkdir)
	}
	if errWrite := writePrivateFileAtomically(path, []byte(hex.EncodeToString(salt))); errWrite != nil {
		return nil, fmt.Errorf("persist opencode session salt: %w", errWrite)
	}
	return salt, nil
}

func loadOpenCodeSessionSalt(path string) ([]byte, bool) {
	raw, errRead := os.ReadFile(path)
	if errRead != nil || len(raw) == 0 || len(raw) > openCodeSessionMaxSaltFileBytes {
		return nil, false
	}
	salt, errDecode := hex.DecodeString(strings.TrimSpace(string(raw)))
	if errDecode != nil || len(salt) < openCodeSessionSaltMinBytes {
		return nil, false
	}
	return salt, true
}
