package manager

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"cpa-account-config-manager/internal/cpaapi"
)

type aiProviderFingerprintHost struct {
	detail cpaapi.HostAuthGetResponse
}

func (h aiProviderFingerprintHost) CurrentAuthDocument(context.Context, Account) (currentAuthDocument, error) {
	return currentAuthDocument{Metadata: map[string]any{}}, nil
}

func (h aiProviderFingerprintHost) List(context.Context, ListQuery) (ListResponse, error) {
	return ListResponse{Accounts: []Account{{ID: "provider-auth"}}}, nil
}

func (h aiProviderFingerprintHost) GetAuth(context.Context, string) (cpaapi.HostAuthGetResponse, error) {
	return h.detail, nil
}

func (h aiProviderFingerprintHost) SaveAuth(context.Context, string, json.RawMessage) (cpaapi.HostAuthSaveResponse, error) {
	return cpaapi.HostAuthSaveResponse{}, errors.New("AI-provider credentials must not be rewritten")
}

type codexIdentitySeedFunc struct {
	resolve func(context.Context, fingerprintSeedStore, Account) (string, bool)
	entries []cpaapi.HostAuthFileEntry
	details map[string]cpaapi.HostAuthGetResponse
	listErr error
}

func (s *codexIdentitySeedFunc) CurrentAuthDocument(_ context.Context, account Account) (currentAuthDocument, error) {
	if detail, ok := s.details[account.ID]; ok {
		var metadata map[string]any
		if errDecode := json.Unmarshal(detail.JSON, &metadata); errDecode == nil && metadata != nil {
			return currentAuthDocument{Metadata: metadata}, nil
		}
	}
	return currentAuthDocument{}, errors.New("unexpected auth read")
}

func (s *codexIdentitySeedFunc) List(context.Context, ListQuery) (ListResponse, error) {
	if s.listErr != nil {
		return ListResponse{}, s.listErr
	}
	accounts := make([]Account, 0, len(s.entries))
	for _, entry := range s.entries {
		account := projectHostEntry(entry, nil, nil, nil)
		accounts = append(accounts, account)
	}
	return ListResponse{Accounts: accounts}, nil
}

func (s *codexIdentitySeedFunc) ListAuth(context.Context) ([]cpaapi.HostAuthFileEntry, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return append([]cpaapi.HostAuthFileEntry(nil), s.entries...), nil
}

func (s *codexIdentitySeedFunc) GetAuth(_ context.Context, authIndex string) (cpaapi.HostAuthGetResponse, error) {
	if detail, ok := s.details[authIndex]; ok {
		detail.JSON = append(json.RawMessage(nil), detail.JSON...)
		return detail, nil
	}
	return cpaapi.HostAuthGetResponse{}, errors.New("unexpected auth read")
}

func (s *codexIdentitySeedFunc) SaveAuth(context.Context, string, json.RawMessage) (cpaapi.HostAuthSaveResponse, error) {
	return cpaapi.HostAuthSaveResponse{}, errors.New("unexpected auth save")
}

func TestPairCodexClientIdentity(t *testing.T) {
	cases := []struct {
		name           string
		ua             string
		wantOriginator string
		wantUA         string
		wantOK         bool
	}{
		{name: "cli leading identity", ua: "codex_cli_rs/0.144.1 (Ubuntu 22.4.0; x86_64) xterm-256color", wantOriginator: "codex_cli_rs", wantUA: "codex_cli_rs/0.144.1 (Ubuntu 22.4.0; x86_64) xterm-256color", wantOK: true},
		{name: "tui leading identity", ua: "codex-tui/0.140.2 (Mac OS X 14.0; arm64) iTerm (codex-tui; 0.140.2)", wantOriginator: "codex-tui", wantUA: "codex-tui/0.140.2 (Mac OS X 14.0; arm64) iTerm (codex-tui; 0.140.2)", wantOK: true},
		{name: "latest linux tui identity", ua: "codex-tui/0.153.3 (Linux Unknown; x86_64) xterm-256color (codex-tui; 0.153.3)", wantOriginator: "codex-tui", wantUA: "codex-tui/0.153.3 (Linux Unknown; x86_64) xterm-256color (codex-tui; 0.153.3)", wantOK: true},
		{name: "latest windows tui identity", ua: "codex-tui/0.153.0 (Windows 10.0.26100; x86_64) unknown (codex-tui; 0.153.0)", wantOriginator: "codex-tui", wantUA: "codex-tui/0.153.0 (Windows 10.0.26100; x86_64) unknown (codex-tui; 0.153.0)", wantOK: true},
		{name: "latest mac tui identity", ua: "codex-tui/0.153.2 (Mac OS 26.5.0; arm64) iTerm.app/3.6.10 (codex-tui; 0.153.2)", wantOriginator: "codex-tui", wantUA: "codex-tui/0.153.2 (Mac OS 26.5.0; arm64) iTerm.app/3.6.10 (codex-tui; 0.153.2)", wantOK: true},
		{name: "desktop family keeps case", ua: "Codex Desktop/1.2.3", wantOriginator: "Codex Desktop", wantUA: "Codex Desktop/1.2.3", wantOK: true},
		{name: "trailer rewrites override", ua: "cccc/0.142.0 (Ubuntu 22.4.0; x86_64) screen (codex-tui; 0.142.0)", wantOriginator: "codex-tui", wantUA: "codex-tui/0.142.0 (Ubuntu 22.4.0; x86_64) screen (codex-tui; 0.142.0)", wantOK: true},
		{name: "canonical exact case", ua: "CODEX_CLI_RS/1.0.0", wantOriginator: "codex_cli_rs", wantUA: "codex_cli_rs/1.0.0", wantOK: true},
		{name: "third party rejected", ua: "luna/1.0.0"},
		{name: "forged prefix rejected", ua: "codex_cli_rs_evil/1.0.0"},
		{name: "browser rejected", ua: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/120.0 Safari/537.36"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			originator, pairedUA, ok := pairCodexClientIdentity(test.ua)
			if ok != test.wantOK || originator != test.wantOriginator || pairedUA != test.wantUA {
				t.Fatalf("pair(%q) = %q, %q, %t; want %q, %q, %t", test.ua, originator, pairedUA, ok, test.wantOriginator, test.wantUA, test.wantOK)
			}
		})
	}
}

func TestEnforceCodexIdentityHeaders(t *testing.T) {
	headers := http.Header{}
	headers.Set("User-Agent", "cccc/0.140.0 (Linux; x86_64) term (codex-tui; 0.140.0)")
	headers.Set("Originator", "cccc")
	headers.Set("Version", "0.139.0")
	enforceCodexIdentityHeaders(headers)
	if got := headers.Get("User-Agent"); !strings.HasPrefix(got, "codex-tui/0.140.0 ") {
		t.Fatalf("paired UA = %q", got)
	}
	if got := headers.Get("Originator"); got != "codex-tui" {
		t.Fatalf("originator = %q", got)
	}
	if got := headers.Get("Version"); got != codexCLIVersion {
		t.Fatalf("minimum version = %q", got)
	}

	fallback := http.Header{"Originator": []string{"evil"}}
	enforceCodexIdentityHeaders(fallback)
	if fallback.Get("User-Agent") != defaultCodexCLIUserAgent || fallback.Get("Originator") != "codex-tui" {
		t.Fatalf("fallback identity = %#v", fallback)
	}

	missing := http.Header{}
	enforceCodexIdentityHeaders(missing)
	if len(missing) != 0 {
		t.Fatal("enforce must not synthesize an originator")
	}
}

func TestDetectCodexClientRestrictionModes(t *testing.T) {
	account := codexAccountGateState{codexCLIOnly: true}
	defaultSignals := codexRestrictionPolicy{EngineFingerprintSignals: defaultEngineFingerprintSignals()}
	withFingerprint := func() http.Header {
		header := http.Header{}
		header.Set("X-Codex-Window-ID", "window")
		return header
	}
	cases := []struct {
		name       string
		account    codexAccountGateState
		policy     codexRestrictionPolicy
		ua         string
		originator string
		header     http.Header
		wantReason codexRestrictionReason
		wantMatch  bool
	}{
		{name: "disabled account bypasses", ua: "curl/8", wantReason: codexRestrictionDisabled},
		{name: "official UA passes", account: account, ua: "codex_vscode/1.0.0", header: withFingerprint(), policy: defaultSignals, wantReason: codexRestrictionMatchedUA, wantMatch: true},
		{name: "official originator passes", account: account, ua: "myterm/1.0.0", originator: "codex_chatgpt_desktop", header: withFingerprint(), policy: defaultSignals, wantReason: codexRestrictionMatchedOriginator, wantMatch: true},
		{name: "unmatched client denied", account: account, ua: "curl/8", originator: "custom", header: withFingerprint(), policy: defaultSignals, wantReason: codexRestrictionNotMatchedUA},
		{
			name:    "whitelist AND passes and skips fingerprint",
			account: account,
			policy:  codexRestrictionPolicy{Whitelist: []codexAllowedClientEntry{{Originator: "OpenCode", UAContains: []string{"opencode/"}, SkipEngineFingerprint: true}}},
			ua:      "opencode/1.0", originator: "opencode", wantReason: codexRestrictionMatchedWhitelistClient, wantMatch: true,
		},
		{name: "app server requires fingerprint", account: codexAccountGateState{codexCLIOnly: true, codexCLIOnlyAppServer: true}, ua: "app-server/1", header: withFingerprint(), policy: defaultSignals, wantReason: codexRestrictionMatchedAppServerClient, wantMatch: true},
		{name: "version too low", account: account, ua: "codex_cli_rs/0.143.0", header: withFingerprint(), policy: codexRestrictionPolicy{MinCodexVersion: "0.144.0"}, wantReason: codexRestrictionVersionTooLow},
		{name: "undetectable official version", account: account, ua: "codex_cli_rs/latest", header: withFingerprint(), wantReason: codexRestrictionVersionUndetectable},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got := detectCodexClientRestriction(test.account, test.policy, test.ua, test.originator, test.header, nil)
			if got.Matched != test.wantMatch || got.Reason != test.wantReason {
				t.Fatalf("detect = %#v; want matched=%t reason=%s", got, test.wantMatch, test.wantReason)
			}
		})
	}

	blacklist := codexAccountGateState{codexCLIOnly: true}
	result := detectCodexClientRestriction(blacklist, codexRestrictionPolicy{Blacklist: []codexAllowedClientEntry{{Originator: "evil"}}}, "codex_cli_rs/0.144.1", "evil", withFingerprint(), nil)
	if result.Reason != codexRestrictionBlacklisted || result.Matched {
		t.Fatalf("blacklist result = %#v", result)
	}
}

func TestEvaluateEngineFingerprintBodyPath(t *testing.T) {
	signals := []engineFingerprintSignal{{Type: fingerprintSignalBodyPath, Match: []string{"client_metadata.x-codex-window-id"}, Required: true}}
	if evaluateEngineFingerprint(http.Header{}, []byte(`{"client_metadata":{"x-codex-window-id":"w"}}`), signals) != true {
		t.Fatal("nested body fingerprint was not found")
	}
	if evaluateEngineFingerprint(http.Header{}, []byte(`{"client_metadata":{}}`), signals) != false {
		t.Fatal("missing nested body fingerprint passed")
	}
}

type stubCodexSettings struct {
	settings ExperimentalCodexIdentitySettings
}

func (s stubCodexSettings) CodexIdentity() ExperimentalCodexIdentitySettings { return s.settings }

func (s stubCodexSettings) codexIdentitySnapshot() ExperimentalCodexIdentitySettings {
	return s.settings
}

func TestCodexIdentityExperimentInterceptRequest(t *testing.T) {
	experiment := NewCodexIdentityExperiment(stubCodexSettings{}, nil)
	if _, changed := experiment.InterceptRequest(cpaapi.RequestInterceptRequest{ToFormat: "codex"}); changed {
		t.Fatal("disabled experiment changed a request")
	}

	settings := ExperimentalCodexIdentitySettings{OutboundConvergenceEnabled: true}
	experiment = NewCodexIdentityExperiment(stubCodexSettings{settings}, nil)
	request := cpaapi.RequestInterceptRequest{
		ToFormat: "codex",
		Headers: http.Header{
			"User-Agent": []string{"cccc/0.140.0 (x) (codex-tui; 0.140.0)"},
			"Originator": []string{"cccc"},
			"Version":    []string{"0.100.0"},
		},
	}
	response, changed := experiment.InterceptRequest(request)
	if !changed || response.Headers.Get("User-Agent") == request.Headers.Get("User-Agent") ||
		response.Headers.Get("Originator") != "codex-tui" || response.Headers.Get("Version") != codexCLIVersion {
		t.Fatalf("outbound response = %#v", response)
	}

	settings.IngressGateEnabled = true
	experiment = NewCodexIdentityExperiment(stubCodexSettings{settings}, nil)
	rejected, changed := experiment.InterceptRequest(cpaapi.RequestInterceptRequest{Headers: http.Header{"User-Agent": []string{"curl/8"}, "Originator": []string{"custom"}}})
	if !changed || !rejected.Terminate || rejected.StatusCode != http.StatusForbidden {
		t.Fatalf("gate response = %#v", rejected)
	}
}

func TestCodexIdentityExperimentConvergesWithoutOriginatorHeader(t *testing.T) {
	settings := ExperimentalCodexIdentitySettings{OutboundConvergenceEnabled: true, ConvergenceMode: "full"}
	authIndex := "oauth-no-originator"
	host := &fakeAuthHost{
		entries: []cpaapi.HostAuthFileEntry{{
			AuthIndex: authIndex, ID: "cpa-id", Name: "codex.json",
			Provider: "codex", Type: "codex", AccountType: "oauth", Path: "/auth/codex.json",
		}},
		details: map[string]cpaapi.HostAuthGetResponse{
			authIndex: {AuthIndex: authIndex, Path: "/auth/codex.json", JSON: json.RawMessage(`{"type":"codex","codex_fingerprint_seed":"11111111-1111-4111-8111-111111111111"}`)},
		},
	}
	experiment := NewCodexIdentityExperiment(stubCodexSettings{settings}, NewAccountService(host))
	response, changed := experiment.InterceptRequest(cpaapi.RequestInterceptRequest{
		ToFormat: "codex",
		Headers:  http.Header{"User-Agent": []string{"codex_cli_rs/0.144.1"}, "Session-Id": []string{"client-session"}},
		Body:     []byte(`{"client_metadata":{"session_id":"client-session"}}`),
		Metadata: map[string]any{"selected_auth_index": authIndex},
	})
	if !changed || response.Headers.Get("X-Codex-Installation-Id") == "" || response.Headers.Get("Session-Id") == "" {
		t.Fatalf("request without Originator was not converged: changed=%v headers=%#v", changed, response.Headers)
	}
}

func TestCodexIdentityExperimentConvergesNilHeadersAndBody(t *testing.T) {
	authIndex := "oauth-nil-headers"
	host := &fakeAuthHost{
		entries: []cpaapi.HostAuthFileEntry{{
			AuthIndex: authIndex, ID: "cpa-id", Name: "codex.json",
			Provider: "codex", Type: "codex", AccountType: "oauth", Path: "/auth/codex.json",
		}},
		details: map[string]cpaapi.HostAuthGetResponse{
			authIndex: {AuthIndex: authIndex, Path: "/auth/codex.json", JSON: json.RawMessage(`{"type":"codex","account_type":"oauth"}`)},
		},
	}
	experiment := NewCodexIdentityExperiment(stubCodexSettings{
		ExperimentalCodexIdentitySettings{
			OutboundConvergenceEnabled: true,
			ConvergenceMode:            "full",
		},
	}, NewAccountService(host))

	response, changed := experiment.InterceptRequest(cpaapi.RequestInterceptRequest{
		ToFormat: "codex",
		Body:     []byte(`{"input":"keep","client_metadata":{"session_id":"client-session"},"prompt_cache_key":"client-session"}`),
		Metadata: map[string]any{"selected_auth_index": authIndex},
	})
	if !changed {
		t.Fatal("nil-header Codex request was not transformed")
	}
	if response.Headers == nil ||
		response.Headers.Get("X-Codex-Installation-Id") == "" ||
		response.Headers.Get("Session-Id") == "" {
		t.Fatalf("nil headers did not receive converged official identity: %#v", response.Headers)
	}

	var decoded struct {
		Input          string `json:"input"`
		ClientMetadata struct {
			SessionID string `json:"session_id"`
		} `json:"client_metadata"`
		PromptCacheKey string `json:"prompt_cache_key"`
	}
	if err := json.Unmarshal(response.Body, &decoded); err != nil {
		t.Fatalf("response body is not JSON: %v", err)
	}
	if decoded.Input != "keep" ||
		decoded.ClientMetadata.SessionID == "" ||
		decoded.ClientMetadata.SessionID != response.Headers.Get("Session-Id") ||
		decoded.PromptCacheKey != decoded.ClientMetadata.SessionID {
		t.Fatalf("nil-header body was not converged: body=%s headers=%#v", response.Body, response.Headers)
	}
}

func TestCodexIdentityExperimentInterceptsAIProviderAPIKey(t *testing.T) {
	account := Account{ID: "provider-auth", AuthID: "provider-index", Provider: "codex-api-key", AccountType: "api_key"}
	settings := ExperimentalCodexIdentitySettings{OutboundConvergenceEnabled: true}
	headers := http.Header{
		"User-Agent":        []string{"codex_cli_rs/0.144.1"},
		"Originator":        []string{"codex_cli_rs"},
		"Session-Id":        []string{"client-session"},
		"X-Codex-Window-Id": []string{"original-window"},
	}
	body := []byte(`{"client_metadata":{"session_id":"body-session","turn_metadata":"{}"},"prompt_cache_key":"body-session"}`)

	run := func(mode string) cpaapi.RequestInterceptResponse {
		modeSettings := settings
		modeSettings.ConvergenceMode = mode
		experiment := NewCodexIdentityExperiment(stubCodexSettings{modeSettings}, aiProviderFingerprintHost{})
		requestHeaders := headers.Clone()
		response, changed := experiment.InterceptRequest(cpaapi.RequestInterceptRequest{
			ToFormat: "codex", Headers: requestHeaders, Body: append([]byte(nil), body...),
			Metadata: map[string]any{"selected_auth_id": account.ID},
		})
		if !changed {
			t.Fatalf("mode %s did not change request", mode)
		}
		return response
	}

	off := run("off")
	if off.Headers.Get("X-Codex-Installation-Id") != "" || off.Body == nil ||
		off.Headers.Get("Session-Id") == "client-session" ||
		string(off.Body) == string(body) {
		t.Fatalf("off mode changed identity: %#v", off)
	}

	// The AI-provider host derives a stable non-persisted seed, so repeated
	// device-mode requests remain comparable across later modes.
	run("device")
	device := run("device")
	deviceInstall := device.Headers.Get("X-Codex-Installation-Id")
	if deviceInstall == "" ||
		device.Headers.Get("Session-Id") == "client-session" ||
		device.Headers.Get("X-Codex-Window-Id") == "original-window" {
		t.Fatalf("device mode over-converged: %#v", device)
	}
	var deviceMetadata struct {
		InstallationID string `json:"installation_id"`
	}
	rawDeviceMetadata := device.Headers.Get("X-Codex-Turn-Metadata")
	if err := json.Unmarshal([]byte(rawDeviceMetadata), &deviceMetadata); err != nil ||
		deviceMetadata.InstallationID != deviceInstall {
		t.Fatalf("device turn metadata = %q, err=%v", rawDeviceMetadata, err)
	}

	session := run("session")
	if session.Headers.Get("X-Codex-Installation-Id") != deviceInstall ||
		session.Headers.Get("Session-Id") == "client-session" ||
		session.Headers.Get("Thread-Id") == session.Headers.Get("Session-Id") {
		var decoded map[string]any
		_ = json.Unmarshal(session.Body, &decoded)
		metadata, _ := decoded["client_metadata"].(map[string]any)
		if metadata["session_id"] == metadata["thread_id"] {
			t.Fatalf("session mode used full thread convergence: headers=%#v body=%#v", session.Headers, metadata)
		}
	} else if !strings.HasPrefix(session.Headers.Get("X-Codex-Window-Id"), session.Headers.Get("Thread-Id")+":") {
		t.Fatalf("session window/thread mismatch: %#v", session.Headers)
	}

	full := run("full")
	if full.Headers.Get("X-Codex-Installation-Id") != deviceInstall ||
		full.Headers.Get("Session-Id") != full.Headers.Get("Thread-Id") {
		t.Fatalf("full mode did not converge IDs: %#v", full.Headers)
	}

}

type codexSeedRaceHost struct {
	mu      sync.Mutex
	detail  cpaapi.HostAuthGetResponse
	gets    int
	saves   int
	seed    string
	saveErr error
}

func (h *codexSeedRaceHost) CurrentAuthDocument(context.Context, Account) (currentAuthDocument, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	metadata := map[string]any{}
	if h.seed != "" {
		metadata[codexFingerprintSeedExtraKey] = h.seed
	}
	return currentAuthDocument{Metadata: metadata}, nil
}

func (h *codexSeedRaceHost) ListAuth(context.Context) ([]cpaapi.HostAuthFileEntry, error) {
	return []cpaapi.HostAuthFileEntry{{
		AuthIndex: "oauth-auth", ID: "oauth-auth", Name: "codex.json", Type: "codex",
		Provider: "codex", AccountType: "oauth", Source: "file", Path: "/auths/codex.json",
	}}, nil
}

func (h *codexSeedRaceHost) List(context.Context, ListQuery) (ListResponse, error) {
	entries, err := h.ListAuth(context.Background())
	if err != nil {
		return ListResponse{}, err
	}
	accounts := make([]Account, 0, len(entries))
	for _, entry := range entries {
		accounts = append(accounts, Account{
			ID: entry.AuthIndex, AuthID: entry.ID, Name: entry.Name,
			Provider: entry.Provider, Type: entry.Type, AccountType: entry.AccountType,
			Source: entry.Source, path: normalizedPath(entry.Path), Editable: true,
		})
	}
	return ListResponse{Accounts: accounts}, nil
}

func (h *codexSeedRaceHost) GetAuth(_ context.Context, _ string) (cpaapi.HostAuthGetResponse, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.gets++
	if h.saveErr != nil {
		return cpaapi.HostAuthGetResponse{}, h.saveErr
	}
	return h.detail, nil
}

func (h *codexSeedRaceHost) SaveAuth(_ context.Context, _ string, rawJSON json.RawMessage) (cpaapi.HostAuthSaveResponse, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.saves++
	var payload map[string]any
	if err := json.Unmarshal(rawJSON, &payload); err != nil {
		return cpaapi.HostAuthSaveResponse{}, err
	}
	if seed, ok := payload[codexFingerprintSeedExtraKey].(string); ok {
		if h.seed != "" && h.seed != seed {
			return cpaapi.HostAuthSaveResponse{}, errors.New("concurrent seed writes diverged")
		}
		h.seed = seed
	}
	detail := append(json.RawMessage(nil), rawJSON...)
	h.detail.JSON = detail
	return cpaapi.HostAuthSaveResponse{Name: "codex.json", Path: "/auths/codex.json"}, nil
}

func TestCodexIdentityExperimentConvergesOAuthRequestByMode(t *testing.T) {
	authIndex := "oauth-auth"
	rawJSON := json.RawMessage(`{"type":"codex","codex_fingerprint_seed":"11111111-1111-4111-8111-111111111111"}`)
	host := &fakeAuthHost{
		entries: []cpaapi.HostAuthFileEntry{{
			AuthIndex: authIndex, ID: "cpa-id", Name: "codex.json",
			Provider: "codex", Type: "codex", AccountType: "oauth",
			Path: "/auth/codex.json",
		}},
		details: map[string]cpaapi.HostAuthGetResponse{
			authIndex: {AuthIndex: authIndex, Path: "/auth/codex.json", JSON: rawJSON},
		},
	}
	clientSession := "client-session"
	headers := http.Header{
		"User-Agent":            []string{"codex_cli_rs/0.144.1"},
		"Originator":            []string{"codex_cli_rs"},
		"Session-Id":            []string{clientSession},
		"X-Codex-Turn-Metadata": []string{`{"installation_id":"old-install","session_id":"old-session","trace":"keep"}`},
	}
	body := []byte(`{"client_metadata":{"session_id":"body-session","x-codex-turn-metadata":"{\"installation_id\":\"old-install\",\"session_id\":\"old-session\"}"},"prompt_cache_key":"body-session"}`)
	settings := ExperimentalCodexIdentitySettings{OutboundConvergenceEnabled: true}

	run := func(mode string) cpaapi.RequestInterceptResponse {
		modeSettings := settings
		modeSettings.ConvergenceMode = mode
		experiment := NewCodexIdentityExperiment(stubCodexSettings{modeSettings}, NewAccountService(host))
		response, changed := experiment.InterceptRequest(cpaapi.RequestInterceptRequest{
			ToFormat: "codex", Headers: headers.Clone(), Body: append([]byte(nil), body...),
			Metadata: map[string]any{"selected_auth_id": authIndex},
		})
		if !changed {
			t.Fatalf("mode %s did not change request", mode)
		}
		return response
	}
	assertBodyIDs := func(t *testing.T, response cpaapi.RequestInterceptResponse, wantThreadFromClient bool) map[string]any {
		t.Helper()
		var decoded struct {
			ClientMetadata map[string]any `json:"client_metadata"`
			PromptCacheKey string         `json:"prompt_cache_key"`
		}
		if err := json.Unmarshal(response.Body, &decoded); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if decoded.PromptCacheKey != decoded.ClientMetadata["session_id"] {
			t.Fatalf("prompt cache key/body session diverged: %q / %#v", decoded.PromptCacheKey, decoded.ClientMetadata)
		}
		if wantThreadFromClient && decoded.ClientMetadata["thread_id"] == decoded.ClientMetadata["session_id"] {
			t.Fatal("session mode collapsed the client thread identity")
		}
		return decoded.ClientMetadata
	}

	off := run("off")
	if off.Headers.Get("X-Codex-Installation-Id") != "" ||
		off.Headers.Get("Session-Id") == clientSession ||
		string(off.Body) == string(body) {
		t.Fatalf("off mode changed identity: headers=%#v body=%s", off.Headers, off.Body)
	}

	device := run("device")
	deviceInstall := device.Headers.Get("X-Codex-Installation-Id")
	if deviceInstall == "" ||
		device.Headers.Get("X-Codex-Window-Id") != "" ||
		device.Headers.Get(codexFingerprintHeader) != `{"installation_id":"`+deviceInstall+`","session_id":"ecdeda58-22bd-412a-abc1-b877bedd0a29","trace":"keep"}` ||
		strings.HasPrefix(device.Headers.Get("Session-Id"), "client") {
		t.Fatalf("device mode = %#v", device.Headers)
	}

	session := run("session")
	if session.Headers.Get("X-Codex-Installation-Id") != deviceInstall ||
		session.Headers.Get("X-Codex-Window-Id") == "" ||
		session.Headers.Get("Session-Id") == clientSession ||
		session.Headers.Get("Thread-Id") == session.Headers.Get("Session-Id") {
		t.Fatalf("session mode = %#v", session.Headers)
	}
	sessionBody := assertBodyIDs(t, session, true)
	if session.Headers.Get("Session-Id") != sessionBody["session_id"] ||
		session.Headers.Get("Thread-Id") != sessionBody["thread_id"] {
		t.Fatalf("header/body identities diverged: headers=%#v body=%#v", session.Headers, sessionBody)
	}

	full := run("full")
	fullBody := assertBodyIDs(t, full, false)
	if full.Headers.Get("X-Codex-Installation-Id") != deviceInstall ||
		full.Headers.Get("Session-Id") != full.Headers.Get("Thread-Id") ||
		full.Headers.Get("Session-Id") != fullBody["session_id"] {
		t.Fatalf("full mode did not converge header/body identities: headers=%#v body=%#v", full.Headers, fullBody)
	}
}

func TestCodexIdentityExperimentDoesNotReuseFingerprintAcrossFailoverAccounts(t *testing.T) {
	host := &fakeAuthHost{
		entries: []cpaapi.HostAuthFileEntry{
			{AuthIndex: "auth-a", ID: "credential-a", Name: "codex-a.json", Provider: "codex", Type: "codex", AccountType: "oauth", Path: "/auth/codex-a.json"},
			{AuthIndex: "auth-b", ID: "credential-b", Name: "codex-b.json", Provider: "codex", Type: "codex", AccountType: "oauth", Path: "/auth/codex-b.json"},
		},
		details: map[string]cpaapi.HostAuthGetResponse{
			"auth-a": {AuthIndex: "auth-a", Name: "codex-a.json", Path: "/auth/codex-a.json", JSON: json.RawMessage(`{"type":"codex","codex_fingerprint_seed":"11111111-1111-4111-8111-111111111111"}`)},
			"auth-b": {AuthIndex: "auth-b", Name: "codex-b.json", Path: "/auth/codex-b.json", JSON: json.RawMessage(`{"type":"codex","codex_fingerprint_seed":"22222222-2222-4222-8222-222222222222"}`)},
		},
	}
	experiment := NewCodexIdentityExperiment(stubCodexSettings{ExperimentalCodexIdentitySettings{
		OutboundConvergenceEnabled: true,
		ConvergenceMode:            "full",
	}}, NewAccountService(host))
	request := func(authIndex string) cpaapi.RequestInterceptRequest {
		return cpaapi.RequestInterceptRequest{
			ToFormat: "codex",
			Headers: http.Header{
				"User-Agent": []string{"codex_cli_rs/0.144.1"},
				"Originator": []string{"codex_cli_rs"},
				"Session-Id": []string{"client-session"},
			},
			Body:     []byte(`{"client_metadata":{"session_id":"client-session"}}`),
			Metadata: map[string]any{"selected_auth_index": authIndex},
		}
	}
	firstA, changed := experiment.InterceptRequest(request("auth-a"))
	if !changed {
		t.Fatal("account A request was not transformed")
	}
	firstB, changed := experiment.InterceptRequest(request("auth-b"))
	if !changed {
		t.Fatal("account B request was not transformed")
	}
	repeatedA, changed := experiment.InterceptRequest(request("auth-a"))
	if !changed {
		t.Fatal("repeated account A request was not transformed")
	}
	installA := firstA.Headers.Get("X-Codex-Installation-Id")
	installB := firstB.Headers.Get("X-Codex-Installation-Id")
	if installA == "" || installB == "" || installA == installB {
		t.Fatalf("failover accounts reused installation identity: A=%q B=%q", installA, installB)
	}
	if repeatedA.Headers.Get("X-Codex-Installation-Id") != installA ||
		repeatedA.Headers.Get("Session-Id") != firstA.Headers.Get("Session-Id") ||
		repeatedA.Headers.Get("Thread-Id") != firstA.Headers.Get("Thread-Id") {
		t.Fatalf("account A identity was not stable across retries: first=%#v repeated=%#v", firstA.Headers, repeatedA.Headers)
	}
	if firstA.Headers.Get("Session-Id") == firstB.Headers.Get("Session-Id") ||
		firstA.Headers.Get("Thread-Id") == firstB.Headers.Get("Thread-Id") {
		t.Fatalf("failover accounts reused session/thread identity: A=%#v B=%#v", firstA.Headers, firstB.Headers)
	}
}

func TestCodexAccountIdentityIsolatesClientIDsBeforeFingerprint(t *testing.T) {
	settings := ExperimentalCodexIdentitySettings{OutboundConvergenceEnabled: true}
	seeds := map[string]string{
		"auth-a": "11111111-1111-4111-8111-111111111111",
		"auth-b": "22222222-2222-4222-8222-222222222222",
	}
	store := codexIdentitySeedFunc{resolve: func(_ context.Context, _ fingerprintSeedStore, account Account) (string, bool) {
		return seeds[account.ID], seeds[account.ID] != ""
	}, entries: []cpaapi.HostAuthFileEntry{
		{AuthIndex: "auth-a", ID: "credential-a", Name: "codex-a.json", Provider: "codex", Type: "codex", AccountType: "oauth", Path: "/auth/codex-a.json"},
		{AuthIndex: "auth-b", ID: "credential-b", Name: "codex-b.json", Provider: "codex", Type: "codex", AccountType: "oauth", Path: "/auth/codex-b.json"},
	}, details: map[string]cpaapi.HostAuthGetResponse{
		"auth-a": {Name: "codex-a.json", Path: "/auth/codex-a.json", JSON: json.RawMessage(`{"type":"codex","account_type":"oauth","codex_fingerprint_seed":"11111111-1111-4111-8111-111111111111"}`)},
		"auth-b": {Name: "codex-b.json", Path: "/auth/codex-b.json", JSON: json.RawMessage(`{"type":"codex","account_type":"oauth","codex_fingerprint_seed":"22222222-2222-4222-8222-222222222222"}`)},
	}}
	experiment := NewCodexIdentityExperiment(stubCodexSettings{settings}, NewAccountService(&store))
	request := func(authIndex string) cpaapi.RequestInterceptRequest {
		return cpaapi.RequestInterceptRequest{
			ToFormat: "codex",
			Headers: http.Header{
				"User-Agent":            []string{"codex_cli_rs/0.144.1"},
				"Originator":            []string{"codex_cli_rs"},
				"Session-Id":            []string{"client-session"},
				"Thread-Id":             []string{"client-thread"},
				"X-Codex-Turn-Metadata": []string{`{"installation_id":"client-install","session_id":"client-session","thread_id":"client-thread","turn_id":"client-turn","trace":"keep"}`},
			},
			Body:     []byte(`{"client_metadata":{"session_id":"body-session"},"prompt_cache_key":"cache-key"}`),
			Metadata: map[string]any{"selected_auth_index": authIndex},
		}
	}

	firstA, changedA := experiment.InterceptRequest(request("auth-a"))
	firstB, changedB := experiment.InterceptRequest(request("auth-b"))
	if !changedA || !changedB {
		t.Fatalf("identity requests were not transformed: A=%t B=%t", changedA, changedB)
	}
	sessionA := firstA.Headers.Get("Session-Id")
	sessionB := firstB.Headers.Get("Session-Id")
	if sessionA == "client-session" || sessionB == "client-session" || sessionA == sessionB ||
		strings.Contains(firstA.Headers.Get(codexFingerprintHeader), `"installation_id":"client-install"`) ||
		strings.Contains(firstB.Headers.Get(codexFingerprintHeader), `"installation_id":"client-install"`) {
		t.Fatalf("accounts were not independently isolated from client IDs: A=%#v B=%#v", firstA.Headers, firstB.Headers)
	}
	secondA, changedSecondA := experiment.InterceptRequest(request("auth-a"))
	if !changedSecondA {
		t.Fatal("seeded account request was not transformed")
	}
	if !strings.Contains(secondA.Headers.Get(codexFingerprintHeader), `"installation_id":"`) ||
		strings.Contains(secondA.Headers.Get(codexFingerprintHeader), `"installation_id":"client-install"`) {
		t.Fatalf("seeded account did not override host identity before convergence: %#v", secondA.Headers)
	}
}

func TestValidateExperimentalCodexIdentitySettings(t *testing.T) {
	valid := ExperimentalCodexIdentitySettings{
		MinVersion: "0.144.0", MaxVersion: "0.145.0",
		Whitelist:          `[{"originator":"opencode","ua_contains":["opencode/"]}]`,
		Blacklist:          `[{"originator":"evil"}]`,
		FingerprintSignals: `[{"type":"header_prefix","match":["x-codex-"],"required":true}]`,
	}
	if err := ValidateExperimentalCodexIdentitySettings(valid); err != nil {
		t.Fatalf("valid settings rejected: %v", err)
	}
	if err := ValidateExperimentalCodexIdentitySettings(ExperimentalCodexIdentitySettings{MinVersion: "latest"}); err == nil {
		t.Fatal("invalid version accepted")
	}
	if err := ValidateExperimentalCodexIdentitySettings(ExperimentalCodexIdentitySettings{MinVersion: "0.145.0", MaxVersion: "0.144.0"}); err == nil {
		t.Fatal("inverted version range accepted")
	}
	if err := ValidateExperimentalCodexIdentitySettings(ExperimentalCodexIdentitySettings{Whitelist: `[{"originator":"opencode"}]`}); err == nil {
		t.Fatal("single-factor whitelist accepted")
	}
}

func TestEnsureCodexFingerprintSeedIsStableAcrossConcurrentRequests(t *testing.T) {
	host := &codexSeedRaceHost{detail: cpaapi.HostAuthGetResponse{
		AuthIndex: "oauth-auth", Name: "codex.json", Path: "/auths/codex.json",
		JSON: json.RawMessage(`{"type":"codex","account_type":"oauth"}`),
	}}
	experiment := NewCodexIdentityExperiment(stubCodexSettings{}, host)

	const attempts = 32
	seeds := make(chan string, attempts)
	failures := make(chan error, attempts)
	for range attempts {
		go func() {
			account := Account{ID: "oauth-auth", AuthID: "oauth-auth", Name: "codex.json", AccountType: "oauth", path: "/auths/codex.json"}
			seed, ok := resolveCodexFingerprintSeed(context.Background(), experiment.seeds, account)
			if !ok {
				failures <- errors.New("seed resolution failed")
				return
			}
			seeds <- seed
		}()
	}
	for range attempts {
		select {
		case seed := <-seeds:
			if seed == "" || canonicalCodexFingerprintSeed(seed) == "" {
				t.Fatalf("invalid seed %q", seed)
			}
		case err := <-failures:
			t.Fatal(err)
		}
	}
	if host.saves != 1 || host.gets != 1 {
		t.Fatalf("seed creation calls: saves=%d gets=%d, want 1/1", host.saves, host.gets)
	}
	if host.seed == "" || canonicalCodexFingerprintSeed(host.seed) == "" {
		t.Fatalf("persisted seed invalid: %q", host.seed)
	}
}
