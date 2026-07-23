package manager

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

func TestAgentIdentitySessionLoginConvertsAndPollsWithoutOAuthSecrets(t *testing.T) {
	now := time.Date(2026, time.July, 23, 13, 30, 0, 0, time.UTC)
	accessToken := testAgentIdentitySessionAccessToken(t, now, "session@example.com")
	sessionToken := "session-cookie-material-must-not-survive"
	sessionJSON, errSession := json.Marshal(map[string]any{"accessToken": accessToken, "sessionToken": sessionToken})
	if errSession != nil {
		t.Fatalf("Marshal() Session error = %v", errSession)
	}
	var requestCount int
	transport := &fakeAgentIdentityTransport{do: func(callbackID string, request cpaapi.HostHTTPRequest) (cpaapi.HostHTTPResponse, error) {
		requestCount++
		if callbackID != "callback-login" {
			t.Fatalf("callback id = %q", callbackID)
		}
		switch requestCount {
		case 1:
			if request.URL != agentIdentityRegistrationBase+"/v1/agent/register" || request.Method != http.MethodPost {
				t.Fatalf("runtime request = %s %s", request.Method, request.URL)
			}
			if request.Headers.Get("Authorization") != "Bearer "+accessToken {
				t.Fatal("runtime registration did not use the submitted access token")
			}
			if !strings.Contains(string(request.Body), `"agent_public_key":"ssh-ed25519 `) {
				t.Fatal("runtime registration omitted the SSH Ed25519 public key")
			}
			return cpaapi.HostHTTPResponse{StatusCode: http.StatusOK, Body: []byte(`{"agent_runtime_id":"runtime-session"}`)}, nil
		case 2:
			if request.URL != agentIdentityRegistrationBase+"/v1/agent/runtime-session/task/register" || request.Headers.Get("Authorization") != "" {
				t.Fatalf("task registration request = %s with unexpected authorization", request.URL)
			}
			return cpaapi.HostHTTPResponse{StatusCode: http.StatusOK, Body: []byte(`{"task_id":"task-session"}`)}, nil
		default:
			t.Fatalf("unexpected registration request %d", requestCount)
			return cpaapi.HostHTTPResponse{}, nil
		}
	}}
	experiment := NewAgentIdentityExperiment(func() bool { return true }, transport)
	experiment.now = func() time.Time { return now }
	defer experiment.Clear()

	started, errStart := experiment.StartLogin(cpaapi.AuthLoginStartRequest{Provider: agentIdentityProvider})
	if errStart != nil {
		t.Fatalf("StartLogin() error = %v", errStart)
	}
	if started.State == "" || !strings.HasPrefix(started.URL, agentIdentityLoginPagePath+"?agent_identity_login=") || !started.ExpiresAt.Equal(now.Add(agentIdentityLoginTTL)) {
		t.Fatalf("StartLogin() = %#v", started)
	}
	if pending := experiment.PollLogin(cpaapi.AuthLoginPollRequest{State: started.State}); pending.Status != "pending" {
		t.Fatalf("initial PollLogin() = %#v", pending)
	}
	result, errComplete := experiment.CompleteSessionLogin(t.Context(), "callback-login", started.State, sessionJSON)
	if errComplete != nil {
		t.Fatalf("CompleteSessionLogin() error = %v", errComplete)
	}
	if result.Status != "completed" || result.Account.Email != "session@example.com" || result.Account.PlanType != "team" || result.Account.Provider != agentIdentityProvider {
		t.Fatalf("CompleteSessionLogin() = %#v", result)
	}
	completed := experiment.PollLogin(cpaapi.AuthLoginPollRequest{State: started.State})
	if completed.Status != "success" || completed.Auth.FileName != "codex-session_example_com.json" || completed.Auth.Provider != agentIdentityProvider {
		t.Fatalf("completed PollLogin() = %#v", completed)
	}
	parsed, errParse := parseAgentIdentityCredential(completed.Auth.StorageJSON, now)
	if errParse != nil || parsed.claims.AgentRuntimeID != "runtime-session" || parsed.taskID != "task-session" || parsed.claims.PlanType != "team" {
		t.Fatalf("generated credential parse = %#v, %v", parsed.claims, errParse)
	}
	publicResult, _ := json.Marshal(result)
	for _, secret := range []string{accessToken, sessionToken, parsed.claims.AgentPrivateKey, parsed.taskID} {
		if strings.Contains(string(completed.Auth.StorageJSON), accessToken) || strings.Contains(string(completed.Auth.StorageJSON), sessionToken) || strings.Contains(string(publicResult), secret) {
			t.Fatal("Session login leaked OAuth or Agent Identity credential material")
		}
	}
	if requestCount != 2 {
		t.Fatalf("request count = %d, want 2", requestCount)
	}
}

func TestAgentIdentitySessionLoginRejectsInvalidExpiredAndUpstreamFailureWithoutLeaks(t *testing.T) {
	now := time.Date(2026, time.July, 23, 13, 40, 0, 0, time.UTC)
	transport := &fakeAgentIdentityTransport{do: func(_ string, _ cpaapi.HostHTTPRequest) (cpaapi.HostHTTPResponse, error) {
		return cpaapi.HostHTTPResponse{StatusCode: http.StatusUnauthorized, Body: []byte(`{"error":"upstream-secret-body"}`)}, nil
	}}
	experiment := NewAgentIdentityExperiment(func() bool { return true }, transport)
	experiment.now = func() time.Time { return now }
	defer experiment.Clear()

	invalid, errStart := experiment.StartLogin(cpaapi.AuthLoginStartRequest{})
	if errStart != nil {
		t.Fatalf("StartLogin() invalid state error = %v", errStart)
	}
	if _, errInvalid := experiment.CompleteSessionLogin(t.Context(), "", invalid.State, []byte(`{"sessionToken":"ignored-secret"}`)); !errors.Is(errInvalid, ErrAgentIdentitySessionInvalid) {
		t.Fatalf("invalid Session error = %v", errInvalid)
	}
	transport.mu.Lock()
	invalidRequests := len(transport.requests)
	transport.mu.Unlock()
	if invalidRequests != 0 {
		t.Fatalf("invalid Session made %d upstream requests", invalidRequests)
	}

	rejected, _ := experiment.StartLogin(cpaapi.AuthLoginStartRequest{})
	token := testAgentIdentitySessionAccessToken(t, now, "rejected@example.com")
	raw, _ := json.Marshal(map[string]string{"accessToken": token})
	_, errRejected := experiment.CompleteSessionLogin(t.Context(), "", rejected.State, raw)
	if !errors.Is(errRejected, ErrAgentIdentitySessionRejected) || strings.Contains(errRejected.Error(), "upstream-secret-body") || strings.Contains(errRejected.Error(), token) {
		t.Fatalf("rejected Session error = %v", errRejected)
	}

	expired, _ := experiment.StartLogin(cpaapi.AuthLoginStartRequest{})
	now = now.Add(agentIdentityLoginTTL + time.Second)
	if _, errExpired := experiment.CompleteSessionLogin(t.Context(), "", expired.State, raw); !errors.Is(errExpired, ErrAgentIdentityLoginExpired) {
		t.Fatalf("expired login error = %v", errExpired)
	}
}

func TestAgentIdentitySessionLoginManagementRouteIsRedactedAndAudited(t *testing.T) {
	now := time.Date(2026, time.July, 23, 13, 50, 0, 0, time.UTC)
	transport := &fakeAgentIdentityTransport{do: func(callbackID string, request cpaapi.HostHTTPRequest) (cpaapi.HostHTTPResponse, error) {
		if callbackID != "management-callback" {
			t.Fatalf("management callback id = %q", callbackID)
		}
		if strings.HasSuffix(request.URL, "/agent/register") {
			return cpaapi.HostHTTPResponse{StatusCode: http.StatusOK, Body: []byte(`{"agent_runtime_id":"runtime-management"}`)}, nil
		}
		return cpaapi.HostHTTPResponse{StatusCode: http.StatusOK, Body: []byte(`{"task_id":"task-management"}`)}, nil
	}}
	experiment := NewAgentIdentityExperiment(func() bool { return true }, transport)
	experiment.now = func() time.Time { return now }
	app := NewApp(&fakeAuthHost{}, []byte("index"))
	app.agentIdentity.Clear()
	app.agentIdentity = experiment
	defer app.Close()
	started, errStart := experiment.StartLogin(cpaapi.AuthLoginStartRequest{})
	if errStart != nil {
		t.Fatalf("StartLogin() error = %v", errStart)
	}
	token := testAgentIdentitySessionAccessToken(t, now, "management@example.com")
	sessionJSON, _ := json.Marshal(map[string]string{"accessToken": token})
	body, _ := json.Marshal(AgentIdentitySessionLoginRequest{State: started.State, SessionJSON: string(sessionJSON)})
	response := app.HandleManagement(t.Context(), cpaapi.ManagementRequest{
		Method: http.MethodPost,
		Path:   "/v0/management/plugins/cpa-account-config-manager/experiments/agent-identity/session-login",
		Body:   body, HostCallbackID: "management-callback",
	})
	if response.StatusCode != http.StatusOK || strings.Contains(string(response.Body), token) || strings.Contains(string(response.Body), "task-management") {
		t.Fatalf("management response = %d %s", response.StatusCode, response.Body)
	}
	journal := app.operations.List(OperationQuery{Page: 1, PageSize: operationPageSize})
	if len(journal.Operations) != 1 || journal.Operations[0].Action != OperationActionAgentIdentityLogin || journal.Operations[0].Status != OperationStatusSucceeded || journal.Operations[0].ReasonCode != "credential_converted" {
		t.Fatalf("operation journal = %#v", journal.Operations)
	}
}

func testAgentIdentitySessionAccessToken(t *testing.T, now time.Time, email string) string {
	t.Helper()
	header, _ := json.Marshal(map[string]string{"alg": "none", "typ": "JWT"})
	payload, _ := json.Marshal(map[string]any{
		"exp": now.Add(time.Hour).Unix(), "email": email,
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "account-session", "chatgpt_user_id": "user-session",
			"chatgpt_plan_type": "team", "chatgpt_account_is_fedramp": false,
		},
	})
	return base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload) + ".test-signature"
}
