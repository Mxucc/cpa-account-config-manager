package manager

import (
	"context"
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
	Timeout int               `json:"timeout_seconds,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// AIProviderProbeResult reports whether the channel endpoint is reachable and
// how the upstream treated the supplied credential.
type AIProviderProbeResult struct {
	Reachable  bool   `json:"reachable"`
	StatusCode int    `json:"status_code,omitempty"`
	Detail     string `json:"detail,omitempty"`
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
	result := a.probeAIProviderEndpoint(ctx, kind, baseURL, apiKey, request.Headers, timeout)
	status := http.StatusOK
	if !result.Reachable {
		status = http.StatusBadGateway
	}
	return jsonResponse(status, AIProviderProbeResult{Reachable: result.Reachable, StatusCode: result.StatusCode, Detail: result.Detail})
}

func (a *App) probeAIProviderEndpoint(ctx context.Context, kind, baseURL, apiKey string, customHeaders map[string]string, timeout time.Duration) AIProviderProbeResult {
	probeURL, errParse := url.Parse(baseURL)
	if errParse != nil || probeURL.Scheme == "" || probeURL.Host == "" {
		return AIProviderProbeResult{Reachable: false, Detail: "invalid base URL"}
	}
	// Build a candidate endpoint: the base URL itself or the common models
	// listing path when the base URL looks like a plain origin.
	candidates := []string{baseURL}
	trimmed := strings.TrimRight(baseURL, "/")
	if !strings.HasSuffix(trimmed, "/models") {
		candidates = append([]string{trimmed + "/models"}, candidates...)
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
		response, errDo := transport.AgentIdentityDo(probeCtx, "", request)
		if errDo != nil {
			lastDetail = sanitizeAIProviderProbeError(errDo)
			continue
		}
		lastStatus = response.StatusCode
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			return AIProviderProbeResult{Reachable: true, StatusCode: response.StatusCode, Detail: "reachable"}
		}
		lastDetail = aiProviderProbeStatusDetail(response.StatusCode)
		if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
			break
		}
	}
	return AIProviderProbeResult{Reachable: false, StatusCode: lastStatus, Detail: lastDetail}
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
	case "gemini-api-key":
		return "https://generativelanguage.googleapis.com/v1beta/models"
	case "xai-api-key":
		return "https://api.x.ai/v1/models"
	case "vertex-api-key":
		return "https://aiplatform.googleapis.com/v1/publishers/google/models"
	case "opencode-go", "opencode-zen":
		return "https://api.openai.com/v1/models"
	default:
		return "https://api.openai.com/v1/models"
	}
}

func applyAIProviderProbeAuth(headers http.Header, kind, apiKey string) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return
	}
	switch normalizeAIProviderKind(kind) {
	case "claude-api-key":
		headers.Set("x-api-key", apiKey)
		headers.Set("anthropic-version", "2023-06-01")
	case "gemini-api-key", "vertex-api-key":
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
