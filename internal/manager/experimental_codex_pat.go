package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"cpa-account-config-manager/internal/cpaapi"
)

const (
	codexPATAuthMode        = "personalAccessToken"
	codexPATLegacyAuthMode  = "personal_access_token"
	codexPATAccountType     = "personal_access_token"
	codexPATWhoamiURL       = "https://auth.openai.com/api/accounts/v1/user-auth-credential/whoami"
	codexPATUserAgent       = "codex_cli_rs/0.144.1 (Ubuntu 22.4.0; x86_64) xterm-256color"
	codexPATMaxTokenLength  = 8192
	codexPATMaxWhoamiResult = 64 << 10
)

type codexPATCredential struct {
	Type             string `json:"type"`
	AuthMode         string `json:"auth_mode"`
	LegacyAuthMode   string `json:"openai_auth_mode"`
	AccessToken      string `json:"access_token"`
	Email            string `json:"email"`
	AccountID        string `json:"account_id"`
	ChatGPTAccountID string `json:"chatgpt_account_id"`
	ChatGPTUserID    string `json:"chatgpt_user_id"`
	PlanType         string `json:"plan_type"`
	ChatGPTPlanType  string `json:"chatgpt_plan_type"`
	Disabled         bool   `json:"disabled"`
	Priority         int    `json:"priority"`
	Note             string `json:"note"`
	Prefix           string `json:"prefix"`
	ProxyURL         string `json:"proxy_url"`
	Websockets       bool   `json:"websockets"`
}

type codexPATParsed struct {
	credential  codexPATCredential
	accessToken string
	accountID   string
	planType    string
}

type codexPATWhoamiResponse struct {
	Email                   string `json:"email"`
	ChatGPTUserID           string `json:"chatgpt_user_id"`
	ChatGPTAccountID        string `json:"chatgpt_account_id"`
	ChatGPTPlanType         string `json:"chatgpt_plan_type"`
	ChatGPTAccountIsFedRAMP *bool  `json:"chatgpt_account_is_fedramp"`
}

func isCodexPATCredential(raw []byte) bool {
	if len(raw) == 0 || len(raw) > agentIdentityMaxCredential {
		return false
	}
	var marker codexPATCredential
	if errDecode := json.Unmarshal(raw, &marker); errDecode != nil {
		return false
	}
	credentialType := strings.TrimSpace(marker.Type)
	if credentialType != "" && !strings.EqualFold(credentialType, agentIdentityProvider) && !strings.EqualFold(credentialType, "codex") {
		return false
	}
	mode := strings.TrimSpace(firstNonEmpty(marker.AuthMode, marker.LegacyAuthMode))
	if mode != "" {
		return isCodexPATAuthMode(mode)
	}
	return strings.HasPrefix(strings.TrimSpace(marker.AccessToken), "at-")
}

func isCodexPATAuthMode(mode string) bool {
	mode = strings.TrimSpace(mode)
	return strings.EqualFold(mode, codexPATAuthMode) || strings.EqualFold(mode, codexPATLegacyAuthMode)
}

func parseCodexPATCredential(raw []byte) (codexPATParsed, error) {
	if len(raw) == 0 || len(raw) > agentIdentityMaxCredential {
		return codexPATParsed{}, fmt.Errorf("Codex personal access token credential size is invalid")
	}
	var credential codexPATCredential
	if errDecode := json.Unmarshal(raw, &credential); errDecode != nil {
		return codexPATParsed{}, fmt.Errorf("decode Codex personal access token credential")
	}
	credential.Type = strings.TrimSpace(credential.Type)
	credential.AuthMode = strings.TrimSpace(credential.AuthMode)
	credential.LegacyAuthMode = strings.TrimSpace(credential.LegacyAuthMode)
	if mode := firstNonEmpty(credential.AuthMode, credential.LegacyAuthMode); mode != "" && !isCodexPATAuthMode(mode) {
		return codexPATParsed{}, fmt.Errorf("Codex personal access token auth mode is unsupported")
	}
	if credential.Type != "" && !strings.EqualFold(credential.Type, agentIdentityProvider) && !strings.EqualFold(credential.Type, "codex") {
		return codexPATParsed{}, fmt.Errorf("Codex personal access token credential type is unsupported")
	}
	token := strings.TrimSpace(credential.AccessToken)
	if !strings.HasPrefix(token, "at-") || len(token) > codexPATMaxTokenLength {
		return codexPATParsed{}, fmt.Errorf("Codex personal access token must use the at-* format")
	}
	accountID := strings.TrimSpace(firstNonEmpty(credential.AccountID, credential.ChatGPTAccountID))
	if accountID == "" || len(accountID) > 256 {
		return codexPATParsed{}, fmt.Errorf("Codex personal access token account id is missing or invalid")
	}
	credential.Email = strings.TrimSpace(credential.Email)
	credential.ChatGPTUserID = strings.TrimSpace(credential.ChatGPTUserID)
	if len(credential.Email) > 512 || len(credential.ChatGPTUserID) > 256 {
		return codexPATParsed{}, fmt.Errorf("Codex personal access token identity metadata is invalid")
	}
	planType := strings.TrimSpace(firstNonEmpty(credential.PlanType, credential.ChatGPTPlanType))
	if len(planType) > 64 {
		return codexPATParsed{}, fmt.Errorf("Codex personal access token plan type is invalid")
	}
	return codexPATParsed{credential: credential, accessToken: token, accountID: accountID, planType: planType}, nil
}

func codexPATAuthData(raw []byte, parsed codexPATParsed) cpaapi.AuthData {
	metadata := map[string]any{
		"type": agentIdentityProvider, "auth_mode": codexPATAuthMode,
		"account_type": codexPATAccountType, "email": parsed.credential.Email,
		"account_id": parsed.accountID, "chatgpt_account_id": parsed.accountID,
		"plan_type": parsed.planType, "chatgpt_plan_type": parsed.planType,
		"priority": parsed.credential.Priority, "note": parsed.credential.Note,
	}
	attributes := map[string]string{"plan_type": parsed.planType, "auth_mode": codexPATAuthMode}
	return cpaapi.AuthData{
		Provider: agentIdentityProvider, Label: parsed.credential.Email,
		Prefix: parsed.credential.Prefix, ProxyURL: parsed.credential.ProxyURL,
		Disabled: parsed.credential.Disabled, StorageJSON: append([]byte(nil), raw...),
		Metadata: metadata, Attributes: attributes,
	}
}

func (e *AgentIdentityExperiment) verifyCodexPATImport(ctx context.Context, parsed codexPATParsed) error {
	if e == nil || e.transport == nil {
		return fmt.Errorf("CPA host HTTP transport is unavailable")
	}
	response, errRequest := e.transport.AgentIdentityDo(ctx, "", cpaapi.HostHTTPRequest{
		Method: http.MethodGet, URL: codexPATWhoamiURL,
		Headers: codexPATHeaders(parsed, false, false),
	})
	if errRequest != nil {
		return fmt.Errorf("verify Codex personal access token: %w", errRequest)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("Codex personal access token validation returned HTTP %d", response.StatusCode)
	}
	if len(response.Body) == 0 || len(response.Body) > codexPATMaxWhoamiResult {
		return fmt.Errorf("Codex personal access token validation response size is invalid")
	}
	var whoami codexPATWhoamiResponse
	if errDecode := json.Unmarshal(response.Body, &whoami); errDecode != nil {
		return fmt.Errorf("decode Codex personal access token validation response")
	}
	if strings.TrimSpace(whoami.Email) == "" || strings.TrimSpace(whoami.ChatGPTUserID) == "" || strings.TrimSpace(whoami.ChatGPTAccountID) == "" || strings.TrimSpace(whoami.ChatGPTPlanType) == "" || whoami.ChatGPTAccountIsFedRAMP == nil {
		return fmt.Errorf("Codex personal access token validation response is incomplete")
	}
	if !strings.EqualFold(strings.TrimSpace(whoami.ChatGPTAccountID), parsed.accountID) {
		return fmt.Errorf("Codex personal access token account id does not match the import metadata")
	}
	return nil
}

func (e *AgentIdentityExperiment) executeCodexPATHTTP(ctx context.Context, callbackID string, rawCredential []byte, method, target string, body []byte, model string, stream bool) (cpaapi.HostHTTPResponse, error) {
	if e == nil || e.enabled == nil || !e.enabled() {
		return cpaapi.HostHTTPResponse{}, fmt.Errorf("Codex personal access token support is disabled in experimental settings")
	}
	if e.transport == nil {
		return cpaapi.HostHTTPResponse{}, fmt.Errorf("CPA host HTTP transport is unavailable")
	}
	parsed, errParse := parseCodexPATCredential(rawCredential)
	if errParse != nil {
		return cpaapi.HostHTTPResponse{}, errParse
	}
	if model != "" {
		var errBody error
		body, errBody = agentIdentityRequestBody(body, model, stream)
		if errBody != nil {
			return cpaapi.HostHTTPResponse{}, errBody
		}
	}
	response, errRequest := e.transport.AgentIdentityDo(ctx, callbackID, cpaapi.HostHTTPRequest{
		Method: method, URL: target, Headers: codexPATHeaders(parsed, stream, true), Body: body,
	})
	if errRequest != nil {
		return cpaapi.HostHTTPResponse{}, errRequest
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return cpaapi.HostHTTPResponse{}, codexPATHTTPError{StatusCode: response.StatusCode}
	}
	if len(response.Body) > agentIdentityMaxResponse {
		return cpaapi.HostHTTPResponse{}, fmt.Errorf("Codex personal access token upstream response exceeded the size limit")
	}
	return response, nil
}

func (e *AgentIdentityExperiment) startCodexPATStream(ctx context.Context, callbackID string, rawCredential, body []byte) (cpaapi.HostHTTPStreamResponse, error) {
	if e == nil || e.enabled == nil || !e.enabled() {
		return cpaapi.HostHTTPStreamResponse{}, fmt.Errorf("Codex personal access token support is disabled in experimental settings")
	}
	if e.transport == nil {
		return cpaapi.HostHTTPStreamResponse{}, fmt.Errorf("CPA host HTTP transport is unavailable")
	}
	parsed, errParse := parseCodexPATCredential(rawCredential)
	if errParse != nil {
		return cpaapi.HostHTTPStreamResponse{}, errParse
	}
	response, errStart := e.transport.AgentIdentityDoStream(ctx, callbackID, cpaapi.HostHTTPRequest{
		Method: http.MethodPost, URL: agentIdentityResponsesURL,
		Headers: codexPATHeaders(parsed, true, true), Body: body,
	})
	if errStart != nil {
		return cpaapi.HostHTTPStreamResponse{}, errStart
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_ = e.transport.AgentIdentityCloseHTTPStream(ctx, response.StreamID)
		return cpaapi.HostHTTPStreamResponse{}, codexPATHTTPError{StatusCode: response.StatusCode}
	}
	if strings.TrimSpace(response.StreamID) == "" {
		return cpaapi.HostHTTPStreamResponse{}, fmt.Errorf("CPA host returned an empty Codex personal access token stream id")
	}
	return response, nil
}

func codexPATHeaders(parsed codexPATParsed, stream, includeAccount bool) http.Header {
	accept := "application/json"
	if stream {
		accept = "text/event-stream"
	}
	headers := http.Header{
		"Authorization": []string{"Bearer " + parsed.accessToken},
		"Accept":        []string{accept}, "Originator": []string{"codex_cli_rs"},
		"User-Agent": []string{codexPATUserAgent},
	}
	if includeAccount {
		headers.Set("ChatGPT-Account-ID", parsed.accountID)
		headers.Set("Content-Type", "application/json")
		headers.Set("OpenAI-Beta", "responses=experimental")
	}
	return headers
}

type codexPATHTTPError struct {
	StatusCode int
}

func (e codexPATHTTPError) Error() string {
	return fmt.Sprintf("Codex personal access token upstream returned HTTP %d", e.StatusCode)
}

func (e codexPATHTTPError) HTTPStatus() int {
	return e.StatusCode
}
