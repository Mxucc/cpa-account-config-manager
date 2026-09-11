package manager

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cpa-account-config-manager/internal/cpaapi"
)

// openCodeModelTestServer emulates the OpenCode gateway contract: Bearer auth
// plus the OpenCode client header on /v1/models and /v1/chat/completions.
func openCodeModelTestServer(t *testing.T, seen *[]*http.Request) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		clone := request.Clone(request.Context())
		*seen = append(*seen, clone)
		if request.Header.Get("Authorization") != "Bearer sk-opencode-test-key" {
			writer.WriteHeader(http.StatusUnauthorized)
			_, _ = writer.Write([]byte(`{"error":{"message":"unauthorized"}}`))
			return
		}
		if request.Header.Get("x-opencode-client") != "cli" || !strings.HasPrefix(request.Header.Get("User-Agent"), "opencode/") {
			writer.WriteHeader(http.StatusForbidden)
			return
		}
		switch {
		case strings.HasSuffix(request.URL.Path, "/v1/models"):
			_ = json.NewEncoder(writer).Encode(map[string]any{"data": []map[string]string{
				{"id": "opencode-go-alpha"}, {"id": "opencode-go-beta"}, {"id": "opencode-go-alpha"},
			}})
		case strings.HasSuffix(request.URL.Path, "/v1/chat/completions"):
			var payload struct {
				Model string `json:"model"`
			}
			_ = json.NewDecoder(request.Body).Decode(&payload)
			if payload.Model != "opencode-go-alpha" {
				writer.WriteHeader(http.StatusNotFound)
				_, _ = writer.Write([]byte(`{"error":{"message":"model not found"}}`))
				return
			}
			_ = json.NewEncoder(writer).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": "pong"}}}})
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestParseOpenCodeModelCatalogShapes(t *testing.T) {
	models, errEnvelope := parseOpenCodeModelCatalog([]byte(`{"data":[{"id":"b"},{"id":"a"},{"id":"b"},{"id":""}]}`))
	if errEnvelope != nil || len(models) != 2 || models[0] != "a" || models[1] != "b" {
		t.Fatalf("envelope catalog = %#v, err=%v", models, errEnvelope)
	}
	list, errList := parseOpenCodeModelCatalog([]byte(`["z","y"]`))
	if errList != nil || len(list) != 2 || list[0] != "y" {
		t.Fatalf("list catalog = %#v, err=%v", list, errList)
	}
	if _, errInvalid := parseOpenCodeModelCatalog([]byte(`{"unexpected":true}`)); errInvalid == nil {
		t.Fatal("an unrecognized catalog was accepted")
	}
}

// The catalog and the model probe must speak the exact upstream contract:
// Bearer key, OpenCode client header, and the /v1 paths.
func TestOpenCodeModelCatalogAndProbeUseTheUpstreamContract(t *testing.T) {
	seen := make([]*http.Request, 0, 4)
	server := openCodeModelTestServer(t, &seen)

	models, statusCode, errFetch := fetchOpenCodeModels(context.Background(), server.URL+"/", "sk-opencode-test-key", openCodeModelTimeout(0))
	if errFetch != nil || statusCode != http.StatusOK {
		t.Fatalf("fetch models = %#v, %d, %v", models, statusCode, errFetch)
	}
	if len(models) != 2 {
		t.Fatalf("models = %#v, want two deduplicated ids", models)
	}
	if !strings.HasSuffix(seen[0].URL.Path, "/v1/models") {
		t.Fatalf("models path = %q", seen[0].URL.Path)
	}

	available := probeOpenCodeModel(context.Background(), server.URL, "sk-opencode-test-key", "opencode-go-alpha", openCodeModelTimeout(0))
	if available.Status != "available" || available.ReasonCode != "model_response_ok" || available.StatusCode != http.StatusOK {
		t.Fatalf("available probe = %#v", available)
	}
	missing := probeOpenCodeModel(context.Background(), server.URL, "sk-opencode-test-key", "opencode-go-missing", openCodeModelTimeout(0))
	if missing.Status != "unavailable" || missing.ReasonCode != "model_not_found" {
		t.Fatalf("missing-model probe = %#v", missing)
	}
	unauthorized := probeOpenCodeModel(context.Background(), server.URL, "sk-wrong", "opencode-go-alpha", openCodeModelTimeout(0))
	if unauthorized.Status != "unavailable" || unauthorized.ReasonCode != "authentication_failed" {
		t.Fatalf("unauthorized probe = %#v", unauthorized)
	}
	incomplete := probeOpenCodeModel(context.Background(), server.URL, "", "opencode-go-alpha", openCodeModelTimeout(0))
	if incomplete.Status != "unsupported" || incomplete.ReasonCode != "credential_incomplete" {
		t.Fatalf("incomplete credential probe = %#v", incomplete)
	}
}

// A stored API key enables the catalog, the model test, and the CPA binding, and
// the key itself never appears in a management response.
func TestOpenCodeGoModelRoutesAndBinding(t *testing.T) {
	seen := make([]*http.Request, 0, 8)
	upstream := openCodeModelTestServer(t, &seen)

	service := NewOpenCodeQuotaService()
	service.Configure(Config{DataDir: t.TempDir()})
	accountID, errSave := service.SaveAccount("wrk_models", "cookie-models", "sk-opencode-test-key")
	if errSave != nil {
		t.Fatalf("save account: %v", errSave)
	}
	// Point the account at the test gateway instead of the public upstream.
	service.mu.Lock()
	for index := range service.accounts {
		if service.accounts[index].ID == accountID {
			service.accounts[index].BaseURL = upstream.URL
		}
	}
	service.mu.Unlock()

	// The CPA channel write is observed through the injected host doer.
	channelWrites := make([][]map[string]any, 0, 1)
	app := NewApp(&fakeAuthHost{}, nil)
	app.opencode = service
	app.managementDoer = httpDoerFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method == http.MethodGet {
			return jsonHTTPResponse(http.StatusOK, `{"openai-compatibility":[]}`), nil
		}
		var items []map[string]any
		_ = json.NewDecoder(request.Body).Decode(&items)
		channelWrites = append(channelWrites, items)
		return jsonHTTPResponse(http.StatusOK, `{}`), nil
	})
	headers := http.Header{"Authorization": []string{"Bearer management-secret"}}

	modelsResponse := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPost, Path: "/v0/management" + managementRoutePrefix + "/opencode/models",
		Headers: headers, Body: []byte(`{"kind":"go","account_id":"` + accountID + `"}`),
	})
	if modelsResponse.StatusCode != http.StatusOK {
		t.Fatalf("models status = %d, body=%s", modelsResponse.StatusCode, modelsResponse.Body)
	}
	var modelsPayload struct {
		Account OpenCodeAccountView `json:"account"`
	}
	if errDecode := json.Unmarshal(modelsResponse.Body, &modelsPayload); errDecode != nil {
		t.Fatalf("decode models response: %v", errDecode)
	}
	if !modelsPayload.Account.KeySet || len(modelsPayload.Account.Models) != 2 {
		t.Fatalf("account view = %#v", modelsPayload.Account)
	}
	if strings.Contains(string(modelsResponse.Body), "sk-opencode-test-key") {
		t.Fatal("the model response leaked the OpenCode API key")
	}

	testResponse := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPost, Path: "/v0/management" + managementRoutePrefix + "/opencode/model-test",
		Headers: headers, Body: []byte(`{"kind":"go","account_id":"` + accountID + `","model":"opencode-go-alpha"}`),
	})
	if testResponse.StatusCode != http.StatusOK {
		t.Fatalf("model-test status = %d, body=%s", testResponse.StatusCode, testResponse.Body)
	}
	var testPayload struct {
		Result OpenCodeModelTestResult `json:"result"`
	}
	if errDecode := json.Unmarshal(testResponse.Body, &testPayload); errDecode != nil {
		t.Fatalf("decode model-test response: %v", errDecode)
	}
	if testPayload.Result.Status != "available" {
		t.Fatalf("model-test result = %#v", testPayload.Result)
	}

	// Without a management key every new route is rejected.
	for _, route := range []string{"/opencode/models", "/opencode/model-test", "/opencode/bind"} {
		unauthorized := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
			Method: http.MethodPost, Path: "/v0/management" + managementRoutePrefix + route,
			Body: []byte(`{"kind":"go","account_id":"` + accountID + `","model":"opencode-go-alpha"}`),
		})
		if unauthorized.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s status without key = %d", route, unauthorized.StatusCode)
		}
	}

	bindResponse := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPost, Path: "/v0/management" + managementRoutePrefix + "/opencode/bind",
		Headers: headers, Body: []byte(`{"kind":"go","account_id":"` + accountID + `"}`),
	})
	if bindResponse.StatusCode != http.StatusOK {
		t.Fatalf("bind status = %d, body=%s", bindResponse.StatusCode, bindResponse.Body)
	}
	if len(channelWrites) != 1 || len(channelWrites[0]) != 1 {
		t.Fatalf("channel writes = %#v", channelWrites)
	}
	entry := channelWrites[0][0]
	if entry["base-url"] != upstream.URL+"/v1" {
		t.Fatalf("bound base-url = %#v", entry["base-url"])
	}
	rows, _ := entry["api-key-entries"].([]any)
	if len(rows) != 1 {
		t.Fatalf("bound key rows = %#v", entry["api-key-entries"])
	}
	firstRow, _ := rows[0].(map[string]any)
	if firstRow["api-key"] != "sk-opencode-test-key" {
		t.Fatalf("bound key row = %#v", rows[0])
	}
	boundHeaders, _ := entry["headers"].(map[string]any)
	if boundHeaders["x-opencode-client"] != "cli" || !strings.HasPrefix(boundHeaders["User-Agent"].(string), "opencode/") {
		t.Fatalf("bound headers = %#v", entry["headers"])
	}
	if name, _ := entry["name"].(string); !strings.HasPrefix(name, "OpenCode Go") {
		t.Fatalf("bound channel name = %#v", entry["name"])
	}
	channelModels, _ := entry["models"].([]any)
	if len(channelModels) != 2 {
		t.Fatalf("bound channel models = %#v, want the two catalog ids", entry["models"])
	}
	alphaPublished := false
	for _, item := range channelModels {
		row, _ := item.(map[string]any)
		if row["name"] != row["alias"] {
			t.Fatalf("bound channel model row lost its identity: %#v", row)
		}
		if row["name"] == "opencode-go-alpha" {
			alphaPublished = true
		}
	}
	if !alphaPublished {
		t.Fatalf("bound channel models = %#v, want opencode-go-alpha", entry["models"])
	}
	var bindPayload struct {
		Binding OpenCodeBindingResult `json:"binding"`
	}
	if errDecode := json.Unmarshal(bindResponse.Body, &bindPayload); errDecode != nil {
		t.Fatalf("decode bind response: %v", errDecode)
	}
	if bindPayload.Binding.Models != 2 {
		t.Fatalf("bind response models = %d, body=%s", bindPayload.Binding.Models, bindResponse.Body)
	}

	// A second bind updates the same channel instead of duplicating it.
	if errBind := func() error {
		_, errSecond := app.bindOpenCodeChannel(context.Background(), "management-secret", upstream.URL, "sk-opencode-test-key", "OpenCode Go wrk_models", []string{"opencode-go-alpha"})
		return errSecond
	}(); errBind != nil {
		t.Fatalf("second bind: %v", errBind)
	}
	if len(channelWrites) != 2 || len(channelWrites[1]) != 1 {
		t.Fatalf("second bind changed the channel count: %#v", channelWrites)
	}
}

// Binding requires a stored key: the session cookie alone cannot serve models.
func TestOpenCodeBindRequiresStoredAPIKey(t *testing.T) {
	service := NewOpenCodeQuotaService()
	service.Configure(Config{DataDir: t.TempDir()})
	accountID, errSave := service.SaveAccount("wrk_no_key", "cookie-no-key", "")
	if errSave != nil {
		t.Fatalf("save account: %v", errSave)
	}
	app := NewApp(&fakeAuthHost{}, nil)
	app.opencode = service
	app.managementDoer = httpDoerFunc(func(*http.Request) (*http.Response, error) {
		return jsonHTTPResponse(http.StatusOK, `{"openai-compatibility":[]}`), nil
	})
	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPost, Path: "/v0/management" + managementRoutePrefix + "/opencode/bind",
		Headers: http.Header{"Authorization": []string{"Bearer management-secret"}},
		Body:    []byte(`{"kind":"go","account_id":"` + accountID + `"}`),
	})
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("bind without key status = %d, body=%s", response.StatusCode, response.Body)
	}
	if !strings.Contains(string(response.Body), "API key") {
		t.Fatalf("bind error did not explain the missing key: %s", response.Body)
	}
}

// Setting the key clears a previously cached catalog, so a rotated credential can
// never show models fetched with the old one.
func TestOpenCodeSetAPIKeyInvalidatesCatalog(t *testing.T) {
	seen := make([]*http.Request, 0, 4)
	upstream := openCodeModelTestServer(t, &seen)
	service := NewOpenCodeQuotaService()
	service.Configure(Config{DataDir: t.TempDir()})
	accountID, errSave := service.SaveAccount("wrk_rotate", "cookie-rotate", "sk-opencode-test-key")
	if errSave != nil {
		t.Fatalf("save account: %v", errSave)
	}
	service.mu.Lock()
	for index := range service.accounts {
		if service.accounts[index].ID == accountID {
			service.accounts[index].BaseURL = upstream.URL
		}
	}
	service.mu.Unlock()

	if _, errRefresh := service.RefreshModels(context.Background(), accountID, 0); errRefresh != nil {
		t.Fatalf("refresh models: %v", errRefresh)
	}
	if view, errView := service.accountView(accountID); errView != nil || len(view.Models) != 2 {
		t.Fatalf("view after refresh = %#v, err=%v", view, errView)
	}
	if _, errSet := service.SetAPIKey(accountID, "sk-rotated"); errSet != nil {
		t.Fatalf("set api key: %v", errSet)
	}
	if view, errView := service.accountView(accountID); errView != nil || len(view.Models) != 0 || !view.KeySet {
		t.Fatalf("view after rotation = %#v, err=%v", view, errView)
	}
	// The stored key survives a reload and stays private.
	reloaded := NewOpenCodeQuotaService()
	reloaded.Configure(Config{DataDir: service.dataDir})
	credential, errCredential := reloaded.accountCredential(accountID)
	if errCredential != nil || credential.APIKey != "sk-rotated" {
		t.Fatalf("reloaded credential = %#v, err=%v", credential, errCredential)
	}
}

// Merge must preserve existing rows in every shape CPA or a hand-edited config
// can produce, and must not duplicate model ids the channel already serves.
func TestOpenCodeMergeChannelModelsPreservesExistingRows(t *testing.T) {
	typed := mergeOpenCodeChannelModels([]map[string]any{{"name": "gpt-5.5", "alias": "gpt-5.5-fast"}}, []string{"gpt-5.5", "kimi-k2"})
	if len(typed) != 2 {
		t.Fatalf("typed rows = %#v", typed)
	}
	if typed[0]["alias"] != "gpt-5.5-fast" {
		t.Fatalf("operator alias was lost: %#v", typed[0])
	}
	if typed[1]["name"] != "kimi-k2" || typed[1]["alias"] != "kimi-k2" {
		t.Fatalf("catalog row = %#v", typed[1])
	}

	legacy := mergeOpenCodeChannelModels([]any{"legacy-model", 42, map[string]any{"name": "kimi-k2"}}, []string{"kimi-k2", "deepseek-chat"})
	if len(legacy) != 3 {
		t.Fatalf("legacy rows = %#v", legacy)
	}
	if legacy[0]["name"] != "legacy-model" || legacy[1]["name"] != "kimi-k2" || legacy[2]["name"] != "deepseek-chat" {
		t.Fatalf("legacy rows = %#v", legacy)
	}

	if empty := mergeOpenCodeChannelModels(nil, nil); len(empty) != 0 {
		t.Fatalf("empty merge = %#v", empty)
	}
}
