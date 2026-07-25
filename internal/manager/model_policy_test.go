package manager

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"cpa-account-config-manager/internal/cpaapi"
)

func TestModelPolicyPatchValidatesAllAllowAndDenyModes(t *testing.T) {
	tests := []struct {
		name    string
		patch   ModelPolicyPatch
		wantErr bool
	}{
		{name: "all", patch: ModelPolicyPatch{Mode: ModelPolicyModeAll}},
		{name: "allow", patch: ModelPolicyPatch{Mode: ModelPolicyModeAllowOnly, Models: []string{"gpt-5.6-sol", "GPT-5.6-SOL", "gpt-5.5"}}},
		{name: "deny", patch: ModelPolicyPatch{Mode: ModelPolicyModeDenyOnly, Models: []string{"gpt-5.4"}}},
		{name: "empty allow", patch: ModelPolicyPatch{Mode: ModelPolicyModeAllowOnly}, wantErr: true},
		{name: "empty deny", patch: ModelPolicyPatch{Mode: ModelPolicyModeDenyOnly}, wantErr: true},
		{name: "all with models", patch: ModelPolicyPatch{Mode: ModelPolicyModeAll, Models: []string{"gpt-5.5"}}, wantErr: true},
		{name: "unsafe id", patch: ModelPolicyPatch{Mode: ModelPolicyModeDenyOnly, Models: []string{"gpt-5.5\nsecret"}}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, errValidate := test.patch.Validate()
			if (errValidate != nil) != test.wantErr {
				t.Fatalf("Validate() error = %v", errValidate)
			}
			if errValidate == nil && test.patch.Mode == ModelPolicyModeAllowOnly && len(got.Models) != 2 {
				t.Fatalf("normalized models = %#v", got.Models)
			}
		})
	}
}

func TestResolveAccountProbeModelRespectsAllowAndDenyPolicies(t *testing.T) {
	tests := []struct {
		name          string
		requested     string
		provider      string
		policy        *AccountModelPolicySummary
		allowFallback bool
		wantModel     string
		wantAllowed   bool
		wantReplaced  bool
	}{
		{name: "no policy", requested: "gpt-5.6-sol", provider: "codex", wantModel: "gpt-5.6-sol", wantAllowed: true},
		{name: "allow listed", requested: "gpt-5.5", provider: "codex", policy: &AccountModelPolicySummary{Mode: ModelPolicyModeAllowOnly, Models: []string{"gpt-5.5"}}, wantModel: "gpt-5.5", wantAllowed: true},
		{name: "allow list blocks manual", requested: "gpt-5.6-sol", provider: "codex", policy: &AccountModelPolicySummary{Mode: ModelPolicyModeAllowOnly, Models: []string{"gpt-5.5"}}, wantModel: "gpt-5.6-sol"},
		{name: "allow list selects first for inspection", requested: "gpt-5.6-sol", provider: "codex", policy: &AccountModelPolicySummary{Mode: ModelPolicyModeAllowOnly, Models: []string{"gpt-5.4-mini", "gpt-5.5"}}, allowFallback: true, wantModel: "gpt-5.4-mini", wantAllowed: true, wantReplaced: true},
		{name: "deny list permits another model", requested: "gpt-5.5", provider: "codex", policy: &AccountModelPolicySummary{Mode: ModelPolicyModeDenyOnly, Models: []string{"gpt-5.6-sol"}}, wantModel: "gpt-5.5", wantAllowed: true},
		{name: "deny list blocks manual", requested: "gpt-5.6-sol", provider: "codex", policy: &AccountModelPolicySummary{Mode: ModelPolicyModeDenyOnly, Models: []string{"gpt-5.6-sol"}}, wantModel: "gpt-5.6-sol"},
		{name: "deny list selects safe inspection model", requested: "gpt-5.6-sol", provider: "codex", policy: &AccountModelPolicySummary{Mode: ModelPolicyModeDenyOnly, Models: []string{"gpt-5.6-sol"}}, allowFallback: true, wantModel: "gpt-5.5", wantAllowed: true, wantReplaced: true},
		{name: "deny list can exhaust safe candidates", requested: "gpt-5.6-sol", provider: "codex", policy: &AccountModelPolicySummary{Mode: ModelPolicyModeDenyOnly, Models: []string{"gpt-5.6-sol", "gpt-5.5", "gpt-5.4", "gpt-5.4-mini"}}, allowFallback: true, wantModel: "gpt-5.6-sol"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := resolveAccountProbeModel(test.requested, test.provider, test.policy, test.allowFallback)
			if got.Model != test.wantModel || got.Allowed != test.wantAllowed || got.Replaced != test.wantReplaced {
				t.Fatalf("resolution = %#v, want model=%q allowed=%v replaced=%v", got, test.wantModel, test.wantAllowed, test.wantReplaced)
			}
		})
	}
}

func TestDetectedModelWhitelistPreservesExistingManualPolicy(t *testing.T) {
	host := &fakeAuthHost{
		entries: []cpaapi.HostAuthFileEntry{
			{AuthIndex: "manual-policy", Name: "manual-policy.json", Provider: "codex", Type: "codex", AccountType: "oauth", Source: "file", Path: "/auths/manual-policy.json"},
			{AuthIndex: "native-exclusions", Name: "native-exclusions.json", Provider: "codex", Type: "codex", AccountType: "oauth", Source: "file", Path: "/auths/native-exclusions.json"},
		},
		details: map[string]cpaapi.HostAuthGetResponse{
			"manual-policy": {
				AuthIndex: "manual-policy", Name: "manual-policy.json", Path: "/auths/manual-policy.json",
				JSON: json.RawMessage(`{"type":"codex","cpa_account_config_manager":{"model_policy":{"schema":1,"mode":"deny_only","models":["gpt-5.4"],"managed_excluded_models":["gpt-5.4"]}}}`),
			},
			"native-exclusions": {
				AuthIndex: "native-exclusions", Name: "native-exclusions.json", Path: "/auths/native-exclusions.json",
				JSON: json.RawMessage(`{"type":"codex","excluded_models":["gpt-5.4"]}`),
			},
		},
	}
	app := NewApp(host, nil)
	app.Configure([]byte("data_dir: " + t.TempDir() + "\n"))
	defer app.Close()
	for _, accountID := range []string{"manual-policy", "native-exclusions"} {
		adjustment := app.applyDetectedModelWhitelist(t.Context(), accountID, []string{codexCompatibilityMiniModel, defaultCodexFallbackModel}, app.configSnapshot(), "management-secret")
		if adjustment == nil || adjustment.Status != "skipped" || adjustment.ReasonCode != "existing_model_policy" {
			t.Fatalf("existing policy adjustment for %s = %#v", accountID, adjustment)
		}
	}
	if len(host.saves) != 0 {
		t.Fatalf("existing policy was overwritten: %#v", host.saves)
	}
}

func TestResolveModelPolicyFieldsPreservesUserExclusionsAcrossModes(t *testing.T) {
	metadata := map[string]any{
		"excluded_models": []any{"manual-model", "old-managed"},
		"cpa_account_config_manager": map[string]any{
			"model_policy": map[string]any{
				"schema":                  float64(1),
				"mode":                    ModelPolicyModeAllowOnly,
				"models":                  []any{"model-a"},
				"managed_excluded_models": []any{"old-managed"},
				"base_excluded_models":    []any{"manual-model"},
			},
		},
	}
	catalog := []AccountModelOption{{ID: "model-a"}, {ID: "model-b"}, {ID: "model-c"}}

	fields, errResolve := resolveModelPolicyFields(metadata, ModelPolicyPatch{Mode: ModelPolicyModeDenyOnly, Models: []string{"model-b"}}, catalog)
	if errResolve != nil {
		t.Fatalf("resolve deny policy: %v", errResolve)
	}
	if got := fields["excluded_models"]; !equalStringSlice(got, []string{"manual-model", "model-b"}) {
		t.Fatalf("deny exclusions = %#v", got)
	}

	nextMetadata := modelPolicyMetadataFromFields(t, fields)
	cleared, errClear := resolveModelPolicyFields(nextMetadata, ModelPolicyPatch{Mode: ModelPolicyModeAll}, nil)
	if errClear != nil {
		t.Fatalf("resolve all policy: %v", errClear)
	}
	if got := cleared["excluded_models"]; !equalStringSlice(got, []string{"manual-model"}) {
		t.Fatalf("cleared exclusions = %#v", got)
	}
}

func TestResolveAllowOnlyPolicyReversesKnownCatalog(t *testing.T) {
	metadata := map[string]any{"excluded_models": []any{"operator-blocked"}}
	fields, errResolve := resolveModelPolicyFields(metadata, ModelPolicyPatch{
		Mode: ModelPolicyModeAllowOnly, Models: []string{"model-b"},
	}, []AccountModelOption{{ID: "model-a"}, {ID: "model-b"}, {ID: "model-c"}})
	if errResolve != nil {
		t.Fatalf("resolve allow policy: %v", errResolve)
	}
	if got := fields["excluded_models"]; !equalStringSlice(got, []string{"model-a", "model-c", "operator-blocked"}) {
		t.Fatalf("allow exclusions = %#v", got)
	}
}

func TestManagementClientLoadsAndSanitizesAuthModels(t *testing.T) {
	doer := httpDoerFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet || request.URL.Path != "/v0/management/auth-files/models" || request.URL.Query().Get("name") != "account.json" {
			t.Fatalf("request = %s %s", request.Method, request.URL.String())
		}
		if got := request.Header.Get("Authorization"); got != "Bearer management-secret" {
			t.Fatalf("authorization = %q", got)
		}
		return jsonHTTPResponse(http.StatusOK, `{"models":[{"id":"gpt-5.6-sol","display_name":"GPT 5.6"},{"id":"bad model"},{"id":"gpt-5.5","owned_by":"openai"}]}`), nil
	})
	client, errClient := newManagementClient("http://127.0.0.1:8317", "management-secret", doer)
	if errClient != nil {
		t.Fatalf("newManagementClient() error = %v", errClient)
	}
	models, errModels := client.GetAuthFileModels(context.Background(), "account.json")
	if errModels != nil {
		t.Fatalf("GetAuthFileModels() error = %v", errModels)
	}
	if len(models) != 2 || models[0].ID != "gpt-5.5" || models[1].ID != "gpt-5.6-sol" {
		t.Fatalf("models = %#v", models)
	}
}

func TestJobAppliesAccountSpecificModelPolicyPayload(t *testing.T) {
	host := &fakeAuthHost{
		entries: []cpaapi.HostAuthFileEntry{
			{AuthIndex: "a", Name: "a.json", Provider: "codex", Source: "file", Path: "/auths/a.json"},
			{AuthIndex: "b", Name: "b.json", Provider: "codex", Source: "file", Path: "/auths/b.json"},
		},
		details: map[string]cpaapi.HostAuthGetResponse{
			"a": {AuthIndex: "a", Name: "a.json", Path: "/auths/a.json", JSON: json.RawMessage(`{"type":"codex"}`)},
			"b": {AuthIndex: "b", Name: "b.json", Path: "/auths/b.json", JSON: json.RawMessage(`{"type":"codex"}`)},
		},
	}
	accounts := NewAccountService(host)
	resolved, errResolve := accounts.ResolveTargets(context.Background(), TargetScope{Mode: "selected", IDs: []string{"a", "b"}})
	if errResolve != nil {
		t.Fatalf("ResolveTargets() error = %v", errResolve)
	}
	writer := &modelPolicyCaptureWriter{
		catalogs: map[string][]AccountModelOption{
			"a.json": {{ID: "shared"}, {ID: "a-only"}},
			"b.json": {{ID: "shared"}, {ID: "b-only"}},
		},
		payloads: make(map[string]map[string]any),
	}
	engine := NewJobEngine(accounts)
	patch, errPatch := (BatchPatch{ModelPolicy: &ModelPolicyPatch{Mode: ModelPolicyModeAllowOnly, Models: []string{"shared"}}}).Validate()
	if errPatch != nil {
		t.Fatalf("Validate() error = %v", errPatch)
	}
	for _, account := range resolved.Accounts {
		result := engine.applyAccount(context.Background(), account, BatchOperationPatch, patch, writer)
		if result.Status != ResultSucceeded {
			t.Fatalf("apply %s = %#v", account.ID, result)
		}
	}
	if got := writer.payloads["a.json"]["excluded_models"]; !equalStringSlice(got, []string{"a-only"}) {
		t.Fatalf("a exclusions = %#v", got)
	}
	if got := writer.payloads["b.json"]["excluded_models"]; !equalStringSlice(got, []string{"b-only"}) {
		t.Fatalf("b exclusions = %#v", got)
	}
}

func TestAccountModelsEndpointRequiresManagementKey(t *testing.T) {
	app := NewApp(&fakeAuthHost{}, nil)
	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPost,
		Path:   "/v0/management/plugins/cpa-account-config-manager/accounts/models",
		Body:   []byte(`{"scope":{"mode":"selected","ids":["missing"]}}`),
	})
	if response.StatusCode != http.StatusUnauthorized && response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", response.StatusCode, response.Body)
	}
}

func TestAccountModelsEndpointReturnsSanitizedCommonCatalog(t *testing.T) {
	host := &fakeAuthHost{
		entries: []cpaapi.HostAuthFileEntry{
			{AuthIndex: "a", Name: "a.json", Provider: "codex", Source: "file", Path: "/auths/a.json"},
			{AuthIndex: "b", Name: "b.json", Provider: "codex", Source: "file", Path: "/auths/b.json"},
		},
		details: map[string]cpaapi.HostAuthGetResponse{
			"a": {AuthIndex: "a", Name: "a.json", Path: "/auths/a.json", JSON: json.RawMessage(`{"type":"codex"}`)},
			"b": {AuthIndex: "b", Name: "b.json", Path: "/auths/b.json", JSON: json.RawMessage(`{"type":"codex"}`)},
		},
	}
	app := NewApp(host, nil)
	app.managementDoer = httpDoerFunc(func(request *http.Request) (*http.Response, error) {
		models := `[{"id":"shared","display_name":"Shared"},{"id":"a-only"}]`
		if request.URL.Query().Get("name") == "b.json" {
			models = `[{"id":"shared","display_name":"Shared"},{"id":"b-only"},{"id":"bad model"}]`
		}
		return jsonHTTPResponse(http.StatusOK, `{"models":`+models+`}`), nil
	})
	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method:  http.MethodPost,
		Path:    "/v0/management/plugins/cpa-account-config-manager/accounts/models",
		Headers: http.Header{"Authorization": []string{"Bearer management-secret"}},
		Body:    []byte(`{"scope":{"mode":"selected","ids":["a","b"]}}`),
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.StatusCode, response.Body)
	}
	var catalog AccountModelCatalogResponse
	if errDecode := json.Unmarshal(response.Body, &catalog); errDecode != nil {
		t.Fatalf("decode response: %v", errDecode)
	}
	if catalog.Loaded != 2 || catalog.Failed != 0 || len(catalog.Models) != 1 || catalog.Models[0].ID != "shared" {
		t.Fatalf("catalog = %#v", catalog)
	}
	if strings.Contains(string(response.Body), "management-secret") {
		t.Fatal("response leaked the management key")
	}
}

func modelPolicyMetadataFromFields(t *testing.T, fields map[string]any) map[string]any {
	t.Helper()
	encoded, errMarshal := json.Marshal(fields["cpa_account_config_manager.model_policy"])
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	var policy map[string]any
	if errUnmarshal := json.Unmarshal(encoded, &policy); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	excluded := fields["excluded_models"]
	return map[string]any{
		"excluded_models":            excluded,
		"cpa_account_config_manager": map[string]any{"model_policy": policy},
	}
}

func equalStringSlice(value any, want []string) bool {
	got, ok := value.([]string)
	if !ok || len(got) != len(want) {
		return false
	}
	for index := range want {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

type httpDoerFunc func(*http.Request) (*http.Response, error)

func (function httpDoerFunc) Do(request *http.Request) (*http.Response, error) {
	return function(request)
}

func jsonHTTPResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

type modelPolicyCaptureWriter struct {
	mu       sync.Mutex
	catalogs map[string][]AccountModelOption
	payloads map[string]map[string]any
}

func (writer *modelPolicyCaptureWriter) GetAuthFileModels(_ context.Context, name string) ([]AccountModelOption, error) {
	return append([]AccountModelOption(nil), writer.catalogs[name]...), nil
}

func (writer *modelPolicyCaptureWriter) PatchFields(_ context.Context, name string, patch BatchPatch) error {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	writer.payloads[name] = patch.FieldPayload(name)
	return nil
}

func (*modelPolicyCaptureWriter) PatchDisabled(context.Context, string, bool) error { return nil }
func (*modelPolicyCaptureWriter) DeleteAuthFile(context.Context, string) error      { return nil }
