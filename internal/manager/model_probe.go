package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
)

const (
	modelTestTimeout          = 20 * time.Second
	maxModelTestResponseBytes = 256 << 10
	maxModelTestBodyBytes     = 128 << 10
	maxModelIdentifierLength  = 128
)

var (
	ErrModelTestBusy            = errors.New("too many model tests are running")
	ErrModelTestAccountNotFound = errors.New("account was not found")
)

type ModelTestRequest struct {
	AccountID  string `json:"account_id"`
	Model      string `json:"model,omitempty"`
	Inspection bool   `json:"-"`
}

type ModelTestResult struct {
	AccountID   string    `json:"account_id"`
	Provider    string    `json:"provider"`
	Model       string    `json:"model"`
	Status      string    `json:"status"`
	ProbeKind   string    `json:"probe_kind"`
	ReasonCode  string    `json:"reason_code"`
	StatusCode  int       `json:"status_code,omitempty"`
	QuotaWindow string    `json:"quota_window,omitempty"`
	LatencyMS   int64     `json:"latency_ms"`
	TestedAt    time.Time `json:"tested_at"`
}

type ModelTestService struct {
	accounts  *AccountService
	usage     credentialUsageObserver
	doer      HTTPDoer
	semaphore chan struct{}
	now       func() time.Time
}

type credentialUsageObserver interface {
	ObserveCredentialUsage(string, *CodexUsageSnapshot)
}

type modelProbe struct {
	kind    string
	method  string
	url     string
	headers map[string]string
	data    string
}

type modelTestAuthMetadata struct {
	hasAPIKey bool
	accountID string
}

type managementAPICallRequest struct {
	AuthIndex string            `json:"auth_index"`
	Method    string            `json:"method"`
	URL       string            `json:"url"`
	Header    map[string]string `json:"header"`
	Data      string            `json:"data"`
}

type managementAPICallResponse struct {
	StatusCode int                   `json:"status_code"`
	Header     map[string][]string   `json:"header"`
	Body       managementAPICallBody `json:"body"`
}

type managementAPICallBody string

type codexUsageProbeEnvelope struct {
	RateLimit      *codexUsageProbeLimit `json:"rate_limit"`
	RateLimitCamel *codexUsageProbeLimit `json:"rateLimit"`
}

type codexUsageProbeLimit struct {
	Allowed              *bool                  `json:"allowed"`
	LimitReached         *bool                  `json:"limit_reached"`
	LimitReachedCamel    *bool                  `json:"limitReached"`
	PrimaryWindow        *codexUsageProbeWindow `json:"primary_window"`
	PrimaryWindowCamel   *codexUsageProbeWindow `json:"primaryWindow"`
	SecondaryWindow      *codexUsageProbeWindow `json:"secondary_window"`
	SecondaryWindowCamel *codexUsageProbeWindow `json:"secondaryWindow"`
}

type codexUsageProbeWindow struct {
	UsedPercent             *float64 `json:"used_percent"`
	UsedPercentCamel        *float64 `json:"usedPercent"`
	LimitWindowSeconds      *float64 `json:"limit_window_seconds"`
	LimitWindowSecondsCamel *float64 `json:"limitWindowSeconds"`
	ResetAfterSeconds       *float64 `json:"reset_after_seconds"`
	ResetAfterSecondsCamel  *float64 `json:"resetAfterSeconds"`
	ResetAt                 *float64 `json:"reset_at"`
	ResetAtCamel            *float64 `json:"resetAt"`
}

func (b *managementAPICallBody) UnmarshalJSON(raw []byte) error {
	trimmed := bytes.TrimSpace(raw)
	if bytes.Equal(trimmed, []byte("null")) {
		*b = ""
		return nil
	}
	if len(trimmed) > 0 && trimmed[0] == '"' {
		var text string
		if errDecode := json.Unmarshal(trimmed, &text); errDecode != nil {
			return errDecode
		}
		*b = managementAPICallBody(text)
		return nil
	}
	if !json.Valid(trimmed) {
		return fmt.Errorf("api-call body is not valid JSON")
	}
	var compact bytes.Buffer
	if errCompact := json.Compact(&compact, trimmed); errCompact != nil {
		return errCompact
	}
	*b = managementAPICallBody(compact.String())
	return nil
}

func NewModelTestService(accounts *AccountService, usage ...credentialUsageObserver) *ModelTestService {
	service := &ModelTestService{
		accounts:  accounts,
		semaphore: make(chan struct{}, 4),
		now:       time.Now,
	}
	if len(usage) > 0 {
		service.usage = usage[0]
	}
	return service
}

func (s *ModelTestService) Run(ctx context.Context, request ModelTestRequest, managementBaseURL, managementKey string) (ModelTestResult, error) {
	accountID := safeOperationIdentifier(request.AccountID, 256)
	if accountID == "" {
		return ModelTestResult{}, fmt.Errorf("account_id is required and must be at most 256 characters")
	}
	model := strings.TrimSpace(request.Model)
	if model != "" && safeModelIdentifier(model) == "" {
		return ModelTestResult{}, fmt.Errorf("model contains unsupported characters or exceeds 128 characters")
	}
	if s == nil || s.accounts == nil {
		return ModelTestResult{}, fmt.Errorf("account service is unavailable")
	}
	select {
	case s.semaphore <- struct{}{}:
		defer func() { <-s.semaphore }()
	default:
		return ModelTestResult{}, ErrModelTestBusy
	}

	resolved, errResolve := s.accounts.ResolveTargets(ctx, TargetScope{Mode: "selected", IDs: []string{accountID}})
	if errResolve != nil {
		return ModelTestResult{}, fmt.Errorf("resolve model-test account: %w", errResolve)
	}
	if len(resolved.Accounts) != 1 {
		return ModelTestResult{}, ErrModelTestAccountNotFound
	}
	account := resolved.Accounts[0]
	provider := strings.ToLower(strings.TrimSpace(firstNonEmpty(account.Provider, account.Type)))
	metadata := s.authMetadata(ctx, account.ID)
	if accountTypeUsesAPIKey(account.AccountType) {
		metadata.hasAPIKey = true
	}
	startedAt := s.currentTime()
	result := ModelTestResult{
		AccountID: account.ID,
		Provider:  provider,
		Model:     model,
		TestedAt:  startedAt,
	}
	probeCtx, cancel := context.WithTimeout(ctx, modelTestTimeout)
	defer cancel()
	if provider == "codex" && !metadata.hasAPIKey {
		credential := buildCodexCredentialProbe(metadata)
		statusCode, body, errCredential := s.callManagementAPI(probeCtx, managementBaseURL, managementKey, account.ID, credential)
		if errCredential == nil {
			if statusCode >= http.StatusOK && statusCode < http.StatusMultipleChoices && s.usage != nil {
				s.usage.ObserveCredentialUsage(account.ID, codexUsageProbeSnapshot(body, s.currentTime()))
			}
			status, reason, quotaWindow := classifyCredentialProbeDetails(statusCode, body)
			if credentialProbeResultIsDefinitive(reason) || (request.Inspection && reason == "credential_response_ok") {
				result.Status = status
				result.ProbeKind = InspectionProbeKindCredential
				result.ReasonCode = reason
				result.StatusCode = boundedHTTPStatus(statusCode)
				result.QuotaWindow = quotaWindow
				result.LatencyMS = maxInt64(0, s.currentTime().Sub(startedAt).Milliseconds())
				return result, nil
			}
		} else if errors.Is(probeCtx.Err(), context.DeadlineExceeded) {
			result.Status = "review"
			result.ProbeKind = InspectionProbeKindCredential
			result.ReasonCode = "request_timeout"
			result.LatencyMS = maxInt64(0, s.currentTime().Sub(startedAt).Milliseconds())
			return result, nil
		} else if errors.Is(probeCtx.Err(), context.Canceled) {
			return ModelTestResult{}, probeCtx.Err()
		}
	}
	probe, selectedModel, supported, errProbe := buildModelProbe(provider, model, metadata)
	if errProbe != nil {
		return ModelTestResult{}, errProbe
	}
	result.Model = selectedModel
	result.ProbeKind = InspectionProbeKindModel
	if !supported {
		result.Status = "unsupported"
		result.ReasonCode = "unsupported_provider"
		return result, nil
	}

	upstreamStatus, upstreamBody, errCall := s.callManagementAPI(probeCtx, managementBaseURL, managementKey, account.ID, probe)
	result.LatencyMS = maxInt64(0, s.currentTime().Sub(startedAt).Milliseconds())
	if errCall != nil {
		result.Status = "review"
		if errors.Is(probeCtx.Err(), context.DeadlineExceeded) || errors.Is(errCall, context.DeadlineExceeded) {
			result.ReasonCode = "request_timeout"
		} else {
			result.ReasonCode = "upstream_unavailable"
		}
		return result, nil
	}
	result.Status, result.ReasonCode = classifyModelProbe(probe.kind, upstreamStatus, upstreamBody)
	result.StatusCode = boundedHTTPStatus(upstreamStatus)
	return result, nil
}

func buildCodexCredentialProbe(metadata modelTestAuthMetadata) modelProbe {
	headers := bearerJSONHeaders(false)
	headers["Originator"] = "codex_cli_rs"
	headers["User-Agent"] = "codex_cli_rs/0.1.0"
	if metadata.accountID != "" {
		headers["Chatgpt-Account-Id"] = metadata.accountID
	}
	return modelProbe{
		kind: "credential", method: http.MethodGet,
		url: "https://chatgpt.com/backend-api/wham/usage", headers: headers,
	}
}

func (s *ModelTestService) authMetadata(ctx context.Context, authIndex string) modelTestAuthMetadata {
	if s == nil || s.accounts == nil || s.accounts.host == nil {
		return modelTestAuthMetadata{}
	}
	detail, errGet := s.accounts.host.GetAuth(ctx, authIndex)
	if errGet != nil || len(detail.JSON) == 0 || len(detail.JSON) > 1<<20 {
		return modelTestAuthMetadata{}
	}
	var raw map[string]any
	if errDecode := json.Unmarshal(detail.JSON, &raw); errDecode != nil {
		return modelTestAuthMetadata{}
	}
	metadata := modelTestAuthMetadata{hasAPIKey: strings.TrimSpace(modelTestStringValue(raw, "api_key")) != ""}
	metadata.accountID = safeOperationIdentifier(firstNonEmpty(
		modelTestStringValue(raw, "account_id"),
		modelTestStringValue(raw, "chatgpt_account_id"),
	), 256)
	return metadata
}

func buildModelProbe(provider, requestedModel string, metadata modelTestAuthMetadata) (modelProbe, string, bool, error) {
	model := safeModelIdentifier(requestedModel)
	marshal := func(payload any) (string, error) {
		raw, errMarshal := json.Marshal(payload)
		if errMarshal != nil {
			return "", fmt.Errorf("encode model-test payload: %w", errMarshal)
		}
		return string(raw), nil
	}
	switch provider {
	case "codex":
		if model == "" {
			model = "gpt-5.4"
		}
		if metadata.hasAPIKey {
			data, errMarshal := marshal(openAIResponsesProbePayload(model, false))
			return modelProbe{kind: "openai", url: "https://api.openai.com/v1/responses", headers: bearerJSONHeaders(false), data: data}, model, true, errMarshal
		}
		data, errMarshal := marshal(openAIResponsesProbePayload(model, true))
		headers := bearerJSONHeaders(true)
		headers["OpenAI-Beta"] = "responses=experimental"
		headers["Originator"] = "codex_cli_rs"
		headers["User-Agent"] = "codex_cli_rs/0.1.0"
		if metadata.accountID != "" {
			headers["Chatgpt-Account-Id"] = metadata.accountID
		}
		return modelProbe{kind: "codex", url: "https://chatgpt.com/backend-api/codex/responses", headers: headers, data: data}, model, true, errMarshal
	case "openai":
		if model == "" {
			model = "gpt-5.4"
		}
		data, errMarshal := marshal(openAIResponsesProbePayload(model, false))
		return modelProbe{kind: "openai", url: "https://api.openai.com/v1/responses", headers: bearerJSONHeaders(false), data: data}, model, true, errMarshal
	case "claude", "anthropic":
		if model == "" {
			model = "claude-sonnet-4-5-20250929"
		}
		data, errMarshal := marshal(map[string]any{"model": model, "max_tokens": 1, "messages": []map[string]string{{"role": "user", "content": "hi"}}})
		headers := map[string]string{"Content-Type": "application/json", "Accept": "application/json", "anthropic-version": "2023-06-01"}
		if metadata.hasAPIKey {
			headers["x-api-key"] = "$TOKEN$"
		} else {
			headers["Authorization"] = "Bearer $TOKEN$"
			headers["anthropic-beta"] = "oauth-2025-04-20"
		}
		return modelProbe{kind: "claude", url: "https://api.anthropic.com/v1/messages", headers: headers, data: data}, model, true, errMarshal
	case "gemini", "gemini-cli", "gemini-interactions", "aistudio":
		if model == "" {
			model = "gemini-2.0-flash"
		}
		geminiModel := strings.TrimPrefix(model, "models/")
		data, errMarshal := marshal(map[string]any{
			"contents":         []map[string]any{{"role": "user", "parts": []map[string]string{{"text": "hi"}}}},
			"generationConfig": map[string]int{"maxOutputTokens": 1},
		})
		headers := map[string]string{"Content-Type": "application/json", "Accept": "application/json"}
		if metadata.hasAPIKey || provider == "aistudio" {
			headers["x-goog-api-key"] = "$TOKEN$"
		} else {
			headers["Authorization"] = "Bearer $TOKEN$"
		}
		probeURL := "https://generativelanguage.googleapis.com/v1beta/models/" + url.PathEscape(geminiModel) + ":generateContent"
		return modelProbe{kind: "gemini", url: probeURL, headers: headers, data: data}, model, true, errMarshal
	case "xai", "grok":
		if model == "" {
			model = "grok-4"
		}
		data, errMarshal := marshal(openAIResponsesProbePayload(model, false))
		return modelProbe{kind: "openai", url: "https://api.x.ai/v1/responses", headers: bearerJSONHeaders(false), data: data}, model, true, errMarshal
	default:
		return modelProbe{}, model, false, nil
	}
}

func openAIResponsesProbePayload(model string, streaming bool) map[string]any {
	payload := map[string]any{
		"model":        model,
		"input":        []map[string]any{{"role": "user", "content": []map[string]string{{"type": "input_text", "text": "hi"}}}},
		"instructions": "Reply with OK only.",
		"stream":       streaming,
	}
	if streaming {
		payload["store"] = false
	} else {
		payload["max_output_tokens"] = 16
	}
	return payload
}

func bearerJSONHeaders(streaming bool) map[string]string {
	accept := "application/json"
	if streaming {
		accept = "text/event-stream"
	}
	return map[string]string{
		"Accept":        accept,
		"Authorization": "Bearer $TOKEN$",
		"Content-Type":  "application/json",
	}
}

func (s *ModelTestService) callManagementAPI(ctx context.Context, managementBaseURL, managementKey, authIndex string, probe modelProbe) (int, []byte, error) {
	baseURL, errBaseURL := validateManagementBaseURL(managementBaseURL)
	if errBaseURL != nil {
		return 0, nil, errBaseURL
	}
	managementKey = strings.TrimSpace(managementKey)
	if managementKey == "" {
		return 0, nil, fmt.Errorf("management key is unavailable")
	}
	payload, errMarshal := json.Marshal(managementAPICallRequest{
		AuthIndex: authIndex,
		Method:    firstNonEmpty(probe.method, http.MethodPost),
		URL:       probe.url,
		Header:    probe.headers,
		Data:      probe.data,
	})
	if errMarshal != nil {
		return 0, nil, fmt.Errorf("encode management model-test request: %w", errMarshal)
	}
	request, errRequest := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/v0/management/api-call", bytes.NewReader(payload))
	if errRequest != nil {
		return 0, nil, fmt.Errorf("create management model-test request: %w", errRequest)
	}
	request.Header.Set("Authorization", "Bearer "+managementKey)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	managementKey = ""
	doer := s.doer
	if doer == nil {
		doer = &http.Client{Timeout: modelTestTimeout + 2*time.Second}
	}
	response, errDo := doer.Do(request)
	if errDo != nil {
		return 0, nil, fmt.Errorf("management model-test request failed: %w", errDo)
	}
	defer func() { _ = response.Body.Close() }()
	outerBody, errRead := io.ReadAll(io.LimitReader(response.Body, maxModelTestResponseBytes+1))
	if errRead != nil {
		return 0, nil, fmt.Errorf("read management model-test response: %w", errRead)
	}
	if len(outerBody) > maxModelTestResponseBytes {
		return 0, nil, fmt.Errorf("management model-test response exceeded the size limit")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return 0, nil, fmt.Errorf("management model-test request returned HTTP %d", response.StatusCode)
	}
	var decoded managementAPICallResponse
	if errDecode := json.Unmarshal(outerBody, &decoded); errDecode != nil {
		return 0, nil, fmt.Errorf("decode management model-test response: %w", errDecode)
	}
	if len(decoded.Body) > maxModelTestBodyBytes {
		return 0, nil, fmt.Errorf("upstream model-test response exceeded the size limit")
	}
	return decoded.StatusCode, []byte(string(decoded.Body)), nil
}

func classifyCredentialProbe(statusCode int, body []byte) (string, string) {
	status, reason, _ := classifyCredentialProbeDetails(statusCode, body)
	return status, reason
}

func classifyCredentialProbeDetails(statusCode int, body []byte) (string, string, string) {
	lower := bytes.ToLower(bytes.TrimSpace(body))
	switch statusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		if bytes.Contains(lower, []byte("account_deactivated")) {
			return "unavailable", "account_deactivated", ""
		}
		return "unavailable", "authentication_failed", ""
	case http.StatusPaymentRequired:
		if bytes.Contains(lower, []byte("deactivated_workspace")) {
			return "unavailable", "workspace_deactivated", ""
		}
		if bytes.Contains(lower, []byte("account_deactivated")) {
			return "unavailable", "account_deactivated", ""
		}
		return "review", "quota_limited", InspectionQuotaWindowMultiple
	case http.StatusTooManyRequests:
		return "review", "quota_limited", InspectionQuotaWindowMultiple
	default:
		if statusCode >= http.StatusOK && statusCode < http.StatusMultipleChoices {
			valid, limited, quotaWindow := codexUsageProbeState(body)
			if !valid {
				return "review", "invalid_response", ""
			}
			if limited {
				return "review", "quota_limited", quotaWindow
			}
			return "available", "credential_response_ok", ""
		}
		if statusCode == http.StatusRequestTimeout || statusCode == http.StatusGatewayTimeout {
			return "review", "request_timeout", ""
		}
		if statusCode >= http.StatusInternalServerError {
			return "review", "upstream_unavailable", ""
		}
		return "review", "invalid_response", ""
	}
}

func codexUsageProbeQuotaLimited(body []byte) bool {
	_, limited, _ := codexUsageProbeState(body)
	return limited
}

func codexUsageProbeState(body []byte) (bool, bool, string) {
	var payload codexUsageProbeEnvelope
	if errDecode := json.Unmarshal(bytes.TrimSpace(body), &payload); errDecode != nil {
		return false, false, ""
	}
	limit := payload.RateLimit
	if limit == nil {
		limit = payload.RateLimitCamel
	}
	if limit == nil {
		return false, false, ""
	}
	valid := limit.Allowed != nil || limit.LimitReached != nil || limit.LimitReachedCamel != nil
	topLevelLimited := (limit.LimitReached != nil && *limit.LimitReached) ||
		(limit.LimitReachedCamel != nil && *limit.LimitReachedCamel) ||
		(limit.Allowed != nil && !*limit.Allowed)
	type probeWindow struct {
		window   *codexUsageProbeWindow
		fallback string
	}
	fiveHourLimited := false
	longLimited := false
	windowObserved := false
	for _, candidate := range []probeWindow{
		{limit.PrimaryWindow, InspectionQuotaWindowFiveHour},
		{limit.PrimaryWindowCamel, InspectionQuotaWindowFiveHour},
		{limit.SecondaryWindow, InspectionQuotaWindowSevenDay},
		{limit.SecondaryWindowCamel, InspectionQuotaWindowSevenDay},
	} {
		window := candidate.window
		if window == nil {
			continue
		}
		usedPercent := window.UsedPercent
		if usedPercent == nil {
			usedPercent = window.UsedPercentCamel
		}
		if usedPercent == nil {
			continue
		}
		valid = true
		windowObserved = true
		if *usedPercent < 100 {
			continue
		}
		windowKind := candidate.fallback
		seconds := window.LimitWindowSeconds
		if seconds == nil {
			seconds = window.LimitWindowSecondsCamel
		}
		if seconds != nil {
			if *seconds <= 24*60*60 {
				windowKind = InspectionQuotaWindowFiveHour
			} else {
				windowKind = InspectionQuotaWindowSevenDay
			}
		}
		if windowKind == InspectionQuotaWindowFiveHour {
			fiveHourLimited = true
		} else {
			longLimited = true
		}
	}
	if fiveHourLimited && longLimited {
		return valid, true, InspectionQuotaWindowMultiple
	}
	if longLimited {
		return valid, true, InspectionQuotaWindowSevenDay
	}
	if fiveHourLimited {
		return valid, true, InspectionQuotaWindowFiveHour
	}
	// Window percentages are more specific than the aggregate allowed flag.
	// A five-hour cooldown can set allowed=false while the long window remains
	// healthy, which must not disable an otherwise recoverable account.
	if windowObserved {
		return true, false, ""
	}
	if topLevelLimited {
		return true, true, InspectionQuotaWindowMultiple
	}
	return valid, false, ""
}

func codexUsageProbeSnapshot(body []byte, now time.Time) *CodexUsageSnapshot {
	var payload codexUsageProbeEnvelope
	if errDecode := json.Unmarshal(bytes.TrimSpace(body), &payload); errDecode != nil {
		return nil
	}
	limit := payload.RateLimit
	if limit == nil {
		limit = payload.RateLimitCamel
	}
	if limit == nil {
		return nil
	}
	snapshot := &CodexUsageSnapshot{ObservedAt: now.UTC()}
	for _, candidate := range []struct {
		window   *codexUsageProbeWindow
		fallback string
	}{
		{limit.PrimaryWindow, InspectionQuotaWindowFiveHour},
		{limit.PrimaryWindowCamel, InspectionQuotaWindowFiveHour},
		{limit.SecondaryWindow, InspectionQuotaWindowSevenDay},
		{limit.SecondaryWindowCamel, InspectionQuotaWindowSevenDay},
	} {
		window, kind := usageWindowFromCredentialProbe(candidate.window, candidate.fallback, now)
		if window == nil {
			continue
		}
		if kind == InspectionQuotaWindowFiveHour {
			snapshot.FiveHour = window
		} else {
			snapshot.SevenDay = window
		}
	}
	if snapshot.FiveHour == nil && snapshot.SevenDay == nil {
		return nil
	}
	return snapshot
}

func usageWindowFromCredentialProbe(raw *codexUsageProbeWindow, fallback string, now time.Time) (*UsageWindowSnapshot, string) {
	if raw == nil {
		return nil, ""
	}
	usedPercent := raw.UsedPercent
	if usedPercent == nil {
		usedPercent = raw.UsedPercentCamel
	}
	if usedPercent == nil || math.IsNaN(*usedPercent) || math.IsInf(*usedPercent, 0) || *usedPercent < 0 || *usedPercent > 10_000 {
		return nil, ""
	}
	window := &UsageWindowSnapshot{UsedPercent: *usedPercent}
	kind := fallback
	seconds := raw.LimitWindowSeconds
	if seconds == nil {
		seconds = raw.LimitWindowSecondsCamel
	}
	if seconds != nil && !math.IsNaN(*seconds) && !math.IsInf(*seconds, 0) && *seconds > 0 && *seconds <= maxUsageWindowMinutes*60 {
		window.WindowMinutes = int(math.Round(*seconds / 60))
		if *seconds <= 24*60*60 {
			kind = InspectionQuotaWindowFiveHour
		} else {
			kind = InspectionQuotaWindowSevenDay
		}
	}
	resetAfter := raw.ResetAfterSeconds
	if resetAfter == nil {
		resetAfter = raw.ResetAfterSecondsCamel
	}
	if resetAfter != nil && !math.IsNaN(*resetAfter) && !math.IsInf(*resetAfter, 0) && *resetAfter >= 0 && *resetAfter <= maxUsageResetAfter.Seconds() {
		resetAt := now.Add(time.Duration(*resetAfter * float64(time.Second))).UTC()
		window.ResetAt = &resetAt
	} else {
		resetAtSeconds := raw.ResetAt
		if resetAtSeconds == nil {
			resetAtSeconds = raw.ResetAtCamel
		}
		if resetAtSeconds != nil && !math.IsNaN(*resetAtSeconds) && !math.IsInf(*resetAtSeconds, 0) {
			resetAt := time.Unix(int64(*resetAtSeconds), 0).UTC()
			if !resetAt.Before(now.Add(-time.Minute)) && !resetAt.After(now.Add(maxUsageResetAfter)) {
				window.ResetAt = &resetAt
			}
		}
	}
	return window, kind
}

func credentialProbeResultIsDefinitive(reason string) bool {
	switch safeModelProbeReason(reason) {
	case "authentication_failed", "workspace_deactivated", "account_deactivated", "quota_limited":
		return true
	default:
		return false
	}
}

func classifyModelProbe(kind string, statusCode int, body []byte) (string, string) {
	if statusCode >= http.StatusOK && statusCode < http.StatusMultipleChoices {
		if validModelProbeBody(kind, body) {
			return "available", "model_response_ok"
		}
		if bodyIndicatesMissingModel(body) {
			return "unavailable", "model_not_found"
		}
		return "review", "invalid_response"
	}
	switch statusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return "unavailable", "authentication_failed"
	case http.StatusTooManyRequests:
		return "review", "quota_limited"
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		return "review", "request_timeout"
	case http.StatusBadRequest, http.StatusNotFound:
		if bodyIndicatesMissingModel(body) {
			return "unavailable", "model_not_found"
		}
		return "review", "invalid_response"
	default:
		if statusCode >= http.StatusInternalServerError {
			return "review", "upstream_unavailable"
		}
		return "review", "invalid_response"
	}
}

func validModelProbeBody(kind string, body []byte) bool {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || len(trimmed) > maxModelTestBodyBytes {
		return false
	}
	lower := bytes.ToLower(trimmed)
	if bytes.Contains(lower, []byte(`"type":"error"`)) || bytes.Contains(lower, []byte(`"type": "error"`)) ||
		bytes.Contains(lower, []byte(`"response.failed"`)) {
		return false
	}
	if kind == "codex" {
		return bytes.Contains(lower, []byte(`"response.completed"`)) || bytes.Contains(lower, []byte(`"response.output_item.done"`))
	}
	var decoded map[string]any
	if errDecode := json.Unmarshal(trimmed, &decoded); errDecode != nil {
		return false
	}
	if _, hasError := decoded["error"]; hasError {
		return false
	}
	switch kind {
	case "claude":
		return strings.TrimSpace(modelTestStringValue(decoded, "id")) != "" && strings.EqualFold(modelTestStringValue(decoded, "type"), "message")
	case "gemini":
		candidates, ok := decoded["candidates"].([]any)
		return ok && len(candidates) > 0
	default:
		id := strings.TrimSpace(modelTestStringValue(decoded, "id"))
		object := strings.ToLower(strings.TrimSpace(modelTestStringValue(decoded, "object")))
		return id != "" && (object == "response" || strings.Contains(object, "completion"))
	}
}

func bodyIndicatesMissingModel(body []byte) bool {
	lower := strings.ToLower(string(bytes.TrimSpace(body)))
	if !strings.Contains(lower, "model") {
		return false
	}
	for _, marker := range []string{"not found", "does not exist", "unsupported", "unknown model", "invalid model", "not available"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func safeModelIdentifier(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len([]rune(value)) > maxModelIdentifierLength || strings.Contains(value, "://") {
		return ""
	}
	for _, character := range value {
		if unicode.IsLetter(character) || unicode.IsDigit(character) || strings.ContainsRune("-._:/@", character) {
			continue
		}
		return ""
	}
	return value
}

func modelTestStringValue(values map[string]any, key string) string {
	value, ok := values[key].(string)
	if !ok {
		return ""
	}
	return value
}

func accountTypeUsesAPIKey(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "api_key", "api-key", "apikey":
		return true
	default:
		return false
	}
}

func (s *ModelTestService) currentTime() time.Time {
	now := time.Now
	if s != nil && s.now != nil {
		now = s.now
	}
	return now().UTC()
}

func maxInt64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}
