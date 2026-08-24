package manager

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

type fakeProbeTransport struct {
	responses []cpaapi.HostHTTPResponse
	errors    []error
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
