package manager

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

const (
	// codexUpstreamMinVersion is the lowest engine version accepted by the
	// current Codex backend when the request supplies a Version header.
	codexUpstreamMinVersion = "0.144.0"
	// codexCLIVersion is the stable identity used when an untrusted or legacy
	// client identity cannot be paired safely.
	codexCLIVersion = "0.144.1"
	// defaultCodexCLIUserAgent matches the current official Rust CLI.
	defaultCodexCLIUserAgent = "codex_cli_rs/0.144.1 (Ubuntu 22.4.0; x86_64) xterm-256color"
)

// codexOfficialClientUAPrefixes are deterministic first-party User-Agent
// prefixes. They intentionally exclude the bare "codex" token.
var codexOfficialClientUAPrefixes = []string{
	"codex_cli_rs/",
	"codex-tui/",
	"codex_vscode/",
	"codex_vscode_copilot/",
	"codex_app/",
	"codex_chatgpt_desktop/",
	"codex_atlas/",
	"codex_exec/",
	"codex_sdk_ts/",
}

// codexOfficialClientFamilyPrefix covers the "Codex Desktop/..." family while
// retaining the trailing space so it cannot degrade to a broad "codex" match.
const codexOfficialClientFamilyPrefix = "codex "

var codexOfficialClientOriginators = map[string]bool{
	"codex_cli_rs":          true,
	"codex-tui":             true,
	"codex_vscode":          true,
	"codex_vscode_copilot":  true,
	"codex_app":             true,
	"codex_chatgpt_desktop": true,
	"codex_atlas":           true,
	"codex_exec":            true,
	"codex_sdk_ts":          true,
}

type codexAllowedClientEntry struct {
	Originator            string   `json:"originator"`
	UAContains            []string `json:"ua_contains"`
	SkipEngineFingerprint bool     `json:"skip_engine_fingerprint"`
}

type engineFingerprintSignal struct {
	Type     string   `json:"type"`
	Match    []string `json:"match"`
	Required bool     `json:"required"`
}

const (
	fingerprintSignalHeaderExact  = "header_exact"
	fingerprintSignalHeaderPrefix = "header_prefix"
	fingerprintSignalBodyPath     = "body_path"
)

func defaultEngineFingerprintSignals() []engineFingerprintSignal {
	return []engineFingerprintSignal{
		{Type: fingerprintSignalHeaderPrefix, Match: []string{"x-codex-"}, Required: true},
		{Type: fingerprintSignalHeaderExact, Match: []string{"session-id", "session_id"}, Required: false},
		{Type: fingerprintSignalHeaderExact, Match: []string{"thread-id", "thread_id"}, Required: false},
		{Type: fingerprintSignalBodyPath, Match: []string{"client_metadata.x-codex-window-id", "client_metadata.x-codex-installation-id"}, Required: false},
	}
}

func normalizeCodexClientHeader(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func matchCodexClientHeaderStrictPrefixes(value string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if normalized := normalizeCodexClientHeader(prefix); normalized != "" && strings.HasPrefix(value, normalized) {
			return true
		}
	}
	return false
}

func codexUATrailerName(ua string) string {
	last := strings.LastIndexByte(ua, '(')
	if last < 0 {
		return ""
	}
	rest := ua[last+1:]
	closeIndex := strings.IndexByte(rest, ')')
	if closeIndex < 0 {
		return ""
	}
	inner := strings.TrimSpace(rest[:closeIndex])
	if semicolon := strings.IndexByte(inner, ';'); semicolon >= 0 {
		inner = strings.TrimSpace(inner[:semicolon])
	}
	return inner
}

func isCodexOfficialClientRequestStrict(userAgent string) bool {
	ua := normalizeCodexClientHeader(userAgent)
	if ua == "" {
		return false
	}
	if matchCodexClientHeaderStrictPrefixes(ua, codexOfficialClientUAPrefixes) {
		return true
	}
	if strings.HasPrefix(ua, codexOfficialClientFamilyPrefix) {
		return true
	}
	if name := codexUATrailerName(ua); name != "" {
		return isCodexOfficialClientOriginator(name)
	}
	return false
}

func isCodexOfficialClientOriginator(originator string) bool {
	value := normalizeCodexClientHeader(originator)
	if value == "" {
		return false
	}
	if codexOfficialClientOriginators[value] {
		return true
	}
	return strings.HasPrefix(value, codexOfficialClientFamilyPrefix)
}

func pairCodexClientIdentity(userAgent string) (originator, pairedUA string, ok bool) {
	ua := strings.TrimSpace(userAgent)
	slash := strings.IndexByte(ua, '/')
	if slash <= 0 {
		return "", "", false
	}
	if leading := strings.TrimSpace(ua[:slash]); isSaneCodexOriginator(leading) && isCodexOfficialClientOriginator(leading) {
		leading = canonicalizeCodexOriginator(leading)
		return leading, leading + ua[slash:], true
	}
	if trailer := codexUATrailerName(ua); trailer != "" && !strings.ContainsRune(trailer, '/') &&
		isSaneCodexOriginator(trailer) && isCodexOfficialClientOriginator(trailer) {
		trailer = canonicalizeCodexOriginator(trailer)
		return trailer, trailer + ua[slash:], true
	}
	return "", "", false
}

const codexOriginatorMaxLen = 64

func isSaneCodexOriginator(name string) bool {
	if name == "" || len(name) > codexOriginatorMaxLen {
		return false
	}
	for index := 0; index < len(name); index++ {
		if char := name[index]; char < 0x20 || char > 0x7e {
			return false
		}
	}
	return true
}

func canonicalizeCodexOriginator(name string) string {
	if lower := normalizeCodexClientHeader(name); codexOfficialClientOriginators[lower] {
		return lower
	}
	return name
}

var codexEngineVersionPattern = regexp.MustCompile(`^(\d+\.\d+\.\d+)`)

func parseCodexEngineVersion(userAgent string) (version string, ok bool) {
	ua := strings.TrimSpace(userAgent)
	slash := strings.IndexByte(ua, '/')
	if slash < 0 {
		return "", false
	}
	rest := ua[slash+1:]
	end := len(rest)
	for index := 0; index < len(rest); index++ {
		if rest[index] == ' ' || rest[index] == '(' {
			end = index
			break
		}
	}
	version = codexEngineVersionPattern.FindString(strings.TrimSpace(rest[:end]))
	return version, version != ""
}

func compareVersions(a, b string) int {
	left := parseSemverForComparison(a)
	right := parseSemverForComparison(b)
	for index := 0; index < 3; index++ {
		if left[index] < right[index] {
			return -1
		}
		if left[index] > right[index] {
			return 1
		}
	}
	return 0
}

func parseSemverForComparison(value string) [3]int {
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(value), "v"), ".")
	result := [3]int{}
	for index := 0; index < len(parts) && index < 3; index++ {
		number := 0
		for _, char := range parts[index] {
			if char < '0' || char > '9' {
				break
			}
			number = number*10 + int(char-'0')
		}
		result[index] = number
	}
	return result
}

func ensureCodexIdentityHeaders(h http.Header) {
	if h == nil {
		return
	}
	if strings.TrimSpace(h.Get("User-Agent")) == "" {
		h.Set("User-Agent", defaultCodexCLIUserAgent)
	}
	if strings.TrimSpace(h.Get("Originator")) == "" {
		h.Set("Originator", "codex_cli_rs")
	}
	if strings.TrimSpace(h.Get("Version")) == "" {
		h.Set("Version", codexCLIVersion)
	}
	h.Set("OpenAI-Beta", "responses=experimental")
}

func enforceCodexIdentityHeaders(h http.Header) {
	if h == nil || strings.TrimSpace(h.Get("Originator")) == "" {
		return
	}
	originator, pairedUA, ok := pairCodexClientIdentity(h.Get("User-Agent"))
	if !ok {
		originator = "codex_cli_rs"
		pairedUA = defaultCodexCLIUserAgent
	}
	h.Set("User-Agent", pairedUA)
	h.Set("Originator", originator)
	if version := strings.TrimSpace(h.Get("Version")); version != "" && compareVersions(version, codexUpstreamMinVersion) < 0 {
		h.Set("Version", codexCLIVersion)
	}
}

func isAllowedCodexClientMatch(userAgent, originator string, entry codexAllowedClientEntry) bool {
	wantOriginator := normalizeCodexClientHeader(entry.Originator)
	if wantOriginator == "" || normalizeCodexClientHeader(originator) != wantOriginator || len(entry.UAContains) == 0 {
		return false
	}
	ua := normalizeCodexClientHeader(userAgent)
	for _, marker := range entry.UAContains {
		normalizedMarker := normalizeCodexClientHeader(marker)
		if normalizedMarker == "" || !strings.Contains(ua, normalizedMarker) {
			return false
		}
	}
	return true
}

func matchCodexClientEntry(userAgent, originator string, entries []codexAllowedClientEntry) (codexAllowedClientEntry, bool) {
	for _, entry := range entries {
		if isAllowedCodexClientMatch(userAgent, originator, entry) {
			return entry, true
		}
	}
	return codexAllowedClientEntry{}, false
}

func isDeniedCodexClientMatch(userAgent, originator string, entry codexAllowedClientEntry) bool {
	if want := normalizeCodexClientHeader(entry.Originator); want != "" && normalizeCodexClientHeader(originator) == want {
		return true
	}
	ua := normalizeCodexClientHeader(userAgent)
	for _, marker := range entry.UAContains {
		if marker = normalizeCodexClientHeader(marker); marker != "" && strings.Contains(ua, marker) {
			return true
		}
	}
	return false
}

func matchDeniedCodexClients(userAgent, originator string, entries []codexAllowedClientEntry) bool {
	for _, entry := range entries {
		if isDeniedCodexClientMatch(userAgent, originator, entry) {
			return true
		}
	}
	return false
}

func evaluateEngineFingerprint(h http.Header, body []byte, signals []engineFingerprintSignal) bool {
	for _, signal := range signals {
		if !signal.Required || engineFingerprintSignalMatches(h, body, signal) {
			continue
		}
		return false
	}
	return true
}

func engineFingerprintSignalMatches(h http.Header, body []byte, signal engineFingerprintSignal) bool {
	switch signal.Type {
	case fingerprintSignalHeaderExact:
		for _, name := range signal.Match {
			if name = strings.TrimSpace(name); name != "" && h != nil && strings.TrimSpace(h.Get(name)) != "" {
				return true
			}
		}
	case fingerprintSignalHeaderPrefix:
		if h == nil {
			return false
		}
		for key := range h {
			lowerKey := normalizeCodexClientHeader(key)
			for _, prefix := range signal.Match {
				if prefix = normalizeCodexClientHeader(prefix); prefix != "" && strings.HasPrefix(lowerKey, prefix) {
					return true
				}
			}
		}
	case fingerprintSignalBodyPath:
		for _, path := range signal.Match {
			if path = strings.TrimSpace(path); path != "" && jsonPathExists(body, path) {
				return true
			}
		}
	}
	return false
}

func jsonPathExists(document []byte, path string) bool {
	var value any
	if errDecode := json.Unmarshal(document, &value); errDecode != nil {
		return false
	}
	for path = strings.TrimSpace(path); ; {
		var key string
		var found bool
		key, path, found = splitJSONPath(path)
		if !found {
			return false
		}
		object, ok := value.(map[string]any)
		if !ok || value == nil {
			return false
		}
		value, ok = object[key]
		if !ok {
			return false
		}
		if strings.TrimSpace(path) == "" {
			return true
		}
	}
}

func splitJSONPath(path string) (key, remaining string, ok bool) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", "", false
	}
	if dot := strings.IndexByte(path, '.'); dot >= 0 {
		return path[:dot], path[dot+1:], true
	}
	return path, "", true
}

type codexRestrictionPolicy struct {
	OutboundConvergenceEnabled bool
	IngressGateEnabled         bool
	Whitelist                  []codexAllowedClientEntry
	Blacklist                  []codexAllowedClientEntry
	MinCodexVersion            string
	MaxCodexVersion            string
	AllowAppServerClients      bool
	EngineFingerprintSignals   []engineFingerprintSignal
}

type codexRestrictionReason string

const (
	codexRestrictionDisabled                 codexRestrictionReason = "disabled"
	codexRestrictionMatchedUA                codexRestrictionReason = "official_client_user_agent_matched"
	codexRestrictionMatchedOriginator        codexRestrictionReason = "official_client_originator_matched"
	codexRestrictionNotMatchedUA             codexRestrictionReason = "official_client_user_agent_not_matched"
	codexRestrictionBlacklisted              codexRestrictionReason = "blacklist_matched"
	codexRestrictionMatchedWhitelistClient   codexRestrictionReason = "whitelist_client_matched"
	codexRestrictionMatchedAppServerClient   codexRestrictionReason = "app_server_client_matched"
	codexRestrictionVersionTooLow            codexRestrictionReason = "codex_version_too_low"
	codexRestrictionVersionTooHigh           codexRestrictionReason = "codex_version_too_high"
	codexRestrictionVersionUndetectable      codexRestrictionReason = "codex_version_undetectable"
	codexRestrictionMissingEngineFingerprint codexRestrictionReason = "missing_engine_fingerprint"
)

type codexRestrictionDetectionResult struct {
	Enabled bool
	Matched bool
	Reason  codexRestrictionReason
}

type codexAccountPolicy interface {
	CodexCLIOnlyEnabled() bool
	CodexCLIOnlyAllowAppServer() bool
}

func detectCodexClientRestriction(account codexAccountPolicy, policy codexRestrictionPolicy, userAgent, originator string, headers http.Header, body []byte) codexRestrictionDetectionResult {
	if account == nil || !account.CodexCLIOnlyEnabled() {
		return codexRestrictionDetectionResult{Reason: codexRestrictionDisabled}
	}
	if matchDeniedCodexClients(userAgent, originator, policy.Blacklist) {
		return codexRestrictionDetectionResult{Enabled: true, Reason: codexRestrictionBlacklisted}
	}

	skipFingerprint := false
	var reason codexRestrictionReason
	switch {
	case isCodexOfficialClientRequestStrict(userAgent):
		reason = codexRestrictionMatchedUA
	case isCodexOfficialClientOriginator(originator):
		reason = codexRestrictionMatchedOriginator
	default:
		if entry, matched := matchCodexClientEntry(userAgent, originator, policy.Whitelist); matched {
			reason = codexRestrictionMatchedWhitelistClient
			skipFingerprint = entry.SkipEngineFingerprint
		} else if policy.AllowAppServerClients || account.CodexCLIOnlyAllowAppServer() {
			reason = codexRestrictionMatchedAppServerClient
		}
	}
	if reason == "" {
		return codexRestrictionDetectionResult{Enabled: true, Reason: codexRestrictionNotMatchedUA}
	}

	if reason == codexRestrictionMatchedUA || reason == codexRestrictionMatchedOriginator {
		version, ok := parseCodexEngineVersion(userAgent)
		if !ok {
			return codexRestrictionDetectionResult{Enabled: true, Reason: codexRestrictionVersionUndetectable}
		}
		if policy.MinCodexVersion != "" && compareVersions(version, policy.MinCodexVersion) < 0 {
			return codexRestrictionDetectionResult{Enabled: true, Reason: codexRestrictionVersionTooLow}
		}
		if policy.MaxCodexVersion != "" && compareVersions(version, policy.MaxCodexVersion) > 0 {
			return codexRestrictionDetectionResult{Enabled: true, Reason: codexRestrictionVersionTooHigh}
		}
	}

	if !skipFingerprint && !evaluateEngineFingerprint(headers, body, policy.EngineFingerprintSignals) {
		return codexRestrictionDetectionResult{Enabled: true, Reason: codexRestrictionMissingEngineFingerprint}
	}
	return codexRestrictionDetectionResult{Enabled: true, Matched: true, Reason: reason}
}

func codexOfficialClientsOnlyMessage() string {
	return "This account only allows Codex official clients"
}

func codexRestrictionMessage(result codexRestrictionDetectionResult) string {
	switch result.Reason {
	case codexRestrictionVersionTooLow:
		return "Your Codex version is below the minimum required version. Please update Codex."
	case codexRestrictionVersionTooHigh:
		return "Your Codex version exceeds the maximum allowed version. Please downgrade Codex."
	default:
		return codexOfficialClientsOnlyMessage()
	}
}

func parseCodexAllowedClientsJSON(raw string, whitelist bool) ([]codexAllowedClientEntry, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var entries []codexAllowedClientEntry
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return nil, fmt.Errorf("must be an empty string or a valid JSON array")
	}
	for index, entry := range entries {
		hasMarker := false
		for _, marker := range entry.UAContains {
			if strings.TrimSpace(marker) != "" {
				hasMarker = true
				break
			}
		}
		if whitelist && (strings.TrimSpace(entry.Originator) == "" || !hasMarker) {
			return nil, fmt.Errorf("entry %d: originator and at least one ua_contains value are required", index)
		}
	}
	return entries, nil
}

func validateCodexAllowedClientsJSON(raw string, whitelist bool) error {
	_, err := parseCodexAllowedClientsJSON(raw, whitelist)
	return err
}

func parseEngineFingerprintSignals(raw string) []engineFingerprintSignal {
	if strings.TrimSpace(raw) == "" {
		return defaultEngineFingerprintSignals()
	}
	var signals []engineFingerprintSignal
	if err := json.Unmarshal([]byte(raw), &signals); err != nil {
		return nil
	}
	return signals
}

func validateEngineFingerprintSignalsJSON(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var signals []engineFingerprintSignal
	if err := json.Unmarshal([]byte(raw), &signals); err != nil {
		return fmt.Errorf("must be an empty string or a valid JSON array")
	}
	for index, signal := range signals {
		switch signal.Type {
		case fingerprintSignalHeaderExact, fingerprintSignalHeaderPrefix, fingerprintSignalBodyPath:
		default:
			return fmt.Errorf("entry %d: unsupported signal type", index)
		}
		hasMatch := false
		for _, match := range signal.Match {
			if strings.TrimSpace(match) != "" {
				hasMatch = true
				break
			}
		}
		if !hasMatch {
			return fmt.Errorf("entry %d: match must contain a non-empty value", index)
		}
	}
	return nil
}

type codexPolicyProvider interface {
	CodexIdentity() ExperimentalCodexIdentitySettings
	codexIdentitySnapshot() ExperimentalCodexIdentitySettings
}

type codexAccountWithMetadata struct {
	codexAccountGateState
	account *Account
}

type codexAccountGateState struct {
	codexCLIOnly          bool
	codexCLIOnlyAppServer bool
}

func (s codexAccountGateState) CodexCLIOnlyEnabled() bool        { return s.codexCLIOnly }
func (s codexAccountGateState) CodexCLIOnlyAllowAppServer() bool { return s.codexCLIOnlyAppServer }

func codexRestrictionPolicyFromSettings(settings ExperimentalCodexIdentitySettings) codexRestrictionPolicy {
	whitelist, _ := parseCodexAllowedClientsJSON(settings.Whitelist, true)
	blacklist, _ := parseCodexAllowedClientsJSON(settings.Blacklist, false)
	return codexRestrictionPolicy{
		Whitelist:                whitelist,
		Blacklist:                blacklist,
		MinCodexVersion:          strings.TrimSpace(settings.MinVersion),
		MaxCodexVersion:          strings.TrimSpace(settings.MaxVersion),
		AllowAppServerClients:    settings.AllowAppServerClients,
		EngineFingerprintSignals: parseEngineFingerprintSignals(settings.FingerprintSignals),
	}
}
