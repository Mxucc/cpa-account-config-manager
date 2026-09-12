package manager

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"sort"
	"strings"
	"time"
)

// OpenCode upstreams are OpenAI-compatible gateways. The community reference
// implementation (github.com/jasonxu114514/opencode2api) documents the contract
// this file relies on:
//
//	Zen:    https://opencode.ai/zen      -> /v1/models, /v1/chat/completions
//	Zen Go: https://opencode.ai/zen/go   -> /v1/models, /v1/chat/completions
//
// Requests authenticate with a Bearer API key and identify the client with an
// OpenCode user agent plus the x-opencode-client header. Because the upstream is
// OpenAI-compatible, CPA can route these models natively once a channel points
// at the matching base URL, which is what the bind action below configures.
const (
	openCodeGoDefaultBaseURL     = "https://opencode.ai/zen/go"
	openCodeModelCatalogMaxBytes = 8 << 20
	openCodeModelCatalogMaxItems = 1024
	openCodeModelProbeMaxBytes   = 1 << 20
	openCodeModelTestTimeout     = 30
	openCodeModelTestMaxTimeout  = 60
)

// openCodeClientUserAgent mirrors the official OpenCode client identifier. The
// upstream rejects requests that look like a different client, so the plugin
// sends the same shape as the reference proxy.
func openCodeClientUserAgent() string {
	return fmt.Sprintf("opencode/1.18.21 (%s %s; %s)", runtime.GOOS, runtime.GOARCH, runtime.Version())
}

// openCodeAPIBase normalizes an OpenCode gateway base URL. A stored base may
// already include the API version (https://opencode.ai/zen/v1); both shapes reach
// the same endpoints.
func openCodeAPIBase(baseURL string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	trimmed = strings.TrimSuffix(trimmed, "/v1")
	return trimmed
}

// OpenCodeModelCatalog is one resolved model list for an OpenCode credential.
type OpenCodeModelCatalog struct {
	Models    []string  `json:"models"`
	FetchedAt time.Time `json:"fetched_at,omitempty"`
	Error     string    `json:"error,omitempty"`
}

// OpenCodeModelTestResult is the sanitized outcome of one OpenCode model probe.
// It never contains the credential, an upstream request body, or a raw header.
type OpenCodeModelTestResult struct {
	Reachable  bool      `json:"reachable"`
	Status     string    `json:"status"`
	StatusCode int       `json:"status_code,omitempty"`
	ReasonCode string    `json:"reason_code,omitempty"`
	Model      string    `json:"model,omitempty"`
	Detail     string    `json:"detail,omitempty"`
	LatencyMS  int64     `json:"latency_ms,omitempty"`
	TestedAt   time.Time `json:"tested_at"`
}

func openCodeModelTimeout(seconds int) time.Duration {
	if seconds >= 1 && seconds <= openCodeModelTestMaxTimeout {
		return time.Duration(seconds) * time.Second
	}
	return time.Duration(openCodeModelTestTimeout) * time.Second
}

func newOpenCodeRequest(ctx context.Context, method, target, apiKey string, body io.Reader) (*http.Request, error) {
	request, errRequest := http.NewRequestWithContext(ctx, method, target, body)
	if errRequest != nil {
		return nil, errRequest
	}
	request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
	request.Header.Set("User-Agent", openCodeClientUserAgent())
	request.Header.Set("x-opencode-client", "cli")
	request.Header.Set("Accept", "application/json")
	// The gateway rejects a request that carries no session id at all, exactly like the CLI
	// traffic it expects: this header is what makes the plugin's own probes routable.
	request.Header.Set("x-opencode-session", newOpenCodeProbeSessionValue())
	return request, nil
}

// newOpenCodeProbeSessionValue mints one session id for a plugin-initiated request. The CLI keeps
// a session per conversation; a probe is its own short conversation, so it gets a fresh id in the
// same shape the router uses for routed traffic.
func newOpenCodeProbeSessionValue() string {
	raw := make([]byte, 16)
	if _, errRead := rand.Read(raw); errRead != nil {
		// A missing random source must not produce a request without the header.
		sum := sha256.Sum256([]byte(fmt.Sprintf("probe-%d", time.Now().UnixNano())))
		return "oc-" + hex.EncodeToString(sum[:16])
	}
	return "oc-" + hex.EncodeToString(raw)
}

// fetchOpenCodeModels lists the models one OpenCode credential can reach. It
// returns the upstream status code so callers can distinguish an authentication
// failure from a transport problem.
func fetchOpenCodeModels(ctx context.Context, baseURL, apiKey string, timeout time.Duration) ([]string, int, error) {
	base := openCodeAPIBase(baseURL)
	key := strings.TrimSpace(apiKey)
	if base == "" {
		return nil, 0, fmt.Errorf("OpenCode base URL is required")
	}
	if key == "" {
		return nil, 0, fmt.Errorf("OpenCode API key is required")
	}
	client := &http.Client{Timeout: timeout}
	request, errRequest := newOpenCodeRequest(ctx, http.MethodGet, base+"/v1/models", key, nil)
	if errRequest != nil {
		return nil, 0, fmt.Errorf("create OpenCode models request")
	}
	response, errDo := client.Do(request)
	if errDo != nil {
		return nil, 0, fmt.Errorf("OpenCode models request failed: %s", sanitizeOpenCodeError(errDo.Error()))
	}
	if response == nil || response.Body == nil {
		return nil, 0, fmt.Errorf("OpenCode models endpoint returned an empty response")
	}
	defer func() { _ = response.Body.Close() }()
	body, errRead := io.ReadAll(io.LimitReader(response.Body, openCodeModelCatalogMaxBytes))
	if errRead != nil {
		return nil, response.StatusCode, fmt.Errorf("read OpenCode models response: %s", sanitizeOpenCodeError(errRead.Error()))
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, response.StatusCode, fmt.Errorf("OpenCode models endpoint returned HTTP %d", response.StatusCode)
	}
	models, errParse := parseOpenCodeModelCatalog(body)
	if errParse != nil {
		return nil, response.StatusCode, errParse
	}
	return models, response.StatusCode, nil
}

// parseOpenCodeModelCatalog accepts the OpenAI `{"data":[{"id":...}]}` shape and
// a plain string array, because gateways in this ecosystem return both.
func parseOpenCodeModelCatalog(body []byte) ([]string, error) {
	var envelope struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if errDecode := json.Unmarshal(body, &envelope); errDecode == nil && len(envelope.Data) > 0 {
		models := make([]string, 0, len(envelope.Data))
		for _, item := range envelope.Data {
			if trimmed := strings.TrimSpace(item.ID); trimmed != "" {
				models = append(models, trimmed)
			}
		}
		return normalizeOpenCodeModels(models), nil
	}
	var list []string
	if errDecode := json.Unmarshal(body, &list); errDecode == nil && len(list) > 0 {
		return normalizeOpenCodeModels(list), nil
	}
	return nil, fmt.Errorf("OpenCode models endpoint returned an unrecognized catalog")
}

func normalizeOpenCodeModels(models []string) []string {
	normalized := make([]string, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		trimmed := strings.TrimSpace(model)
		if trimmed == "" || len(trimmed) > 256 {
			continue
		}
		if _, exists := seen[trimmed]; exists {
			continue
		}
		seen[trimmed] = struct{}{}
		normalized = append(normalized, trimmed)
		if len(normalized) >= openCodeModelCatalogMaxItems {
			break
		}
	}
	sort.Strings(normalized)
	return normalized
}

// probeOpenCodeModel sends one minimal chat completion so an operator can verify
// that a credential really serves a model. The request body is deliberately tiny
// and the response body is never stored: only the status and a bounded, redacted
// detail are returned.
func probeOpenCodeModel(ctx context.Context, baseURL, apiKey, model string, timeout time.Duration) OpenCodeModelTestResult {
	result := OpenCodeModelTestResult{Model: strings.TrimSpace(model), TestedAt: time.Now().UTC()}
	base := openCodeAPIBase(baseURL)
	key := strings.TrimSpace(apiKey)
	if base == "" || key == "" {
		result.Status, result.ReasonCode = "unsupported", "credential_incomplete"
		result.Detail = "OpenCode base URL and API key are both required"
		return result
	}
	if result.Model == "" {
		result.Status, result.ReasonCode = "unsupported", "invalid_model"
		result.Detail = "a model id is required"
		return result
	}
	payload, errMarshal := json.Marshal(map[string]any{
		"model":      result.Model,
		"messages":   []map[string]string{{"role": "user", "content": "ping"}},
		"max_tokens": 16,
		"stream":     false,
	})
	if errMarshal != nil {
		result.Status, result.ReasonCode = "unsupported", "invalid_model"
		result.Detail = "the probe request could not be encoded"
		return result
	}
	client := &http.Client{Timeout: timeout}
	request, errRequest := newOpenCodeRequest(ctx, http.MethodPost, base+"/v1/chat/completions", key, bytes.NewReader(payload))
	if errRequest != nil {
		result.Status, result.ReasonCode = "unavailable", "request_failed"
		result.Detail = "the probe request could not be created"
		return result
	}
	request.Header.Set("Content-Type", "application/json")
	startedAt := time.Now()
	response, errDo := client.Do(request)
	result.LatencyMS = time.Since(startedAt).Milliseconds()
	if errDo != nil {
		result.Status, result.ReasonCode = "unavailable", "request_timeout"
		result.Detail = sanitizeOpenCodeError(errDo.Error())
		return result
	}
	if response == nil || response.Body == nil {
		result.Status, result.ReasonCode = "unavailable", "invalid_response"
		result.Detail = "the upstream returned an empty response"
		return result
	}
	defer func() { _ = response.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(response.Body, openCodeModelProbeMaxBytes))
	result.StatusCode = response.StatusCode
	result.Reachable = response.StatusCode > 0
	switch {
	case response.StatusCode >= 200 && response.StatusCode < 300:
		result.Status, result.ReasonCode = "available", "model_response_ok"
		return result
	case response.StatusCode == http.StatusUnauthorized, response.StatusCode == http.StatusForbidden:
		result.Status, result.ReasonCode = "unavailable", "authentication_failed"
	case response.StatusCode == http.StatusNotFound:
		result.Status, result.ReasonCode = "unavailable", "model_not_found"
	case response.StatusCode == http.StatusTooManyRequests:
		result.Status, result.ReasonCode = "unavailable", "quota_limited"
	case response.StatusCode >= 500:
		result.Status, result.ReasonCode = "unavailable", "upstream_unavailable"
	default:
		result.Status, result.ReasonCode = "review", "unconfirmed_upstream_response"
	}
	result.Detail = sanitizeOpenCodeError(string(body))
	return result
}
