package manager

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

const (
	agentIdentityLoginTTL        = 10 * time.Minute
	agentIdentitySessionMaxBytes = 128 << 10
	agentIdentityLoginPagePath   = "/v0/resource/plugins/cpa-account-config-manager/index.html"
)

var (
	ErrAgentIdentitySessionDisabled = errors.New("Agent Identity Session login is disabled")
	ErrAgentIdentityLoginNotFound   = errors.New("Agent Identity login state was not found")
	ErrAgentIdentityLoginExpired    = errors.New("Agent Identity login state expired")
	ErrAgentIdentityLoginBusy       = errors.New("Agent Identity Session conversion is already running")
	ErrAgentIdentitySessionInvalid  = errors.New("ChatGPT Session JSON is invalid")
	ErrAgentIdentitySessionRejected = errors.New("OpenAI rejected the ChatGPT Session")
	ErrAgentIdentityServiceFailed   = errors.New("OpenAI Agent Identity service is unavailable")
)

type agentIdentityLoginFlow struct {
	expiresAt  time.Time
	converting bool
	auth       *cpaapi.AuthData
}

type AgentIdentitySessionLoginRequest struct {
	State       string `json:"state"`
	SessionJSON string `json:"session_json"`
}

type AgentIdentitySessionLoginAccount struct {
	Email      string `json:"email,omitempty"`
	PlanType   string `json:"plan_type"`
	Provider   string `json:"provider"`
	LoginState string `json:"login_state"`
}

type AgentIdentitySessionLoginResponse struct {
	Status  string                           `json:"status"`
	Account AgentIdentitySessionLoginAccount `json:"account"`
}

type agentIdentitySessionClaims struct {
	AccountID string
	UserID    string
	Email     string
	PlanType  string
	FedRAMP   bool
}

func (e *AgentIdentityExperiment) StartLogin(_ cpaapi.AuthLoginStartRequest) (cpaapi.AuthLoginStartResponse, error) {
	if e == nil || e.enabled == nil || !e.enabled() {
		return cpaapi.AuthLoginStartResponse{}, ErrAgentIdentitySessionDisabled
	}
	now := e.currentTime().UTC()
	var state string
	for range 3 {
		candidate, errID := randomIdentifier()
		if errID != nil {
			return cpaapi.AuthLoginStartResponse{}, fmt.Errorf("create Agent Identity login state: %w", errID)
		}
		e.mu.Lock()
		e.cleanupLoginFlowsLocked(now)
		if _, exists := e.logins[candidate]; !exists {
			state = candidate
			e.logins[state] = &agentIdentityLoginFlow{expiresAt: now.Add(agentIdentityLoginTTL)}
		}
		e.mu.Unlock()
		if state != "" {
			break
		}
	}
	if state == "" {
		return cpaapi.AuthLoginStartResponse{}, fmt.Errorf("create unique Agent Identity login state")
	}
	query := url.Values{"agent_identity_login": []string{state}}
	return cpaapi.AuthLoginStartResponse{
		Provider:  agentIdentityProvider,
		URL:       agentIdentityLoginPagePath + "?" + query.Encode(),
		State:     state,
		ExpiresAt: now.Add(agentIdentityLoginTTL),
	}, nil
}

func (e *AgentIdentityExperiment) PollLogin(request cpaapi.AuthLoginPollRequest) cpaapi.AuthLoginPollResponse {
	if e == nil {
		return cpaapi.AuthLoginPollResponse{Status: "error", Message: "Agent Identity login is unavailable"}
	}
	state := strings.TrimSpace(request.State)
	now := e.currentTime().UTC()
	e.mu.Lock()
	defer e.mu.Unlock()
	e.cleanupLoginFlowsLocked(now)
	flow, exists := e.logins[state]
	if !exists {
		return cpaapi.AuthLoginPollResponse{Status: "error", Message: "Agent Identity login expired or was cancelled"}
	}
	if flow.auth == nil {
		return cpaapi.AuthLoginPollResponse{Status: "pending", Message: "Waiting for ChatGPT Session JSON"}
	}
	auth := cloneAgentIdentityLoginAuth(*flow.auth)
	return cpaapi.AuthLoginPollResponse{Status: "success", Message: "Agent Identity credential is ready", Auth: auth}
}

func (e *AgentIdentityExperiment) CompleteSessionLogin(ctx context.Context, callbackID, state string, sessionJSON []byte) (AgentIdentitySessionLoginResponse, error) {
	if e == nil || e.enabled == nil || !e.enabled() {
		return AgentIdentitySessionLoginResponse{}, ErrAgentIdentitySessionDisabled
	}
	state = strings.TrimSpace(state)
	now := e.currentTime().UTC()
	e.mu.Lock()
	flow, exists := e.logins[state]
	switch {
	case !exists:
		e.cleanupLoginFlowsLocked(now)
		e.mu.Unlock()
		return AgentIdentitySessionLoginResponse{}, ErrAgentIdentityLoginNotFound
	case !flow.expiresAt.After(now):
		clearAgentIdentityLoginFlow(flow)
		delete(e.logins, state)
		e.mu.Unlock()
		return AgentIdentitySessionLoginResponse{}, ErrAgentIdentityLoginExpired
	case flow.auth != nil:
		result := publicAgentIdentityLoginResult(state, *flow.auth)
		e.mu.Unlock()
		return result, nil
	case flow.converting:
		e.mu.Unlock()
		return AgentIdentitySessionLoginResponse{}, ErrAgentIdentityLoginBusy
	default:
		flow.converting = true
		e.mu.Unlock()
	}

	auth, errConvert := e.convertSession(ctx, callbackID, sessionJSON)
	e.mu.Lock()
	current, stillExists := e.logins[state]
	if !stillExists || current != flow {
		e.mu.Unlock()
		clear(auth.StorageJSON)
		return AgentIdentitySessionLoginResponse{}, ErrAgentIdentityLoginNotFound
	}
	flow.converting = false
	if errConvert != nil {
		e.mu.Unlock()
		return AgentIdentitySessionLoginResponse{}, errConvert
	}
	stored := cloneAgentIdentityLoginAuth(auth)
	flow.auth = &stored
	e.mu.Unlock()
	return publicAgentIdentityLoginResult(state, auth), nil
}

func (e *AgentIdentityExperiment) convertSession(ctx context.Context, callbackID string, raw []byte) (cpaapi.AuthData, error) {
	if len(raw) == 0 || len(raw) > agentIdentitySessionMaxBytes {
		return cpaapi.AuthData{}, ErrAgentIdentitySessionInvalid
	}
	if e.transport == nil {
		return cpaapi.AuthData{}, ErrAgentIdentityServiceFailed
	}
	var session struct {
		AccessToken string `json:"accessToken"`
		Tokens      *struct {
			AccessToken string `json:"access_token"`
			IDToken     string `json:"id_token"`
		} `json:"tokens"`
	}
	if errDecode := json.Unmarshal(raw, &session); errDecode != nil {
		return cpaapi.AuthData{}, ErrAgentIdentitySessionInvalid
	}
	accessToken := strings.TrimSpace(session.AccessToken)
	idToken := accessToken
	if accessToken == "" && session.Tokens != nil {
		accessToken = strings.TrimSpace(session.Tokens.AccessToken)
		idToken = strings.TrimSpace(session.Tokens.IDToken)
	}
	if accessToken == "" || len(accessToken) > 16<<10 || len(idToken) > 16<<10 {
		return cpaapi.AuthData{}, ErrAgentIdentitySessionInvalid
	}
	claims, errClaims := parseAgentIdentitySessionClaims(e.currentTime().UTC(), idToken, accessToken)
	if errClaims != nil {
		return cpaapi.AuthData{}, errClaims
	}
	publicKey, privateKey, errKey := ed25519.GenerateKey(rand.Reader)
	if errKey != nil {
		return cpaapi.AuthData{}, fmt.Errorf("generate Agent Identity key: %w", errKey)
	}
	runtimeID, errRuntime := e.registerSessionRuntime(ctx, callbackID, accessToken, claims, publicKey)
	if errRuntime != nil {
		return cpaapi.AuthData{}, errRuntime
	}
	taskID, errTask := e.registerSessionTask(ctx, callbackID, runtimeID, privateKey)
	if errTask != nil {
		return cpaapi.AuthData{}, errTask
	}
	encodedPrivateKey, errEncodeKey := encodeAgentIdentityPrivateKey(privateKey)
	if errEncodeKey != nil {
		return cpaapi.AuthData{}, errEncodeKey
	}
	email := claims.Email
	record := agentIdentityRecord{
		AgentRuntimeID: runtimeID, AgentPrivateKey: encodedPrivateKey,
		AccountID: claims.AccountID, ChatGPTUserID: claims.UserID,
		Email: &email, PlanType: claims.PlanType,
		ChatGPTAccountIsFedRAMP: claims.FedRAMP, TaskID: taskID,
	}
	credential, errCredential := json.Marshal(map[string]any{
		"type": agentIdentityProvider, "auth_mode": agentIdentityAuthMode,
		"OPENAI_API_KEY": nil, "agent_identity": record,
	})
	if errCredential != nil {
		return cpaapi.AuthData{}, fmt.Errorf("encode Agent Identity credential")
	}
	parsed, errParse := parseAgentIdentityCredential(credential, e.currentTime().UTC())
	if errParse != nil {
		clear(credential)
		return cpaapi.AuthData{}, fmt.Errorf("validate generated Agent Identity credential: %w", errParse)
	}
	auth := agentIdentityAuthData(credential, parsed)
	auth.ID = claims.AccountID
	token := sanitizeImportFilenameToken(firstNonEmpty(claims.Email, claims.AccountID))
	if token == "" {
		token = "agent-identity"
	}
	auth.FileName = "codex-" + token + ".json"
	auth.Label = firstNonEmpty(claims.Email, claims.AccountID, auth.FileName)
	return auth, nil
}

func parseAgentIdentitySessionClaims(now time.Time, tokens ...string) (agentIdentitySessionClaims, error) {
	for _, token := range tokens {
		parts := strings.Split(strings.TrimSpace(token), ".")
		if len(parts) != 3 || parts[1] == "" {
			continue
		}
		var payload struct {
			ExpiresAt int64  `json:"exp"`
			Email     string `json:"email"`
			Profile   struct {
				Email string `json:"email"`
			} `json:"https://api.openai.com/profile"`
			Auth struct {
				AccountID    string `json:"chatgpt_account_id"`
				UserID       string `json:"chatgpt_user_id"`
				LegacyUserID string `json:"user_id"`
				PlanType     string `json:"chatgpt_plan_type"`
				FedRAMP      bool   `json:"chatgpt_account_is_fedramp"`
			} `json:"https://api.openai.com/auth"`
		}
		if errDecode := decodeAgentIdentityJWTPart(parts[1], &payload); errDecode != nil {
			continue
		}
		accountID := strings.TrimSpace(payload.Auth.AccountID)
		userID := strings.TrimSpace(firstNonEmpty(payload.Auth.UserID, payload.Auth.LegacyUserID))
		if accountID == "" || userID == "" || payload.ExpiresAt <= now.Unix()+60 {
			continue
		}
		claims := agentIdentitySessionClaims{
			AccountID: accountID, UserID: userID,
			Email:    strings.TrimSpace(firstNonEmpty(payload.Email, payload.Profile.Email)),
			PlanType: strings.TrimSpace(firstNonEmpty(payload.Auth.PlanType, "unknown")),
			FedRAMP:  payload.Auth.FedRAMP,
		}
		if len(claims.AccountID) > 256 || len(claims.UserID) > 256 || len(claims.Email) > 512 || len(claims.PlanType) > 64 {
			continue
		}
		return claims, nil
	}
	return agentIdentitySessionClaims{}, ErrAgentIdentitySessionInvalid
}

func (e *AgentIdentityExperiment) registerSessionRuntime(ctx context.Context, callbackID, accessToken string, claims agentIdentitySessionClaims, publicKey ed25519.PublicKey) (string, error) {
	body, errBody := json.Marshal(map[string]any{
		"abom": map[string]string{
			"agent_version": "cpa-account-config-manager", "agent_harness_id": "codex-cli", "running_location": "cliproxyapi-plugin",
		},
		"agent_public_key": encodeAgentIdentitySSHPublicKey(publicKey),
		"capabilities":     []string{"responsesapi"}, "ttl": nil,
	})
	if errBody != nil {
		return "", fmt.Errorf("encode Agent Identity runtime registration")
	}
	headers := http.Header{
		"Authorization": []string{"Bearer " + accessToken},
		"Content-Type":  []string{"application/json"},
		"Accept":        []string{"application/json"},
	}
	if claims.FedRAMP {
		headers.Set("X-OpenAI-Fedramp", "true")
	}
	response, errRequest := e.transport.AgentIdentityDo(ctx, callbackID, cpaapi.HostHTTPRequest{
		Method: http.MethodPost, URL: agentIdentityRegistrationBase + "/v1/agent/register", Headers: headers, Body: body,
	})
	if errRequest != nil {
		return "", ErrAgentIdentityServiceFailed
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if response.StatusCode >= 400 && response.StatusCode < 500 {
			return "", ErrAgentIdentitySessionRejected
		}
		return "", ErrAgentIdentityServiceFailed
	}
	if len(response.Body) == 0 || len(response.Body) > 64<<10 {
		return "", ErrAgentIdentityServiceFailed
	}
	var decoded struct {
		AgentRuntimeID string `json:"agent_runtime_id"`
	}
	if errDecode := json.Unmarshal(response.Body, &decoded); errDecode != nil {
		return "", ErrAgentIdentityServiceFailed
	}
	runtimeID := strings.TrimSpace(decoded.AgentRuntimeID)
	if runtimeID == "" || len(runtimeID) > 256 {
		return "", ErrAgentIdentityServiceFailed
	}
	return runtimeID, nil
}

func (e *AgentIdentityExperiment) registerSessionTask(ctx context.Context, callbackID, runtimeID string, privateKey ed25519.PrivateKey) (string, error) {
	timestamp := agentIdentityTimestamp(e.currentTime().UTC())
	signature := ed25519.Sign(privateKey, []byte(runtimeID+":"+timestamp))
	body, errBody := json.Marshal(map[string]string{
		"timestamp": timestamp, "signature": base64.StdEncoding.EncodeToString(signature),
	})
	if errBody != nil {
		return "", fmt.Errorf("encode Agent Identity task registration")
	}
	response, errRequest := e.transport.AgentIdentityDo(ctx, callbackID, cpaapi.HostHTTPRequest{
		Method:  http.MethodPost,
		URL:     agentIdentityRegistrationBase + "/v1/agent/" + url.PathEscape(runtimeID) + "/task/register",
		Headers: http.Header{"Content-Type": []string{"application/json"}, "Accept": []string{"application/json"}},
		Body:    body,
	})
	if errRequest != nil || response.StatusCode < 200 || response.StatusCode >= 300 || len(response.Body) == 0 || len(response.Body) > 64<<10 {
		return "", ErrAgentIdentityServiceFailed
	}
	var decoded struct {
		TaskID               string `json:"task_id"`
		TaskIDCamel          string `json:"taskId"`
		EncryptedTaskID      string `json:"encrypted_task_id"`
		EncryptedTaskIDCamel string `json:"encryptedTaskId"`
	}
	if errDecode := json.Unmarshal(response.Body, &decoded); errDecode != nil {
		return "", ErrAgentIdentityServiceFailed
	}
	taskID := strings.TrimSpace(firstNonEmpty(decoded.TaskID, decoded.TaskIDCamel))
	if taskID == "" {
		var errDecrypt error
		taskID, errDecrypt = decryptAgentIdentityTaskID(privateKey, strings.TrimSpace(firstNonEmpty(decoded.EncryptedTaskID, decoded.EncryptedTaskIDCamel)))
		if errDecrypt != nil {
			return "", ErrAgentIdentityServiceFailed
		}
	}
	if taskID == "" || len(taskID) > 4096 {
		return "", ErrAgentIdentityServiceFailed
	}
	return taskID, nil
}

func encodeAgentIdentitySSHPublicKey(publicKey ed25519.PublicKey) string {
	algorithm := []byte("ssh-ed25519")
	var blob bytes.Buffer
	_ = binary.Write(&blob, binary.BigEndian, uint32(len(algorithm)))
	_, _ = blob.Write(algorithm)
	_ = binary.Write(&blob, binary.BigEndian, uint32(len(publicKey)))
	_, _ = blob.Write(publicKey)
	return "ssh-ed25519 " + base64.StdEncoding.EncodeToString(blob.Bytes())
}

func encodeAgentIdentityPrivateKey(privateKey ed25519.PrivateKey) (string, error) {
	raw, errMarshal := x509.MarshalPKCS8PrivateKey(privateKey)
	if errMarshal != nil {
		return "", fmt.Errorf("encode Agent Identity private key")
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

func (e *AgentIdentityExperiment) cleanupLoginFlowsLocked(now time.Time) {
	for state, flow := range e.logins {
		if flow == nil || !flow.expiresAt.After(now) {
			clearAgentIdentityLoginFlow(flow)
			delete(e.logins, state)
		}
	}
}

func clearAgentIdentityLoginFlow(flow *agentIdentityLoginFlow) {
	if flow == nil || flow.auth == nil {
		return
	}
	clear(flow.auth.StorageJSON)
	flow.auth.StorageJSON = nil
	flow.auth = nil
}

func cloneAgentIdentityLoginAuth(auth cpaapi.AuthData) cpaapi.AuthData {
	clone := auth
	clone.StorageJSON = append([]byte(nil), auth.StorageJSON...)
	clone.Metadata = cloneAgentIdentityAnyMap(auth.Metadata)
	clone.Attributes = cloneAgentIdentityStringMap(auth.Attributes)
	return clone
}

func cloneAgentIdentityAnyMap(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	clone := make(map[string]any, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func cloneAgentIdentityStringMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	clone := make(map[string]string, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func publicAgentIdentityLoginResult(state string, auth cpaapi.AuthData) AgentIdentitySessionLoginResponse {
	return AgentIdentitySessionLoginResponse{
		Status: "completed",
		Account: AgentIdentitySessionLoginAccount{
			Email: auth.Label, PlanType: auth.Attributes["plan_type"], Provider: auth.Provider, LoginState: state,
		},
	}
}

func (a *App) handleAgentIdentitySessionLogin(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	if len(req.Body) == 0 || len(req.Body) > 2*agentIdentitySessionMaxBytes {
		return jsonResponse(http.StatusRequestEntityTooLarge, map[string]any{"error": "ChatGPT Session request is too large"})
	}
	var request AgentIdentitySessionLoginRequest
	if errDecode := json.Unmarshal(req.Body, &request); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "invalid Agent Identity Session login request"})
	}
	session := []byte(request.SessionJSON)
	defer clear(session)
	startedAt := time.Now().UTC()
	result, errLogin := a.agentIdentity.CompleteSessionLogin(ctx, req.HostCallbackID, request.State, session)
	if errLogin != nil {
		status, message, reason := agentIdentitySessionLoginError(errLogin)
		a.operations.Record(OperationEntry{
			Category: OperationCategoryAccount, Action: OperationActionAgentIdentityLogin,
			Status: OperationStatusFailed, Source: OperationSourceManual, Scope: OperationScopeSingle,
			TargetCount: 1, Failed: 1, StartedAt: startedAt, FinishedAt: time.Now().UTC(), ReasonCode: reason,
		})
		return jsonResponse(status, map[string]any{"error": message})
	}
	a.operations.Record(OperationEntry{
		Category: OperationCategoryAccount, Action: OperationActionAgentIdentityLogin,
		Status: OperationStatusSucceeded, Source: OperationSourceManual, Scope: OperationScopeSingle,
		TargetCount: 1, Succeeded: 1, StartedAt: startedAt, FinishedAt: time.Now().UTC(), ReasonCode: "credential_converted",
	})
	return jsonResponse(http.StatusOK, result)
}

func agentIdentitySessionLoginError(errLogin error) (int, string, string) {
	switch {
	case errors.Is(errLogin, ErrAgentIdentitySessionDisabled):
		return http.StatusConflict, "Agent Identity Session login is disabled", "experiment_disabled"
	case errors.Is(errLogin, ErrAgentIdentityLoginNotFound):
		return http.StatusNotFound, "Agent Identity login state was not found", "login_state_not_found"
	case errors.Is(errLogin, ErrAgentIdentityLoginExpired):
		return http.StatusGone, "Agent Identity login state expired", "login_state_expired"
	case errors.Is(errLogin, ErrAgentIdentityLoginBusy):
		return http.StatusConflict, "Agent Identity Session conversion is already running", "conversion_running"
	case errors.Is(errLogin, ErrAgentIdentitySessionRejected):
		return http.StatusUnprocessableEntity, "OpenAI rejected the ChatGPT Session", "session_rejected"
	case errors.Is(errLogin, ErrAgentIdentityServiceFailed):
		return http.StatusBadGateway, "OpenAI Agent Identity service is unavailable", "upstream_unavailable"
	default:
		return http.StatusBadRequest, "ChatGPT Session JSON is invalid", "invalid_session"
	}
}
