package manager

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cpa-account-config-manager/internal/cpaapi"
)

func TestHandleAccountModelTestUsesSelectedCPAAuthAndRecordsSanitizedResult(t *testing.T) {
	host := &fakeAuthHost{
		entries: []cpaapi.HostAuthFileEntry{{
			AuthIndex: "auth-1", Name: "operator.json", Provider: "codex", Type: "codex",
			Source: "file", Path: "/auths/operator.json",
		}},
		details: map[string]cpaapi.HostAuthGetResponse{
			"auth-1": {
				AuthIndex: "auth-1", Name: "operator.json", Path: "/auths/operator.json",
				JSON: json.RawMessage(`{"type":"codex","access_token":"upstream-secret","account_id":"workspace-123"}`),
			},
		},
	}
	var received managementAPICallRequest
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v0/management/api-call" || request.Method != http.MethodPost {
			t.Errorf("management request = %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer management-secret" {
			t.Errorf("authorization header was not forwarded to the loopback Management API")
		}
		if errDecode := json.NewDecoder(request.Body).Decode(&received); errDecode != nil {
			t.Errorf("decode management request: %v", errDecode)
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(managementAPICallResponse{
			StatusCode: http.StatusOK,
			Body:       "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\"}}\n\n",
		})
	}))
	defer server.Close()

	app := NewApp(host, []byte("index"))
	app.modelTests.doer = server.Client()
	app.Configure([]byte("data_dir: " + t.TempDir() + "\nmanagement_base_url: " + server.URL + "\n"))
	defer app.Close()
	body, _ := json.Marshal(ModelTestRequest{AccountID: "auth-1", Model: "gpt-5.4"})
	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPost,
		Path:   "/v0/management/plugins/cpa-account-config-manager/accounts/model-test",
		Headers: http.Header{
			"Authorization": []string{"Bearer management-secret"},
		},
		Body: body,
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("model test = %d %s", response.StatusCode, response.Body)
	}
	var result ModelTestResult
	if errDecode := json.Unmarshal(response.Body, &result); errDecode != nil {
		t.Fatalf("decode result: %v", errDecode)
	}
	if result.Status != "available" || result.ReasonCode != "model_response_ok" || result.Model != "gpt-5.4" {
		t.Fatalf("result = %#v", result)
	}
	if received.AuthIndex != "auth-1" || received.URL != "https://chatgpt.com/backend-api/codex/responses" {
		t.Fatalf("CPA api-call target = %#v", received)
	}
	if received.Header["Authorization"] != "Bearer $TOKEN$" || received.Header["Chatgpt-Account-Id"] != "workspace-123" {
		t.Fatalf("probe headers = %#v", received.Header)
	}
	for _, secret := range []string{"management-secret", "upstream-secret"} {
		if strings.Contains(string(response.Body), secret) || strings.Contains(received.Data, secret) {
			t.Fatalf("model test leaked %q", secret)
		}
	}

	operations := app.operations.List(OperationQuery{Page: 1, PageSize: 20})
	if len(operations.Operations) != 1 {
		t.Fatalf("operation count = %d, want 1", len(operations.Operations))
	}
	operation := operations.Operations[0]
	if operation.Action != OperationActionModelTest || operation.Model != "gpt-5.4" || operation.Status != OperationStatusSucceeded || operation.ReasonCode != "model_response_ok" {
		t.Fatalf("operation = %#v", operation)
	}
	rawOperation, _ := json.Marshal(operation)
	if strings.Contains(string(rawOperation), "upstream-secret") || strings.Contains(string(rawOperation), "management-secret") {
		t.Fatalf("operation leaked a secret: %s", rawOperation)
	}
}

func TestHandleAccountModelTestRejectsBrowserOwnedTransportAndUnsupportedProviderIsStructured(t *testing.T) {
	host := &fakeAuthHost{entries: []cpaapi.HostAuthFileEntry{{
		AuthIndex: "qwen-1", Name: "qwen.json", Provider: "qwen", Type: "qwen", RuntimeOnly: true, Source: "runtime",
	}}}
	app := NewApp(host, []byte("index"))
	app.Configure([]byte("data_dir: " + t.TempDir() + "\n"))
	defer app.Close()

	unknownField := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method:  http.MethodPost,
		Path:    "/v0/management/plugins/cpa-account-config-manager/accounts/model-test",
		Headers: http.Header{"Authorization": []string{"Bearer management-secret"}},
		Body:    []byte(`{"account_id":"qwen-1","model":"qwen-max","url":"https://evil.example"}`),
	})
	if unknownField.StatusCode != http.StatusBadRequest || !strings.Contains(string(unknownField.Body), "unknown field") {
		t.Fatalf("browser-owned URL response = %d %s", unknownField.StatusCode, unknownField.Body)
	}

	body, _ := json.Marshal(ModelTestRequest{AccountID: "qwen-1", Model: "qwen-max"})
	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method:  http.MethodPost,
		Path:    "/v0/management/plugins/cpa-account-config-manager/accounts/model-test",
		Headers: http.Header{"Authorization": []string{"Bearer management-secret"}},
		Body:    body,
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("unsupported test = %d %s", response.StatusCode, response.Body)
	}
	var result ModelTestResult
	_ = json.Unmarshal(response.Body, &result)
	if result.Status != "unsupported" || result.ReasonCode != "unsupported_provider" {
		t.Fatalf("unsupported result = %#v", result)
	}
	operation := app.operations.List(OperationQuery{Page: 1, PageSize: 20}).Operations[0]
	if operation.Status != OperationStatusSkipped || operation.Skipped != 1 {
		t.Fatalf("unsupported operation = %#v", operation)
	}
}

func TestClassifyModelProbeReturnsOnlyNormalizedOutcomes(t *testing.T) {
	tests := []struct {
		name       string
		kind       string
		statusCode int
		body       string
		status     string
		reason     string
	}{
		{name: "openai success", kind: "openai", statusCode: 200, body: `{"id":"resp-1","object":"response"}`, status: "available", reason: "model_response_ok"},
		{name: "claude success", kind: "claude", statusCode: 200, body: `{"id":"msg-1","type":"message"}`, status: "available", reason: "model_response_ok"},
		{name: "gemini success", kind: "gemini", statusCode: 200, body: `{"candidates":[{"finishReason":"STOP"}]}`, status: "available", reason: "model_response_ok"},
		{name: "missing model", kind: "openai", statusCode: 404, body: `{"error":{"message":"model does not exist"}}`, status: "unavailable", reason: "model_not_found"},
		{name: "bad credential", kind: "openai", statusCode: 401, body: `private upstream body`, status: "unavailable", reason: "authentication_failed"},
		{name: "quota", kind: "openai", statusCode: 429, body: `private upstream body`, status: "review", reason: "quota_limited"},
		{name: "invalid success body", kind: "openai", statusCode: 200, body: `private upstream body`, status: "review", reason: "invalid_response"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			status, reason := classifyModelProbe(test.kind, test.statusCode, []byte(test.body))
			if status != test.status || reason != test.reason {
				t.Fatalf("classify = %q %q, want %q %q", status, reason, test.status, test.reason)
			}
		})
	}
}

func TestModelIdentifierRejectsURLsControlsAndOversizedValues(t *testing.T) {
	for _, invalid := range []string{"https://evil.example/model", "model name", "model\nname", strings.Repeat("m", maxModelIdentifierLength+1)} {
		if safeModelIdentifier(invalid) != "" {
			t.Errorf("safeModelIdentifier(%q) should reject the value", invalid)
		}
	}
	for _, valid := range []string{"gpt-5.4", "claude-sonnet-4-5-20250929", "models/gemini-2.0-flash", "provider:model@2026"} {
		if safeModelIdentifier(valid) != valid {
			t.Errorf("safeModelIdentifier(%q) should preserve the value", valid)
		}
	}
}

func TestBuildModelProbeUsesAPIKeyEndpointForRuntimeAccountMetadata(t *testing.T) {
	if !accountTypeUsesAPIKey("api_key") || accountTypeUsesAPIKey("oauth") {
		t.Fatal("account API-key type normalization is incorrect")
	}
	probe, model, supported, errProbe := buildModelProbe("codex", "", modelTestAuthMetadata{hasAPIKey: true})
	if errProbe != nil || !supported {
		t.Fatalf("buildModelProbe() supported=%v error=%v", supported, errProbe)
	}
	if model != "gpt-5.4" || probe.url != "https://api.openai.com/v1/responses" || probe.kind != "openai" {
		t.Fatalf("API-key probe = %#v model=%q", probe, model)
	}
}

func TestManagementAPICallResponseAcceptsCompatibleStatusCodeShapes(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want int
	}{
		{name: "snake case number", raw: `{"status_code":402,"body":{"detail":{"code":"deactivated_workspace"}}}`, want: 402},
		{name: "camel case number", raw: `{"statusCode":402,"body":{"detail":{"code":"deactivated_workspace"}}}`, want: 402},
		{name: "snake case string", raw: `{"status_code":"401","body":{"error":{"code":"invalid_token"}}}`, want: 401},
		{name: "camel case string", raw: `{"statusCode":"200","body":{"rateLimit":{"allowed":true}}}`, want: 200},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var response managementAPICallResponse
			if errDecode := json.Unmarshal([]byte(test.raw), &response); errDecode != nil {
				t.Fatalf("json.Unmarshal() error = %v", errDecode)
			}
			if response.StatusCode != test.want {
				t.Fatalf("StatusCode = %d, want %d", response.StatusCode, test.want)
			}
		})
	}
	for _, raw := range []string{
		`{"status_code":99,"body":null}`,
		`{"statusCode":600,"body":null}`,
		`{"status_code":401.5,"body":null}`,
		`{"status_code":1e100,"body":null}`,
		`{"statusCode":"not-a-status","body":null}`,
	} {
		var response managementAPICallResponse
		if errDecode := json.Unmarshal([]byte(raw), &response); errDecode == nil {
			t.Fatalf("json.Unmarshal(%s) succeeded, want a bounded status-code error", raw)
		}
	}
}

func TestAuthMetadataPrefersOAuthAndResolvesCompatibleAccountIDShapes(t *testing.T) {
	jwtPayload := base64.RawURLEncoding.EncodeToString([]byte(`{"chatgpt_account_id":"jwt-account"}`))
	tests := []struct {
		name      string
		raw       string
		accountID string
	}{
		{name: "metadata camel case", raw: `{"access_token":"oauth-secret","api_key":"api-secret","metadata":{"chatgptAccountId":"metadata-account"}}`, accountID: "metadata-account"},
		{name: "attributes object token", raw: `{"access_token":"oauth-secret","attributes":{"id_token":{"accountId":"object-account"}}}`, accountID: "object-account"},
		{name: "JSON token", raw: `{"access_token":"oauth-secret","id_token":"{\"account_id\":\"json-account\"}"}`, accountID: "json-account"},
		{name: "JWT token", raw: `{"access_token":"oauth-secret","id_token":"header.` + jwtPayload + `.signature"}`, accountID: "jwt-account"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			host := &fakeAuthHost{details: map[string]cpaapi.HostAuthGetResponse{
				"auth-1": {AuthIndex: "auth-1", JSON: json.RawMessage(test.raw)},
			}}
			service := NewModelTestService(NewAccountService(host))
			metadata := service.authMetadata(t.Context(), "auth-1")
			if !metadata.hasAccessToken || metadata.usesAPIKey() {
				t.Fatalf("credential kind = %#v, want OAuth precedence", metadata)
			}
			if metadata.accountID != test.accountID {
				t.Fatalf("accountID = %q, want %q", metadata.accountID, test.accountID)
			}
		})
	}
}

func TestInspectionCredentialProbeOverridesMisleadingAPIKeyType(t *testing.T) {
	host := &fakeAuthHost{
		entries: []cpaapi.HostAuthFileEntry{{
			AuthIndex: "auth-1", Name: "operator.json", Provider: "codex", Type: "codex",
			AccountType: "api_key", Source: "file", Path: "/auths/operator.json", Disabled: true,
		}},
		details: map[string]cpaapi.HostAuthGetResponse{
			"auth-1": {
				AuthIndex: "auth-1", Name: "operator.json", Path: "/auths/operator.json",
				JSON: json.RawMessage(`{"type":"codex","access_token":"oauth-secret","account_id":"workspace-123"}`),
			},
		},
	}
	var received managementAPICallRequest
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if errDecode := json.NewDecoder(request.Body).Decode(&received); errDecode != nil {
			t.Errorf("decode management request: %v", errDecode)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"statusCode":"402","body":{"detail":{"code":"deactivated_workspace"}}}`))
	}))
	defer server.Close()

	service := NewModelTestService(NewAccountService(host))
	service.doer = server.Client()
	result, errRun := service.Run(t.Context(), ModelTestRequest{AccountID: "auth-1", Inspection: true}, server.URL, "management-secret")
	if errRun != nil {
		t.Fatalf("Run() error = %v", errRun)
	}
	if received.URL != "https://chatgpt.com/backend-api/wham/usage" {
		t.Fatalf("probe URL = %q, want the OAuth credential endpoint", received.URL)
	}
	if result.ProbeKind != InspectionProbeKindCredential || result.StatusCode != 402 || result.ReasonCode != "workspace_deactivated" {
		t.Fatalf("result = %#v", result)
	}
}

func TestInspectionCodexAlwaysUsesCredentialProbeForAPIKeyRuntimeMetadata(t *testing.T) {
	host := &fakeAuthHost{
		entries: []cpaapi.HostAuthFileEntry{{
			AuthIndex: "auth-1", Name: "operator.json", Provider: "codex", Type: "codex",
			AccountType: "api_key", Source: "file", Path: "/auths/operator.json", Disabled: true,
		}},
		details: map[string]cpaapi.HostAuthGetResponse{
			"auth-1": {
				AuthIndex: "auth-1", Name: "operator.json", Path: "/auths/operator.json",
				JSON: json.RawMessage(`{"type":"codex","api_key":"runtime-label-only"}`),
			},
		},
	}
	var received managementAPICallRequest
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if errDecode := json.NewDecoder(request.Body).Decode(&received); errDecode != nil {
			t.Errorf("decode management request: %v", errDecode)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"status_code":402,"body":{"detail":{"code":"deactivated_workspace"}}}`))
	}))
	defer server.Close()

	service := NewModelTestService(NewAccountService(host))
	service.doer = server.Client()
	result, errRun := service.Run(t.Context(), ModelTestRequest{AccountID: "auth-1", Inspection: true}, server.URL, "management-secret")
	if errRun != nil {
		t.Fatalf("Run() error = %v", errRun)
	}
	if received.URL != "https://chatgpt.com/backend-api/wham/usage" {
		t.Fatalf("probe URL = %q, want the Codex credential endpoint", received.URL)
	}
	if result.ProbeKind != InspectionProbeKindCredential || result.ReasonCode != "workspace_deactivated" {
		t.Fatalf("result = %#v", result)
	}
}
