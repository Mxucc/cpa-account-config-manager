package manager

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

// The Codex model page tests one model. A Codex credential can live either as a CPA account or as
// an "codex-api-key" AI-provider channel, and an installation that only configured channels has
// no account at all, so both are offered as targets.

const codexModelTestMaxTimeoutSeconds = 60

// CodexTestTarget is one credential the Codex model page can probe.
type CodexTestTarget struct {
	// ID is "account:<id>" or "channel:<index>", so the UI can tell the two apart.
	ID    string `json:"id"`
	Label string `json:"label"`
	Kind  string `json:"kind"`
	// KeySet reports whether the target carries a credential; a channel without one cannot be
	// probed and is shown as unavailable instead of failing later.
	KeySet bool `json:"key_set"`
}

// CodexModelProbeResult is the sanitized outcome of one Codex channel probe.
type CodexModelProbeResult struct {
	Reachable  bool   `json:"reachable"`
	Status     string `json:"status"`
	StatusCode int    `json:"status_code,omitempty"`
	ReasonCode string `json:"reason_code,omitempty"`
	Model      string `json:"model,omitempty"`
	Detail     string `json:"detail,omitempty"`
	LatencyMS  int64  `json:"latency_ms,omitempty"`
	Endpoint   string `json:"endpoint,omitempty"`
	TestedAt   string `json:"tested_at,omitempty"`
}

// handleCodexTestTargets lists every credential the Codex model page can probe.
func (a *App) handleCodexTestTargets(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	managementKey := resolveManagementKey(req.Headers)
	if managementKey == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	targets := make([]CodexTestTarget, 0, 8)
	if entries, errEntries := a.aiProviderChannelEntries(ctx, managementKey, "codex-api-key"); errEntries == nil {
		for index, entry := range entries {
			label := strings.TrimSpace(aiProviderChannelDisplayName(entry))
			if label == "" {
				label = "codex-api-key:" + strconv.Itoa(index)
			}
			targets = append(targets, CodexTestTarget{
				ID:     "channel:" + strconv.Itoa(index),
				Label:  label,
				Kind:   "channel",
				KeySet: aiProviderChannelCredential(entry) != "",
			})
		}
	}
	return jsonResponse(http.StatusOK, map[string]any{"targets": targets})
}

// handleCodexModelTest probes one model through a saved Codex channel. The probe runs against the
// channel's own base URL, credential and headers, which is what makes a channel testable without a
// CPA account.
func (a *App) handleCodexModelTest(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	managementKey := resolveManagementKey(req.Headers)
	if managementKey == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	if a == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "provider probe is unavailable"})
	}
	var request struct {
		Model        string `json:"model"`
		ChannelIndex *int   `json:"channel_index"`
		Timeout      int    `json:"timeout_seconds"`
	}
	if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "invalid Codex model test request"})
	}
	model := strings.TrimSpace(request.Model)
	if request.ChannelIndex == nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "channel_index is required"})
	}
	entries, errEntries := a.aiProviderChannelEntries(ctx, managementKey, "codex-api-key")
	if errEntries != nil {
		return jsonResponse(http.StatusBadGateway, map[string]any{"error": "CPA channels could not be read"})
	}
	index := *request.ChannelIndex
	if index < 0 || index >= len(entries) {
		return jsonResponse(http.StatusNotFound, map[string]any{"error": "the Codex channel no longer exists"})
	}
	entry := entries[index]
	apiKey := aiProviderChannelCredential(entry)
	if apiKey == "" {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "this channel has no stored credential"})
	}
	baseURL := aiProviderChannelBaseURL(entry)
	if baseURL == "" {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "this channel has no base URL"})
	}
	timeout := time.Duration(codexModelTestMaxTimeoutSeconds) * time.Second
	if request.Timeout >= 1 && request.Timeout <= codexModelTestMaxTimeoutSeconds {
		timeout = time.Duration(request.Timeout) * time.Second
	}
	probe := a.probeAIProviderModelWithKey(ctx, "codex-api-key", baseURL, apiKey, "", "", model, aiProviderChannelHeaders(entry), timeout)
	return jsonResponse(http.StatusOK, map[string]any{"result": codexModelProbeResult(probe)})
}

// codexModelProbeResult projects the shared provider probe result into the shape the model page
// already renders.
func codexModelProbeResult(probe AIProviderProbeResult) CodexModelProbeResult {
	result := CodexModelProbeResult{
		Reachable:  probe.Reachable,
		Status:     probe.Status,
		StatusCode: probe.StatusCode,
		ReasonCode: probe.ReasonCode,
		Model:      probe.Model,
		Detail:     sanitizeOpenCodeError(probe.Detail),
		LatencyMS:  probe.LatencyMS,
		TestedAt:   probe.TestedAt.UTC().Format(time.RFC3339Nano),
	}
	if result.Status == "" {
		result.Status = "review"
	}
	return result
}

// aiProviderChannelDisplayName is the channel's operator-facing name.
func aiProviderChannelDisplayName(entry map[string]any) string {
	if entry == nil {
		return ""
	}
	if name, ok := entry["name"].(string); ok && strings.TrimSpace(name) != "" {
		return strings.TrimSpace(name)
	}
	return aiProviderChannelBaseURL(entry)
}

// aiProviderChannelHeaders reads the custom headers configured on a channel.
func aiProviderChannelHeaders(entry map[string]any) map[string]string {
	if entry == nil {
		return nil
	}
	raw, ok := entry["headers"].(map[string]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	headers := make(map[string]string, len(raw))
	for name, value := range raw {
		text, isText := value.(string)
		if !isText || strings.TrimSpace(text) == "" {
			continue
		}
		if !safeHeaderName(name) {
			continue
		}
		headers[strings.TrimSpace(name)] = strings.TrimSpace(text)
	}
	if len(headers) == 0 {
		return nil
	}
	return headers
}

// safeHeaderName rejects a configured header name that could not be set on a request.
func safeHeaderName(name string) bool {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" || len(trimmed) > 128 {
		return false
	}
	for _, char := range trimmed {
		if char <= ' ' || char >= 0x7f {
			return false
		}
	}
	return true
}
