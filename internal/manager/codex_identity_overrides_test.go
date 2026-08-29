package manager

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"cpa-account-config-manager/internal/cpaapi"
)

func TestCodexIdentityOverrideServicePersistsReloadsAndClears(t *testing.T) {
	dir := t.TempDir()
	service := NewCodexIdentityOverrideService()
	service.Configure(Config{DataDir: dir})
	mode := "SESSION"
	gate := false
	allowAppServer := true
	if err := service.SetAccount("account-a", CodexIdentityOverride{
		ConvergenceMode:       &mode,
		IngressGateEnabled:    &gate,
		AllowAppServerClients: &allowAppServer,
	}); err != nil {
		t.Fatalf("save account override: %v", err)
	}
	providerMode := "device"
	if err := service.SetProvider("codex-api-key:provider-a", CodexIdentityOverride{ConvergenceMode: &providerMode}); err != nil {
		t.Fatalf("save provider override: %v", err)
	}

	info, err := os.Stat(filepath.Join(dir, "codex-identity-overrides.json"))
	if err != nil {
		t.Fatalf("stat override store: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("override store mode = %o, want 600", info.Mode().Perm())
	}

	reloaded := NewCodexIdentityOverrideService()
	reloaded.Configure(Config{DataDir: dir})
	account, ok := reloaded.Account("account-a")
	if !ok || account.ConvergenceMode == nil || *account.ConvergenceMode != "session" ||
		account.IngressGateEnabled == nil || *account.IngressGateEnabled ||
		account.AllowAppServerClients == nil || !*account.AllowAppServerClients {
		t.Fatalf("reloaded account override = %#v, found=%t", account, ok)
	}
	provider, ok := reloaded.Provider("codex-api-key:provider-a")
	if !ok || provider.ConvergenceMode == nil || *provider.ConvergenceMode != "device" {
		t.Fatalf("reloaded provider override = %#v, found=%t", provider, ok)
	}

	if err := reloaded.SetAccount("account-a", CodexIdentityOverride{}); err != nil {
		t.Fatalf("clear account override: %v", err)
	}
	if _, ok := reloaded.Account("account-a"); ok {
		t.Fatal("empty override did not restore account inheritance")
	}
}

func TestValidateCodexIdentityOverrideRejectsEmptyExplicitMode(t *testing.T) {
	empty := "  "
	if _, err := validateCodexIdentityOverride(CodexIdentityOverride{ConvergenceMode: &empty}); err == nil {
		t.Fatal("explicit empty convergence mode was accepted")
	}
}

func TestCodexIdentityOverridePrecedence(t *testing.T) {
	settings := ExperimentalCodexIdentitySettings{
		OutboundConvergenceEnabled: true,
		ConvergenceMode:            "full",
		IngressGateEnabled:         true,
		AllowAppServerClients:      true,
	}
	overrides := NewCodexIdentityOverrideService()
	overrides.Configure(Config{DataDir: t.TempDir()})
	experiment := NewCodexIdentityExperiment(stubCodexSettings{settings}, nil)
	experiment.SetOverrides(overrides)
	account := Account{ID: "account-a", AuthID: "auth-a", Provider: "codex"}
	gate := codexAccountWithMetadata{account: &account}
	if got := experiment.effectiveAccountFingerprintMode(gate); got != codexFingerprintFull {
		t.Fatalf("global fingerprint mode = %q, want full", got)
	}

	metadataMode := "session"
	gate.metadata = map[string]any{
		"codex_fingerprint_mode":          metadataMode,
		"codex_cli_only":                  false,
		"codex_cli_only_allow_app_server": false,
	}
	if got := experiment.effectiveAccountFingerprintMode(gate); got != codexFingerprintSession {
		t.Fatalf("metadata fingerprint mode = %q, want session", got)
	}
	if experiment.effectiveAccountIngressGate(gate) || experiment.effectiveAccountAllowAppServer(gate) {
		t.Fatal("metadata false values did not override global true values")
	}

	off := "off"
	falseValue := false
	if err := overrides.SetAccount(account.ID, CodexIdentityOverride{
		ConvergenceMode:       &off,
		IngressGateEnabled:    &falseValue,
		AllowAppServerClients: &falseValue,
	}); err != nil {
		t.Fatalf("save account override: %v", err)
	}
	if got := experiment.effectiveAccountFingerprintMode(gate); got != codexFingerprintOff {
		t.Fatalf("account fingerprint override = %q, want off", got)
	}
	if experiment.effectiveAccountIngressGate(gate) || experiment.effectiveAccountAllowAppServer(gate) {
		t.Fatal("account false overrides did not win")
	}

	providerMode := "device"
	if err := overrides.SetProvider("codex-api-key:provider-a", CodexIdentityOverride{ConvergenceMode: &providerMode}); err != nil {
		t.Fatalf("save provider override: %v", err)
	}
	if got := experiment.effectiveProviderFingerprintMode("codex-api-key:provider-a"); got != codexFingerprintDevice {
		t.Fatalf("provider fingerprint override = %q, want device", got)
	}
}

func TestCodexIdentityOverrideManagementAPI(t *testing.T) {
	service := NewCodexIdentityOverrideService()
	service.Configure(Config{DataDir: t.TempDir()})
	app := &App{codexIdentityOverrides: service}
	base := "/v0/management" + managementRoutePrefix + "/codex-identity-overrides"

	put := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPut,
		Path:   base + "/provider",
		Body:   []byte(`{"provider_key":"codex-api-key:provider-a","override":{"convergence_mode":"device","ingress_gate_enabled":false}}`),
	})
	if put.StatusCode != http.StatusOK {
		t.Fatalf("put status = %d, body=%s", put.StatusCode, put.Body)
	}
	get := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{Method: http.MethodGet, Path: base})
	if get.StatusCode != http.StatusOK {
		t.Fatalf("get status = %d, body=%s", get.StatusCode, get.Body)
	}
	var snapshot CodexIdentityOverrideSnapshot
	if err := json.Unmarshal(get.Body, &snapshot); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if value, ok := snapshot.Providers["codex-api-key:provider-a"]; !ok || value.ConvergenceMode == nil || *value.ConvergenceMode != "device" || value.IngressGateEnabled == nil || *value.IngressGateEnabled {
		t.Fatalf("provider snapshot = %#v", snapshot.Providers)
	}

	cleared := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPut,
		Path:   base + "/provider",
		Body:   []byte(`{"provider_key":"codex-api-key:provider-a","override":{}}`),
	})
	if cleared.StatusCode != http.StatusOK {
		t.Fatalf("clear status = %d, body=%s", cleared.StatusCode, cleared.Body)
	}
	if _, ok := service.Provider("codex-api-key:provider-a"); ok {
		t.Fatal("management clear did not restore provider inheritance")
	}

	missing := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{Method: http.MethodGet, Path: base + "/extra"})
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("unexpected route status = %d", missing.StatusCode)
	}
}

func TestCodexIdentityProviderIngressOverrideWinsOverGlobalSetting(t *testing.T) {
	overrides := NewCodexIdentityOverrideService()
	overrides.Configure(Config{DataDir: t.TempDir()})
	disabled := false
	if err := overrides.SetProvider("codex-api-key:provider-auth", CodexIdentityOverride{IngressGateEnabled: &disabled}); err != nil {
		t.Fatalf("save provider override: %v", err)
	}
	experiment := NewCodexIdentityExperiment(stubCodexSettings{ExperimentalCodexIdentitySettings{
		IngressGateEnabled: true,
	}}, NewAccountService(&fakeAuthHost{}))
	experiment.SetOverrides(overrides)

	response, changed := experiment.InterceptRequest(cpaapi.RequestInterceptRequest{
		ToFormat: "codex",
		Headers:  http.Header{"User-Agent": []string{"unofficial-client/1.0"}},
		Metadata: map[string]any{"selected_auth_index": "provider-auth"},
	})
	if !changed || response.Terminate {
		t.Fatalf("provider false override did not disable global ingress gate: changed=%t response=%#v", changed, response)
	}
}

func TestCodexIdentityProviderIngressOverrideRejectsUnofficialClient(t *testing.T) {
	overrides := NewCodexIdentityOverrideService()
	overrides.Configure(Config{DataDir: t.TempDir()})
	enabled := true
	if err := overrides.SetProvider("codex-api-key:provider-auth", CodexIdentityOverride{IngressGateEnabled: &enabled}); err != nil {
		t.Fatalf("save provider override: %v", err)
	}
	experiment := NewCodexIdentityExperiment(stubCodexSettings{}, NewAccountService(&fakeAuthHost{}))
	experiment.SetOverrides(overrides)

	response, changed := experiment.InterceptRequest(cpaapi.RequestInterceptRequest{
		ToFormat: "codex",
		Headers:  http.Header{"User-Agent": []string{"unofficial-client/1.0"}},
		Metadata: map[string]any{"selected_auth_id": "provider-auth"},
	})
	if !changed || !response.Terminate || response.StatusCode != http.StatusForbidden {
		t.Fatalf("provider ingress override did not reject unofficial client: changed=%t response=%#v", changed, response)
	}
}

func TestCodexIdentityProviderAppServerOverrideAllowsAndDenies(t *testing.T) {
	overrides := NewCodexIdentityOverrideService()
	overrides.Configure(Config{DataDir: t.TempDir()})
	enabled := true
	allow := true
	if err := overrides.SetProvider("codex-api-key:provider-auth", CodexIdentityOverride{
		IngressGateEnabled:    &enabled,
		AllowAppServerClients: &allow,
	}); err != nil {
		t.Fatalf("save provider override: %v", err)
	}
	experiment := NewCodexIdentityExperiment(stubCodexSettings{}, NewAccountService(&fakeAuthHost{}))
	experiment.SetOverrides(overrides)
	request := cpaapi.RequestInterceptRequest{
		ToFormat: "codex",
		Headers: http.Header{
			"User-Agent":   []string{"app-server/1.0"},
			"X-Codex-Test": []string{"present"},
		},
		Metadata: map[string]any{"selected_auth_index": "provider-auth"},
	}
	if response, changed := experiment.InterceptRequest(request); !changed || response.Terminate {
		t.Fatalf("explicit App Server allow was not honored: changed=%t response=%#v", changed, response)
	}

	deny := false
	if err := overrides.SetProvider("codex-api-key:provider-auth", CodexIdentityOverride{
		IngressGateEnabled:    &enabled,
		AllowAppServerClients: &deny,
	}); err != nil {
		t.Fatalf("save provider deny override: %v", err)
	}
	if response, changed := experiment.InterceptRequest(request); !changed || !response.Terminate || response.StatusCode != http.StatusForbidden {
		t.Fatalf("explicit App Server deny was not honored: changed=%t response=%#v", changed, response)
	}
}

func TestCodexIdentityProviderFingerprintModesAreStableWithoutAuthWrites(t *testing.T) {
	host := &fakeAuthHost{}
	overrides := NewCodexIdentityOverrideService()
	overrides.Configure(Config{DataDir: t.TempDir()})
	experiment := NewCodexIdentityExperiment(stubCodexSettings{}, NewAccountService(host))
	experiment.SetOverrides(overrides)
	request := cpaapi.RequestInterceptRequest{
		ToFormat: "codex",
		Headers: http.Header{
			"User-Agent":            []string{"codex_cli_rs/0.144.1"},
			"Originator":            []string{"codex_cli_rs"},
			"Session-Id":            []string{"client-session"},
			"X-Codex-Window-Id":     []string{"client-window"},
			"X-Client-Request-Id":   []string{"client-request"},
			"X-Codex-Turn-Metadata": []string{`{"installation_id":"client-install","session_id":"client-session"}`},
		},
		Body:     []byte(`{"client_metadata":{"session_id":"client-session"},"prompt_cache_key":"client-session"}`),
		Metadata: map[string]any{"selected_auth_index": "provider-auth"},
	}

	for _, mode := range []string{"off", "device", "session", "full"} {
		t.Run(mode, func(t *testing.T) {
			if err := overrides.SetProvider("codex-api-key:provider-auth", CodexIdentityOverride{ConvergenceMode: &mode}); err != nil {
				t.Fatalf("save %s override: %v", mode, err)
			}
			first, changed := experiment.InterceptRequest(request)
			if !changed || first.Terminate {
				t.Fatalf("first %s request = changed %t, response %#v", mode, changed, first)
			}
			second, changed := experiment.InterceptRequest(request)
			if !changed || second.Terminate {
				t.Fatalf("second %s request = changed %t, response %#v", mode, changed, second)
			}
			if mode == "off" {
				if first.Headers.Get("X-Codex-Installation-Id") != "" {
					t.Fatalf("off mode emitted a converged device identity: %#v", first.Headers)
				}
				return
			}
			for _, header := range []string{"X-Codex-Installation-Id", "Session-Id", "Thread-Id"} {
				if first.Headers.Get(header) != second.Headers.Get(header) {
					t.Fatalf("%s mode produced unstable %s: %q != %q", mode, header, first.Headers.Get(header), second.Headers.Get(header))
				}
			}
			if mode == "session" || mode == "full" {
				var firstMetadata, secondMetadata map[string]any
				if err := json.Unmarshal([]byte(first.Headers.Get(codexFingerprintHeader)), &firstMetadata); err != nil {
					t.Fatalf("decode first %s metadata: %v", mode, err)
				}
				if err := json.Unmarshal([]byte(second.Headers.Get(codexFingerprintHeader)), &secondMetadata); err != nil {
					t.Fatalf("decode second %s metadata: %v", mode, err)
				}
				for _, field := range []string{"installation_id", "session_id", "thread_id", "window_id"} {
					if firstMetadata[field] != secondMetadata[field] {
						t.Fatalf("%s mode produced unstable metadata %s: %#v != %#v", mode, field, firstMetadata[field], secondMetadata[field])
					}
				}
			}
			if first.Headers.Get("X-Codex-Installation-Id") == "" {
				t.Fatalf("%s mode did not emit a device identity", mode)
			}
		})
	}

	host.mu.Lock()
	defer host.mu.Unlock()
	if len(host.saves) != 0 || len(host.saveCalls) != 0 {
		t.Fatalf("provider pseudo-account wrote CPA auth storage: saves=%d calls=%#v", len(host.saves), host.saveCalls)
	}
}
