package manager

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

func TestCodexPATParseVerifyRefreshAndExecute(t *testing.T) {
	now := time.Date(2026, time.July, 23, 9, 20, 0, 0, time.UTC)
	const token = "at-test-personal-access-token"
	raw, errMarshal := json.Marshal(map[string]any{
		"type": agentIdentityProvider, "auth_mode": codexPATAuthMode, "access_token": token,
		"email": "pat@example.com", "account_id": "pat-account", "chatgpt_account_id": "pat-account",
		"chatgpt_user_id": "pat-user", "plan_type": "plus", "chatgpt_plan_type": "plus",
	})
	if errMarshal != nil {
		t.Fatalf("Marshal() error = %v", errMarshal)
	}

	transport := &fakeAgentIdentityTransport{do: func(callbackID string, request cpaapi.HostHTTPRequest) (cpaapi.HostHTTPResponse, error) {
		switch request.URL {
		case codexPATWhoamiURL:
			if callbackID != "" || request.Method != http.MethodGet {
				t.Fatalf("whoami callback/method = %q/%q", callbackID, request.Method)
			}
			verifyCodexPATBearerHeaders(t, request.Headers, token, "")
			return cpaapi.HostHTTPResponse{StatusCode: http.StatusOK, Body: []byte(`{"email":"pat@example.com","chatgpt_user_id":"pat-user","chatgpt_account_id":"pat-account","chatgpt_plan_type":"plus","chatgpt_account_is_fedramp":false}`)}, nil
		case agentIdentityResponsesURL:
			if callbackID != "callback-pat" {
				t.Fatalf("response callback id = %q", callbackID)
			}
			verifyCodexPATBearerHeaders(t, request.Headers, token, "pat-account")
			if strings.Contains(string(request.Body), "old-model") || !strings.Contains(string(request.Body), `"model":"gpt-5.4"`) {
				t.Fatalf("PAT executor body = %s", request.Body)
			}
			return cpaapi.HostHTTPResponse{StatusCode: http.StatusOK, Body: []byte(`{"id":"response"}`)}, nil
		case agentIdentityUsageURL:
			if callbackID != "callback-usage" || request.Method != http.MethodGet {
				t.Fatalf("usage callback/method = %q/%q", callbackID, request.Method)
			}
			verifyCodexPATQuotaHeaders(t, request.Headers, token, "pat-account")
			return cpaapi.HostHTTPResponse{StatusCode: http.StatusOK, Body: []byte(`{"plan_type":"plus"}`)}, nil
		default:
			return cpaapi.HostHTTPResponse{}, fmt.Errorf("unexpected URL")
		}
	}}
	experiment := NewAgentIdentityExperiment(func() bool { return true }, transport)
	experiment.now = func() time.Time { return now }

	parsed, errParse := experiment.ParseAuth(raw)
	if errParse != nil || !parsed.Handled || parsed.Auth.Provider != agentIdentityProvider || parsed.Auth.Metadata["account_type"] != codexPATAccountType {
		t.Fatalf("ParseAuth() = %#v, %v", parsed, errParse)
	}
	if parsed.Auth.Metadata["access_token"] != token || parsed.Auth.Metadata["auth_kind"] != "oauth" {
		t.Fatal("PAT was not exposed to CPA's internal management API token substitution")
	}
	if parsed.Auth.Attributes[managementHTTPDelegate] != "true" {
		t.Fatal("PAT auth did not opt in to delegated management HTTP")
	}
	legacyRaw, errLegacy := json.Marshal(map[string]any{
		"type": "codex", "access_token": token, "refresh_token": "",
		"expired": "2027-07-23T00:00:00Z", "email": "pat@example.com",
		"account_id": "pat-account", "chatgpt_account_id": "pat-account",
		"chatgpt_user_id": "pat-user", "plan_type": "plus",
	})
	if errLegacy != nil {
		t.Fatalf("Marshal() legacy error = %v", errLegacy)
	}
	legacy, errLegacyParse := experiment.ParseAuth(legacyRaw)
	if errLegacyParse != nil || !legacy.Handled || legacy.Auth.Provider != agentIdentityProvider || legacy.Auth.Metadata["account_type"] != codexPATAccountType {
		t.Fatalf("legacy ParseAuth() = %#v, %v", legacy, errLegacyParse)
	}
	if _, exists := legacy.Auth.Metadata["expired"]; exists {
		t.Fatal("legacy PAT expiry was exposed to the CPA refresh scheduler")
	}
	if errVerify := experiment.VerifyImport(t.Context(), raw); errVerify != nil {
		t.Fatalf("VerifyImport() error = %v", errVerify)
	}
	refreshed, errRefresh := experiment.RefreshAuth(cpaapi.AuthRefreshRequest{AuthProvider: agentIdentityProvider, StorageJSON: raw})
	if errRefresh != nil || refreshed.Auth.Provider != agentIdentityProvider || refreshed.Auth.Metadata["account_type"] != codexPATAccountType {
		t.Fatalf("RefreshAuth() = %#v, %v", refreshed, errRefresh)
	}
	response, errExecute := experiment.Execute(t.Context(), cpaapi.ExecutorRequest{
		HostCallbackID: "callback-pat", StorageJSON: raw, Model: "gpt-5.4",
		Payload: []byte(`{"model":"old-model","input":[]}`),
	})
	if errExecute != nil || string(response.Payload) != `{"id":"response"}` {
		t.Fatalf("Execute() = %s, %v", response.Payload, errExecute)
	}
	usage, errUsage := experiment.HTTPRequest(t.Context(), cpaapi.ExecutorHTTPRequest{
		HostCallbackID: "callback-usage", StorageJSON: raw, Method: http.MethodGet, URL: agentIdentityUsageURL,
	})
	if errUsage != nil || usage.StatusCode != http.StatusOK || string(usage.Body) != `{"plan_type":"plus"}` {
		t.Fatalf("HTTPRequest() = %#v, %v", usage, errUsage)
	}
}

func TestCodexPATImportPreviewIsTypedAndRedacted(t *testing.T) {
	now := time.Date(2026, time.July, 23, 9, 25, 0, 0, time.UTC)
	const token = "at-preview-private-token"
	payload, errMarshal := json.Marshal(map[string]any{
		"type": "sub2api-data", "version": 1,
		"accounts": []any{map[string]any{
			"name": "PAT preview", "platform": "openai", "type": "oauth",
			"credentials": map[string]any{
				"access_token": token, "refresh_token": "", "expires_at": "2027-07-23T00:00:00Z",
				"email": "preview@example.com", "chatgpt_account_id": "preview-account",
				"chatgpt_user_id": "preview-user", "plan_type": "plus",
			},
		}},
	})
	if errMarshal != nil {
		t.Fatalf("Marshal() error = %v", errMarshal)
	}
	transport := &fakeAgentIdentityTransport{do: func(_ string, request cpaapi.HostHTTPRequest) (cpaapi.HostHTTPResponse, error) {
		if request.URL != codexPATWhoamiURL {
			return cpaapi.HostHTTPResponse{}, fmt.Errorf("unexpected URL")
		}
		return cpaapi.HostHTTPResponse{StatusCode: http.StatusOK, Body: []byte(`{"email":"preview@example.com","chatgpt_user_id":"preview-user","chatgpt_account_id":"preview-account","chatgpt_plan_type":"plus","chatgpt_account_is_fedramp":false}`)}, nil
	}}
	experiment := NewAgentIdentityExperiment(func() bool { return true }, transport)
	service := NewImportService(&fakeAuthHost{details: map[string]cpaapi.HostAuthGetResponse{}}, NewMutationCoordinator())
	service.SetAgentIdentityExperiment(experiment)
	service.now = func() time.Time { return now }
	defer service.Clear()

	preview, errPreview := service.Preview(t.Context(), importUpload{Name: "sub2api-selected-accounts.json", Data: payload})
	if errPreview != nil {
		t.Fatalf("Preview() error = %v", errPreview)
	}
	if preview.Total != 1 || len(preview.Items) != 1 || preview.Items[0].CredentialType != codexPATAccountType {
		t.Fatalf("preview = %#v", preview)
	}
	publicJSON, errPublic := json.Marshal(preview)
	if errPublic != nil {
		t.Fatalf("Marshal() preview error = %v", errPublic)
	}
	if strings.Contains(string(publicJSON), token) {
		t.Fatal("preview leaked Codex personal access token")
	}
}

func TestCodexPATRejectsInvalidCredentialWithoutEchoingToken(t *testing.T) {
	const token = "at-private-value-that-must-not-be-echoed"
	raw := []byte(`{"type":"codex-agent-identity","auth_mode":"personalAccessToken","access_token":"` + token + `"}`)
	transport := &fakeAgentIdentityTransport{do: func(_ string, _ cpaapi.HostHTTPRequest) (cpaapi.HostHTTPResponse, error) {
		return cpaapi.HostHTTPResponse{StatusCode: http.StatusUnauthorized}, nil
	}}
	experiment := NewAgentIdentityExperiment(func() bool { return true }, transport)
	errVerify := experiment.VerifyImport(t.Context(), raw)
	if errVerify == nil || strings.Contains(errVerify.Error(), token) {
		t.Fatalf("VerifyImport() error = %v", errVerify)
	}
}

func TestCodexPATExplicitModeRejectsMissingTokenAndUnavailableTransport(t *testing.T) {
	experiment := NewAgentIdentityExperiment(func() bool { return true }, nil)
	missing := []byte(`{"type":"codex-agent-identity","auth_mode":"personalAccessToken","account_id":"account"}`)
	parsed, errParse := experiment.ParseAuth(missing)
	if errParse == nil || parsed.Handled {
		t.Fatalf("missing-token ParseAuth() = %#v, %v", parsed, errParse)
	}
	raw := []byte(`{"type":"codex-agent-identity","auth_mode":"personalAccessToken","access_token":"at-test-token","account_id":"account"}`)
	_, errExecute := experiment.Execute(t.Context(), cpaapi.ExecutorRequest{StorageJSON: raw, Payload: []byte(`{"input":[]}`)})
	if errExecute == nil || !strings.Contains(errExecute.Error(), "transport") {
		t.Fatalf("missing-transport Execute() error = %v", errExecute)
	}
}

func TestCodexPATQuotaCompatibilityExposesHostTokenAndAllowsExactEndpoints(t *testing.T) {
	const token = "at-test-quota-personal-access-token"
	raw := []byte(`{"type":"codex-agent-identity","auth_mode":"personalAccessToken","access_token":"` + token + `","account_id":"quota-account"}`)
	transport := &fakeAgentIdentityTransport{do: func(_ string, request cpaapi.HostHTTPRequest) (cpaapi.HostHTTPResponse, error) {
		verifyCodexPATQuotaHeaders(t, request.Headers, token, "quota-account")
		return cpaapi.HostHTTPResponse{StatusCode: http.StatusOK, Body: []byte(`{"ok":true}`)}, nil
	}}
	experiment := NewAgentIdentityExperiment(func() bool { return true }, transport)
	parsed, errParse := experiment.ParseAuth(raw)
	if errParse != nil || !parsed.Handled {
		t.Fatalf("ParseAuth() = %#v, %v", parsed, errParse)
	}
	if parsed.Auth.Metadata["access_token"] != token {
		t.Fatal("PAT was not exposed to CPA's internal management API token substitution")
	}
	for _, request := range []struct {
		method string
		target string
	}{
		{method: http.MethodGet, target: agentIdentityUsageURL},
		{method: http.MethodGet, target: codexRateLimitCreditsURL},
		{method: http.MethodPost, target: codexRateLimitConsumeURL},
	} {
		if !allowedAgentIdentityURL(request.target) {
			t.Fatalf("quota URL was rejected: %s", request.target)
		}
		response, errRequest := experiment.HTTPRequest(t.Context(), cpaapi.ExecutorHTTPRequest{
			StorageJSON: raw, Method: request.method, URL: request.target, Body: []byte(`{"redeem_request_id":"test"}`),
		})
		if errRequest != nil || response.StatusCode != http.StatusOK {
			t.Fatalf("HTTPRequest(%s) = %#v, %v", request.target, response, errRequest)
		}
	}
	for _, target := range []string{
		"https://chatgpt.com/backend-api/wham/rate-limit-reset-credits/extra",
		"https://chatgpt.com/backend-api/wham/rate-limit-reset-credits-evil",
	} {
		if allowedAgentIdentityURL(target) {
			t.Fatalf("unexpected quota URL was allowed: %s", target)
		}
	}
}

func verifyCodexPATBearerHeaders(t *testing.T, headers http.Header, token, accountID string) {
	t.Helper()
	if headers.Get("Authorization") != "Bearer "+token || headers.Get("Originator") != "codex_cli_rs" {
		t.Fatalf("PAT authentication headers are invalid")
	}
	if headers.Get("ChatGPT-Account-ID") != accountID {
		t.Fatalf("PAT account header = %q, want %q", headers.Get("ChatGPT-Account-ID"), accountID)
	}
}

func verifyCodexPATQuotaHeaders(t *testing.T, headers http.Header, token, accountID string) {
	t.Helper()
	if headers.Get("Authorization") != "Bearer "+token || headers.Get("ChatGPT-Account-ID") != accountID {
		t.Fatal("PAT quota authentication headers are invalid")
	}
	if headers.Get("Originator") != "Codex Desktop" || headers.Get("OpenAI-Beta") != "codex-1" || headers.Get("OAI-Language") != "zh-CN" {
		t.Fatal("PAT quota compatibility headers are invalid")
	}
	if headers.Get("Sec-Fetch-Site") != "none" || headers.Get("Sec-Fetch-Mode") != "no-cors" || headers.Get("Sec-Fetch-Dest") != "empty" || headers.Get("Priority") != "u=4, i" {
		t.Fatal("PAT quota fetch headers are invalid")
	}
}
