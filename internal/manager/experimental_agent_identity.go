package manager

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"cpa-account-config-manager/internal/cpaapi"

	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/nacl/box"
)

const (
	agentIdentityProvider         = "codex-agent-identity"
	agentIdentityAuthMode         = "agentIdentity"
	agentIdentityIssuer           = "https://chatgpt.com/codex-backend/agent-identity"
	agentIdentityAudience         = "codex-app-server"
	agentIdentityJWKSURL          = "https://chatgpt.com/backend-api/wham/agent-identities/jwks"
	agentIdentityRegistrationBase = "https://auth.openai.com/api/accounts"
	agentIdentityResponsesURL     = "https://chatgpt.com/backend-api/codex/responses"
	agentIdentityUsageURL         = "https://chatgpt.com/backend-api/wham/usage"
	agentIdentityMaxCredential    = 2 << 20
	agentIdentityMaxResponse      = 8 << 20
	agentIdentityJWKSMaxResponse  = 1 << 20
)

type AgentIdentityTransport interface {
	AgentIdentityDo(context.Context, string, cpaapi.HostHTTPRequest) (cpaapi.HostHTTPResponse, error)
	AgentIdentityDoStream(context.Context, string, cpaapi.HostHTTPRequest) (cpaapi.HostHTTPStreamResponse, error)
	AgentIdentityReadStream(context.Context, string) (cpaapi.HostHTTPStreamReadResponse, error)
	AgentIdentityCloseHTTPStream(context.Context, string) error
	AgentIdentityEmitStream(context.Context, cpaapi.HostStreamEmitRequest) error
	AgentIdentityCloseStream(context.Context, cpaapi.HostStreamCloseRequest) error
}

type agentIdentityJWTHeader struct {
	Algorithm string `json:"alg"`
	KeyID     string `json:"kid"`
}

type agentIdentityClaims struct {
	Issuer                  string `json:"iss"`
	Audience                string `json:"aud"`
	IssuedAt                int64  `json:"iat"`
	ExpiresAt               int64  `json:"exp"`
	AgentRuntimeID          string `json:"agent_runtime_id"`
	AgentPrivateKey         string `json:"agent_private_key"`
	AccountID               string `json:"account_id"`
	ChatGPTUserID           string `json:"chatgpt_user_id"`
	Email                   string `json:"email"`
	PlanType                string `json:"plan_type"`
	ChatGPTAccountIsFedRAMP bool   `json:"chatgpt_account_is_fedramp"`
}

type agentIdentityCredential struct {
	Type          string            `json:"type"`
	AuthMode      string            `json:"auth_mode"`
	AgentIdentity json.RawMessage   `json:"agent_identity"`
	Disabled      bool              `json:"disabled"`
	Priority      int               `json:"priority"`
	Note          string            `json:"note"`
	Prefix        string            `json:"prefix"`
	ProxyURL      string            `json:"proxy_url"`
	Websockets    bool              `json:"websockets"`
	Headers       map[string]string `json:"headers"`
}

type agentIdentityParsed struct {
	credential  agentIdentityCredential
	header      agentIdentityJWTHeader
	claims      agentIdentityClaims
	privateKey  ed25519.PrivateKey
	jwt         string
	taskID      string
	fingerprint string
}

type agentIdentityRecord struct {
	AgentRuntimeID          string  `json:"agent_runtime_id"`
	AgentPrivateKey         string  `json:"agent_private_key"`
	AccountID               string  `json:"account_id"`
	ChatGPTUserID           string  `json:"chatgpt_user_id"`
	Email                   *string `json:"email"`
	PlanType                string  `json:"plan_type"`
	ChatGPTAccountIsFedRAMP bool    `json:"chatgpt_account_is_fedramp"`
	TaskID                  string  `json:"task_id"`
}

type agentIdentityTask struct {
	fingerprint string
	taskID      string
}

type agentIdentityTaskCall struct {
	done chan struct{}
	task agentIdentityTask
	err  error
}

type agentIdentityJWK struct {
	KeyType   string `json:"kty"`
	KeyID     string `json:"kid"`
	Algorithm string `json:"alg"`
	Use       string `json:"use"`
	Modulus   string `json:"n"`
	Exponent  string `json:"e"`
}

type agentIdentityJWKS struct {
	Keys []agentIdentityJWK `json:"keys"`
}

type AgentIdentityExperiment struct {
	enabled   func() bool
	transport AgentIdentityTransport
	now       func() time.Time

	mu       sync.Mutex
	tasks    map[string]agentIdentityTask
	inflight map[string]*agentIdentityTaskCall
	jwks     agentIdentityJWKS
	jwksAt   time.Time
}

func NewAgentIdentityExperiment(enabled func() bool, transport AgentIdentityTransport) *AgentIdentityExperiment {
	if enabled == nil {
		enabled = func() bool { return false }
	}
	return &AgentIdentityExperiment{
		enabled: enabled, transport: transport, now: time.Now,
		tasks: make(map[string]agentIdentityTask), inflight: make(map[string]*agentIdentityTaskCall),
	}
}

func (e *AgentIdentityExperiment) Clear() {
	if e == nil {
		return
	}
	e.mu.Lock()
	clear(e.tasks)
	e.jwks = agentIdentityJWKS{}
	e.jwksAt = time.Time{}
	e.mu.Unlock()
}

func (e *AgentIdentityExperiment) ParseAuth(raw []byte) (cpaapi.AuthParseResponse, error) {
	if len(raw) == 0 || len(raw) > agentIdentityMaxCredential {
		return cpaapi.AuthParseResponse{}, nil
	}
	var marker struct {
		AgentIdentity json.RawMessage `json:"agent_identity"`
	}
	if errDecode := json.Unmarshal(raw, &marker); errDecode != nil || len(marker.AgentIdentity) == 0 {
		return cpaapi.AuthParseResponse{}, nil
	}
	parsed, errParse := parseAgentIdentityCredential(raw, e.currentTime())
	if errParse != nil {
		return cpaapi.AuthParseResponse{}, errParse
	}
	auth := agentIdentityAuthData(raw, parsed)
	if e == nil || e.enabled == nil || !e.enabled() {
		auth.Disabled = true
		auth.Metadata["agent_identity_experiment_disabled"] = true
	}
	return cpaapi.AuthParseResponse{Handled: true, Auth: auth}, nil
}

func (e *AgentIdentityExperiment) RefreshAuth(request cpaapi.AuthRefreshRequest) (cpaapi.AuthRefreshResponse, error) {
	if e == nil || e.enabled == nil || !e.enabled() {
		return cpaapi.AuthRefreshResponse{}, fmt.Errorf("Agent Identity support is disabled in experimental settings")
	}
	parsed, errParse := parseAgentIdentityCredential(request.StorageJSON, e.currentTime())
	if errParse != nil {
		return cpaapi.AuthRefreshResponse{}, errParse
	}
	return cpaapi.AuthRefreshResponse{Auth: agentIdentityAuthData(request.StorageJSON, parsed)}, nil
}

func (e *AgentIdentityExperiment) ModelsForAuth(request cpaapi.AuthModelRequest) (cpaapi.ModelResponse, error) {
	if e == nil || e.enabled == nil || !e.enabled() || !strings.EqualFold(request.AuthProvider, agentIdentityProvider) {
		return cpaapi.ModelResponse{Provider: agentIdentityProvider}, nil
	}
	if _, errParse := parseAgentIdentityCredential(request.StorageJSON, e.currentTime()); errParse != nil {
		return cpaapi.ModelResponse{}, errParse
	}
	return cpaapi.ModelResponse{Provider: agentIdentityProvider, Models: agentIdentityModels()}, nil
}

func (e *AgentIdentityExperiment) VerifyImport(ctx context.Context, raw []byte) error {
	if e == nil || e.enabled == nil || !e.enabled() {
		return fmt.Errorf("Agent Identity support is disabled in experimental settings")
	}
	parsed, errParse := parseAgentIdentityCredential(raw, e.currentTime())
	if errParse != nil {
		return errParse
	}
	if parsed.jwt == "" {
		return e.verifyRecordImport(ctx, parsed)
	}
	jwks, errJWKS := e.loadJWKS(ctx)
	if errJWKS != nil {
		return errJWKS
	}
	if errVerify := verifyAgentIdentityJWT(rawJWTParts(parsed.jwt), parsed.header, jwks); errVerify != nil {
		return fmt.Errorf("verify Agent Identity credential: %w", errVerify)
	}
	return nil
}

func (e *AgentIdentityExperiment) verifyRecordImport(ctx context.Context, parsed agentIdentityParsed) error {
	if e == nil || e.transport == nil {
		return fmt.Errorf("CPA host HTTP transport is unavailable")
	}
	authorization, errAuthorization := agentIdentityAuthorization(parsed, parsed.taskID, e.currentTime())
	if errAuthorization != nil {
		return errAuthorization
	}
	response, errRequest := e.transport.AgentIdentityDo(ctx, "", cpaapi.HostHTTPRequest{
		Method: http.MethodGet, URL: agentIdentityUsageURL,
		Headers: agentIdentityHeaders(parsed, authorization, false),
	})
	if errRequest != nil {
		return fmt.Errorf("verify Agent Identity record: %w", errRequest)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("Agent Identity usage verification returned HTTP %d", response.StatusCode)
	}
	if len(response.Body) > agentIdentityMaxResponse {
		return fmt.Errorf("Agent Identity usage verification response exceeded the size limit")
	}
	return nil
}

func agentIdentityAuthData(raw []byte, parsed agentIdentityParsed) cpaapi.AuthData {
	metadata := map[string]any{
		"type": agentIdentityProvider, "auth_mode": agentIdentityAuthMode,
		"account_type": "agent_identity", "email": parsed.claims.Email,
		"account_id": parsed.claims.AccountID, "chatgpt_account_id": parsed.claims.AccountID,
		"plan_type": parsed.claims.PlanType, "chatgpt_plan_type": parsed.claims.PlanType,
		"priority": parsed.credential.Priority, "note": parsed.credential.Note,
	}
	attributes := map[string]string{"plan_type": parsed.claims.PlanType}
	return cpaapi.AuthData{
		Provider: agentIdentityProvider, Label: parsed.claims.Email,
		Prefix: parsed.credential.Prefix, ProxyURL: parsed.credential.ProxyURL,
		Disabled: parsed.credential.Disabled, StorageJSON: append([]byte(nil), raw...),
		Metadata: metadata, Attributes: attributes,
	}
}

func parseAgentIdentityCredential(raw []byte, now time.Time) (agentIdentityParsed, error) {
	if len(raw) == 0 || len(raw) > agentIdentityMaxCredential {
		return agentIdentityParsed{}, fmt.Errorf("Agent Identity credential size is invalid")
	}
	var credential agentIdentityCredential
	if errDecode := json.Unmarshal(raw, &credential); errDecode != nil {
		return agentIdentityParsed{}, fmt.Errorf("decode Agent Identity credential: %w", errDecode)
	}
	credential.Type = strings.TrimSpace(credential.Type)
	credential.AuthMode = strings.TrimSpace(credential.AuthMode)
	credential.AgentIdentity = bytesTrimSpaceClone(credential.AgentIdentity)
	if len(credential.AgentIdentity) == 0 || string(credential.AgentIdentity) == "null" {
		return agentIdentityParsed{}, fmt.Errorf("Agent Identity credential is missing agent_identity")
	}
	if credential.AuthMode != agentIdentityAuthMode {
		return agentIdentityParsed{}, fmt.Errorf("Agent Identity credential auth_mode must be %q", agentIdentityAuthMode)
	}
	if credential.Type != "" && !strings.EqualFold(credential.Type, agentIdentityProvider) && !strings.EqualFold(credential.Type, "codex") {
		return agentIdentityParsed{}, fmt.Errorf("Agent Identity credential type is unsupported")
	}
	if credential.AgentIdentity[0] == '{' {
		return parseAgentIdentityRecordCredential(credential)
	}
	var jwt string
	if errDecode := json.Unmarshal(credential.AgentIdentity, &jwt); errDecode != nil || strings.TrimSpace(jwt) == "" {
		return agentIdentityParsed{}, fmt.Errorf("Agent Identity field must be a JWT string or record object")
	}
	jwt = strings.TrimSpace(jwt)
	headerPart, payloadPart, signaturePart, errParts := splitAgentIdentityJWT(jwt)
	if errParts != nil {
		return agentIdentityParsed{}, errParts
	}
	var header agentIdentityJWTHeader
	if errDecode := decodeAgentIdentityJWTPart(headerPart, &header); errDecode != nil {
		return agentIdentityParsed{}, fmt.Errorf("decode Agent Identity JWT header: %w", errDecode)
	}
	if header.Algorithm != "RS256" || strings.TrimSpace(header.KeyID) == "" {
		return agentIdentityParsed{}, fmt.Errorf("Agent Identity JWT must use RS256 and include a key id")
	}
	var claims agentIdentityClaims
	if errDecode := decodeAgentIdentityJWTPart(payloadPart, &claims); errDecode != nil {
		return agentIdentityParsed{}, fmt.Errorf("decode Agent Identity JWT claims: %w", errDecode)
	}
	if errClaims := validateAgentIdentityClaims(claims, now); errClaims != nil {
		return agentIdentityParsed{}, errClaims
	}
	privateKey, errKey := parseAgentIdentityPrivateKey(claims.AgentPrivateKey)
	if errKey != nil {
		return agentIdentityParsed{}, errKey
	}
	if _, errSignature := base64.RawURLEncoding.DecodeString(signaturePart); errSignature != nil {
		return agentIdentityParsed{}, fmt.Errorf("Agent Identity JWT signature is not valid base64url")
	}
	fingerprint := sha256.Sum256([]byte(jwt))
	return agentIdentityParsed{
		credential: credential, header: header, claims: claims, privateKey: privateKey,
		jwt:         jwt,
		fingerprint: base64.RawURLEncoding.EncodeToString(fingerprint[:]),
	}, nil
}

func parseAgentIdentityRecordCredential(credential agentIdentityCredential) (agentIdentityParsed, error) {
	var record agentIdentityRecord
	if errDecode := json.Unmarshal(credential.AgentIdentity, &record); errDecode != nil {
		return agentIdentityParsed{}, fmt.Errorf("decode Agent Identity record: %w", errDecode)
	}
	claims := agentIdentityClaims{
		AgentRuntimeID: record.AgentRuntimeID, AgentPrivateKey: record.AgentPrivateKey,
		AccountID: record.AccountID, ChatGPTUserID: record.ChatGPTUserID,
		PlanType: record.PlanType, ChatGPTAccountIsFedRAMP: record.ChatGPTAccountIsFedRAMP,
	}
	if record.Email != nil {
		claims.Email = strings.TrimSpace(*record.Email)
	}
	required := []struct {
		name  string
		value string
		max   int
	}{
		{"agent_runtime_id", claims.AgentRuntimeID, 256},
		{"agent_private_key", claims.AgentPrivateKey, 8192},
		{"account_id", claims.AccountID, 256},
		{"chatgpt_user_id", claims.ChatGPTUserID, 256},
		{"plan_type", claims.PlanType, 64},
		{"task_id", record.TaskID, 4096},
	}
	for _, field := range required {
		if value := strings.TrimSpace(field.value); value == "" || len(value) > field.max {
			return agentIdentityParsed{}, fmt.Errorf("Agent Identity record field %s is missing or invalid", field.name)
		}
	}
	if len(claims.Email) > 512 {
		return agentIdentityParsed{}, fmt.Errorf("Agent Identity record email is invalid")
	}
	privateKey, errKey := parseAgentIdentityPrivateKey(claims.AgentPrivateKey)
	if errKey != nil {
		return agentIdentityParsed{}, errKey
	}
	fingerprint := sha256.Sum256(credential.AgentIdentity)
	return agentIdentityParsed{
		credential: credential, claims: claims, privateKey: privateKey,
		taskID:      strings.TrimSpace(record.TaskID),
		fingerprint: base64.RawURLEncoding.EncodeToString(fingerprint[:]),
	}, nil
}

func bytesTrimSpaceClone(raw []byte) []byte {
	return append([]byte(nil), []byte(strings.TrimSpace(string(raw)))...)
}

func validateAgentIdentityClaims(claims agentIdentityClaims, now time.Time) error {
	if claims.Issuer != agentIdentityIssuer || claims.Audience != agentIdentityAudience {
		return fmt.Errorf("Agent Identity JWT issuer or audience is invalid")
	}
	nowUnix := now.UTC().Unix()
	if claims.IssuedAt <= 0 || claims.ExpiresAt <= claims.IssuedAt || claims.ExpiresAt <= nowUnix || claims.IssuedAt > nowUnix+300 {
		return fmt.Errorf("Agent Identity JWT time claims are invalid or expired")
	}
	required := []struct {
		name  string
		value string
		max   int
	}{
		{"agent_runtime_id", claims.AgentRuntimeID, 256},
		{"agent_private_key", claims.AgentPrivateKey, 8192},
		{"account_id", claims.AccountID, 256},
		{"chatgpt_user_id", claims.ChatGPTUserID, 256},
		{"email", claims.Email, 512},
		{"plan_type", claims.PlanType, 64},
	}
	for _, field := range required {
		value := strings.TrimSpace(field.value)
		if value == "" || len(value) > field.max {
			return fmt.Errorf("Agent Identity JWT claim %s is missing or invalid", field.name)
		}
	}
	return nil
}

func parseAgentIdentityPrivateKey(encoded string) (ed25519.PrivateKey, error) {
	raw, errDecode := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if errDecode != nil {
		return nil, fmt.Errorf("Agent Identity private key is not valid base64")
	}
	parsed, errParse := x509.ParsePKCS8PrivateKey(raw)
	if errParse != nil {
		return nil, fmt.Errorf("Agent Identity private key is not valid PKCS#8")
	}
	privateKey, ok := parsed.(ed25519.PrivateKey)
	if !ok || len(privateKey) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("Agent Identity private key is not Ed25519")
	}
	return append(ed25519.PrivateKey(nil), privateKey...), nil
}

func splitAgentIdentityJWT(jwt string) (string, string, string, error) {
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", "", fmt.Errorf("Agent Identity JWT format is invalid")
	}
	return parts[0], parts[1], parts[2], nil
}

func rawJWTParts(jwt string) [3]string {
	header, payload, signature, _ := splitAgentIdentityJWT(jwt)
	return [3]string{header, payload, signature}
}

func decodeAgentIdentityJWTPart(part string, destination any) error {
	raw, errDecode := base64.RawURLEncoding.DecodeString(part)
	if errDecode != nil {
		return errDecode
	}
	return json.Unmarshal(raw, destination)
}

func verifyAgentIdentityJWT(parts [3]string, header agentIdentityJWTHeader, jwks agentIdentityJWKS) error {
	var match *agentIdentityJWK
	for index := range jwks.Keys {
		candidate := &jwks.Keys[index]
		if candidate.KeyID == header.KeyID {
			match = candidate
			break
		}
	}
	if match == nil || match.KeyType != "RSA" || match.Algorithm != "RS256" {
		return fmt.Errorf("Agent Identity JWT key id is not trusted")
	}
	modulus, errModulus := base64.RawURLEncoding.DecodeString(match.Modulus)
	exponent, errExponent := base64.RawURLEncoding.DecodeString(match.Exponent)
	signature, errSignature := base64.RawURLEncoding.DecodeString(parts[2])
	if errModulus != nil || errExponent != nil || errSignature != nil || len(exponent) == 0 || len(exponent) > 8 {
		return fmt.Errorf("Agent Identity JWKS key is invalid")
	}
	var exponentValue uint64
	for _, value := range exponent {
		exponentValue = exponentValue<<8 | uint64(value)
	}
	if exponentValue < 3 || exponentValue > uint64(^uint(0)>>1) {
		return fmt.Errorf("Agent Identity JWKS exponent is invalid")
	}
	publicKey := &rsa.PublicKey{N: new(big.Int).SetBytes(modulus), E: int(exponentValue)}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if errVerify := rsa.VerifyPKCS1v15(publicKey, crypto.SHA256, digest[:], signature); errVerify != nil {
		return fmt.Errorf("Agent Identity JWT signature is invalid")
	}
	return nil
}

func (e *AgentIdentityExperiment) loadJWKS(ctx context.Context) (agentIdentityJWKS, error) {
	if e == nil || e.transport == nil {
		return agentIdentityJWKS{}, fmt.Errorf("CPA host HTTP transport is unavailable")
	}
	now := e.currentTime()
	e.mu.Lock()
	if len(e.jwks.Keys) > 0 && now.Sub(e.jwksAt) < time.Hour {
		cached := e.jwks
		e.mu.Unlock()
		return cached, nil
	}
	e.mu.Unlock()
	response, errRequest := e.transport.AgentIdentityDo(ctx, "", cpaapi.HostHTTPRequest{
		Method: http.MethodGet, URL: agentIdentityJWKSURL,
		Headers: http.Header{"Accept": []string{"application/json"}},
	})
	if errRequest != nil {
		return agentIdentityJWKS{}, fmt.Errorf("request Agent Identity JWKS: %w", errRequest)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return agentIdentityJWKS{}, fmt.Errorf("Agent Identity JWKS returned HTTP %d", response.StatusCode)
	}
	if len(response.Body) == 0 || len(response.Body) > agentIdentityJWKSMaxResponse {
		return agentIdentityJWKS{}, fmt.Errorf("Agent Identity JWKS response size is invalid")
	}
	var jwks agentIdentityJWKS
	if errDecode := json.Unmarshal(response.Body, &jwks); errDecode != nil || len(jwks.Keys) == 0 {
		return agentIdentityJWKS{}, fmt.Errorf("decode Agent Identity JWKS response")
	}
	e.mu.Lock()
	e.jwks = jwks
	e.jwksAt = now
	e.mu.Unlock()
	return jwks, nil
}

func (e *AgentIdentityExperiment) Execute(ctx context.Context, request cpaapi.ExecutorRequest) (cpaapi.ExecutorResponse, error) {
	response, errExecute := e.executeHTTP(ctx, request.HostCallbackID, request.StorageJSON, http.MethodPost, agentIdentityResponsesURL, request.Payload, request.Model, request.Stream, true)
	if errExecute != nil {
		return cpaapi.ExecutorResponse{}, errExecute
	}
	return cpaapi.ExecutorResponse{Payload: response.Body, Headers: response.Headers}, nil
}

func (e *AgentIdentityExperiment) HTTPRequest(ctx context.Context, request cpaapi.ExecutorHTTPRequest) (cpaapi.ExecutorHTTPResponse, error) {
	if !allowedAgentIdentityURL(request.URL) {
		return cpaapi.ExecutorHTTPResponse{}, fmt.Errorf("Agent Identity executor URL is not allowed")
	}
	response, errExecute := e.executeHTTP(ctx, request.HostCallbackID, request.StorageJSON, firstNonEmpty(request.Method, http.MethodPost), request.URL, request.Body, "", false, true)
	if errExecute != nil {
		return cpaapi.ExecutorHTTPResponse{}, errExecute
	}
	return cpaapi.ExecutorHTTPResponse{StatusCode: response.StatusCode, Headers: response.Headers, Body: response.Body}, nil
}

func (e *AgentIdentityExperiment) ExecuteStream(ctx context.Context, request cpaapi.ExecutorRequest) (cpaapi.ExecutorStreamResponse, error) {
	if strings.TrimSpace(request.StreamID) == "" {
		return cpaapi.ExecutorStreamResponse{}, fmt.Errorf("Agent Identity executor stream id is required")
	}
	parsed, errParse := e.executionCredential(request.StorageJSON)
	if errParse != nil {
		return cpaapi.ExecutorStreamResponse{}, errParse
	}
	body, errBody := agentIdentityRequestBody(request.Payload, request.Model, true)
	if errBody != nil {
		return cpaapi.ExecutorStreamResponse{}, errBody
	}
	upstream, errStart := e.startAuthenticatedStream(ctx, request.HostCallbackID, parsed, body, false)
	if errStart != nil {
		return cpaapi.ExecutorStreamResponse{}, errStart
	}
	go e.forwardStream(request.StreamID, upstream.StreamID)
	return cpaapi.ExecutorStreamResponse{Headers: upstream.Headers}, nil
}

func (e *AgentIdentityExperiment) executeHTTP(ctx context.Context, callbackID string, rawCredential []byte, method, target string, body []byte, model string, stream bool, retryUnauthorized bool) (cpaapi.HostHTTPResponse, error) {
	parsed, errParse := e.executionCredential(rawCredential)
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
	response, errRequest := e.doAuthenticated(ctx, callbackID, parsed, method, target, body, stream)
	if errRequest != nil {
		return cpaapi.HostHTTPResponse{}, errRequest
	}
	if response.StatusCode == http.StatusUnauthorized && retryUnauthorized {
		e.invalidateTask(parsed.fingerprint)
		response, errRequest = e.doAuthenticated(ctx, callbackID, parsed, method, target, body, stream)
		if errRequest != nil {
			return cpaapi.HostHTTPResponse{}, errRequest
		}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return cpaapi.HostHTTPResponse{}, agentIdentityHTTPError{StatusCode: response.StatusCode}
	}
	if len(response.Body) > agentIdentityMaxResponse {
		return cpaapi.HostHTTPResponse{}, fmt.Errorf("Agent Identity upstream response exceeded the size limit")
	}
	return response, nil
}

func (e *AgentIdentityExperiment) doAuthenticated(ctx context.Context, callbackID string, parsed agentIdentityParsed, method, target string, body []byte, stream bool) (cpaapi.HostHTTPResponse, error) {
	if e == nil || e.transport == nil {
		return cpaapi.HostHTTPResponse{}, fmt.Errorf("CPA host HTTP transport is unavailable")
	}
	task, errTask := e.registeredTask(ctx, callbackID, parsed)
	if errTask != nil {
		return cpaapi.HostHTTPResponse{}, errTask
	}
	authorization, errAuthorization := agentIdentityAuthorization(parsed, task.taskID, e.currentTime())
	if errAuthorization != nil {
		return cpaapi.HostHTTPResponse{}, errAuthorization
	}
	request := cpaapi.HostHTTPRequest{Method: method, URL: target, Headers: agentIdentityHeaders(parsed, authorization, stream), Body: body}
	return e.transport.AgentIdentityDo(ctx, callbackID, request)
}

func (e *AgentIdentityExperiment) startAuthenticatedStream(ctx context.Context, callbackID string, parsed agentIdentityParsed, body []byte, retryUnauthorized bool) (cpaapi.HostHTTPStreamResponse, error) {
	if e == nil || e.transport == nil {
		return cpaapi.HostHTTPStreamResponse{}, fmt.Errorf("CPA host HTTP transport is unavailable")
	}
	task, errTask := e.registeredTask(ctx, callbackID, parsed)
	if errTask != nil {
		return cpaapi.HostHTTPStreamResponse{}, errTask
	}
	authorization, errAuthorization := agentIdentityAuthorization(parsed, task.taskID, e.currentTime())
	if errAuthorization != nil {
		return cpaapi.HostHTTPStreamResponse{}, errAuthorization
	}
	response, errStart := e.transport.AgentIdentityDoStream(ctx, callbackID, cpaapi.HostHTTPRequest{
		Method: http.MethodPost, URL: agentIdentityResponsesURL,
		Headers: agentIdentityHeaders(parsed, authorization, true), Body: body,
	})
	if errStart != nil {
		return cpaapi.HostHTTPStreamResponse{}, errStart
	}
	if response.StatusCode == http.StatusUnauthorized && !retryUnauthorized {
		_ = e.transport.AgentIdentityCloseHTTPStream(ctx, response.StreamID)
		e.invalidateTask(parsed.fingerprint)
		return e.startAuthenticatedStream(ctx, callbackID, parsed, body, true)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_ = e.transport.AgentIdentityCloseHTTPStream(ctx, response.StreamID)
		return cpaapi.HostHTTPStreamResponse{}, agentIdentityHTTPError{StatusCode: response.StatusCode}
	}
	if strings.TrimSpace(response.StreamID) == "" {
		return cpaapi.HostHTTPStreamResponse{}, fmt.Errorf("CPA host returned an empty Agent Identity stream id")
	}
	return response, nil
}

func (e *AgentIdentityExperiment) forwardStream(pluginStreamID, upstreamStreamID string) {
	ctx := context.Background()
	defer func() { _ = e.transport.AgentIdentityCloseHTTPStream(ctx, upstreamStreamID) }()
	for {
		chunk, errRead := e.transport.AgentIdentityReadStream(ctx, upstreamStreamID)
		if errRead != nil {
			_ = e.transport.AgentIdentityCloseStream(ctx, cpaapi.HostStreamCloseRequest{StreamID: pluginStreamID, Error: "Agent Identity upstream stream failed"})
			return
		}
		if len(chunk.Payload) > 0 {
			if errEmit := e.transport.AgentIdentityEmitStream(ctx, cpaapi.HostStreamEmitRequest{StreamID: pluginStreamID, Payload: chunk.Payload}); errEmit != nil {
				_ = e.transport.AgentIdentityCloseStream(ctx, cpaapi.HostStreamCloseRequest{StreamID: pluginStreamID, Error: "Agent Identity downstream stream closed"})
				return
			}
		}
		if chunk.Error != "" {
			_ = e.transport.AgentIdentityCloseStream(ctx, cpaapi.HostStreamCloseRequest{StreamID: pluginStreamID, Error: "Agent Identity upstream stream failed"})
			return
		}
		if chunk.Done {
			_ = e.transport.AgentIdentityCloseStream(ctx, cpaapi.HostStreamCloseRequest{StreamID: pluginStreamID})
			return
		}
	}
}

func (e *AgentIdentityExperiment) executionCredential(raw []byte) (agentIdentityParsed, error) {
	if e == nil || e.enabled == nil || !e.enabled() {
		return agentIdentityParsed{}, fmt.Errorf("Agent Identity support is disabled in experimental settings")
	}
	return parseAgentIdentityCredential(raw, e.currentTime())
}

func (e *AgentIdentityExperiment) registeredTask(ctx context.Context, callbackID string, parsed agentIdentityParsed) (agentIdentityTask, error) {
	if parsed.taskID != "" {
		return agentIdentityTask{fingerprint: parsed.fingerprint, taskID: parsed.taskID}, nil
	}
	e.mu.Lock()
	if task, exists := e.tasks[parsed.fingerprint]; exists && task.taskID != "" {
		e.mu.Unlock()
		return task, nil
	}
	if call, exists := e.inflight[parsed.fingerprint]; exists {
		e.mu.Unlock()
		select {
		case <-ctx.Done():
			return agentIdentityTask{}, ctx.Err()
		case <-call.done:
			return call.task, call.err
		}
	}
	call := &agentIdentityTaskCall{done: make(chan struct{})}
	e.inflight[parsed.fingerprint] = call
	e.mu.Unlock()

	taskID, errRegister := e.registerTask(ctx, callbackID, parsed)
	if errRegister == nil {
		call.task = agentIdentityTask{fingerprint: parsed.fingerprint, taskID: taskID}
	} else {
		call.err = errRegister
	}
	e.mu.Lock()
	delete(e.inflight, parsed.fingerprint)
	if call.err == nil {
		e.tasks[parsed.fingerprint] = call.task
	}
	close(call.done)
	e.mu.Unlock()
	return call.task, call.err
}

func (e *AgentIdentityExperiment) registerTask(ctx context.Context, callbackID string, parsed agentIdentityParsed) (string, error) {
	if e.transport == nil {
		return "", fmt.Errorf("CPA host HTTP transport is unavailable")
	}
	timestamp := agentIdentityTimestamp(e.currentTime())
	signature := ed25519.Sign(parsed.privateKey, []byte(parsed.claims.AgentRuntimeID+":"+timestamp))
	body, errBody := json.Marshal(map[string]string{
		"timestamp": timestamp, "signature": base64.StdEncoding.EncodeToString(signature),
	})
	if errBody != nil {
		return "", fmt.Errorf("encode Agent Identity task registration")
	}
	registrationURL := agentIdentityRegistrationBase + "/v1/agent/" + url.PathEscape(parsed.claims.AgentRuntimeID) + "/task/register"
	response, errRequest := e.transport.AgentIdentityDo(ctx, callbackID, cpaapi.HostHTTPRequest{
		Method: http.MethodPost, URL: registrationURL,
		Headers: http.Header{"Content-Type": []string{"application/json"}, "Accept": []string{"application/json"}},
		Body:    body,
	})
	if errRequest != nil {
		return "", fmt.Errorf("register Agent Identity task: %w", errRequest)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("Agent Identity task registration returned HTTP %d", response.StatusCode)
	}
	if len(response.Body) == 0 || len(response.Body) > 64<<10 {
		return "", fmt.Errorf("Agent Identity task registration response size is invalid")
	}
	var decoded struct {
		TaskID               string `json:"task_id"`
		TaskIDCamel          string `json:"taskId"`
		EncryptedTaskID      string `json:"encrypted_task_id"`
		EncryptedTaskIDCamel string `json:"encryptedTaskId"`
	}
	if errDecode := json.Unmarshal(response.Body, &decoded); errDecode != nil {
		return "", fmt.Errorf("decode Agent Identity task registration response")
	}
	taskID := strings.TrimSpace(firstNonEmpty(decoded.TaskID, decoded.TaskIDCamel))
	if taskID == "" {
		encrypted := strings.TrimSpace(firstNonEmpty(decoded.EncryptedTaskID, decoded.EncryptedTaskIDCamel))
		var errDecrypt error
		taskID, errDecrypt = decryptAgentIdentityTaskID(parsed.privateKey, encrypted)
		if errDecrypt != nil {
			return "", errDecrypt
		}
	}
	if taskID == "" || len(taskID) > 4096 {
		return "", fmt.Errorf("Agent Identity task registration returned an invalid task id")
	}
	return taskID, nil
}

func decryptAgentIdentityTaskID(privateKey ed25519.PrivateKey, encoded string) (string, error) {
	if encoded == "" {
		return "", fmt.Errorf("Agent Identity task registration omitted the task id")
	}
	ciphertext, errDecode := base64.StdEncoding.DecodeString(encoded)
	if errDecode != nil {
		return "", fmt.Errorf("Agent Identity encrypted task id is invalid")
	}
	digest := sha512.Sum512(privateKey.Seed())
	var secret [32]byte
	copy(secret[:], digest[:32])
	secret[0] &= 248
	secret[31] &= 127
	secret[31] |= 64
	publicBytes, errPublic := curve25519.X25519(secret[:], curve25519.Basepoint)
	if errPublic != nil {
		return "", fmt.Errorf("derive Agent Identity decryption key")
	}
	var public [32]byte
	copy(public[:], publicBytes)
	plaintext, ok := box.OpenAnonymous(nil, ciphertext, &public, &secret)
	if !ok {
		return "", fmt.Errorf("decrypt Agent Identity task id")
	}
	return strings.TrimSpace(string(plaintext)), nil
}

func agentIdentityAuthorization(parsed agentIdentityParsed, taskID string, now time.Time) (string, error) {
	timestamp := agentIdentityTimestamp(now)
	signature := ed25519.Sign(parsed.privateKey, []byte(parsed.claims.AgentRuntimeID+":"+taskID+":"+timestamp))
	envelope := struct {
		AgentRuntimeID string `json:"agent_runtime_id"`
		Signature      string `json:"signature"`
		TaskID         string `json:"task_id"`
		Timestamp      string `json:"timestamp"`
	}{
		AgentRuntimeID: parsed.claims.AgentRuntimeID,
		Signature:      base64.StdEncoding.EncodeToString(signature),
		TaskID:         taskID, Timestamp: timestamp,
	}
	raw, errEncode := json.Marshal(envelope)
	if errEncode != nil {
		return "", fmt.Errorf("encode Agent Identity assertion")
	}
	return "AgentAssertion " + base64.RawURLEncoding.EncodeToString(raw), nil
}

func agentIdentityHeaders(parsed agentIdentityParsed, authorization string, stream bool) http.Header {
	accept := "application/json"
	if stream {
		accept = "text/event-stream"
	}
	headers := http.Header{
		"Authorization": []string{authorization}, "ChatGPT-Account-ID": []string{parsed.claims.AccountID},
		"Content-Type": []string{"application/json"}, "Accept": []string{accept},
		"Originator": []string{"codex_cli_rs"}, "User-Agent": []string{"codex_cli_rs/cpa-account-config-manager"},
	}
	if parsed.claims.ChatGPTAccountIsFedRAMP {
		headers.Set("X-OpenAI-Fedramp", "true")
	}
	return headers
}

func agentIdentityRequestBody(raw []byte, model string, stream bool) ([]byte, error) {
	var document map[string]json.RawMessage
	if errDecode := json.Unmarshal(raw, &document); errDecode != nil || document == nil {
		return nil, fmt.Errorf("Agent Identity executor requires a JSON request body")
	}
	modelRaw, _ := json.Marshal(strings.TrimSpace(model))
	streamRaw, _ := json.Marshal(stream)
	document["model"] = modelRaw
	document["stream"] = streamRaw
	updated, errEncode := json.Marshal(document)
	if errEncode != nil || len(updated) > agentIdentityMaxResponse {
		return nil, fmt.Errorf("encode Agent Identity request body")
	}
	return updated, nil
}

func allowedAgentIdentityURL(rawURL string) bool {
	parsed, errParse := url.Parse(strings.TrimSpace(rawURL))
	if errParse != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Hostname(), "chatgpt.com") || parsed.User != nil || (parsed.Port() != "" && parsed.Port() != "443") {
		return false
	}
	requestPath := parsed.EscapedPath()
	return strings.HasPrefix(requestPath, "/backend-api/codex/") || requestPath == "/backend-api/wham/usage"
}

func agentIdentityTimestamp(now time.Time) string {
	return now.UTC().Truncate(time.Second).Format("2006-01-02T15:04:05Z")
}

func (e *AgentIdentityExperiment) invalidateTask(fingerprint string) {
	e.mu.Lock()
	delete(e.tasks, fingerprint)
	e.mu.Unlock()
}

func (e *AgentIdentityExperiment) currentTime() time.Time {
	if e != nil && e.now != nil {
		return e.now().UTC()
	}
	return time.Now().UTC()
}

func agentIdentityModels() []cpaapi.ModelInfo {
	models := []struct {
		id      string
		display string
		context int64
	}{
		{"gpt-5.3-codex-spark", "GPT 5.3 Codex Spark", 128000},
		{"gpt-5.4", "GPT 5.4", 1050000},
		{"gpt-5.4-mini", "GPT 5.4 Mini", 400000},
		{"gpt-5.5", "GPT 5.5", 1050000},
		{"gpt-5.6-sol", "GPT 5.6 Sol", 1050000},
		{"gpt-5.6-terra", "GPT 5.6 Terra", 1050000},
		{"gpt-5.6-luna", "GPT 5.6 Luna", 1050000},
		{"codex-auto-review", "Codex Auto Review", 1050000},
	}
	result := make([]cpaapi.ModelInfo, 0, len(models))
	for _, model := range models {
		result = append(result, cpaapi.ModelInfo{
			ID: model.id, Object: "model", OwnedBy: "openai", Type: "openai",
			DisplayName: model.display, Name: model.id, Version: model.id,
			ContextLength: model.context, MaxCompletionTokens: 128000,
			SupportedGenerationMethods: []string{"chat"}, SupportedParameters: []string{"tools"},
			Thinking: &cpaapi.ThinkingSupport{Levels: []string{"low", "medium", "high", "xhigh"}},
		})
	}
	return result
}

type agentIdentityHTTPError struct {
	StatusCode int
}

func (e agentIdentityHTTPError) Error() string {
	return fmt.Sprintf("Agent Identity upstream returned HTTP %d", e.StatusCode)
}

func (e agentIdentityHTTPError) HTTPStatus() int {
	return e.StatusCode
}
