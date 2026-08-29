package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

type fakeProbeTransport struct {
	responses []cpaapi.HostHTTPResponse
	errors    []error
	statuses  map[int]int
	requests  []cpaapi.HostHTTPRequest
	calls     int
}

func (t *fakeProbeTransport) AgentIdentityDo(_ context.Context, _ string, request cpaapi.HostHTTPRequest) (cpaapi.HostHTTPResponse, error) {
	t.requests = append(t.requests, request)
	index := t.calls
	t.calls++
	if index < len(t.errors) && t.errors[index] != nil {
		return cpaapi.HostHTTPResponse{}, t.errors[index]
	}
	if len(t.errors) > 0 {
		// When any error is configured, keep failing so multi-candidate
		// probing cannot accidentally fall through to a default 200.
		return cpaapi.HostHTTPResponse{}, t.errors[len(t.errors)-1]
	}
	if status, ok := t.statuses[index]; ok {
		if status == http.StatusOK {
			return cpaapi.HostHTTPResponse{StatusCode: status}, nil
		}
		return cpaapi.HostHTTPResponse{}, fmt.Errorf("upstream returned %d", status)
	}
	if index < len(t.responses) {
		return t.responses[index], nil
	}
	return cpaapi.HostHTTPResponse{StatusCode: http.StatusOK}, nil
}

func (t *fakeProbeTransport) AgentIdentityDoStream(context.Context, string, cpaapi.HostHTTPRequest) (cpaapi.HostHTTPStreamResponse, error) {
	return cpaapi.HostHTTPStreamResponse{}, nil
}

func (t *fakeProbeTransport) AgentIdentityReadStream(context.Context, string) (cpaapi.HostHTTPStreamReadResponse, error) {
	return cpaapi.HostHTTPStreamReadResponse{}, nil
}

func (t *fakeProbeTransport) AgentIdentityCloseHTTPStream(context.Context, string) error { return nil }
func (t *fakeProbeTransport) AgentIdentityEmitStream(context.Context, cpaapi.HostStreamEmitRequest) error {
	return nil
}
func (t *fakeProbeTransport) AgentIdentityCloseStream(context.Context, cpaapi.HostStreamCloseRequest) error {
	return nil
}

func TestAIProviderProbeReachesEndpoint(t *testing.T) {
	app := &App{agentIdentity: &AgentIdentityExperiment{transport: &fakeProbeTransport{responses: []cpaapi.HostHTTPResponse{{StatusCode: http.StatusOK}}}}}
	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPost,
		Path:   "/v0/management/plugins/cpa-account-config-manager/ai-providers/test",
		Headers: http.Header{
			"Authorization": []string{"Bearer management-secret"},
		},
		Body: []byte(`{"kind":"openai-compatibility","base_url":"https://api.example.com/v1","api_key":"sk-test"}`),
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("probe status = %d, body = %s", response.StatusCode, response.Body)
	}
	if !strings.Contains(string(response.Body), `"reachable":true`) {
		t.Fatalf("probe body = %s", response.Body)
	}
}

func TestAIProviderProbeRejectsUnauthorized(t *testing.T) {
	app := &App{agentIdentity: &AgentIdentityExperiment{transport: &fakeProbeTransport{responses: []cpaapi.HostHTTPResponse{{StatusCode: http.StatusUnauthorized}}}}}
	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPost,
		Path:   "/v0/management/plugins/cpa-account-config-manager/ai-providers/test",
		Headers: http.Header{
			"Authorization": []string{"Bearer management-secret"},
		},
		Body: []byte(`{"kind":"gemini-api-key","base_url":"https://generativelanguage.googleapis.com/v1beta","api_key":"bad"}`),
	})
	if response.StatusCode != http.StatusBadGateway {
		t.Fatalf("probe status = %d, body = %s", response.StatusCode, response.Body)
	}
	if !strings.Contains(string(response.Body), "rejected the credential") {
		t.Fatalf("probe body = %s", response.Body)
	}
}

func TestAIProviderProbeUsesProviderSpecificAuthenticationAndDefaultEndpoint(t *testing.T) {
	transport := &fakeProbeTransport{responses: []cpaapi.HostHTTPResponse{{StatusCode: http.StatusOK}}}
	app := &App{agentIdentity: &AgentIdentityExperiment{transport: transport}}
	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPost,
		Path:   "/v0/management/plugins/cpa-account-config-manager/ai-providers/test",
		Headers: http.Header{
			"Authorization": []string{"Bearer management-secret"},
		},
		Body: []byte(`{"kind":"claude-api-key","api_key":"claude-secret"}`),
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("probe status = %d, body = %s", response.StatusCode, response.Body)
	}
	if len(transport.requests) != 1 {
		t.Fatalf("probe requests = %d", len(transport.requests))
	}
	request := transport.requests[0]
	if request.URL != "https://api.anthropic.com/v1/models" {
		t.Fatalf("probe URL = %q", request.URL)
	}
	if request.Headers.Get("x-api-key") != "claude-secret" || request.Headers.Get("anthropic-version") == "" {
		t.Fatalf("provider authentication headers were not applied")
	}
	if request.Headers.Get("Authorization") != "" {
		t.Fatalf("Claude API key leaked into Authorization header")
	}
}

func TestAIProviderProbeRequiresAPIKey(t *testing.T) {
	app := &App{agentIdentity: &AgentIdentityExperiment{transport: &fakeProbeTransport{}}}
	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPost,
		Path:   "/v0/management/plugins/cpa-account-config-manager/ai-providers/test",
		Headers: http.Header{
			"Authorization": []string{"Bearer management-secret"},
		},
		Body: []byte(`{"kind":"api-keys"}`),
	})
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("probe status = %d, body = %s", response.StatusCode, response.Body)
	}
}

func TestAIProviderProbeFallsBackFromModelsPathToBaseURL(t *testing.T) {
	transport := &fakeProbeTransport{statuses: map[int]int{0: http.StatusNotFound}}
	app := &App{agentIdentity: &AgentIdentityExperiment{transport: transport}}
	result := app.probeAIProviderEndpoint(context.Background(), "openai-compatibility", "https://kimi.example/v1", "key", "", nil, time.Second)
	if !result.Reachable || result.StatusCode != http.StatusOK {
		t.Fatalf("result = %#v", result)
	}
	if len(transport.requests) != 2 || transport.requests[0].URL != "https://kimi.example/v1/models" || transport.requests[1].URL != "https://kimi.example/v1" {
		t.Fatalf("requests = %#v", transport.requests)
	}
	if transport.requests[1].Headers.Get("Authorization") != "Bearer key" {
		t.Fatalf("fallback authentication headers missing: %#v", transport.requests[1].Headers)
	}
}

func TestAIProviderProbeStopsAfterUnauthorizedWithoutFallback(t *testing.T) {
	transport := &fakeProbeTransport{responses: []cpaapi.HostHTTPResponse{{StatusCode: http.StatusUnauthorized}, {StatusCode: http.StatusOK}}}
	app := &App{agentIdentity: &AgentIdentityExperiment{transport: transport}}
	result := app.probeAIProviderEndpoint(context.Background(), "openai-compatibility", "https://provider.example/v1", "bad", "", nil, time.Second)
	if result.Reachable || result.StatusCode != http.StatusUnauthorized {
		t.Fatalf("result = %#v", result)
	}
	if len(transport.requests) != 1 {
		t.Fatalf("unauthorized probe retried unexpectedly: %d", len(transport.requests))
	}
}

func TestAIProviderProbeTimeout(t *testing.T) {
	app := &App{agentIdentity: &AgentIdentityExperiment{transport: &fakeProbeTransport{errors: []error{context.DeadlineExceeded}}}}
	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPost,
		Path:   "/v0/management/plugins/cpa-account-config-manager/ai-providers/test",
		Headers: http.Header{
			"Authorization": []string{"Bearer management-secret"},
		},
		Body: []byte(`{"kind":"claude-api-key","base_url":"https://api.anthropic.com/v1","api_key":"k","timeout_seconds":5}`),
	})
	if response.StatusCode != http.StatusBadGateway {
		t.Fatalf("probe status = %d, body = %s", response.StatusCode, response.Body)
	}
	if !strings.Contains(string(response.Body), "timed out") {
		t.Fatalf("probe body = %s", response.Body)
	}
	_ = time.Second
}

func TestAIProviderProbeUsesInteractionsDefaultEndpoint(t *testing.T) {
	transport := &fakeProbeTransport{responses: []cpaapi.HostHTTPResponse{{StatusCode: http.StatusOK}}}
	app := &App{agentIdentity: &AgentIdentityExperiment{transport: transport}}
	result := app.probeAIProviderEndpoint(context.Background(), "interactions-api-key", defaultAIProviderProbeURL("interactions-api-key"), "google-secret", "", nil, time.Second)
	if !result.Reachable {
		t.Fatalf("result = %#v", result)
	}
	if len(transport.requests) != 1 {
		t.Fatalf("requests = %#v", transport.requests)
	}
	request := transport.requests[0]
	if request.URL != "https://generativelanguage.googleapis.com/v1beta/models" {
		t.Fatalf("probe URL = %q", request.URL)
	}
	if request.Headers.Get("x-goog-api-key") != "google-secret" {
		t.Fatalf("interactions API key header missing: %#v", request.Headers)
	}
}

func TestAIProviderProbeUsesVertexOriginWithoutCatalogPath(t *testing.T) {
	transport := &fakeProbeTransport{}
	app := &App{agentIdentity: &AgentIdentityExperiment{transport: transport}}
	result := app.probeAIProviderEndpoint(
		context.Background(),
		"vertex-api-key",
		defaultAIProviderProbeURL("vertex-api-key"),
		"vertex-secret",
		"",
		nil,
		time.Second,
	)
	if !result.Reachable {
		t.Fatalf("result = %#v", result)
	}
	if len(transport.requests) != 1 || transport.requests[0].URL != "https://aiplatform.googleapis.com" {
		t.Fatalf("requests = %#v", transport.requests)
	}
	if transport.requests[0].Headers.Get("x-goog-api-key") != "vertex-secret" {
		t.Fatalf("vertex API key header missing: %#v", transport.requests[0].Headers)
	}
}

func TestAIProviderCodexProbeUsesStableConvergedIdentity(t *testing.T) {
	transport := &fakeProbeTransport{responses: []cpaapi.HostHTTPResponse{{StatusCode: http.StatusOK}}}
	host := &fakeAuthHost{details: map[string]cpaapi.HostAuthGetResponse{
		"provider-auth": {Name: "provider.json", Path: "/auths/provider.json", JSON: json.RawMessage(`{"account_type":"api_key","api_key":"redacted"}`)},
	}}
	accounts := NewAccountService(host)
	app := &App{
		agentIdentity: &AgentIdentityExperiment{transport: transport},
	}
	run := func(mode string) cpaapi.HostHTTPRequest {
		settings := ExperimentalCodexIdentitySettings{OutboundConvergenceEnabled: true, ConvergenceMode: mode}
		app.requestHooks = NewRequestHook(NewCodexIdentityExperiment(stubCodexSettings{settings}, accounts))
		response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
			Method:  http.MethodPost,
			Path:    "/v0/management/plugins/cpa-account-config-manager/ai-providers/test",
			Headers: http.Header{"Authorization": []string{"Bearer management-secret"}},
			Body:    []byte(`{"kind":"codex-api-key","api_key":"codex-secret","auth_id":"provider-auth","timeout_seconds":1}`),
		})
		if response.StatusCode != http.StatusOK {
			t.Fatalf("mode %s probe status = %d body = %s", mode, response.StatusCode, response.Body)
		}
		return transport.requests[len(transport.requests)-1]
	}
	off := run("off")
	for _, name := range []string{"X-Codex-Installation-Id", "X-Codex-Window-Id", "Session-Id", "Thread-Id", "X-Codex-Turn-Metadata"} {
		if off.Headers.Get(name) != "" {
			t.Fatalf("codex probe off identity changed %s = %#v", name, off.Headers.Get(name))
		}
	}

	device := run("device")
	deviceInstall := device.Headers.Get("X-Codex-Installation-Id")
	if deviceInstall == "" || device.Headers.Get("Session-Id") != "" ||
		device.Headers.Get("X-Codex-Window-Id") != "" ||
		device.Headers.Get("X-Codex-Turn-Metadata") == "" {
		t.Fatalf("codex probe device identity = %#v", device.Headers)
	}

	full := run("full")
	if full.Headers.Get("X-Codex-Installation-Id") != deviceInstall ||
		full.Headers.Get("Session-Id") == "" ||
		full.Headers.Get("Session-Id") != full.Headers.Get("Thread-Id") {
		t.Fatalf("codex probe full identity = %#v", full.Headers)
	}
	if len(host.saves) != 0 {
		t.Fatalf("AI-provider credential writes = %d, want 0", len(host.saves))
	}
}

func TestAIProviderModelProbeUsesConfiguredModelAndClassifiesResponse(t *testing.T) {
	transport := &fakeProbeTransport{responses: []cpaapi.HostHTTPResponse{{
		StatusCode: http.StatusOK,
		Headers:    http.Header{"Content-Type": []string{"application/json"}},
		Body:       []byte(`{"id":"chatcmpl_test","object":"chat.completion","choices":[{"message":{"content":"OK"}}]}`),
	}}}
	app := &App{agentIdentity: &AgentIdentityExperiment{transport: transport}}
	result := app.probeAIProviderModel(context.Background(), "openai-compatibility", "https://provider.example/v1", "sk-secret", "", "provider-model", nil, time.Second)
	if !result.Reachable || result.Status != "available" || result.ReasonCode != "model_response_ok" {
		t.Fatalf("result = %#v", result)
	}
	if result.Model != "provider-model" || result.Response == nil {
		t.Fatalf("model/response = %#v", result)
	}
	if len(transport.requests) != 1 {
		t.Fatalf("requests = %#v", transport.requests)
	}
	request := transport.requests[0]
	if request.Method != http.MethodPost || request.URL != "https://provider.example/v1/chat/completions" {
		t.Fatalf("request = %#v", request)
	}
	if !bytes.Contains(request.Body, []byte(`"model":"provider-model"`)) {
		t.Fatalf("request body = %s", request.Body)
	}
	if request.Headers.Get("Authorization") != "Bearer sk-secret" {
		t.Fatalf("authorization header missing: %#v", request.Headers)
	}
}

func TestAIProviderCodexModelProbeUsesConfiguredBaseURL(t *testing.T) {
	transport := &fakeProbeTransport{responses: []cpaapi.HostHTTPResponse{{
		StatusCode: http.StatusOK,
		Headers:    http.Header{"Content-Type": []string{"application/json"}},
		Body:       []byte(`{"id":"resp_test","object":"response","status":"completed","output":[]}`),
	}}}
	app := &App{agentIdentity: &AgentIdentityExperiment{transport: transport}}
	result := app.probeAIProviderModel(context.Background(), "codex-api-key", "https://gptpro.live/v1", "sk-provider", "", "gpt-5.6-sol", nil, time.Second)
	if !result.Reachable || result.Status != "available" {
		t.Fatalf("result = %#v", result)
	}
	if len(transport.requests) != 1 {
		t.Fatalf("requests = %#v", transport.requests)
	}
	request := transport.requests[0]
	if request.Method != http.MethodPost || request.URL != "https://gptpro.live/v1/responses" {
		t.Fatalf("request = %#v", request)
	}
	if request.Headers.Get("Authorization") != "Bearer sk-provider" {
		t.Fatalf("authorization header missing: %#v", request.Headers)
	}
	if !bytes.Contains(request.Body, []byte(`"model":"gpt-5.6-sol"`)) {
		t.Fatalf("request body = %s", request.Body)
	}
}

func TestAIProviderModelProbeClassifiesExpectedFailures(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		status     string
		reason     string
	}{
		{name: "unauthorized", statusCode: http.StatusUnauthorized, body: `{"error":"invalid key"}`, status: "unavailable", reason: "authentication_failed"},
		{name: "quota", statusCode: http.StatusTooManyRequests, body: `{"error":{"type":"usage_limit_reached","message":"usage limit has been reached"}}`, status: "review", reason: "quota_limited"},
		{name: "model missing", statusCode: http.StatusBadRequest, body: `{"error":{"message":"model not found"}}`, status: "unavailable", reason: "model_not_found"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := &fakeProbeTransport{responses: []cpaapi.HostHTTPResponse{{StatusCode: test.statusCode, Body: []byte(test.body)}}}
			app := &App{agentIdentity: &AgentIdentityExperiment{transport: transport}}
			result := app.probeAIProviderModel(context.Background(), "openai-compatibility", "https://provider.example/v1", "sk-secret", "", "provider-model", nil, time.Second)
			if result.Status != test.status || result.ReasonCode != test.reason || result.StatusCode != test.statusCode {
				t.Fatalf("result = %#v", result)
			}
			if result.Reachable {
				t.Fatalf("failure result is reachable: %#v", result)
			}
		})
	}
}

func TestAIProviderModelCatalogParsing(t *testing.T) {
	models := parseAIProviderModelCatalog([]byte(`{"data":[{"id":"gpt-4o-mini","object":"model","owned_by":"openai"},{"id":"gpt-4o-mini"}]}`))
	if len(models) != 1 || models[0].ID != "gpt-4o-mini" || models[0].OwnedBy != "openai" {
		t.Fatalf("models = %#v", models)
	}
	models = parseAIProviderModelCatalog([]byte(`{"models":[{"name":"models/gemini-2.5-flash","displayName":"Gemini Flash"}]}`))
	if len(models) != 1 || models[0].ID != "gemini-2.5-flash" || models[0].DisplayName != "Gemini Flash" {
		t.Fatalf("gemini models = %#v", models)
	}
}

func TestAIProviderRuntimeSnapshotCannotFabricateProviderConcurrencyControl(t *testing.T) {
	tracker := NewProviderRuntimeTracker(nil)
	tracker.ObserveUsage(cpaapi.UsageRecord{
		Provider:  "openai",
		AuthIndex: "provider-index",
		Model:     "gpt-5.5",
		Detail:    cpaapi.UsageDetail{TotalTokens: 12},
	})
	snapshots := tracker.Snapshot()
	if len(snapshots) != 1 {
		t.Fatalf("snapshot count = %d, want 1", len(snapshots))
	}
	if !snapshots[0].Supported {
		t.Fatal("identified provider runtime should remain observable")
	}
	if snapshots[0].Active != 0 || snapshots[0].Limit != 0 {
		t.Fatalf("observability snapshot should not invent admission state: active=%d limit=%d", snapshots[0].Active, snapshots[0].Limit)
	}
	if snapshots[0].ConcurrencyConfigurable {
		t.Fatal("CPA provider/API-key concurrency is not configurable through the plugin and must not be reported as configurable")
	}
}
