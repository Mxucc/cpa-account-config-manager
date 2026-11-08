package manager

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"cpa-account-config-manager/internal/cpaapi"
	"cpa-account-config-manager/internal/magpiecompat"
	"cpa-account-config-manager/internal/provider"
)

type fakeHookRunner struct {
	input  provider.MagpiePluginInput
	err    error
	probed []string
}

func (f *fakeHookRunner) Probe(directory string) (provider.MagpiePluginInput, error) {
	f.probed = append(f.probed, directory)
	if f.err != nil {
		return provider.MagpiePluginInput{}, f.err
	}
	return f.input, nil
}

// importFixturePlugin imports a fixture plugin from the data directory's plugin
// folder, which is the only place the importer accepts.
func importFixturePlugin(t *testing.T, app *App) string {
	t.Helper()
	// The store reports where plugin code lives, which is the plugin's stable home
	// rather than the per-run data directory.
	pluginDir := filepath.Join(app.providerImportStore().PluginsDir(), "fixture")
	if errMkdir := os.MkdirAll(pluginDir, 0o755); errMkdir != nil {
		t.Fatalf("mkdir plugin: %v", errMkdir)
	}
	manifest := `{"name":"@fixture/magpie-provider","version":"0.0.1","main":"./index.mjs"}`
	if errWrite := os.WriteFile(filepath.Join(pluginDir, "index.mjs"), []byte("export const X = 1;\n"), 0o600); errWrite != nil {
		t.Fatalf("write plugin: %v", errWrite)
	}
	body, errMarshal := json.Marshal(map[string]any{
		"manifest":  manifest,
		"source":    "magpie",
		"directory": pluginDir,
	})
	if errMarshal != nil {
		t.Fatalf("marshal: %v", errMarshal)
	}
	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPost,
		Path:   providerRoutePrefix + "/import",
		Body:   body,
	})
	decoded := decodeProviderResponse(t, response)
	entry, ok := decoded["provider"].(map[string]any)
	if !ok {
		t.Fatalf("provider = %#v", decoded["provider"])
	}
	return entry["id"].(string)
}

func accountIDs(t *testing.T, app *App) []string {
	t.Helper()
	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   providerRoutePrefix + "/accounts",
	})
	decoded := decodeProviderResponse(t, response)
	rows, ok := decoded["accounts"].([]any)
	if !ok {
		t.Fatalf("accounts = %#v", decoded["accounts"])
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.(map[string]any)["id"].(string))
	}
	return ids
}

func TestProviderSettingsDefaultToHooksOff(t *testing.T) {
	app := newProviderTestApp(t)
	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   providerRoutePrefix + "/settings",
	})
	decoded := decodeProviderResponse(t, response)
	if decoded["magpie_hook_execution"] != false {
		t.Fatalf("magpie_hook_execution = %v, want false by default", decoded["magpie_hook_execution"])
	}
}

func TestHooksOffKeepTheAccountListUnchanged(t *testing.T) {
	app := newProviderTestApp(t)
	runner := &fakeHookRunner{input: provider.MagpiePluginInput{Models: []string{"m1", "m2"}}}
	app.setPluginHookRunner(runner)
	app.hookRuntimeAvailable = func() bool { return true }

	importFixturePlugin(t, app)
	before := accountIDs(t, app)
	if len(runner.probed) != 0 {
		t.Fatalf("a probe ran while the switch was off: %v", runner.probed)
	}
	if len(before) != 0 {
		t.Fatalf("account ids = %v, want none from a manifest-only import", before)
	}

	// The catalog must say the hooks were not executed.
	catalog := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   providerRoutePrefix + "/catalog",
	})
	decoded := decodeProviderResponse(t, catalog)
	if decoded["hook_execution_enabled"] != false {
		t.Fatalf("hook_execution_enabled = %v", decoded["hook_execution_enabled"])
	}
	imported := decoded["imported"].([]any)
	entry := imported[0].(map[string]any)
	if entry["hook_execution"] != false {
		t.Fatalf("entry hook_execution = %v", entry["hook_execution"])
	}
}

func TestHooksOnAddThePluginAccountWithModelsAndWindows(t *testing.T) {
	app := newProviderTestApp(t)
	used := 1.5
	limit := 10.0
	balance := 4.25
	runner := &fakeHookRunner{input: provider.MagpiePluginInput{
		Models:  []string{"fx-alpha", "fx-beta"},
		Methods: []string{"Device code"},
		Windows: []provider.MagpieWindow{
			{Name: "five_hour", Used: &used, Limit: &limit, Unit: "USD", Reset: "2026-11-08T18:00:00Z"},
			{Name: "wallet", Balance: &balance, Unit: "USD"},
		},
	}}
	app.setPluginHookRunner(runner)
	app.hookRuntimeAvailable = func() bool { return true }

	providerID := importFixturePlugin(t, app)
	if errEnable := app.providerSettingsService().Set(ProviderSettings{MagpieHookExecution: true}); errEnable != nil {
		t.Fatalf("enable hooks: %v", errEnable)
	}

	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   providerRoutePrefix + "/accounts",
	})
	decoded := decodeProviderResponse(t, response)
	rows := decoded["accounts"].([]any)
	if len(rows) != 1 {
		t.Fatalf("accounts = %#v, want the plugin account", rows)
	}
	account := rows[0].(map[string]any)
	if account["id"] != provider.AccountID(providerID, "plugin") {
		t.Fatalf("id = %v", account["id"])
	}
	models := account["models"].(map[string]any)
	if models["count"].(float64) != 2 || models["source"] != "plugin" {
		t.Fatalf("models = %#v", models)
	}
	quota := account["quota"].([]any)
	if len(quota) != 2 {
		t.Fatalf("quota = %#v", quota)
	}
	first := quota[0].(map[string]any)
	if first["used_percent"].(float64) != 15 {
		t.Fatalf("1.5 of 10 must be 15%%, got %#v", first)
	}
	second := quota[1].(map[string]any)
	if second["balance"].(float64) != 4.25 || second["used_percent"] != nil {
		t.Fatalf("wallet window must carry a balance and no share: %#v", second)
	}
	billing := account["billing"].(map[string]any)
	if billing["mode"] != "wallet" {
		t.Fatalf("billing = %#v", billing)
	}
}

func TestHooksOnReportAProbeFailureAndKeepTheList(t *testing.T) {
	app := newProviderTestApp(t)
	app.setPluginHookRunner(&fakeHookRunner{err: magpiecompat.ErrTimeout})
	app.hookRuntimeAvailable = func() bool { return true }
	importFixturePlugin(t, app)
	if errEnable := app.providerSettingsService().Set(ProviderSettings{MagpieHookExecution: true}); errEnable != nil {
		t.Fatalf("enable hooks: %v", errEnable)
	}

	if ids := accountIDs(t, app); len(ids) != 0 {
		t.Fatalf("a timed-out probe must not add an account: %v", ids)
	}
	catalog := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   providerRoutePrefix + "/catalog",
	})
	entry := decodeProviderResponse(t, catalog)["imported"].([]any)[0].(map[string]any)
	if entry["hook_error"] != magpiecompat.ErrTimeout.Error() {
		t.Fatalf("hook_error = %v, want the timeout reason", entry["hook_error"])
	}
}

func TestHooksOnWithoutBunDegradeWithAReadableReason(t *testing.T) {
	app := newProviderTestApp(t)
	runner := &fakeHookRunner{input: provider.MagpiePluginInput{Models: []string{"m"}}}
	app.setPluginHookRunner(runner)
	app.hookRuntimeAvailable = func() bool { return false }
	importFixturePlugin(t, app)
	if errEnable := app.providerSettingsService().Set(ProviderSettings{MagpieHookExecution: true}); errEnable != nil {
		t.Fatalf("enable hooks: %v", errEnable)
	}

	if ids := accountIDs(t, app); len(ids) != 0 {
		t.Fatalf("no runtime must mean no plugin account: %v", ids)
	}
	if len(runner.probed) != 0 {
		t.Fatalf("no probe should be attempted without a runtime: %v", runner.probed)
	}
	catalog := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   providerRoutePrefix + "/catalog",
	})
	decoded := decodeProviderResponse(t, catalog)
	if decoded["bun_available"] != false {
		t.Fatalf("bun_available = %v", decoded["bun_available"])
	}
	entry := decoded["imported"].([]any)[0].(map[string]any)
	if entry["hook_error"] != magpiecompat.ErrBunUnavailable.Error() {
		t.Fatalf("hook_error = %v", entry["hook_error"])
	}
}

func TestProviderSettingsPersistAndRejectGarbage(t *testing.T) {
	app := newProviderTestApp(t)
	body, _ := json.Marshal(map[string]any{"magpie_hook_execution": true})
	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPut,
		Path:   providerRoutePrefix + "/settings",
		Body:   body,
	})
	decoded := decodeProviderResponse(t, response)
	if decoded["magpie_hook_execution"] != true {
		t.Fatalf("settings = %#v", decoded)
	}
	raw, errRead := os.ReadFile(filepath.Join(app.configSnapshot().DataDir, "provider-settings.json"))
	if errRead != nil {
		t.Fatalf("settings were not persisted: %v", errRead)
	}
	if !json.Valid(raw) {
		t.Fatalf("persisted settings are not JSON")
	}

	bad := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPut,
		Path:   providerRoutePrefix + "/settings",
		Body:   []byte("not json"),
	})
	if bad.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d", bad.StatusCode)
	}
}

func TestImportRejectsADirectoryOutsideThePluginFolder(t *testing.T) {
	app := newProviderTestApp(t)
	outside := t.TempDir()
	body, _ := json.Marshal(map[string]any{
		"manifest":  `{"name":"@fixture/outside"}`,
		"directory": outside,
	})
	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPost,
		Path:   providerRoutePrefix + "/import",
		Body:   body,
	})
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", response.StatusCode, string(response.Body))
	}
}

// TestHooksOnWithRealBunProduceAManagerAccount is the end-to-end path: a real Bun
// sidecar, a real fixture plugin on disk, and the account appearing through the
// HTTP handler the page calls. It is skipped when Bun is not installed.
func TestHooksOnWithRealBunProduceAManagerAccount(t *testing.T) {
	if !magpiecompat.BunAvailable() {
		t.Skip("bun is not installed; the real sidecar path cannot run here")
	}
	app := newProviderTestApp(t)
	pluginDir := filepath.Join(app.providerImportStore().PluginsDir(), "real-fixture")
	if errMkdir := os.MkdirAll(pluginDir, 0o755); errMkdir != nil {
		t.Fatalf("mkdir plugin: %v", errMkdir)
	}
	manifest := `{"name":"@fixture/magpie-provider","version":"0.0.1","type":"module","main":"./index.mjs"}`
	plugin := `export const FixturePlugin = async () => ({
  auth: { methods: ["Device code"] },
  provider: {
    fixture: {
      models: async () => [{ id: "fx-alpha" }, { id: "fx-beta" }, "fx-gamma"],
      usage: async () => ({ windows: [
        { name: "five_hour", used: 1.5, limit: 10, unit: "USD", reset: "2026-11-08T18:00:00Z" },
        { name: "wallet", balance: 4.25, unit: "USD" },
      ] }),
    },
  },
});
`
	if errWrite := os.WriteFile(filepath.Join(pluginDir, "index.mjs"), []byte(plugin), 0o600); errWrite != nil {
		t.Fatalf("write plugin: %v", errWrite)
	}
	body, _ := json.Marshal(map[string]any{"manifest": manifest, "source": "magpie", "directory": pluginDir})
	importResponse := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPost,
		Path:   providerRoutePrefix + "/import",
		Body:   body,
	})
	imported := decodeProviderResponse(t, importResponse)["provider"].(map[string]any)
	providerID := imported["id"].(string)

	if errEnable := app.providerSettingsService().Set(ProviderSettings{MagpieHookExecution: true}); errEnable != nil {
		t.Fatalf("enable hooks: %v", errEnable)
	}

	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   providerRoutePrefix + "/accounts",
	})
	rows := decodeProviderResponse(t, response)["accounts"].([]any)
	if len(rows) != 1 {
		t.Fatalf("accounts = %#v, want the probed plugin account", rows)
	}
	account := rows[0].(map[string]any)
	if account["id"] != provider.AccountID(providerID, "plugin") {
		t.Fatalf("id = %v", account["id"])
	}
	if account["models"].(map[string]any)["count"].(float64) != 3 {
		t.Fatalf("models = %#v", account["models"])
	}
	quota := account["quota"].([]any)
	if len(quota) != 2 {
		t.Fatalf("quota = %#v", quota)
	}
	if quota[0].(map[string]any)["used_percent"].(float64) != 15 {
		t.Fatalf("1.5 of 10 must be 15%%: %#v", quota[0])
	}
	if quota[1].(map[string]any)["balance"].(float64) != 4.25 {
		t.Fatalf("wallet = %#v", quota[1])
	}
}
