package manager

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"cpa-account-config-manager/internal/cpaapi"
)

func TestCodexModelControlBlocksDisabledModelsForCodexOnly(t *testing.T) {
	service := NewCodexModelControlService()
	service.Configure(Config{DataDir: t.TempDir()})
	if _, errSet := service.Set([]string{"GPT-5.4-Codex", "gpt-5.4-codex "}); errSet != nil {
		t.Fatalf("set: %v", errSet)
	}
	// The list is normalized and deduplicated.
	if service.Count() != 1 {
		t.Fatalf("disabled count = %d, want 1", service.Count())
	}
	gate := NewCodexModelControl(service)
	if !gate.RequestInterceptionActive() {
		t.Fatalf("the gate is inactive while a model is disabled")
	}

	response, changed := gate.InterceptRequest(cpaapi.RequestInterceptRequest{
		RequestID: "req-1", ToFormat: "codex", Model: "gpt-5.4-codex",
	})
	if !changed || !response.Terminate || response.StatusCode != http.StatusForbidden {
		t.Fatalf("disabled model response = %#v", response)
	}
	var payload struct {
		Error struct {
			Code   string `json:"code"`
			Source string `json:"source"`
		} `json:"error"`
	}
	if errDecode := json.Unmarshal(response.ResponseBody, &payload); errDecode != nil {
		t.Fatalf("decode rejection: %v", errDecode)
	}
	if payload.Error.Code != codexModelDisabledCode || payload.Error.Source != codexModelDisabledSource {
		t.Fatalf("rejection payload = %#v", payload)
	}

	// Another Codex model passes through untouched.
	if _, changed := gate.InterceptRequest(cpaapi.RequestInterceptRequest{
		RequestID: "req-2", ToFormat: "codex", Model: "gpt-5.4",
	}); changed {
		t.Fatalf("an enabled model was blocked")
	}
	// The same model id on a non-Codex route is unaffected: the control is scoped
	// to the Codex family.
	if _, changed := gate.InterceptRequest(cpaapi.RequestInterceptRequest{
		RequestID: "req-3", ToFormat: "openai", Model: "gpt-5.4-codex",
	}); changed {
		t.Fatalf("a non-Codex request was blocked")
	}
	// The requested model is honoured when Model is empty.
	if _, changed := gate.InterceptRequest(cpaapi.RequestInterceptRequest{
		RequestID: "req-4", ToFormat: "codex", RequestedModel: "gpt-5.4-codex",
	}); !changed {
		t.Fatalf("the requested model did not block")
	}
	// Clearing the list deactivates the gate entirely.
	if _, errSet := service.Set(nil); errSet != nil {
		t.Fatalf("clear: %v", errSet)
	}
	if gate.RequestInterceptionActive() {
		t.Fatalf("the gate stayed active with nothing disabled")
	}
}

func TestCodexModelControlPersistsAndSurvivesReconfigure(t *testing.T) {
	dataDir := t.TempDir()
	service := NewCodexModelControlService()
	service.Configure(Config{DataDir: dataDir})
	if _, errSet := service.Set([]string{"gpt-5.4-codex"}); errSet != nil {
		t.Fatalf("set: %v", errSet)
	}
	restored := NewCodexModelControlService()
	restored.Configure(Config{DataDir: dataDir})
	if !restored.Disabled("GPT-5.4-Codex") {
		t.Fatalf("the disabled list did not persist: %#v", restored.Snapshot())
	}
	info, errStat := os.Stat(codexModelControlStorePath(dataDir))
	if errStat != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("store permissions = %v err=%v", info, errStat)
	}
}

// The models list merges the disabled set with what the plugin can observe.
func TestCodexModelControlRowsMergeObservedAndConfiguredModels(t *testing.T) {
	app := NewApp(&fakeAuthHost{}, nil)
	app.Configure([]byte("data_dir: " + t.TempDir()))
	if _, errSet := app.codexModelControl.Set([]string{"gpt-5.4-codex"}); errSet != nil {
		t.Fatalf("set: %v", errSet)
	}
	// One Codex channel lists two models, one of which is also disabled.
	app.managementDoer = httpDoerFunc(func(*http.Request) (*http.Response, error) {
		body, _ := json.Marshal(map[string]any{"codex-api-key": []any{
			map[string]any{"name": "codex-a", "base-url": "https://codex.example/v1", "models": []any{
				map[string]any{"name": "gpt-5.4-codex", "alias": "gpt-5.4-codex"},
				map[string]any{"name": "gpt-5.4", "alias": "gpt-5.4"},
			}},
		}})
		return jsonHTTPResponse(http.StatusOK, string(body)), nil
	})
	app.refreshCodexChannelModels(context.Background(), "management-secret")

	rows := app.codexModelControlRows()
	byID := map[string]CodexModelControlRow{}
	for _, row := range rows {
		byID[row.ID] = row
	}
	disabledRow, ok := byID["gpt-5.4-codex"]
	if !ok || !disabledRow.Disabled || disabledRow.Channels != 1 {
		t.Fatalf("disabled row = %#v (present=%v)", disabledRow, ok)
	}
	enabledRow, ok := byID["gpt-5.4"]
	if !ok || enabledRow.Disabled || enabledRow.Channels != 1 {
		t.Fatalf("enabled row = %#v (present=%v)", enabledRow, ok)
	}
}

func TestCodexModelControlRoutesRequireKeyAndApply(t *testing.T) {
	app := NewApp(&fakeAuthHost{}, nil)
	app.Configure([]byte("data_dir: " + t.TempDir()))
	app.managementDoer = httpDoerFunc(func(*http.Request) (*http.Response, error) {
		return jsonHTTPResponse(http.StatusOK, `{"codex-api-key":[]}`), nil
	})

	if response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodGet, Path: "/v0/management" + managementRoutePrefix + "/codex/models",
	}); response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status without key = %d", response.StatusCode)
	}

	headers := http.Header{"Authorization": []string{"Bearer management-secret"}}
	write := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPut, Path: "/v0/management" + managementRoutePrefix + "/codex/models", Headers: headers,
		Body: []byte(`{"disabled":["gpt-5.4-codex"]}`),
	})
	if write.StatusCode != http.StatusOK {
		t.Fatalf("write status = %d body=%s", write.StatusCode, write.Body)
	}
	if !app.codexModelControl.Disabled("gpt-5.4-codex") {
		t.Fatalf("the route did not apply the disabled list")
	}
	read := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodGet, Path: "/v0/management" + managementRoutePrefix + "/codex/models", Headers: headers,
	})
	if read.StatusCode != http.StatusOK {
		t.Fatalf("read status = %d body=%s", read.StatusCode, read.Body)
	}
	var payload CodexModelControlSnapshot
	if errDecode := json.Unmarshal(read.Body, &payload); errDecode != nil {
		t.Fatalf("decode: %v", errDecode)
	}
	if len(payload.Disabled) != 1 || payload.Disabled[0] != "gpt-5.4-codex" {
		t.Fatalf("payload = %#v", payload)
	}
	// The overview reports the control state.
	overview := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodGet, Path: "/v0/management" + managementRoutePrefix + "/codex/overview", Headers: headers,
	})
	if overview.StatusCode != http.StatusOK {
		t.Fatalf("overview status = %d body=%s", overview.StatusCode, overview.Body)
	}
	var overviewPayload struct {
		Overview struct {
			DisabledModels     int  `json:"disabled_models"`
			ModelControlActive bool `json:"model_control_active"`
		} `json:"overview"`
	}
	if errDecode := json.Unmarshal(overview.Body, &overviewPayload); errDecode != nil {
		t.Fatalf("decode overview: %v", errDecode)
	}
	if overviewPayload.Overview.DisabledModels != 1 || !overviewPayload.Overview.ModelControlActive {
		t.Fatalf("overview = %#v", overviewPayload.Overview)
	}
}
