package manager

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The Codex fingerprint used to be a fixed implementation: the derivation
// prefixes, the client identity strings, the window suffix and the extra
// relationship fields were compiled in, and only the convergence mode could be
// changed. This service exposes every one of those values as an editable field
// with a visible default and a per-field reset, so an operator can adapt the
// fingerprint without a new plugin release and can always return to the built-in
// behavior.
//
// A field that is not overridden keeps the compiled default. Overrides are stored
// as plain strings, validated per field, and applied to the request path through
// one atomic snapshot so the hot path never takes a lock.

const (
	codexFingerprintProfileStoreFile = "codex-fingerprint-profile.json"
	codexFingerprintProfileVersion   = 1

	codexFingerprintGroupClient     = "client"
	codexFingerprintGroupIdentity   = "identity"
	codexFingerprintGroupDerivation = "derivation"
	codexFingerprintGroupBody       = "body"

	codexFingerprintKindText   = "text"
	codexFingerprintKindSelect = "select"
	codexFingerprintKindBool   = "bool"

	codexFingerprintMaxValueLength = 512

	codexFingerprintSeedStrategyPerAccount = "per_account"
	codexFingerprintSeedStrategyFixed      = "fixed"
)

// Field keys. They are stable identifiers used by the UI and the persisted store.
const (
	codexFingerprintFieldMode                      = "mode"
	codexFingerprintFieldUserAgent                 = "user_agent"
	codexFingerprintFieldOriginator                = "originator"
	codexFingerprintFieldVersion                   = "version"
	codexFingerprintFieldOpenAIBeta                = "openai_beta"
	codexFingerprintFieldTurnMetadataHeader        = "turn_metadata_header"
	codexFingerprintFieldInstallationID            = "installation_id"
	codexFingerprintFieldSessionID                 = "session_id"
	codexFingerprintFieldThreadID                  = "thread_id"
	codexFingerprintFieldWindowSuffix              = "window_suffix"
	codexFingerprintFieldInstallPrefix             = "install_prefix"
	codexFingerprintFieldSessionPrefix             = "session_prefix"
	codexFingerprintFieldThreadPrefix              = "thread_prefix"
	codexFingerprintFieldSeedStrategy              = "seed_strategy"
	codexFingerprintFieldFixedSeed                 = "fixed_seed"
	codexFingerprintFieldIncludeTurnStartedAt      = "include_turn_started_at"
	codexFingerprintFieldIncludeRelationshipFields = "include_relationship_fields"
	codexFingerprintFieldRewritePromptCacheKey     = "rewrite_prompt_cache_key"
)

// Compiled defaults. These are the values the implementation used before the
// profile existed, so an untouched profile behaves exactly as before.
const (
	defaultCodexOriginator    = "codex-tui"
	defaultCodexOpenAIBeta    = "responses=experimental"
	defaultCodexWindowSuffix  = ":0"
	defaultCodexInstallPrefix = "sub2api:codex-install-id:v2:"
	defaultCodexSessionPrefix = "sub2api:codex-session-id:v2:"
	defaultCodexThreadPrefix  = "sub2api:codex-thread-id:v2:"
)

// CodexFingerprintField is one editable fingerprint value with its default.
type CodexFingerprintField struct {
	Key        string   `json:"key"`
	Group      string   `json:"group"`
	Kind       string   `json:"kind"`
	Default    string   `json:"default"`
	Value      string   `json:"value"`
	Overridden bool     `json:"overridden"`
	Options    []string `json:"options,omitempty"`
}

// CodexFingerprintProfile is the redacted snapshot exposed to the UI.
type CodexFingerprintProfile struct {
	Fields           []CodexFingerprintField `json:"fields"`
	OverriddenFields int                     `json:"overridden_fields"`
	UpdatedAt        time.Time               `json:"updated_at,omitempty"`
	StorageError     string                  `json:"storage_error,omitempty"`
}

// codexFingerprintValues is the resolved, hot-path copy of the profile. It holds
// only effective values, so request handling never parses or validates again.
type codexFingerprintValues struct {
	mode                      string
	userAgent                 string
	originator                string
	version                   string
	openAIBeta                string
	turnMetadataHeader        string
	installationID            string
	sessionID                 string
	threadID                  string
	windowSuffix              string
	installPrefix             string
	sessionPrefix             string
	threadPrefix              string
	seedStrategy              string
	fixedSeed                 string
	includeTurnStartedAt      bool
	includeRelationshipFields bool
	rewritePromptCacheKey     bool
}

func defaultCodexFingerprintValues() codexFingerprintValues {
	return codexFingerprintValues{
		mode:                      string(codexFingerprintOff),
		userAgent:                 defaultCodexCLIUserAgent,
		originator:                defaultCodexOriginator,
		version:                   codexCLIVersion,
		openAIBeta:                defaultCodexOpenAIBeta,
		turnMetadataHeader:        codexFingerprintHeader,
		installationID:            "",
		sessionID:                 "",
		threadID:                  "",
		windowSuffix:              defaultCodexWindowSuffix,
		installPrefix:             defaultCodexInstallPrefix,
		sessionPrefix:             defaultCodexSessionPrefix,
		threadPrefix:              defaultCodexThreadPrefix,
		seedStrategy:              codexFingerprintSeedStrategyPerAccount,
		fixedSeed:                 "",
		includeTurnStartedAt:      true,
		includeRelationshipFields: true,
		rewritePromptCacheKey:     true,
	}
}

// activeCodexFingerprint carries the effective profile to the free functions that
// build the fingerprint, so they stay callable without threading a parameter
// through every caller. It always holds a complete value set.
var activeCodexFingerprint atomic.Pointer[codexFingerprintValues]

func storeCodexFingerprintValues(values codexFingerprintValues) {
	copied := values
	activeCodexFingerprint.Store(&copied)
}

// codexProfile returns the effective profile, falling back to the compiled
// defaults when the service was never constructed (tests, probes).
func codexProfile() codexFingerprintValues {
	if values := activeCodexFingerprint.Load(); values != nil {
		return *values
	}
	return defaultCodexFingerprintValues()
}

func init() {
	defaults := defaultCodexFingerprintValues()
	storeCodexFingerprintValues(defaults)
}

// codexFingerprintFieldSpec describes one field for validation and presentation.
type codexFingerprintFieldSpec struct {
	Key     string
	Group   string
	Kind    string
	Default string
	Options []string
	// validate rejects a non-empty operator value that would produce an invalid
	// fingerprint. An empty value always clears the override.
	validate func(value string) error
}

func validateCodexFingerprintText(value string) error {
	if len(value) > codexFingerprintMaxValueLength {
		return fmt.Errorf("value is too long")
	}
	for _, symbol := range value {
		if symbol < 0x20 || symbol == 0x7f {
			return fmt.Errorf("value must not contain control characters")
		}
	}
	return nil
}

func validateCodexFingerprintUUID(value string) error {
	if err := validateCodexFingerprintText(value); err != nil {
		return err
	}
	if value == "" {
		return nil
	}
	if !isCanonicalUUIDString(strings.ToLower(value)) {
		return fmt.Errorf("value must be a canonical UUID")
	}
	return nil
}

func validateCodexFingerprintPrefix(value string) error {
	if err := validateCodexFingerprintText(value); err != nil {
		return err
	}
	if value == "" {
		return nil
	}
	if strings.TrimSpace(value) != value {
		return fmt.Errorf("value must not start or end with whitespace")
	}
	return nil
}

func validateCodexFingerprintHeaderName(value string) error {
	if err := validateCodexFingerprintText(value); err != nil {
		return err
	}
	if value == "" {
		return nil
	}
	if strings.ContainsAny(value, " \t:,") {
		return fmt.Errorf("value must be a bare header name")
	}
	return nil
}

// codexFingerprintFieldSpecs is the full schema, in display order.
func codexFingerprintFieldSpecs() []codexFingerprintFieldSpec {
	return []codexFingerprintFieldSpec{
		{Key: codexFingerprintFieldMode, Group: codexFingerprintGroupClient, Kind: codexFingerprintKindSelect,
			Default: string(codexFingerprintOff),
			Options: []string{"off", "device", "session", "full"},
			validate: func(value string) error {
				if value == "" || validCodexFingerprintMode(value) {
					return nil
				}
				return fmt.Errorf("mode must be off, device, session or full")
			}},
		{Key: codexFingerprintFieldUserAgent, Group: codexFingerprintGroupClient, Kind: codexFingerprintKindText,
			Default: defaultCodexCLIUserAgent, validate: validateCodexFingerprintText},
		{Key: codexFingerprintFieldOriginator, Group: codexFingerprintGroupClient, Kind: codexFingerprintKindText,
			Default: defaultCodexOriginator, validate: validateCodexFingerprintText},
		{Key: codexFingerprintFieldVersion, Group: codexFingerprintGroupClient, Kind: codexFingerprintKindText,
			Default: codexCLIVersion, validate: validateCodexFingerprintText},
		{Key: codexFingerprintFieldOpenAIBeta, Group: codexFingerprintGroupClient, Kind: codexFingerprintKindText,
			Default: defaultCodexOpenAIBeta, validate: validateCodexFingerprintText},
		{Key: codexFingerprintFieldTurnMetadataHeader, Group: codexFingerprintGroupClient, Kind: codexFingerprintKindText,
			Default: codexFingerprintHeader, validate: validateCodexFingerprintHeaderName},
		{Key: codexFingerprintFieldInstallationID, Group: codexFingerprintGroupIdentity, Kind: codexFingerprintKindText,
			Default: "", validate: validateCodexFingerprintUUID},
		{Key: codexFingerprintFieldSessionID, Group: codexFingerprintGroupIdentity, Kind: codexFingerprintKindText,
			Default: "", validate: validateCodexFingerprintUUID},
		{Key: codexFingerprintFieldThreadID, Group: codexFingerprintGroupIdentity, Kind: codexFingerprintKindText,
			Default: "", validate: validateCodexFingerprintUUID},
		{Key: codexFingerprintFieldWindowSuffix, Group: codexFingerprintGroupIdentity, Kind: codexFingerprintKindText,
			Default: defaultCodexWindowSuffix, validate: validateCodexFingerprintText},
		{Key: codexFingerprintFieldInstallPrefix, Group: codexFingerprintGroupDerivation, Kind: codexFingerprintKindText,
			Default: defaultCodexInstallPrefix, validate: validateCodexFingerprintPrefix},
		{Key: codexFingerprintFieldSessionPrefix, Group: codexFingerprintGroupDerivation, Kind: codexFingerprintKindText,
			Default: defaultCodexSessionPrefix, validate: validateCodexFingerprintPrefix},
		{Key: codexFingerprintFieldThreadPrefix, Group: codexFingerprintGroupDerivation, Kind: codexFingerprintKindText,
			Default: defaultCodexThreadPrefix, validate: validateCodexFingerprintPrefix},
		{Key: codexFingerprintFieldSeedStrategy, Group: codexFingerprintGroupDerivation, Kind: codexFingerprintKindSelect,
			Default: codexFingerprintSeedStrategyPerAccount,
			Options: []string{codexFingerprintSeedStrategyPerAccount, codexFingerprintSeedStrategyFixed},
			validate: func(value string) error {
				switch value {
				case "", codexFingerprintSeedStrategyPerAccount, codexFingerprintSeedStrategyFixed:
					return nil
				}
				return fmt.Errorf("seed strategy must be per_account or fixed")
			}},
		{Key: codexFingerprintFieldFixedSeed, Group: codexFingerprintGroupDerivation, Kind: codexFingerprintKindText,
			Default: "", validate: validateCodexFingerprintText},
		{Key: codexFingerprintFieldIncludeTurnStartedAt, Group: codexFingerprintGroupBody, Kind: codexFingerprintKindBool,
			Default: "true", validate: validateCodexFingerprintBool},
		{Key: codexFingerprintFieldIncludeRelationshipFields, Group: codexFingerprintGroupBody, Kind: codexFingerprintKindBool,
			Default: "true", validate: validateCodexFingerprintBool},
		{Key: codexFingerprintFieldRewritePromptCacheKey, Group: codexFingerprintGroupBody, Kind: codexFingerprintKindBool,
			Default: "true", validate: validateCodexFingerprintBool},
	}
}

func validateCodexFingerprintBool(value string) error {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "true", "false":
		return nil
	}
	return fmt.Errorf("value must be true or false")
}

func codexFingerprintBoolValue(value string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true":
		return true
	case "false":
		return false
	}
	return fallback
}

// CodexFingerprintProfileService persists operator overrides for the schema.
type CodexFingerprintProfileService struct {
	mu         sync.RWMutex
	store      string
	loaded     bool
	values     map[string]string
	updatedAt  time.Time
	storageErr string
}

func NewCodexFingerprintProfileService() *CodexFingerprintProfileService {
	return &CodexFingerprintProfileService{values: map[string]string{}}
}

func codexFingerprintProfileStorePath(dataDir string) string {
	return filepath.Join(dataDir, codexFingerprintProfileStoreFile)
}

func (s *CodexFingerprintProfileService) Configure(config Config) {
	if s == nil {
		return
	}
	path := codexFingerprintProfileStorePath(normalizeConfig(config).DataDir)
	s.mu.Lock()
	if s.loaded && s.store == path {
		s.mu.Unlock()
		return
	}
	s.store, s.loaded = path, true
	values := map[string]string{}
	updatedAt := time.Time{}
	storageErr := ""
	raw, errRead := os.ReadFile(path)
	switch {
	case errRead == nil:
		persisted, errDecode := decodeCodexFingerprintProfile(raw)
		if errDecode != nil {
			storageErr = "Codex fingerprint profile could not be read"
		} else {
			values, updatedAt = persisted.Values, persisted.UpdatedAt
		}
	case errors.Is(errRead, os.ErrNotExist):
		// No overrides yet: the compiled defaults apply.
	default:
		storageErr = "Codex fingerprint profile could not be read"
	}
	normalized, errNormalize := normalizeCodexFingerprintOverrides(values)
	if errNormalize != nil {
		// A stored value that no longer validates is dropped rather than applied.
		storageErr = "Codex fingerprint profile had an invalid value"
		normalized = map[string]string{}
	}
	s.values = normalized
	s.updatedAt = updatedAt
	s.storageErr = storageErr
	s.mu.Unlock()
	s.publish()
}

// publish rebuilds the hot-path snapshot from the current overrides.
func (s *CodexFingerprintProfileService) publish() {
	if s == nil {
		return
	}
	s.mu.RLock()
	values := make(map[string]string, len(s.values))
	for key, value := range s.values {
		values[key] = value
	}
	s.mu.RUnlock()
	storeCodexFingerprintValues(resolveCodexFingerprintValues(values))
}

// resolveCodexFingerprintValues merges overrides onto the compiled defaults.
func resolveCodexFingerprintValues(overrides map[string]string) codexFingerprintValues {
	resolved := defaultCodexFingerprintValues()
	lookup := func(key, fallback string) string {
		if value, ok := overrides[key]; ok && strings.TrimSpace(value) != "" {
			return value
		}
		return fallback
	}
	resolved.mode = string(effectiveCodexFingerprintMode(lookup(codexFingerprintFieldMode, resolved.mode)))
	resolved.userAgent = lookup(codexFingerprintFieldUserAgent, resolved.userAgent)
	resolved.originator = lookup(codexFingerprintFieldOriginator, resolved.originator)
	resolved.version = lookup(codexFingerprintFieldVersion, resolved.version)
	resolved.openAIBeta = lookup(codexFingerprintFieldOpenAIBeta, resolved.openAIBeta)
	resolved.turnMetadataHeader = lookup(codexFingerprintFieldTurnMetadataHeader, resolved.turnMetadataHeader)
	resolved.installationID = lookup(codexFingerprintFieldInstallationID, "")
	resolved.sessionID = lookup(codexFingerprintFieldSessionID, "")
	resolved.threadID = lookup(codexFingerprintFieldThreadID, "")
	resolved.windowSuffix = lookup(codexFingerprintFieldWindowSuffix, resolved.windowSuffix)
	resolved.installPrefix = lookup(codexFingerprintFieldInstallPrefix, resolved.installPrefix)
	resolved.sessionPrefix = lookup(codexFingerprintFieldSessionPrefix, resolved.sessionPrefix)
	resolved.threadPrefix = lookup(codexFingerprintFieldThreadPrefix, resolved.threadPrefix)
	resolved.seedStrategy = lookup(codexFingerprintFieldSeedStrategy, resolved.seedStrategy)
	resolved.fixedSeed = lookup(codexFingerprintFieldFixedSeed, resolved.fixedSeed)
	resolved.includeTurnStartedAt = codexFingerprintBoolValue(overrides[codexFingerprintFieldIncludeTurnStartedAt], resolved.includeTurnStartedAt)
	resolved.includeRelationshipFields = codexFingerprintBoolValue(overrides[codexFingerprintFieldIncludeRelationshipFields], resolved.includeRelationshipFields)
	resolved.rewritePromptCacheKey = codexFingerprintBoolValue(overrides[codexFingerprintFieldRewritePromptCacheKey], resolved.rewritePromptCacheKey)
	return resolved
}

func normalizeCodexFingerprintOverrides(values map[string]string) (map[string]string, error) {
	normalized := make(map[string]string, len(values))
	for _, spec := range codexFingerprintFieldSpecs() {
		raw, ok := values[spec.Key]
		if !ok {
			continue
		}
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		if spec.kind() == codexFingerprintKindBool {
			value = strings.ToLower(value)
		}
		if errValidate := spec.validate(value); errValidate != nil {
			return nil, fmt.Errorf("%s: %w", spec.Key, errValidate)
		}
		normalized[spec.Key] = value
	}
	return normalized, nil
}

func (s codexFingerprintFieldSpec) kind() string {
	return s.Kind
}

// Snapshot reports every field with its default and effective value.
func (s *CodexFingerprintProfileService) Snapshot() CodexFingerprintProfile {
	profile := CodexFingerprintProfile{Fields: make([]CodexFingerprintField, 0, len(codexFingerprintFieldSpecs()))}
	if s == nil {
		for _, spec := range codexFingerprintFieldSpecs() {
			profile.Fields = append(profile.Fields, CodexFingerprintField{
				Key: spec.Key, Group: spec.Group, Kind: spec.Kind, Default: spec.Default, Value: spec.Default, Options: spec.Options,
			})
		}
		return profile
	}
	s.mu.RLock()
	overrides := make(map[string]string, len(s.values))
	for key, value := range s.values {
		overrides[key] = value
	}
	profile.UpdatedAt = s.updatedAt
	profile.StorageError = s.storageErr
	s.mu.RUnlock()
	for _, spec := range codexFingerprintFieldSpecs() {
		field := CodexFingerprintField{
			Key: spec.Key, Group: spec.Group, Kind: spec.Kind, Default: spec.Default, Value: spec.Default, Options: spec.Options,
		}
		if value, ok := overrides[spec.Key]; ok && value != "" {
			field.Value = value
			field.Overridden = true
			profile.OverriddenFields++
		}
		profile.Fields = append(profile.Fields, field)
	}
	return profile
}

// Set applies the supplied values. An empty value clears that field, so the
// operator can return a single field to its default without a separate call.
func (s *CodexFingerprintProfileService) Set(values map[string]string) (CodexFingerprintProfile, error) {
	if s == nil {
		return CodexFingerprintProfile{}, ErrCodexFingerprintStorageUnavailable
	}
	s.mu.Lock()
	if strings.TrimSpace(s.store) == "" {
		s.mu.Unlock()
		return CodexFingerprintProfile{}, ErrCodexFingerprintStorageUnavailable
	}
	next := make(map[string]string, len(s.values)+len(values))
	for key, value := range s.values {
		next[key] = value
	}
	for key, raw := range values {
		spec, ok := codexFingerprintSpecFor(key)
		if !ok {
			s.mu.Unlock()
			return CodexFingerprintProfile{}, fmt.Errorf("unknown fingerprint field %q", key)
		}
		value := strings.TrimSpace(raw)
		if value == "" {
			delete(next, key)
			continue
		}
		if spec.Kind == codexFingerprintKindBool {
			value = strings.ToLower(value)
		}
		if errValidate := spec.validate(value); errValidate != nil {
			s.mu.Unlock()
			return CodexFingerprintProfile{}, fmt.Errorf("%s: %w", key, errValidate)
		}
		next[key] = value
	}
	s.values = next
	s.updatedAt = time.Now().UTC()
	errPersist := s.persistLocked()
	s.mu.Unlock()
	s.publish()
	if errPersist != nil {
		return s.Snapshot(), errPersist
	}
	return s.Snapshot(), nil
}

// Reset clears the listed fields, or every field when the list is empty.
func (s *CodexFingerprintProfileService) Reset(keys []string) (CodexFingerprintProfile, error) {
	if s == nil {
		return CodexFingerprintProfile{}, ErrCodexFingerprintStorageUnavailable
	}
	s.mu.Lock()
	if strings.TrimSpace(s.store) == "" {
		s.mu.Unlock()
		return CodexFingerprintProfile{}, ErrCodexFingerprintStorageUnavailable
	}
	if len(keys) == 0 {
		s.values = map[string]string{}
	} else {
		for _, key := range keys {
			key = strings.TrimSpace(key)
			if key == "" {
				continue
			}
			if _, ok := codexFingerprintSpecFor(key); !ok {
				s.mu.Unlock()
				return CodexFingerprintProfile{}, fmt.Errorf("unknown fingerprint field %q", key)
			}
			delete(s.values, key)
		}
	}
	s.updatedAt = time.Now().UTC()
	errPersist := s.persistLocked()
	s.mu.Unlock()
	s.publish()
	if errPersist != nil {
		return s.Snapshot(), errPersist
	}
	return s.Snapshot(), nil
}

// OverriddenFields reports how many fields differ from their default.
func (s *CodexFingerprintProfileService) OverriddenFields() int {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.values)
}

// EffectiveValues exposes the resolved profile for previews and tests.
func (s *CodexFingerprintProfileService) EffectiveValues() codexFingerprintValues {
	if s == nil {
		return defaultCodexFingerprintValues()
	}
	s.mu.RLock()
	overrides := make(map[string]string, len(s.values))
	for key, value := range s.values {
		overrides[key] = value
	}
	s.mu.RUnlock()
	return resolveCodexFingerprintValues(overrides)
}

func codexFingerprintSpecFor(key string) (codexFingerprintFieldSpec, bool) {
	for _, spec := range codexFingerprintFieldSpecs() {
		if spec.Key == key {
			return spec, true
		}
	}
	return codexFingerprintFieldSpec{}, false
}

type persistedCodexFingerprintProfile struct {
	Version   int               `json:"version"`
	UpdatedAt time.Time         `json:"updated_at,omitempty"`
	Values    map[string]string `json:"values,omitempty"`
}

func decodeCodexFingerprintProfile(raw []byte) (persistedCodexFingerprintProfile, error) {
	var persisted persistedCodexFingerprintProfile
	if errDecode := json.Unmarshal(raw, &persisted); errDecode != nil {
		return persistedCodexFingerprintProfile{}, errDecode
	}
	if persisted.Version != codexFingerprintProfileVersion {
		return persistedCodexFingerprintProfile{}, fmt.Errorf("unsupported Codex fingerprint profile version")
	}
	return persisted, nil
}

func (s *CodexFingerprintProfileService) persistLocked() error {
	persisted := persistedCodexFingerprintProfile{
		Version:   codexFingerprintProfileVersion,
		UpdatedAt: s.updatedAt,
		Values:    s.values,
	}
	encoded, errEncode := json.Marshal(persisted)
	if errEncode != nil {
		return errEncode
	}
	if errMkdir := os.MkdirAll(filepath.Dir(s.store), 0o700); errMkdir != nil {
		s.storageErr = "Codex fingerprint profile could not be saved"
		return errMkdir
	}
	if errWrite := writePrivateFileAtomically(s.store, encoded); errWrite != nil {
		s.storageErr = "Codex fingerprint profile could not be saved"
		return errWrite
	}
	s.storageErr = ""
	return nil
}

// ErrCodexFingerprintStorageUnavailable reports that the profile has no store.
var ErrCodexFingerprintStorageUnavailable = errors.New("Codex fingerprint storage is unavailable")

// CodexFingerprintUserAgent returns the client identity strings the outbound
// path must use.
func CodexFingerprintUserAgent() string  { return codexProfile().userAgent }
func CodexFingerprintOriginator() string { return codexProfile().originator }
func CodexFingerprintVersion() string    { return codexProfile().version }
func CodexFingerprintOpenAIBeta() string { return codexProfile().openAIBeta }
func codexFingerprintMetadataHeader() string {
	header := strings.TrimSpace(codexProfile().turnMetadataHeader)
	if header == "" {
		return codexFingerprintHeader
	}
	return header
}

// CodexFingerprintMetadataHeaderName is the configured turn-metadata header name.
func CodexFingerprintMetadataHeaderName() string { return codexFingerprintMetadataHeader() }
