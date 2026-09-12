package manager

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"cpa-account-config-manager/internal/cpaapi"
)

// codexProfileTestReset restores the compiled defaults after a test changed the
// process-wide profile snapshot.
func codexProfileTestReset(t *testing.T) {
	t.Helper()
	defaults := defaultCodexFingerprintValues()
	storeCodexFingerprintValues(defaults)
	t.Cleanup(func() { storeCodexFingerprintValues(defaults) })
}

func TestCodexFingerprintProfileDefaultsAreVisibleAndEditable(t *testing.T) {
	codexProfileTestReset(t)
	service := NewCodexFingerprintProfileService()
	service.Configure(Config{DataDir: t.TempDir()})

	profile := service.Snapshot()
	if len(profile.Fields) == 0 {
		t.Fatalf("profile has no fields")
	}
	byKey := map[string]CodexFingerprintField{}
	for _, field := range profile.Fields {
		byKey[field.Key] = field
		if field.Default == "" && field.Kind != codexFingerprintKindText {
			t.Fatalf("field %s has no visible default: %#v", field.Key, field)
		}
		if field.Value != field.Default || field.Overridden {
			t.Fatalf("field %s is not at its default: %#v", field.Key, field)
		}
	}
	// Every value the outbound path hard coded before must be exposed with its
	// compiled value as the default.
	for key, want := range map[string]string{
		codexFingerprintFieldMode:                      string(codexFingerprintOff),
		codexFingerprintFieldUserAgent:                 defaultCodexCLIUserAgent,
		codexFingerprintFieldOriginator:                defaultCodexOriginator,
		codexFingerprintFieldVersion:                   codexCLIVersion,
		codexFingerprintFieldOpenAIBeta:                defaultCodexOpenAIBeta,
		codexFingerprintFieldTurnMetadataHeader:        codexFingerprintHeader,
		codexFingerprintFieldWindowSuffix:              defaultCodexWindowSuffix,
		codexFingerprintFieldInstallPrefix:             defaultCodexInstallPrefix,
		codexFingerprintFieldSessionPrefix:             defaultCodexSessionPrefix,
		codexFingerprintFieldThreadPrefix:              defaultCodexThreadPrefix,
		codexFingerprintFieldIncludeTurnStartedAt:      "true",
		codexFingerprintFieldIncludeRelationshipFields: "true",
		codexFingerprintFieldRewritePromptCacheKey:     "true",
	} {
		field, ok := byKey[key]
		if !ok {
			t.Fatalf("field %s is missing from the profile", key)
		}
		if field.Default != want {
			t.Fatalf("field %s default = %q, want %q", key, field.Default, want)
		}
	}

	// An edit changes the effective values and is reported as overridden.
	updated, errSet := service.Set(map[string]string{
		codexFingerprintFieldUserAgent:  "custom-agent/9.9 (Test OS) testterm",
		codexFingerprintFieldOriginator: "custom-originator",
	})
	if errSet != nil {
		t.Fatalf("set: %v", errSet)
	}
	if updated.OverriddenFields != 2 {
		t.Fatalf("overridden count = %d, want 2", updated.OverriddenFields)
	}
	values := service.EffectiveValues()
	if values.userAgent != "custom-agent/9.9 (Test OS) testterm" || values.originator != "custom-originator" {
		t.Fatalf("effective values = %#v", values)
	}
	if CodexFingerprintUserAgent() != values.userAgent || CodexFingerprintOriginator() != values.originator {
		t.Fatalf("the outbound accessors did not follow the profile")
	}

	// One field can be restored alone; the other override survives.
	restored, errReset := service.Reset([]string{codexFingerprintFieldUserAgent})
	if errReset != nil {
		t.Fatalf("reset one: %v", errReset)
	}
	if restored.OverriddenFields != 1 {
		t.Fatalf("overridden count after single reset = %d", restored.OverriddenFields)
	}
	if CodexFingerprintUserAgent() != defaultCodexCLIUserAgent {
		t.Fatalf("user agent was not restored: %q", CodexFingerprintUserAgent())
	}
	if CodexFingerprintOriginator() != "custom-originator" {
		t.Fatalf("the unrelated override was cleared")
	}

	// Reset with an empty list restores everything.
	all, errResetAll := service.Reset(nil)
	if errResetAll != nil {
		t.Fatalf("reset all: %v", errResetAll)
	}
	if all.OverriddenFields != 0 {
		t.Fatalf("overridden count after reset all = %d", all.OverriddenFields)
	}
	if values := service.EffectiveValues(); values.userAgent != defaultCodexCLIUserAgent || values.originator != defaultCodexOriginator {
		t.Fatalf("defaults were not restored: %#v", values)
	}
}

func TestCodexFingerprintProfilePersistsAndValidates(t *testing.T) {
	codexProfileTestReset(t)
	dataDir := t.TempDir()
	service := NewCodexFingerprintProfileService()
	service.Configure(Config{DataDir: dataDir})

	if _, errSet := service.Set(map[string]string{codexFingerprintFieldInstallPrefix: "custom-install-prefix:v9:"}); errSet != nil {
		t.Fatalf("set prefix: %v", errSet)
	}
	// Invalid values are rejected and change nothing.
	for key, value := range map[string]string{
		codexFingerprintFieldMode:                 "sometimes",
		codexFingerprintFieldInstallationID:       "not-a-uuid",
		codexFingerprintFieldSeedStrategy:         "random",
		codexFingerprintFieldTurnMetadataHeader:   "Bad Header: Name",
		codexFingerprintFieldIncludeTurnStartedAt: "yes",
	} {
		if _, errSet := service.Set(map[string]string{key: value}); errSet == nil {
			t.Fatalf("field %s accepted the invalid value %q", key, value)
		}
	}
	if _, errSet := service.Set(map[string]string{"unknown_field": "x"}); errSet == nil {
		t.Fatalf("an unknown field was accepted")
	}
	if service.OverriddenFields() != 1 {
		t.Fatalf("a rejected value was stored")
	}

	// A new instance reads the persisted override back.
	restored := NewCodexFingerprintProfileService()
	restored.Configure(Config{DataDir: dataDir})
	if restored.OverriddenFields() != 1 {
		t.Fatalf("override did not persist")
	}
	if restored.EffectiveValues().installPrefix != "custom-install-prefix:v9:" {
		t.Fatalf("persisted value = %q", restored.EffectiveValues().installPrefix)
	}
	// The stored file must not be world readable.
	info, errStat := statCodexProfileStore(dataDir)
	if errStat != nil || info != 0o600 {
		t.Fatalf("store permissions = %v err=%v", info, errStat)
	}
}

// A changed derivation prefix must change the ids the request path produces, and
// an explicit id must win over derivation.
func TestCodexFingerprintProfileChangesDerivedIDs(t *testing.T) {
	codexProfileTestReset(t)
	service := NewCodexFingerprintProfileService()
	service.Configure(Config{DataDir: t.TempDir()})
	account := Account{ID: "acct-1", Provider: "codex", Type: "codex"}

	base := resolveCodexFingerprintIDs(account, "seed-value", "", codexFingerprintDevice)
	if base == nil {
		// The device mode needs an installation id; without a device or a
		// derivation it returns nil, so assert through session mode instead.
		base = resolveCodexFingerprintIDs(account, "seed-value", "", codexFingerprintSession)
	}
	if base == nil {
		t.Fatalf("no fingerprint ids were produced")
	}
	defaultInstall := base.installationID

	if _, errSet := service.Set(map[string]string{codexFingerprintFieldInstallPrefix: "custom:v9:"}); errSet != nil {
		t.Fatalf("set: %v", errSet)
	}
	changed := resolveCodexFingerprintIDs(account, "seed-value", "", codexFingerprintSession)
	if changed == nil || changed.installationID == defaultInstall {
		t.Fatalf("the derivation prefix did not change the installation id")
	}

	// An explicit installation id wins over derivation and is a valid UUID.
	explicit := "11111111-2222-4333-8444-555555555555"
	if _, errSet := service.Set(map[string]string{codexFingerprintFieldInstallationID: explicit}); errSet != nil {
		t.Fatalf("set explicit id: %v", errSet)
	}
	fixed := resolveCodexFingerprintIDs(account, "seed-value", "", codexFingerprintSession)
	if fixed == nil || fixed.installationID != explicit {
		t.Fatalf("explicit installation id was ignored: %#v", fixed)
	}

	// The window suffix and the relationship fields are configurable too.
	if _, errSet := service.Set(map[string]string{codexFingerprintFieldWindowSuffix: ":7"}); errSet != nil {
		t.Fatalf("set suffix: %v", errSet)
	}
	suffixed := resolveCodexFingerprintIDs(account, "seed-value", "", codexFingerprintSession)
	if suffixed == nil || !strings.HasSuffix(suffixed.windowID, ":7") {
		t.Fatalf("window suffix was ignored: %#v", suffixed)
	}
	if _, errSet := service.Set(map[string]string{codexFingerprintFieldIncludeRelationshipFields: "false"}); errSet != nil {
		t.Fatalf("set relationship toggle: %v", errSet)
	}
	fields := codexFingerprintMetadataFields(suffixed)
	addCodexConvergenceRelationshipFields(fields, suffixed)
	if _, exists := fields["parent_thread_id"]; exists {
		t.Fatalf("relationship fields were emitted while disabled: %#v", fields)
	}
	if _, errSet := service.Set(map[string]string{codexFingerprintFieldIncludeTurnStartedAt: "false"}); errSet != nil {
		t.Fatalf("set turn timestamp toggle: %v", errSet)
	}
	if _, exists := codexFingerprintMetadataFields(suffixed)["turn_started_at_unix_ms"]; exists {
		t.Fatalf("the turn timestamp was emitted while disabled")
	}
}

func TestCodexFingerprintProfileRoutesRequireKeyAndExposeDefaults(t *testing.T) {
	codexProfileTestReset(t)
	app := NewApp(&fakeAuthHost{}, nil)
	app.Configure([]byte("data_dir: " + t.TempDir()))

	unauthorized := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodGet, Path: "/v0/management" + managementRoutePrefix + "/codex/fingerprint",
	})
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status without key = %d", unauthorized.StatusCode)
	}

	headers := http.Header{"Authorization": []string{"Bearer management-secret"}}
	read := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodGet, Path: "/v0/management" + managementRoutePrefix + "/codex/fingerprint", Headers: headers,
	})
	if read.StatusCode != http.StatusOK {
		t.Fatalf("read status = %d body=%s", read.StatusCode, read.Body)
	}
	var payload struct {
		Profile CodexFingerprintProfile `json:"profile"`
	}
	if errDecode := json.Unmarshal(read.Body, &payload); errDecode != nil {
		t.Fatalf("decode: %v", errDecode)
	}
	if len(payload.Profile.Fields) == 0 || payload.Profile.OverriddenFields != 0 {
		t.Fatalf("profile payload = %#v", payload.Profile)
	}

	write := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPut, Path: "/v0/management" + managementRoutePrefix + "/codex/fingerprint", Headers: headers,
		Body: []byte(`{"values":{"originator":"operator-choice"}}`),
	})
	if write.StatusCode != http.StatusOK {
		t.Fatalf("write status = %d body=%s", write.StatusCode, write.Body)
	}
	if CodexFingerprintOriginator() != "operator-choice" {
		t.Fatalf("the request did not change the effective profile")
	}
	reset := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPost, Path: "/v0/management" + managementRoutePrefix + "/codex/fingerprint/reset", Headers: headers,
		Body: []byte(`{"keys":[]}`),
	})
	if reset.StatusCode != http.StatusOK {
		t.Fatalf("reset status = %d body=%s", reset.StatusCode, reset.Body)
	}
	if CodexFingerprintOriginator() != defaultCodexOriginator {
		t.Fatalf("reset did not restore the default")
	}
	// An invalid value is a client error, not a silent write.
	invalid := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPut, Path: "/v0/management" + managementRoutePrefix + "/codex/fingerprint", Headers: headers,
		Body: []byte(`{"values":{"mode":"sometimes"}}`),
	})
	if invalid.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid write status = %d body=%s", invalid.StatusCode, invalid.Body)
	}
}

// The profile's mode acts as the operator-set global default, so convergence can be
// switched on from the Codex workspace without enabling the experimental flag, and
// an explicit per-account override still wins.
func TestCodexFingerprintProfileModeDrivesConvergenceForAccountsAndProviders(t *testing.T) {
	codexProfileTestReset(t)
	app := NewApp(&fakeAuthHost{}, nil)
	app.Configure([]byte("data_dir: " + t.TempDir()))
	codexTransformer := findCodexIdentityTransformer(app.requestHooks)
	if codexTransformer == nil {
		t.Fatalf("the Codex identity transformer is not registered")
	}
	account := Account{ID: "acct-mode", AuthID: "acct-mode", Provider: "codex", Type: "codex", AccountType: "oauth"}

	// Default: off, matching the compiled behavior.
	if mode := codexTransformer.effectiveAccountFingerprintMode(codexAccountWithMetadata{account: &account}); mode != codexFingerprintOff {
		t.Fatalf("default account mode = %q", mode)
	}
	if _, errSet := app.codexFingerprints.Set(map[string]string{codexFingerprintFieldMode: "session"}); errSet != nil {
		t.Fatalf("set mode: %v", errSet)
	}
	if mode := codexTransformer.effectiveAccountFingerprintMode(codexAccountWithMetadata{account: &account}); mode != codexFingerprintSession {
		t.Fatalf("profile mode did not reach the account path: %q", mode)
	}
	if mode := codexTransformer.effectiveProviderFingerprintMode("codex-api-key:prov-mode"); mode != codexFingerprintSession {
		t.Fatalf("profile mode did not reach the provider path: %q", mode)
	}
	// A per-account override still wins over the profile default.
	if errOverride := app.codexIdentityOverrides.SetAccount("acct-mode", CodexIdentityOverride{ConvergenceMode: codexTestStringPointer("full")}); errOverride != nil {
		t.Fatalf("set override: %v", errOverride)
	}
	if mode := codexTransformer.effectiveAccountFingerprintMode(codexAccountWithMetadata{account: &account}); mode != codexFingerprintFull {
		t.Fatalf("the per-account override was ignored: %q", mode)
	}
	// Restoring the default returns the profile value, not the experiment value.
	if _, errReset := app.codexFingerprints.Reset(nil); errReset != nil {
		t.Fatalf("reset: %v", errReset)
	}
	if mode := codexTransformer.effectiveProviderFingerprintMode("codex-api-key:prov-mode"); mode != codexFingerprintOff {
		t.Fatalf("reset did not restore the compiled default: %q", mode)
	}
}

func codexTestStringPointer(value string) *string { return &value }

func statCodexProfileStore(dataDir string) (uint32, error) {
	info, errStat := os.Stat(codexFingerprintProfileStorePath(dataDir))
	if errStat != nil {
		return 0, errStat
	}
	return uint32(info.Mode().Perm()), nil
}
