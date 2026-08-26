package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

// AIProviderProbeRequest carries the minimal channel information needed to
// test connectivity. Secrets are accepted only from the authenticated
// management request and are never persisted or logged.
type AIProviderProbeRequest struct {
	Kind    string            `json:"kind"`
	BaseURL string            `json:"base_url"`
	APIKey  string            `json:"api_key,omitempty"`
	AuthID  string            `json:"auth_id,omitempty"`
	Model   string            `json:"model,omitempty"`
	Timeout int               `json:"timeout_seconds,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// AIProviderProbeResult reports whether the channel endpoint is reachable and
// how the upstream treated the supplied credential.
type AIProviderProbeResult struct {
	Reachable  bool                      `json:"reachable"`
	StatusCode int                       `json:"status_code,omitempty"`
	Detail     string                    `json:"detail,omitempty"`
	Model      string                    `json:"model,omitempty"`
	Status     string                    `json:"status,omitempty"`
	ProbeKind  string                    `json:"probe_kind,omitempty"`
	ReasonCode string                    `json:"reason_code,omitempty"`
	LatencyMS  int64                     `json:"latency_ms,omitempty"`
	TestedAt   time.Time                 `json:"tested_at,omitempty"`
	Response   *ModelTestResponsePreview `json:"response,omitempty"`
	Models     []AccountModelOption      `json:"models,omitempty"`
}

const (
	aiProviderProbeMaxTimeoutSeconds = 20
	aiProviderProbeMaxResponseBytes  = 8 << 10
)

func (a *App) handleAIProviderProbe(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	if a == nil || a.agentIdentity == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "provider probe is unavailable"})
	}
	if resolveManagementKey(req.Headers) == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	var request AIProviderProbeRequest
	if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "invalid provider probe request"})
	}
	kind := normalizeAIProviderKind(request.Kind)
	apiKey := strings.TrimSpace(request.APIKey)
	if apiKey == "" {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "api_key is required"})
	}
	baseURL := strings.TrimSpace(request.BaseURL)
	if baseURL == "" {
		baseURL = defaultAIProviderProbeURL(kind)
	}
	timeout := time.Duration(aiProviderProbeMaxTimeoutSeconds) * time.Second
	if request.Timeout >= 1 && request.Timeout <= aiProviderProbeMaxTimeoutSeconds {
		timeout = time.Duration(request.Timeout) * time.Second
	}
	var result AIProviderProbeResult
	// Keep the original catalog reachability probe for old clients. New clients
	// submit a model and receive the same structured result as account testing.
	if strings.TrimSpace(request.Model) != "" {
		result = a.probeAIProviderModel(ctx, kind, baseURL, apiKey, request.AuthID, request.Model, request.Headers, timeout)
	} else {
		result = a.probeAIProviderEndpoint(ctx, kind, baseURL, apiKey, request.AuthID, request.Headers, timeout)
	}
	status := http.StatusOK
	// Model tests return a structured result even for expected upstream
	// failures (401/400/429), matching account model-test semantics. Keep the
	// legacy catalog-only endpoint's 502 behavior for old clients.
	if !result.Reachable && strings.TrimSpace(request.Model) == "" {
		status = http.StatusBadGateway
	}
	return jsonResponse(status, result)
}

func (a *App) probeAIProviderEndpoint(
	ctx context.Context,
	kind, baseURL, apiKey, authID string,
	customHeaders map[string]string,
	timeout time.Duration,
) AIProviderProbeResult {
	probeURL, errParse := url.Parse(baseURL)
	if errParse != nil || probeURL.Scheme == "" || probeURL.Host == "" {
		return AIProviderProbeResult{Reachable: false, Detail: "invalid base URL"}
	}
	// Build a candidate endpoint: the base URL itself or the common models
	// listing path when the base URL looks like a plain origin.
	// Build a candidate endpoint: the common model catalog first, then the
	// configured origin as a compatibility fallback. Vertex-compatible
	// channels do not expose a catalog listing path, so probe their origin
	// directly to avoid a guaranteed 404 round trip.
	var candidates []string
	if normalizeAIProviderKind(kind) == "vertex-api-key" {
		candidates = []string{baseURL}
	} else {
		trimmed := strings.TrimRight(baseURL, "/")
		if !strings.HasSuffix(trimmed, "/models") {
			candidates = append(candidates, trimmed+"/models")
		}
		candidates = append(candidates, baseURL)
	}

	transport := a.agentIdentity.transport
	if transport == nil {
		return AIProviderProbeResult{Reachable: false, Detail: "host HTTP transport is unavailable"}
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var lastStatus int
	var lastDetail string
	for _, candidate := range candidates {
		request := cpaapi.HostHTTPRequest{
			Method: http.MethodGet,
			URL:    candidate,
			Headers: http.Header{
				"Accept": []string{"application/json"},
			},
		}
		for key, value := range customHeaders {
			key = strings.TrimSpace(key)
			value = strings.TrimSpace(value)
			if key == "" || value == "" || strings.ContainsAny(key, "\r\n") || strings.ContainsAny(value, "\r\n") {
				continue
			}
			request.Headers.Set(key, value)
		}
		applyAIProviderProbeAuth(request.Headers, kind, apiKey)
		a.applyAIProviderCodexFingerprint(request.Headers, kind, authID)
		response, errDo := transport.AgentIdentityDo(probeCtx, "", request)
		if errDo != nil {
			lastDetail = sanitizeAIProviderProbeError(errDo)
			continue
		}
		lastStatus = response.StatusCode
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			return AIProviderProbeResult{Reachable: true, StatusCode: response.StatusCode, Detail: "reachable", Models: parseAIProviderModelCatalog(response.Body)}
		}
		lastDetail = aiProviderProbeStatusDetail(response.StatusCode)
		if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
			break
		}
	}
	return AIProviderProbeResult{Reachable: false, StatusCode: lastStatus, Detail: lastDetail}
}

// probeAIProviderModel performs a real, minimal model request with the
// submitted provider credential. It deliberately uses the same payload builder
// and response classifier as account model tests, but sends directly through
// the host transport because an AI-provider channel is not an auth file.
func (a *App) probeAIProviderModel(ctx context.Context, kind, baseURL, apiKey, authID, model string, customHeaders map[string]string, timeout time.Duration) AIProviderProbeResult {
	started := time.Now()
	result := AIProviderProbeResult{Model: strings.TrimSpace(model), ProbeKind: InspectionProbeKindModel, TestedAt: started}
	if safeModelIdentifier(model) == "" {
		result.ReasonCode = "invalid_model"
		result.Detail = "model contains unsupported characters or exceeds 128 characters"
		return result
	}
	provider := aiProviderModelKind(kind)
	metadata := modelTestAuthMetadata{hasAPIKey: true, baseURL: normalizeAIProviderBaseURL(baseURL)}
	probe, selected, supported, errBuild := buildModelProbe(provider, model, metadata)
	if errBuild != nil {
		result.ReasonCode = "unsupported_provider"
		result.Detail = sanitizeAIProviderProbeError(errBuild)
		return result
	}
	result.Model = selected
	if !supported {
		result.ReasonCode = "unsupported_provider"
		result.Detail = "provider model testing is not supported for this channel"
		return result
	}
	// CPA channel settings may point at a compatible proxy rather than the
	// provider's public origin. Keep the provider-specific request shape while
	// replacing only its destination with the configured base URL.
	probe.url = aiProviderModelProbeURL(kind, baseURL, selected, probe.url)
	for key, value := range customHeaders {
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if key == "" || value == "" || strings.ContainsAny(key, "\r\n") || strings.ContainsAny(value, "\r\n") {
			continue
		}
		probe.headers[key] = value
	}
	replaceModelProbeToken(probe.headers, apiKey)
	probeHeaders := http.Header{}
	for key, value := range probe.headers {
		probeHeaders.Set(key, value)
	}
	applyAIProviderProbeAuth(probeHeaders, kind, apiKey)
	a.applyAIProviderCodexFingerprint(probeHeaders, kind, authID)
	probe.headers = map[string]string{}
	for key, values := range probeHeaders {
		if len(values) > 0 {
			probe.headers[key] = values[0]
		}
	}
	transport := a.agentIdentity.transport
	if transport == nil {
		result.ReasonCode, result.Detail = "transport_unavailable", "host HTTP transport is unavailable"
		return result
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request := cpaapi.HostHTTPRequest{Method: firstNonEmpty(probe.method, http.MethodPost), URL: probe.url, Headers: http.Header{}, Body: []byte(probe.data)}
	for key, value := range probe.headers {
		request.Headers.Set(key, value)
	}
	response, errDo := transport.AgentIdentityDo(probeCtx, "", request)
	result.LatencyMS = maxInt64(0, time.Since(started).Milliseconds())
	if errDo != nil {
		result.ReasonCode = "request_failed"
		result.Detail = sanitizeAIProviderProbeError(errDo)
		return result
	}
	result.StatusCode = response.StatusCode
	body := response.Body
	if len(body) > maxModelTestResponseBytes {
		body = body[:maxModelTestResponseBytes]
	}
	result.Response = sanitizeModelTestResponsePreview(modelProbeHTTPResponse{StatusCode: response.StatusCode, Header: response.Headers, Body: body})
	result.Status, result.ReasonCode = classifyModelProbe(probe.kind, response.StatusCode, body)
	result.Reachable = result.Status == "available"
	result.Detail = aiProviderModelProbeDetail(result.Status, result.ReasonCode)
	return result
}

func aiProviderModelKind(kind string) string {
	switch normalizeAIProviderKind(kind) {
	case "codex-api-key":
		return "codex"
	case "claude-api-key":
		return "claude"
	case "gemini-api-key", "interactions-api-key", "gemini-interactions", "aistudio":
		return "gemini"
	case "vertex-api-key":
		return "vertex"
	case "xai-api-key":
		return "xai"
	case "api-keys":
		return "openai"
	case "openai-compatibility", "openai-compatible":
		return "openai-compatible"
	default:
		return normalizeAIProviderKind(kind)
	}
}

func aiProviderModelProbeURL(kind, baseURL, model, fallback string) string {
	base := normalizeAIProviderBaseURL(baseURL)
	if base == "" {
		return fallback
	}
	// A CPA entry may already contain the concrete inference endpoint. Keep it
	// intact instead of silently falling back to the public provider URL; this
	// is important for OpenAI-compatible proxies and self-hosted Claude/Gemini
	// gateways.
	if strings.HasSuffix(base, "/chat/completions") || strings.HasSuffix(base, "/responses") || strings.Contains(base, ":generateContent") || strings.HasSuffix(base, "/messages") {
		return base
	}
	switch normalizeAIProviderKind(kind) {
	case "openai-compatibility", "openai-compatible":
		return openAICompatibleChatURL(base)
	case "claude-api-key":
		return strings.TrimRight(base, "/") + "/messages"
	case "xai-api-key":
		return strings.TrimRight(base, "/") + "/responses"
	case "gemini-api-key", "interactions-api-key":
		trimmed := strings.TrimRight(base, "/")
		if strings.HasSuffix(trimmed, "/v1beta") || strings.HasSuffix(trimmed, "/v1") {
			return trimmed + "/models/" + url.PathEscape(strings.TrimPrefix(model, "models/")) + ":generateContent"
		}
	}
	return fallback
}

func normalizeAIProviderBaseURL(value string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(value), "/")
	if strings.HasSuffix(trimmed, "/models") {
		trimmed = strings.TrimSuffix(trimmed, "/models")
	}
	return trimmed
}

func replaceModelProbeToken(headers map[string]string, token string) {
	for key, value := range headers {
		if strings.Contains(value, "$TOKEN$") {
			headers[key] = strings.ReplaceAll(value, "$TOKEN$", token)
		}
	}
}

func aiProviderModelProbeDetail(status, reason string) string {
	if status == "available" {
		return "model response is available"
	}
	switch reason {
	case "authentication_failed":
		return "upstream rejected the provider credential"
	case "quota_limited":
		return "upstream quota or request limit reached"
	case "model_not_found":
		return "the selected model is unavailable"
	default:
		return "upstream model probe could not confirm availability"
	}
}

// parseAIProviderModelCatalog accepts the common OpenAI and Gemini catalog
// envelopes while bounding input and output. The returned entries are safe
// allow-listed data and never contain credentials or arbitrary upstream fields.
func parseAIProviderModelCatalog(body []byte) []AccountModelOption {
	if len(body) == 0 || len(body) > aiProviderProbeMaxResponseBytes {
		return nil
	}
	var envelope struct {
		Data []struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
		Models []struct {
			Name        string `json:"name"`
			DisplayName string `json:"displayName"`
			Description string `json:"description"`
		} `json:"models"`
	}
	if json.Unmarshal(bytes.TrimSpace(body), &envelope) != nil {
		return nil
	}
	seen := map[string]bool{}
	out := make([]AccountModelOption, 0, len(envelope.Data)+len(envelope.Models))
	add := func(id, display, owner, typ string) {
		id = strings.TrimSpace(strings.TrimPrefix(id, "models/"))
		if safeModelIdentifier(id) == "" || seen[id] {
			return
		}
		seen[id] = true
		out = append(out, AccountModelOption{ID: id, DisplayName: strings.TrimSpace(display), OwnedBy: strings.TrimSpace(owner), Type: typ})
	}
	for _, item := range envelope.Data {
		add(item.ID, item.ID, item.OwnedBy, item.Object)
	}
	for _, item := range envelope.Models {
		add(item.Name, firstNonEmpty(item.DisplayName, item.Name), "", "model")
	}
	if len(out) > 1000 {
		out = out[:1000]
	}
	return out
}

func normalizeAIProviderKind(kind string) string {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind == "" {
		return "openai-compatibility"
	}
	return kind
}

func defaultAIProviderProbeURL(kind string) string {
	switch normalizeAIProviderKind(kind) {
	case "claude-api-key":
		return "https://api.anthropic.com/v1/models"
	case "gemini-api-key", "interactions-api-key":
		// CPA's native Gemini and Interactions API-key channels both use the
		// Google Generative Language model catalog and x-goog-api-key auth.
		return "https://generativelanguage.googleapis.com/v1beta/models"
	case "xai-api-key":
		return "https://api.x.ai/v1/models"
	case "vertex-api-key":
		// Vertex-compatible requests append the model action to this origin;
		// probing a catalog path would reject otherwise-valid third-party hosts.
		return "https://aiplatform.googleapis.com"
	case "opencode-go", "opencode-zen":
		return "https://api.openai.com/v1/models"
	default:
		return "https://api.openai.com/v1/models"
	}
}

func applyAIProviderProbeAuth(headers http.Header, kind, apiKey string) {
	if normalizeAIProviderKind(kind) == "codex-api-key" {
		// CPA's codex-api-key channel reaches the Responses-compatible OpenAI
		// edge. Keep its health checks on the same converged official identity.
		ensureCodexIdentityHeaders(headers)
	}
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return
	}
	switch normalizeAIProviderKind(kind) {
	case "claude-api-key":
		headers.Set("x-api-key", apiKey)
		headers.Set("anthropic-version", "2023-06-01")
	case "gemini-api-key", "interactions-api-key", "vertex-api-key":
		headers.Set("x-goog-api-key", apiKey)
	default:
		headers.Set("Authorization", "Bearer "+apiKey)
	}
}

func aiProviderProbeStatusDetail(statusCode int) string {
	switch statusCode {
	case http.StatusUnauthorized:
		return "upstream rejected the credential (401)"
	case http.StatusForbidden:
		return "upstream rejected the credential (403)"
	case http.StatusNotFound:
		return "endpoint not found (404)"
	case http.StatusTooManyRequests:
		return "upstream rate limited (429)"
	default:
		if statusCode >= 500 {
			return fmt.Sprintf("upstream error (%d)", statusCode)
		}
		return fmt.Sprintf("unexpected status (%d)", statusCode)
	}
}

func sanitizeAIProviderProbeError(err error) string {
	if err == nil {
		return "request failed"
	}
	message := err.Error()
	if strings.Contains(message, "timeout") || strings.Contains(message, "deadline exceeded") {
		return "request timed out"
	}
	if strings.Contains(message, "connection refused") || strings.Contains(message, "no such host") || strings.Contains(message, "dial tcp") {
		return "unreachable endpoint"
	}
	if strings.Contains(message, "certificate") {
		return "TLS verification failed"
	}
	return "request failed"
}
