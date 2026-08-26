package manager

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

// The convergence modes intentionally mirror the reference sub2api account
// setting. Convergence is explicit opt-in: unset, empty, and invalid values
// fail closed to passthrough; only device/session/full converge identity.
type codexFingerprintMode string

const (
	codexFingerprintOff     codexFingerprintMode = "off"
	codexFingerprintDevice  codexFingerprintMode = "device"
	codexFingerprintSession codexFingerprintMode = "session"
	codexFingerprintFull    codexFingerprintMode = "full"
)

const (
	codexFingerprintSeedExtraKey = "codex_fingerprint_seed"
	codexFingerprintHeader       = "X-Codex-Turn-Metadata"
)

type codexFingerprintIDs struct {
	mode                          codexFingerprintMode
	installationID                string
	sessionID                     string
	threadID                      string
	turnID                        string
	windowID                      string
	turnStartedAtUnixMs           int64
	originalBodySessionID         string
	originalBodySessionIDCaptured bool
}

func normalizeCodexFingerprintMode(value string) codexFingerprintMode {
	switch codexFingerprintMode(strings.ToLower(strings.TrimSpace(value))) {
	case codexFingerprintOff:
		return codexFingerprintOff
	case codexFingerprintDevice:
		return codexFingerprintDevice
	case codexFingerprintSession:
		return codexFingerprintSession
	case codexFingerprintFull:
		return codexFingerprintFull
	default:
		return ""
	}
}

func validCodexFingerprintMode(value string) bool {
	return normalizeCodexFingerprintMode(value) != "" || strings.TrimSpace(value) == ""
}

func effectiveCodexFingerprintMode(value string) codexFingerprintMode {
	if mode := normalizeCodexFingerprintMode(value); mode != "" {
		return mode
	}
	return codexFingerprintOff
}

func deriveStableUUIDv4(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	b := sum[:16]
	b[6] = (b[6] & 0x0f) | 0x40 // UUID version 4.
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant.
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		binary.BigEndian.Uint32(b[0:4]),
		binary.BigEndian.Uint16(b[4:6]),
		binary.BigEndian.Uint16(b[6:8]),
		binary.BigEndian.Uint16(b[8:10]),
		b[10:16])
}

func isCanonicalUUIDString(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, char := range value {
		isHex := (char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')
		if !isHex {
			switch index {
			case 8, 13, 18, 23:
				if char != '-' {
					return false
				}
			default:
				return false
			}
		}
	}
	if value[14] != '4' || value[19] != '8' && value[19] != '9' &&
		value[19] != 'a' && value[19] != 'b' {
		return false
	}
	return true
}

// resolveConvergedInstallationID returns the account-stable device value.
func resolveConvergedInstallationID(account Account, seed string) string {
	if deviceID := codexAccountDeviceID(account); deviceID != "" {
		return deviceID
	}
	if seed == "" {
		return ""
	}
	return deriveStableUUIDv4("sub2api:codex-install-id:v2:" + seed)
}

// codexAccountDeviceID returns an administrator-configured physical auth field.
// It intentionally reads only a UUID-shaped value and treats credential fields
// as unavailable to fingerprint derivation.
func codexAccountDeviceID(account Account) string {
	raw := account.DeviceID
	deviceID := strings.TrimSpace(raw)
	if deviceID == "" || !isCanonicalUUIDString(strings.ToLower(deviceID)) ||
		strings.EqualFold(deviceID, "00000000-0000-0000-0000-000000000000") {
		return ""
	}
	return strings.ToLower(deviceID)
}

func resolveConvergedSessionID(seed string) string {
	if seed == "" {
		return ""
	}
	return deriveStableUUIDv4("sub2api:codex-session-id:v2:" + seed)
}

func resolveConvergedThreadID(seed, clientSessionID string) string {
	if seed == "" || clientSessionID == "" {
		return ""
	}
	return deriveStableUUIDv4("sub2api:codex-thread-id:v2:" + seed + ":" + clientSessionID)
}

// resolveCodexFingerprintIDs computes one shared ID set for headers and body.
// turn_id is random, so callers must call this once per request attempt.
func resolveCodexFingerprintIDs(account Account, seed string, clientSessionID string, mode codexFingerprintMode) *codexFingerprintIDs {
	if seed == "" || mode == codexFingerprintOff {
		return nil
	}
	ids := &codexFingerprintIDs{
		mode:                mode,
		installationID:      resolveConvergedInstallationID(account, seed),
		turnStartedAtUnixMs: time.Now().UnixMilli(),
	}
	if ids.installationID == "" {
		return nil
	}
	if mode != codexFingerprintDevice && mode != codexFingerprintSession && mode != codexFingerprintFull {
		return nil
	}
	if mode == codexFingerprintDevice {
		return ids
	}
	ids.sessionID = resolveConvergedSessionID(seed)
	if mode == codexFingerprintSession && clientSessionID != "" {
		ids.threadID = resolveConvergedThreadID(seed, clientSessionID)
	} else {
		ids.threadID = ids.sessionID
	}
	ids.turnID = newTimeUUIDv7(ids.turnStartedAtUnixMs)
	ids.windowID = ids.threadID + ":0"
	return ids
}

// newTimeUUIDv7 produces the timestamp-prefixed shape used by Codex clients.
func newTimeUUIDv7(unixMilli int64) string {
	var b [16]byte
	binary.BigEndian.PutUint64(b[0:8], uint64(unixMilli))
	if _, err := rand.Read(b[8:]); err != nil {
		for index := range b[8:] {
			b[8+index] = byte(index*31 + int(unixMilli))
		}
	}
	b[6] = (b[6] & 0x0f) | 0x70
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		binary.BigEndian.Uint32(b[0:4]),
		binary.BigEndian.Uint16(b[4:6]),
		binary.BigEndian.Uint16(b[6:8]),
		binary.BigEndian.Uint16(b[8:10]),
		b[10:16])
}

func extractClientSessionID(h http.Header) string {
	if h == nil {
		return ""
	}
	if value := strings.TrimSpace(h.Get("session-id")); value != "" {
		return value
	}
	return strings.TrimSpace(h.Get("session_id"))
}

func applyCodexFingerprintHeaders(h http.Header, ids *codexFingerprintIDs) {
	if h == nil || ids == nil || ids.installationID == "" {
		return
	}
	h.Set("X-Codex-Installation-Id", ids.installationID)
	if ids.mode == codexFingerprintDevice {
		rewriteCodexTurnMetadataFields(h, map[string]any{"installation_id": ids.installationID})
		return
	}
	h.Set("X-Codex-Window-Id", ids.windowID)
	h.Set("X-Client-Request-Id", ids.threadID)
	h.Set("Session-Id", ids.sessionID)
	h.Set("Session_Id", ids.sessionID)
	h.Set("Thread-Id", ids.threadID)
	rewriteCodexTurnMetadataFields(h, map[string]any{
		"installation_id":         ids.installationID,
		"session_id":              ids.sessionID,
		"thread_id":               ids.threadID,
		"turn_id":                 ids.turnID,
		"window_id":               ids.windowID,
		"turn_started_at_unix_ms": ids.turnStartedAtUnixMs,
	})
}

func rewriteCodexTurnMetadataFields(h http.Header, fields map[string]any) {
	if h == nil {
		return
	}
	raw := strings.TrimSpace(h.Get(codexFingerprintHeader))
	metadata := make(map[string]any)
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &metadata)
	}
	for key, value := range fields {
		metadata[key] = value
	}
	rebuilt, errMarshal := json.Marshal(metadata)
	if errMarshal != nil {
		return
	}
	h.Set(codexFingerprintHeader, string(rebuilt))
}

// applyCodexFingerprintClientMetadata rewrites only the identity-bearing object
// and returns true when the encoded request must be replaced.
func applyCodexFingerprintClientMetadata(reqBody map[string]any, ids *codexFingerprintIDs) bool {
	if reqBody == nil || ids == nil {
		return false
	}
	captureOriginalBodySessionID(ids, reqBody["client_metadata"])
	existing, _ := reqBody["client_metadata"].(map[string]any)
	if existing == nil {
		existing = make(map[string]any)
	}
	modified := applyCodexFingerprintToClientMetadataMap(existing, ids)
	reqBody["client_metadata"] = existing
	if applyCodexFingerprintPromptCacheKey(reqBody, ids) {
		modified = true
	}
	return modified
}

func applyCodexFingerprintToClientMetadataMap(existing map[string]any, ids *codexFingerprintIDs) bool {
	if existing == nil || ids == nil || ids.installationID == "" {
		return false
	}
	existing["x-codex-installation-id"] = ids.installationID
	if ids.mode == codexFingerprintDevice {
		rewriteEmbeddedTurnMetadata(existing, map[string]any{"installation_id": ids.installationID})
		return true
	}
	existing["session_id"] = ids.sessionID
	existing["thread_id"] = ids.threadID
	existing["turn_id"] = ids.turnID
	existing["x-codex-window-id"] = ids.windowID
	rewriteEmbeddedTurnMetadata(existing, map[string]any{
		"installation_id":         ids.installationID,
		"session_id":              ids.sessionID,
		"thread_id":               ids.threadID,
		"turn_id":                 ids.turnID,
		"window_id":               ids.windowID,
		"turn_started_at_unix_ms": ids.turnStartedAtUnixMs,
	})
	return true
}

func rewriteEmbeddedTurnMetadata(clientMetadata map[string]any, fields map[string]any) {
	raw, ok := clientMetadata[codexFingerprintHeader].(string)
	if !ok || raw == "" {
		return
	}
	metadata := make(map[string]any)
	if err := json.Unmarshal([]byte(raw), &metadata); err != nil || metadata == nil {
		metadata = make(map[string]any)
	}
	for key, value := range fields {
		metadata[key] = value
	}
	if rebuilt, errMarshal := json.Marshal(metadata); errMarshal == nil {
		clientMetadata[codexFingerprintHeader] = string(rebuilt)
	}
}

func captureOriginalBodySessionID(ids *codexFingerprintIDs, value any) {
	if ids == nil || ids.originalBodySessionIDCaptured {
		return
	}
	ids.originalBodySessionIDCaptured = true
	metadata, ok := value.(map[string]any)
	if !ok {
		return
	}
	if sessionID, ok := metadata["session_id"].(string); ok {
		ids.originalBodySessionID = strings.TrimSpace(sessionID)
	}
}

func applyCodexFingerprintPromptCacheKey(reqBody map[string]any, ids *codexFingerprintIDs) bool {
	if reqBody == nil || ids == nil || ids.sessionID == "" ||
		(ids.mode != codexFingerprintSession && ids.mode != codexFingerprintFull) ||
		!ids.originalBodySessionIDCaptured || ids.originalBodySessionID == "" {
		return false
	}
	promptCacheKey, ok := reqBody["prompt_cache_key"].(string)
	if !ok || promptCacheKey != ids.originalBodySessionID || promptCacheKey == ids.sessionID {
		return false
	}
	reqBody["prompt_cache_key"] = ids.sessionID
	return true
}

// ensureCodexFingerprintSeed atomically creates the system-managed per-account
// seed when convergence is enabled. Seed material is credential-adjacent and is
// never logged or returned through public API models.
type fingerprintAccountStore interface {
	CurrentAuthDocument(context.Context, Account) (currentAuthDocument, error)
	SaveAuth(ctx context.Context, name string, rawJSON json.RawMessage) (cpaapi.HostAuthSaveResponse, error)
	GetAuth(ctx context.Context, authIndex string) (cpaapi.HostAuthGetResponse, error)
}

var codexFingerprintSeedMu sync.Mutex

func ensureCodexFingerprintSeed(ctx context.Context, store fingerprintAccountStore, account Account) (string, bool) {
	codexFingerprintSeedMu.Lock()
	defer codexFingerprintSeedMu.Unlock()
	if ctx.Err() != nil || store == nil {
		return "", false
	}
	document, errRead := store.CurrentAuthDocument(ctx, account)
	if errRead != nil {
		return "", false
	}
	if seed := canonicalCodexFingerprintSeed(document.Metadata[codexFingerprintSeedExtraKey]); seed != "" {
		return seed, true
	}
	detail, errGet := store.GetAuth(ctx, account.ID)
	if errGet != nil {
		return "", false
	}
	name := strings.TrimSpace(firstNonEmpty(detail.Name, account.Name))
	path := normalizedPath(firstNonEmpty(detail.Path, account.path))
	raw := bytes.TrimSpace(detail.JSON)
	if name == "" || path == "" || len(raw) == 0 || !json.Valid(raw) ||
		!safeAuthJSONName(name) || path != account.path {
		return "", false
	}
	var payload map[string]json.RawMessage
	if errDecode := json.Unmarshal(raw, &payload); errDecode != nil || payload == nil {
		return "", false
	}
	var b [16]byte
	if _, errReadRandom := rand.Read(b[:]); errReadRandom != nil {
		return "", false
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	seed := formatUUIDBytes(b)
	encodedSeed, _ := json.Marshal(seed)
	payload[codexFingerprintSeedExtraKey] = encodedSeed
	updated, errEncode := json.Marshal(payload)
	if errEncode != nil {
		return "", false
	}
	if _, errSave := store.SaveAuth(ctx, name, updated); errSave != nil {
		return "", false
	}
	return seed, true
}

// resolveCodexFingerprintSeed chooses the reference fork's persisted OAuth-like
// seed when credential storage is available. AI-provider credentials are not
// represented by editable auth JSON, so derive a stable non-persisted seed from
// their stable host identity instead of sharing one global fingerprint.
func resolveCodexFingerprintSeed(
	ctx context.Context,
	store fingerprintSeedStore,
	account Account,
) (string, bool) {
	if isCodexOAuthLikeAccount(account) {
		return ensureCodexFingerprintSeed(ctx, store, account)
	}
	identity := strings.TrimSpace(firstNonEmpty(account.ID, account.AuthID))
	if identity != "" && strings.ContainsAny(identity, "/\\") {
		return "", false
	}
	if identity == "" || strings.EqualFold(identity, "unknown") {
		return "", false
	}
	sum := sha256.Sum256([]byte("sub2api:codex-api-key-seed:v1:" + identity))
	var uuid [16]byte
	copy(uuid[:], sum[:16])
	uuid[6] = (uuid[6] & 0x0f) | 0x40
	uuid[8] = (uuid[8] & 0x3f) | 0x80
	return formatUUIDBytes(uuid), true
}

// isCodexOAuthLikeAccount matches the account classes that sub2api treats as
// Codex OAuth-like and therefore persists a system-managed random seed.
func isCodexOAuthLikeAccount(account Account) bool {
	provider := strings.ToLower(strings.TrimSpace(account.Provider))
	accountType := strings.ToLower(strings.TrimSpace(account.AccountType))
	if accountType == "" {
		accountType = strings.ToLower(strings.TrimSpace(account.Type))
	}
	switch accountType {
	case "oauth", "setup_token", "setup-token":
		return provider == "" || provider == "codex"
	case "":
		// Older CPA hosts identify native Codex OAuth files only as
		// provider/type=codex and omit account_type. Treat that shape as
		// OAuth-like, but never infer it for an explicitly API-key channel.
		return provider == "codex" && strings.EqualFold(strings.TrimSpace(account.Type), "codex")
	default:
		return false
	}
}

func canonicalCodexFingerprintSeed(value any) string {
	raw, ok := value.(string)
	if !ok {
		return ""
	}
	seed := strings.TrimSpace(raw)
	if len(seed) != 36 || !isCanonicalUUIDString(seed) {
		return ""
	}
	return seed
}

func formatUUIDBytes(b [16]byte) string {
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		binary.BigEndian.Uint32(b[0:4]),
		binary.BigEndian.Uint16(b[4:6]),
		binary.BigEndian.Uint16(b[6:8]),
		binary.BigEndian.Uint16(b[8:10]),
		b[10:16])
}
