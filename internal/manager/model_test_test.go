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
