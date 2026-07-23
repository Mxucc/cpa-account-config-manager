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
			Header: map[string][]string{
				"Content-Type":  {"text/event-stream"},
				"X-Request-ID":  {"request-123"},
				"Set-Cookie":    {"session=response-cookie-secret"},
				"Authorization": {"Bearer response-header-secret"},
			},
			Body: "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\"}}\naccess_token=response-body-secret\n\n",
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
	if result.Response == nil || result.Response.Format != "text" || !strings.Contains(result.Response.Body, "response.completed") {
		t.Fatalf("response preview = %#v", result.Response)
	}
	if len(result.Response.Headers) != 2 || result.Response.Headers[0].Name != "content-type" || result.Response.Headers[1].Name != "x-request-id" {
		t.Fatalf("safe response headers = %#v", result.Response.Headers)
	}
	if received.AuthIndex != "auth-1" || received.URL != "https://chatgpt.com/backend-api/codex/responses" {
		t.Fatalf("CPA api-call target = %#v", received)
	}
	if received.Header["Authorization"] != "Bearer $TOKEN$" || received.Header["Chatgpt-Account-Id"] != "workspace-123" {
		t.Fatalf("probe headers = %#v", received.Header)
	}
	for _, secret := range []string{"management-secret", "upstream-secret", "response-cookie-secret", "response-header-secret", "response-body-secret"} {
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

func TestHandleAgentIdentityModelTestUsesSelectedCPAAuthForCredentialAndModelProbes(t *testing.T) {
	host := &fakeAuthHost{
		entries: []cpaapi.HostAuthFileEntry{{
			AuthIndex: "agent-auth", Name: "agent.json", Provider: agentIdentityProvider, Type: agentIdentityProvider,
			AccountType: "agent_identity", Source: "file", Path: "/auths/agent.json",
		}},
		details: map[string]cpaapi.HostAuthGetResponse{
			"agent-auth": {
				AuthIndex: "agent-auth", Name: "agent.json", Path: "/auths/agent.json",
				JSON: json.RawMessage(`{"type":"codex-agent-identity","auth_mode":"agentIdentity","account_id":"agent-account"}`),
			},
		},
	}
	received := make([]managementAPICallRequest, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var call managementAPICallRequest
		if errDecode := json.NewDecoder(request.Body).Decode(&call); errDecode != nil {
			t.Errorf("decode management request: %v", errDecode)
		}
		received = append(received, call)
		response := managementAPICallResponse{StatusCode: http.StatusOK, Header: map[string][]string{"Content-Type": {"application/json"}}}
		if strings.HasSuffix(call.URL, "/wham/usage") {
			response.Body = `{"rate_limit":{"allowed":true}}`
		} else {
			response.Header = map[string][]string{"Content-Type": {"text/event-stream"}}
			response.Body = "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"agent-response\"}}\n\n"
		}
		_ = json.NewEncoder(writer).Encode(response)
	}))
	defer server.Close()

	app := NewApp(host, []byte("index"))
	app.modelTests.doer = server.Client()
	app.Configure([]byte("data_dir: " + t.TempDir() + "\nmanagement_base_url: " + server.URL + "\n"))
	defer app.Close()
	body, _ := json.Marshal(ModelTestRequest{AccountID: "agent-auth", Model: "gpt-5.4"})
	response := app.HandleManagement(t.Context(), cpaapi.ManagementRequest{
		Method: http.MethodPost, Path: managementRoutePrefix + "/accounts/model-test",
		Headers: http.Header{"Authorization": []string{"Bearer management-secret"}}, Body: body,
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("Agent Identity model test = %d %s", response.StatusCode, response.Body)
	}
	var result ModelTestResult
	if errDecode := json.Unmarshal(response.Body, &result); errDecode != nil {
		t.Fatalf("decode result: %v", errDecode)
	}
	if result.Provider != agentIdentityProvider || result.Status != "available" || result.Model != "gpt-5.4" {
		t.Fatalf("Agent Identity model result = %#v", result)
	}
	if len(received) != 2 || received[0].AuthIndex != "agent-auth" || received[1].AuthIndex != "agent-auth" ||
		!strings.HasSuffix(received[0].URL, "/wham/usage") || !strings.HasSuffix(received[1].URL, "/codex/responses") {
		t.Fatalf("Agent Identity probes = %#v", received)
	}
}

func TestHandleAccountModelTestLoadsEnabledWeeklyOverdraftExperiment(t *testing.T) {
	host := &fakeAuthHost{
		entries: []cpaapi.HostAuthFileEntry{{
			AuthIndex: "auth-1", Name: "experimental.json", Provider: "codex", Type: "codex",
			AccountType: "oauth", Source: "file", Path: "/auths/experimental.json",
		}},
		details: map[string]cpaapi.HostAuthGetResponse{
			"auth-1": {
				AuthIndex: "auth-1", Name: "experimental.json", Path: "/auths/experimental.json",
				JSON: json.RawMessage(`{"type":"codex","access_token":"upstream-secret","account_id":"workspace-123"}`),
			},
		},
	}
	var received managementAPICallRequest
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if errDecode := json.NewDecoder(request.Body).Decode(&received); errDecode != nil {
			t.Errorf("decode management request: %v", errDecode)
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(managementAPICallResponse{
			StatusCode: http.StatusOK,
			Header:     map[string][]string{"Content-Type": {"text/event-stream"}},
			Body:       "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-experimental\"}}\n\n",
		})
	}))
	defer server.Close()

	app := NewApp(host, []byte("index"))
	app.modelTests.doer = server.Client()
	transformer, ok := app.modelTests.experimentalTransformer.(*WeeklyOverdraftExperiment)
	if !ok {
		t.Fatal("model test is not wired to the removable weekly-overdraft transformer")
	}
	transformer.newCallID = func() (string, bool) { return "call_cpa_overdraft_account_test", true }
	app.Configure([]byte("data_dir: " + t.TempDir() + "\nmanagement_base_url: " + server.URL + "\nexperimental_settings:\n  weekly_overdraft_enabled: true\n"))
	defer app.Close()

	body, _ := json.Marshal(ModelTestRequest{AccountID: "auth-1", Model: "gpt-5.4", ExperimentalWeeklyOverdraft: true})
	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method:  http.MethodPost,
		Path:    "/v0/management/plugins/cpa-account-config-manager/accounts/model-test",
		Headers: http.Header{"Authorization": []string{"Bearer management-secret"}},
		Body:    body,
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("experimental model test = %d %s", response.StatusCode, response.Body)
	}
	var result ModelTestResult
	if errDecode := json.Unmarshal(response.Body, &result); errDecode != nil {
		t.Fatalf("decode result: %v", errDecode)
	}
	if result.Experiment == nil || !result.Experiment.Applied || result.Experiment.Name != "weekly_overdraft" || result.Experiment.CallID != "call_cpa_overdraft_account_test" {
		t.Fatalf("experiment result = %#v", result.Experiment)
	}
	if received.URL != "https://chatgpt.com/backend-api/codex/responses" {
		t.Fatalf("experimental probe URL = %q", received.URL)
	}
	var payload struct {
		Input []struct {
			Type   string `json:"type"`
			Role   string `json:"role"`
			CallID string `json:"call_id"`
		} `json:"input"`
	}
	if errDecode := json.Unmarshal([]byte(received.Data), &payload); errDecode != nil {
		t.Fatalf("decode experimental probe data: %v", errDecode)
	}
	if len(payload.Input) != 3 || payload.Input[0].Type != "message" || payload.Input[0].Role != "user" ||
		payload.Input[1].Type != "custom_tool_call" || payload.Input[2].Type != "custom_tool_call_output" ||
		payload.Input[1].CallID != result.Experiment.CallID || payload.Input[2].CallID != result.Experiment.CallID {
		t.Fatalf("experimental input = %#v", payload.Input)
	}
	if strings.Contains(received.Data, "upstream-secret") || strings.Contains(string(response.Body), "upstream-secret") {
		t.Fatal("experimental model test leaked an account credential")
	}
}

func TestHandleAccountModelTestRejectsDisabledWeeklyOverdraftExperiment(t *testing.T) {
	app := NewApp(&fakeAuthHost{}, []byte("index"))
	app.Configure([]byte("data_dir: " + t.TempDir() + "\n"))
	defer app.Close()
	body, _ := json.Marshal(ModelTestRequest{AccountID: "auth-1", Model: "gpt-5.4", ExperimentalWeeklyOverdraft: true})
	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method:  http.MethodPost,
		Path:    "/v0/management/plugins/cpa-account-config-manager/accounts/model-test",
		Headers: http.Header{"Authorization": []string{"Bearer management-secret"}},
		Body:    body,
	})
	if response.StatusCode != http.StatusConflict || !strings.Contains(string(response.Body), "not enabled") {
		t.Fatalf("disabled experiment response = %d %s", response.StatusCode, response.Body)
	}
}

func TestModelTestResponsePreviewAllowListsDiagnosticsAndRedactsSecrets(t *testing.T) {
	preview := sanitizeModelTestResponsePreview(modelProbeHTTPResponse{
		StatusCode: http.StatusTooManyRequests,
		Header: map[string][]string{
			"Content-Type":          {"application/json"},
			"Retry-After":           {"45"},
			"X-RateLimit-Remaining": {"0"},
			"X-Request-ID":          {"request-429"},
			"Set-Cookie":            {"session=cookie-secret"},
			"Authorization":         {"Bearer header-secret"},
		},
		Body: []byte(`{
			"error":{"type":"rate_limit_error","code":"weekly_limit_reached","message":"Rate limited for private@example.com; retry later","access_token":"body-token-secret"},
			"rate_limit":{"allowed":false,"limit_reached":true,"secondary_window":{"used_percent":100,"reset_after_seconds":3600}},
			"account_id":"workspace-secret",
			"unknown_private_field":"must-not-be-returned",
			"output":{"text":"Authorization: Bearer inline-secret-token"}
		}`),
	})
	if preview == nil || preview.Format != "json" || preview.Truncated {
		t.Fatalf("preview = %#v", preview)
	}
	for _, expected := range []string{"rate_limit_error", "weekly_limit_reached", "used_percent", "reset_after_seconds", "[redacted]", "[redacted-email]", "_omitted_fields"} {
		if !strings.Contains(preview.Body, expected) {
			t.Errorf("preview body missing %q: %s", expected, preview.Body)
		}
	}
	for _, secret := range []string{"private@example.com", "body-token-secret", "workspace-secret", "must-not-be-returned", "inline-secret-token", "cookie-secret", "header-secret"} {
		encoded, _ := json.Marshal(preview)
		if strings.Contains(string(encoded), secret) {
			t.Errorf("preview leaked %q: %s", secret, encoded)
		}
	}
	if len(preview.Headers) != 4 {
		t.Fatalf("safe headers = %#v", preview.Headers)
	}
}

func TestModelTestResponsePreviewBoundsTextAndMarksTruncation(t *testing.T) {
	preview := sanitizeModelTestResponsePreview(modelProbeHTTPResponse{
		Body: []byte("api_key=plain-secret upstream diagnostic " + strings.Repeat("x", maxModelTestPreviewBytes*2)),
	})
	if preview == nil || preview.Format != "text" || !preview.Truncated || len(preview.Body) > maxModelTestPreviewBytes+32 {
		t.Fatalf("bounded preview = %#v", preview)
	}
	if strings.Contains(preview.Body, "plain-secret") || !strings.Contains(preview.Body, "[truncated]") {
		t.Fatalf("text preview was not safely truncated: %q", preview.Body)
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
	if result.Response != nil {
		t.Fatalf("inspection result retained an upstream response preview: %#v", result.Response)
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
	if result.Response != nil {
		t.Fatalf("inspection result retained an upstream response preview: %#v", result.Response)
	}
}
