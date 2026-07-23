package manager

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

type fakeAgentIdentityTransport struct {
	mu       sync.Mutex
	requests []cpaapi.HostHTTPRequest
	do       func(string, cpaapi.HostHTTPRequest) (cpaapi.HostHTTPResponse, error)
}

func (f *fakeAgentIdentityTransport) AgentIdentityDo(_ context.Context, callbackID string, request cpaapi.HostHTTPRequest) (cpaapi.HostHTTPResponse, error) {
	f.mu.Lock()
	f.requests = append(f.requests, request)
	f.mu.Unlock()
	if f.do == nil {
		return cpaapi.HostHTTPResponse{}, fmt.Errorf("unexpected HTTP request")
	}
	return f.do(callbackID, request)
}

func (*fakeAgentIdentityTransport) AgentIdentityDoStream(context.Context, string, cpaapi.HostHTTPRequest) (cpaapi.HostHTTPStreamResponse, error) {
	return cpaapi.HostHTTPStreamResponse{}, fmt.Errorf("unexpected streaming request")
}

func (*fakeAgentIdentityTransport) AgentIdentityReadStream(context.Context, string) (cpaapi.HostHTTPStreamReadResponse, error) {
	return cpaapi.HostHTTPStreamReadResponse{}, fmt.Errorf("unexpected stream read")
}

func (*fakeAgentIdentityTransport) AgentIdentityCloseHTTPStream(context.Context, string) error {
	return nil
}

func (*fakeAgentIdentityTransport) AgentIdentityEmitStream(context.Context, cpaapi.HostStreamEmitRequest) error {
	return fmt.Errorf("unexpected stream emit")
}

func (*fakeAgentIdentityTransport) AgentIdentityCloseStream(context.Context, cpaapi.HostStreamCloseRequest) error {
	return nil
}

func TestAgentIdentityRecordParseDisabledAndModels(t *testing.T) {
	now := time.Date(2026, time.July, 23, 10, 0, 0, 0, time.UTC)
	raw := testAgentIdentityCredential(t, testAgentIdentityRecord(t, "runtime-disabled", "task-disabled", "disabled@example.com"))
	disabled := NewAgentIdentityExperiment(func() bool { return false }, nil)
	disabled.now = func() time.Time { return now }
	response, errParse := disabled.ParseAuth(raw)
	if errParse != nil {
		t.Fatalf("ParseAuth() error = %v", errParse)
	}
	if !response.Handled || !response.Auth.Disabled || response.Auth.Provider != agentIdentityProvider {
		t.Fatalf("disabled ParseAuth() response = %#v", response)
	}
	if response.Auth.Metadata["agent_identity_experiment_disabled"] != true || response.Auth.Metadata["account_type"] != "agent_identity" {
		t.Fatalf("disabled ParseAuth() metadata = %#v", response.Auth.Metadata)
	}
	if _, errExecute := disabled.Execute(t.Context(), cpaapi.ExecutorRequest{StorageJSON: raw}); errExecute == nil || !strings.Contains(errExecute.Error(), "disabled") {
		t.Fatalf("disabled Execute() error = %v", errExecute)
	}

	enabled := NewAgentIdentityExperiment(func() bool { return true }, nil)
	enabled.now = func() time.Time { return now }
	models, errModels := enabled.ModelsForAuth(cpaapi.AuthModelRequest{AuthProvider: agentIdentityProvider, StorageJSON: raw})
	if errModels != nil || models.Provider != agentIdentityProvider || len(models.Models) == 0 {
		t.Fatalf("ModelsForAuth() = %#v, %v", models, errModels)
	}
	nativeModels, errNative := enabled.ModelsForAuth(cpaapi.AuthModelRequest{AuthProvider: "codex", StorageJSON: raw})
	if errNative != nil || len(nativeModels.Models) != 0 {
		t.Fatalf("native ModelsForAuth() = %#v, %v", nativeModels, errNative)
	}
}

func TestAgentIdentityRecordVerificationAndExecution(t *testing.T) {
	now := time.Date(2026, time.July, 23, 10, 5, 6, 0, time.UTC)
	raw := testAgentIdentityCredential(t, testAgentIdentityRecord(t, "runtime-record", "task-record", "record@example.com"))
	parsed, errParse := parseAgentIdentityCredential(raw, now)
	if errParse != nil {
		t.Fatalf("parseAgentIdentityCredential() error = %v", errParse)
	}
	transport := &fakeAgentIdentityTransport{}
	transport.do = func(callbackID string, request cpaapi.HostHTTPRequest) (cpaapi.HostHTTPResponse, error) {
		if request.URL == agentIdentityUsageURL {
			if callbackID != "" {
				t.Fatalf("usage callback id = %q", callbackID)
			}
			if request.Method != http.MethodGet {
				t.Fatalf("usage method = %q", request.Method)
			}
			verifyAgentIdentityAssertionHeader(t, request.Headers.Get("Authorization"), parsed, "task-record", now)
			return cpaapi.HostHTTPResponse{StatusCode: http.StatusOK, Body: []byte(`{"ok":true}`)}, nil
		}
		if request.URL == agentIdentityResponsesURL {
			if callbackID != "callback-record" {
				t.Fatalf("executor callback id = %q", callbackID)
			}
			verifyAgentIdentityAssertionHeader(t, request.Headers.Get("Authorization"), parsed, "task-record", now)
			if strings.Contains(string(request.Body), "original-model") || !strings.Contains(string(request.Body), `"model":"gpt-5.4"`) {
				t.Fatalf("executor body = %s", request.Body)
			}
			return cpaapi.HostHTTPResponse{StatusCode: http.StatusOK, Body: []byte(`{"id":"response"}`)}, nil
		}
		t.Fatalf("unexpected URL %q", request.URL)
		return cpaapi.HostHTTPResponse{}, nil
	}
	experiment := NewAgentIdentityExperiment(func() bool { return true }, transport)
	experiment.now = func() time.Time { return now }
	if errVerify := experiment.VerifyImport(t.Context(), raw); errVerify != nil {
		t.Fatalf("VerifyImport() error = %v", errVerify)
	}
	response, errExecute := experiment.Execute(t.Context(), cpaapi.ExecutorRequest{
		HostCallbackID: "callback-record", StorageJSON: raw, Model: "gpt-5.4",
		Payload: []byte(`{"model":"original-model","input":[]}`),
	})
	if errExecute != nil || string(response.Payload) != `{"id":"response"}` {
		t.Fatalf("Execute() = %s, %v", response.Payload, errExecute)
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if len(transport.requests) != 2 {
		t.Fatalf("record request count = %d, want 2 without task registration", len(transport.requests))
	}
}

func TestAgentIdentityJWTVerificationRegistrationAndUnauthorizedRetry(t *testing.T) {
	now := time.Date(2026, time.July, 23, 10, 10, 0, 0, time.UTC)
	raw, jwks := testAgentIdentityJWTCredential(t, now)
	var registrationCount int
	var responseCount int
	transport := &fakeAgentIdentityTransport{}
	transport.do = func(_ string, request cpaapi.HostHTTPRequest) (cpaapi.HostHTTPResponse, error) {
		switch {
		case request.URL == agentIdentityJWKSURL:
			body, _ := json.Marshal(jwks)
			return cpaapi.HostHTTPResponse{StatusCode: http.StatusOK, Body: body}, nil
		case strings.HasPrefix(request.URL, agentIdentityRegistrationBase+"/"):
			registrationCount++
			return cpaapi.HostHTTPResponse{StatusCode: http.StatusOK, Body: []byte(fmt.Sprintf(`{"task_id":"registered-%d"}`, registrationCount))}, nil
		case request.URL == agentIdentityResponsesURL:
			responseCount++
			if responseCount == 1 {
				return cpaapi.HostHTTPResponse{StatusCode: http.StatusUnauthorized}, nil
			}
			return cpaapi.HostHTTPResponse{StatusCode: http.StatusOK, Body: []byte(`{"ok":true}`)}, nil
		default:
			return cpaapi.HostHTTPResponse{}, fmt.Errorf("unexpected URL")
		}
	}
	experiment := NewAgentIdentityExperiment(func() bool { return true }, transport)
	experiment.now = func() time.Time { return now }
	if errVerify := experiment.VerifyImport(t.Context(), raw); errVerify != nil {
		t.Fatalf("VerifyImport() error = %v", errVerify)
	}
	if _, errExecute := experiment.Execute(t.Context(), cpaapi.ExecutorRequest{StorageJSON: raw, Model: "gpt-5.4", Payload: []byte(`{"input":[]}`)}); errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	if registrationCount != 2 || responseCount != 2 {
		t.Fatalf("registration/response counts = %d/%d, want 2/2", registrationCount, responseCount)
	}
}

func TestAgentIdentityRejectsMalformedCredentialsWithoutEchoingSecrets(t *testing.T) {
	secret := "private-value-that-must-not-be-echoed"
	tests := []struct {
		name string
		raw  []byte
	}{
		{name: "missing auth mode", raw: []byte(`{"agent_identity":{"agent_private_key":"` + secret + `"}}`)},
		{name: "invalid private key", raw: []byte(`{"auth_mode":"agentIdentity","agent_identity":{"agent_runtime_id":"runtime","agent_private_key":"` + secret + `","account_id":"account","chatgpt_user_id":"user","plan_type":"plus","task_id":"task"}}`)},
		{name: "invalid jwt", raw: []byte(`{"auth_mode":"agentIdentity","agent_identity":"` + secret + `"}`)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, errParse := parseAgentIdentityCredential(test.raw, time.Now())
			if errParse == nil {
				t.Fatal("parse unexpectedly succeeded")
			}
			if strings.Contains(errParse.Error(), secret) {
				t.Fatalf("error leaked credential material: %v", errParse)
			}
		})
	}
}

func TestAgentIdentityURLAllowlist(t *testing.T) {
	tests := map[string]bool{
		"https://chatgpt.com/backend-api/codex/responses":      true,
		"https://chatgpt.com/backend-api/codex/models?x=1":     true,
		"https://chatgpt.com/backend-api/wham/usage":           true,
		"http://chatgpt.com/backend-api/codex/responses":       false,
		"https://evil.example/backend-api/codex/responses":     false,
		"https://chatgpt.com.evil.example/backend-api/codex/":  false,
		"https://chatgpt.com/backend-api/wham/usage/extra":     false,
		"https://chatgpt.com/backend-api/other":                false,
		"https://chatgpt.com:444/backend-api/codex/responses":  false,
		"https://user@chatgpt.com/backend-api/codex/responses": false,
	}
	for target, expected := range tests {
		if actual := allowedAgentIdentityURL(target); actual != expected {
			t.Errorf("allowedAgentIdentityURL(%q) = %v, want %v", target, actual, expected)
		}
	}
}

func TestAgentIdentityRegistrationCapabilities(t *testing.T) {
	app := NewApp(&fakeAuthHost{}, []byte("index"))
	defer app.Close()
	capabilities := app.Registration().Capabilities
	if !capabilities.AuthProvider || !capabilities.ModelProvider || !capabilities.Executor || capabilities.ExecutorModelScope != "oauth" {
		t.Fatalf("Agent Identity capabilities = %#v", capabilities)
	}
	if len(capabilities.ExecutorInputFormats) != 1 || capabilities.ExecutorInputFormats[0] != "codex" || len(capabilities.ExecutorOutputFormats) != 1 || capabilities.ExecutorOutputFormats[0] != "codex" {
		t.Fatalf("Agent Identity executor formats = %#v", capabilities)
	}
}

func TestAgentIdentityImportPreviewIsTypedAndRedacted(t *testing.T) {
	now := time.Date(2026, time.July, 23, 11, 0, 0, 0, time.UTC)
	identity := testAgentIdentityRecord(t, "runtime-preview", "task-preview", "preview@example.com")
	privateKey := identity["agent_private_key"].(string)
	raw := testAgentIdentityCredential(t, identity)
	transport := &fakeAgentIdentityTransport{do: func(_ string, request cpaapi.HostHTTPRequest) (cpaapi.HostHTTPResponse, error) {
		if request.URL != agentIdentityUsageURL {
			return cpaapi.HostHTTPResponse{}, fmt.Errorf("unexpected URL")
		}
		return cpaapi.HostHTTPResponse{StatusCode: http.StatusOK, Body: []byte(`{"ok":true}`)}, nil
	}}
	experiment := NewAgentIdentityExperiment(func() bool { return true }, transport)
	experiment.now = func() time.Time { return now }
	service := NewImportService(&fakeAuthHost{details: map[string]cpaapi.HostAuthGetResponse{}}, NewMutationCoordinator())
	service.SetAgentIdentityExperiment(experiment)
	service.now = func() time.Time { return now }
	defer service.Clear()

	preview, errPreview := service.Preview(t.Context(), importUpload{Name: "identity.json", Data: raw})
	if errPreview != nil {
		t.Fatalf("Preview() error = %v", errPreview)
	}
	if preview.Total != 1 || len(preview.Items) != 1 || preview.Items[0].CredentialType != "agent_identity" {
		t.Fatalf("preview = %#v", preview)
	}
	publicJSON, errMarshal := json.Marshal(preview)
	if errMarshal != nil {
		t.Fatalf("Marshal() preview error = %v", errMarshal)
	}
	for _, secret := range []string{privateKey, "task-preview"} {
		if strings.Contains(string(publicJSON), secret) {
			t.Fatalf("preview leaked Agent Identity credential material")
		}
	}
}

func testAgentIdentityRecord(t *testing.T, runtimeID, taskID, email string) map[string]any {
	t.Helper()
	_, privateKey, errGenerate := ed25519.GenerateKey(rand.Reader)
	if errGenerate != nil {
		t.Fatalf("GenerateKey() error = %v", errGenerate)
	}
	encodedKey := testAgentIdentityEncodedPrivateKey(t, privateKey)
	return map[string]any{
		"agent_runtime_id": runtimeID, "agent_private_key": encodedKey,
		"account_id": "account-" + runtimeID, "chatgpt_user_id": "user-" + runtimeID,
		"email": email, "plan_type": "plus", "chatgpt_account_is_fedramp": false,
		"task_id": taskID,
	}
}

func testAgentIdentityCredential(t *testing.T, identity map[string]any) []byte {
	t.Helper()
	raw, errMarshal := json.Marshal(map[string]any{
		"type": agentIdentityProvider, "auth_mode": agentIdentityAuthMode, "agent_identity": identity,
	})
	if errMarshal != nil {
		t.Fatalf("Marshal() error = %v", errMarshal)
	}
	return raw
}

func testAgentIdentityEncodedPrivateKey(t *testing.T, privateKey ed25519.PrivateKey) string {
	t.Helper()
	raw, errMarshal := x509.MarshalPKCS8PrivateKey(privateKey)
	if errMarshal != nil {
		t.Fatalf("MarshalPKCS8PrivateKey() error = %v", errMarshal)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

func testAgentIdentityJWTCredential(t *testing.T, now time.Time) ([]byte, agentIdentityJWKS) {
	t.Helper()
	_, agentPrivateKey, errAgentKey := ed25519.GenerateKey(rand.Reader)
	if errAgentKey != nil {
		t.Fatalf("GenerateKey() error = %v", errAgentKey)
	}
	rsaPrivateKey, errRSA := rsa.GenerateKey(rand.Reader, 2048)
	if errRSA != nil {
		t.Fatalf("GenerateKey() RSA error = %v", errRSA)
	}
	headerRaw, _ := json.Marshal(agentIdentityJWTHeader{Algorithm: "RS256", KeyID: "test-key"})
	claimsRaw, _ := json.Marshal(agentIdentityClaims{
		Issuer: agentIdentityIssuer, Audience: agentIdentityAudience,
		IssuedAt: now.Add(-time.Minute).Unix(), ExpiresAt: now.Add(time.Hour).Unix(),
		AgentRuntimeID: "runtime-jwt", AgentPrivateKey: testAgentIdentityEncodedPrivateKey(t, agentPrivateKey),
		AccountID: "account-jwt", ChatGPTUserID: "user-jwt", Email: "jwt@example.com", PlanType: "team",
	})
	header := base64.RawURLEncoding.EncodeToString(headerRaw)
	claims := base64.RawURLEncoding.EncodeToString(claimsRaw)
	digest := sha256.Sum256([]byte(header + "." + claims))
	signature, errSign := rsa.SignPKCS1v15(rand.Reader, rsaPrivateKey, crypto.SHA256, digest[:])
	if errSign != nil {
		t.Fatalf("SignPKCS1v15() error = %v", errSign)
	}
	jwt := header + "." + claims + "." + base64.RawURLEncoding.EncodeToString(signature)
	raw, _ := json.Marshal(map[string]any{"type": agentIdentityProvider, "auth_mode": agentIdentityAuthMode, "agent_identity": jwt})
	jwks := agentIdentityJWKS{Keys: []agentIdentityJWK{{
		KeyType: "RSA", KeyID: "test-key", Algorithm: "RS256", Use: "sig",
		Modulus:  base64.RawURLEncoding.EncodeToString(rsaPrivateKey.PublicKey.N.Bytes()),
		Exponent: base64.RawURLEncoding.EncodeToString([]byte{1, 0, 1}),
	}}}
	return raw, jwks
}

func verifyAgentIdentityAssertionHeader(t *testing.T, authorization string, parsed agentIdentityParsed, taskID string, now time.Time) {
	t.Helper()
	const prefix = "AgentAssertion "
	if !strings.HasPrefix(authorization, prefix) {
		t.Fatalf("authorization scheme is invalid")
	}
	raw, errDecode := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(authorization, prefix))
	if errDecode != nil {
		t.Fatalf("DecodeString() assertion error = %v", errDecode)
	}
	var assertion struct {
		AgentRuntimeID string `json:"agent_runtime_id"`
		Signature      string `json:"signature"`
		TaskID         string `json:"task_id"`
		Timestamp      string `json:"timestamp"`
	}
	if errJSON := json.Unmarshal(raw, &assertion); errJSON != nil {
		t.Fatalf("Unmarshal() assertion error = %v", errJSON)
	}
	if assertion.AgentRuntimeID != parsed.claims.AgentRuntimeID || assertion.TaskID != taskID || assertion.Timestamp != agentIdentityTimestamp(now) {
		t.Fatalf("assertion metadata is invalid")
	}
	signature, errSignature := base64.StdEncoding.DecodeString(assertion.Signature)
	if errSignature != nil || !ed25519.Verify(parsed.privateKey.Public().(ed25519.PublicKey), []byte(assertion.AgentRuntimeID+":"+taskID+":"+assertion.Timestamp), signature) {
		t.Fatal("assertion signature is invalid")
	}
}

func cloneStringAnyMap(source map[string]any) map[string]any {
	clone := make(map[string]any, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}
