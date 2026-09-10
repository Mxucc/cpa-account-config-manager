package manager

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"cpa-account-config-manager/internal/cpaapi"
)

func writeJSONFile(t *testing.T, path string, payload any) {
	t.Helper()
	raw, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		t.Fatalf("marshal %s: %v", path, errMarshal)
	}
	if errWrite := os.WriteFile(path, raw, 0o600); errWrite != nil {
		t.Fatalf("write %s: %v", path, errWrite)
	}
}

// The permanent global policy must not carry a Codex identity copy: the settings
// used to live in two places, and the policy copy silently became authoritative
// whenever that policy was enabled.
func TestGlobalPolicyDropsIncomingCodexIdentityCopy(t *testing.T) {
	dir := t.TempDir()
	service := NewGlobalPolicyService()
	service.Configure(Config{DataDir: dir})
	identity := ExperimentalCodexIdentitySettings{
		OutboundConvergenceEnabled: true,
		IngressGateEnabled:         true,
		ConvergenceMode:            "session",
	}
	identityJSON, errMarshal := json.Marshal(identity)
	if errMarshal != nil {
		t.Fatalf("marshal identity: %v", errMarshal)
	}
	body := []byte(`{"enabled":true,"codex_identity":` + string(identityJSON) + `}`)
	app := &App{globalPolicy: service}
	response := app.HandleManagement(t.Context(), cpaapi.ManagementRequest{
		Method: http.MethodPut,
		Path:   "/v0/management/plugins/cpa-account-config-manager/global-policy",
		Body:   body,
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("put global policy status = %d, body=%s", response.StatusCode, response.Body)
	}

	reloaded := NewGlobalPolicyService()
	reloaded.Configure(Config{DataDir: dir})
	snapshot := reloaded.Snapshot()
	if !globalIdentityEmpty(snapshot.Policy.CodexIdentity) {
		t.Fatalf("global policy kept an identity copy: %#v", snapshot.Policy.CodexIdentity)
	}
	if !snapshot.Policy.Enabled {
		t.Fatal("global policy lost its unrelated fields")
	}
}

// An installation that configured the identity policy through the former global
// policy keeps that effective value, and the automatically derived per-account
// copies are removed so the single global switch applies again.
func TestCodexIdentityMigratesFromGlobalPolicyAndDropsDerivedOverrides(t *testing.T) {
	dir := t.TempDir()
	identity := ExperimentalCodexIdentitySettings{
		OutboundConvergenceEnabled: true,
		IngressGateEnabled:         true,
		ConvergenceMode:            "session",
	}
	writeJSONFile(t, filepath.Join(dir, "global-policy.json"), map[string]any{
		"version": 1,
		"policy": map[string]any{
			"enabled":        true,
			"codex_identity": identity,
		},
	})
	// One override is the exact copy the plugin derived from the global policy;
	// the other was authored for a single account and must survive.
	writeJSONFile(t, filepath.Join(dir, "codex-identity-overrides.json"), map[string]any{
		"version": 1,
		"accounts": map[string]any{
			"auth-derived":  map[string]any{"convergence_mode": "session", "ingress_gate_enabled": true},
			"auth-explicit": map[string]any{"convergence_mode": "device", "ingress_gate_enabled": false},
		},
	})

	app := NewApp(&fakeAuthHost{}, nil)
	app.ConfigureHost([]byte("data_dir: "+dir), cpaapi.SchemaVersion)

	migrated := app.experiments.CodexIdentity()
	if !migrated.OutboundConvergenceEnabled || !migrated.IngressGateEnabled || migrated.ConvergenceMode != "session" {
		t.Fatalf("migrated identity = %#v", migrated)
	}
	snapshot := app.codexIdentityOverrides.Snapshot()
	if _, exists := snapshot.Accounts["auth-derived"]; exists {
		t.Fatalf("derived account override survived: %#v", snapshot.Accounts)
	}
	explicit, exists := snapshot.Accounts["auth-explicit"]
	if !exists || explicit.ConvergenceMode == nil || *explicit.ConvergenceMode != "device" || explicit.IngressGateEnabled == nil || *explicit.IngressGateEnabled {
		t.Fatalf("explicit account override was not preserved: %#v", snapshot.Accounts)
	}

	// The active settings provider must now serve the migrated value even though
	// the global policy is enabled.
	if current := codexIdentitySettingsSnapshot(); !current.OutboundConvergenceEnabled || !current.IngressGateEnabled {
		t.Fatalf("runtime settings = %#v", current)
	}
}

func TestAdoptCodexIdentityIgnoresEmptyValue(t *testing.T) {
	service := NewExperimentalSettingsService()
	service.Configure(Config{DataDir: t.TempDir()})
	if _, errSet := service.Set(ExperimentalSettings{CodexIdentity: ExperimentalCodexIdentitySettings{IngressGateEnabled: true}}); errSet != nil {
		t.Fatalf("seed settings: %v", errSet)
	}
	if errAdopt := service.AdoptCodexIdentity(ExperimentalCodexIdentitySettings{}); errAdopt != nil {
		t.Fatalf("adopt empty: %v", errAdopt)
	}
	if current := service.CodexIdentity(); !current.IngressGateEnabled {
		t.Fatalf("empty migration cleared the configured identity: %#v", current)
	}
}

func TestDropMatchingAccountsKeepsDifferentOverrides(t *testing.T) {
	service := NewCodexIdentityOverrideService()
	service.Configure(Config{DataDir: t.TempDir()})
	mode := "session"
	enabled := true
	match := CodexIdentityOverride{ConvergenceMode: &mode, IngressGateEnabled: &enabled}
	if errSet := service.SetAccount("auth-match", match); errSet != nil {
		t.Fatalf("set matching override: %v", errSet)
	}
	device := "device"
	if errSet := service.SetAccount("auth-different", CodexIdentityOverride{ConvergenceMode: &device}); errSet != nil {
		t.Fatalf("set different override: %v", errSet)
	}
	if removed := service.DropMatchingAccounts(match); removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if _, exists := service.Account("auth-match"); exists {
		t.Fatal("matching override survived")
	}
	if value, exists := service.Account("auth-different"); !exists || value.ConvergenceMode == nil || *value.ConvergenceMode != "device" {
		t.Fatalf("different override = %#v, exists=%t", value, exists)
	}
}
