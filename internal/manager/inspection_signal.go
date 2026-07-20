package manager

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

const (
	maxInspectionFailureBody = 64 * 1024
	inspectionEvidenceTTL    = 7 * 24 * time.Hour
	modelProbeEvidenceTTL    = 24 * time.Hour
)

type inspectionEvidence struct {
	ReasonCode          string
	Confidence          string
	StatusCode          int
	AutoDisableEligible bool
	RecoverAfter        time.Time
}

type inspectionDecision struct {
	Health              string
	ReasonCode          string
	Confidence          string
	Recommendation      string
	AutoDisableEligible bool
	RecoverAfter        time.Time
	FailureCount        int
	HealthyCount        int
}

func classifyUsageFailure(record cpaapi.UsageRecord, now time.Time) inspectionEvidence {
	status := boundedHTTPStatus(record.Failure.StatusCode)
	text := normalizedFailureText(record.Failure.Body)
	shouldRetry := safeShouldRetry(record.ResponseHeaders)

	if status == http.StatusTooManyRequests || strings.Contains(text, "usage_limit_reached") || strings.Contains(text, "quota exhausted") {
		return inspectionEvidence{
			ReasonCode:          "quota_exhausted",
			Confidence:          InspectionConfidenceHigh,
			StatusCode:          status,
			AutoDisableEligible: true,
			RecoverAfter:        quotaRecoveryFromHeaders(record.ResponseHeaders, now),
		}
	}
	if status == http.StatusPaymentRequired && strings.Contains(text, "deactivated_workspace") {
		return inspectionEvidence{
			ReasonCode:          "workspace_deactivated",
			Confidence:          InspectionConfidenceHigh,
			StatusCode:          status,
			AutoDisableEligible: true,
		}
	}
	if status != http.StatusUnauthorized && status != http.StatusForbidden {
		if status >= 500 {
			return inspectionEvidence{ReasonCode: "transient_failure", Confidence: InspectionConfidenceLow, StatusCode: status}
		}
		return inspectionEvidence{StatusCode: status}
	}
	if strings.Contains(text, "account_deactivated") {
		return inspectionEvidence{
			ReasonCode:          "account_deactivated",
			Confidence:          InspectionConfidenceHigh,
			StatusCode:          status,
			AutoDisableEligible: true,
		}
	}
	if containsInspectionText(text,
		"token_revoked",
		"token_invalidated",
		"invalidated_oauth_token",
		"invalidated oauth token",
		"oauth token revoked",
		"authentication token has been invalidated",
		"token has been invalidated",
	) {
		return inspectionEvidence{
			ReasonCode:          "token_revoked",
			Confidence:          InspectionConfidenceHigh,
			StatusCode:          status,
			AutoDisableEligible: true,
		}
	}
	if containsInspectionText(text,
		"invalid_token",
		"invalid or expired credentials",
		"provided authentication token is expired",
		"authentication token is expired",
		"token is expired",
		"no auth context",
		"invalid_grant",
		"auth_unavailable",
		"requires reauthorization",
		"requires re-authentication",
	) {
		return inspectionEvidence{
			ReasonCode:          "invalid_credentials",
			Confidence:          InspectionConfidenceHigh,
			StatusCode:          status,
			AutoDisableEligible: true,
		}
	}
	provider := normalizeInspectionProvider(record.Provider)
	if provider == "xai" && strings.Contains(text, "permission_denied") &&
		strings.Contains(text, "access to the chat endpoint is denied") &&
		containsInspectionText(text, "correct credentials", "update the permissions", "contact support") &&
		(shouldRetry == nil || !*shouldRetry) {
		return inspectionEvidence{
			ReasonCode:          "credential_permission_denied",
			Confidence:          InspectionConfidenceHigh,
			StatusCode:          status,
			AutoDisableEligible: true,
		}
	}
	return inspectionEvidence{
		ReasonCode: "authentication_review",
		Confidence: InspectionConfidenceMedium,
		StatusCode: status,
	}
}

func normalizedFailureText(body string) string {
	if len(body) > maxInspectionFailureBody {
		body = body[:maxInspectionFailureBody]
	}
	body = strings.ToLower(strings.TrimSpace(body))
	if body == "" {
		return ""
	}
	var document any
	if json.Unmarshal([]byte(body), &document) == nil {
		parts := make([]string, 0, 6)
		collectFailureStrings(document, &parts, 0)
		if len(parts) > 0 {
			return strings.Join(parts, "\n")
		}
	}
	return body
}

func collectFailureStrings(value any, parts *[]string, depth int) {
	if depth > 4 || len(*parts) >= 16 {
		return
	}
	switch typed := value.(type) {
	case map[string]any:
		for _, key := range []string{"code", "type", "error", "message", "detail", "reason"} {
			child, exists := typed[key]
			if !exists {
				continue
			}
			collectFailureStrings(child, parts, depth+1)
		}
	case []any:
		for _, child := range typed {
			collectFailureStrings(child, parts, depth+1)
		}
	case string:
		value := strings.ToLower(strings.TrimSpace(typed))
		if value != "" && len(value) <= 2_048 {
			*parts = append(*parts, value)
		}
	}
}

func safeShouldRetry(headers http.Header) *bool {
	value := strings.ToLower(strings.TrimSpace(headers.Get("X-Should-Retry")))
	switch value {
	case "true":
		result := true
		return &result
	case "false":
		result := false
		return &result
	default:
		return nil
	}
}

func quotaRecoveryFromHeaders(headers http.Header, now time.Time) time.Time {
	var latest time.Time
	for _, name := range []string{"x-codex-primary-reset-after-seconds", "x-codex-secondary-reset-after-seconds"} {
		reset := parseResetAfter(headers.Get(name))
		if reset == nil {
			continue
		}
		candidate := now.Add(*reset).UTC()
		if candidate.After(latest) {
			latest = candidate
		}
	}
	return latest
}

func applyUsageRecordToInspection(record *inspectionRecord, usage cpaapi.UsageRecord, now time.Time) {
	if record == nil {
		return
	}
	now = now.UTC()
	if !usage.Failed {
		record.Signal.ConsecutiveSuccess = boundedCounter(record.Signal.ConsecutiveSuccess + 1)
		record.Signal.ConsecutiveFailures = 0
		record.Signal.LastSuccessAt = now
		return
	}
	evidence := classifyUsageFailure(usage, now)
	record.Signal.ConsecutiveFailures = boundedCounter(record.Signal.ConsecutiveFailures + 1)
	record.Signal.ConsecutiveSuccess = 0
	record.Signal.LastFailureAt = now
	record.Signal.StatusCode = evidence.StatusCode
	if evidence.ReasonCode != "" {
		record.Signal.ReasonCode = evidence.ReasonCode
		record.Signal.Confidence = evidence.Confidence
		record.Signal.AutoDisableEligible = evidence.AutoDisableEligible
		record.Signal.RecoverAfter = evidence.RecoverAfter
	}
}

func decideInspection(account Account, record inspectionRecord, now time.Time) inspectionDecision {
	now = now.UTC()
	if limited, recoverAfter := accountQuotaLimited(account, now); limited {
		return inspectionDecision{
			Health:              InspectionHealthQuotaLimited,
			ReasonCode:          "quota_exhausted",
			Confidence:          InspectionConfidenceHigh,
			Recommendation:      InspectionRecommendationDisable,
			AutoDisableEligible: true,
			RecoverAfter:        recoverAfter,
		}
	}
	if account.Disabled && !record.Result.OwnedDisable {
		return inspectionDecision{
			Health:         InspectionHealthDisabled,
			ReasonCode:     "manual_disabled",
			Confidence:     InspectionConfidenceHigh,
			Recommendation: InspectionRecommendationKeep,
		}
	}
	if decision, ok := decisionFromModelProbe(record.Probe, now); ok {
		return decision
	}
	if activeInspectionSignal(record.Signal, now) {
		return decisionFromSignal(record.Signal)
	}
	status := strings.ToLower(strings.TrimSpace(account.StatusMessage))
	if status == "" {
		status = strings.ToLower(strings.TrimSpace(account.Status))
	}
	switch status {
	case "invalid_grant":
		return inspectionDecision{
			Health:              InspectionHealthInvalidCredentials,
			ReasonCode:          "invalid_credentials",
			Confidence:          InspectionConfidenceHigh,
			Recommendation:      InspectionRecommendationReauth,
			AutoDisableEligible: true,
		}
	case "unauthorized":
		return inspectionDecision{
			Health:         InspectionHealthReview,
			ReasonCode:     "authentication_review",
			Confidence:     InspectionConfidenceMedium,
			Recommendation: InspectionRecommendationReview,
		}
	case "payment_required":
		return inspectionDecision{
			Health:         InspectionHealthReview,
			ReasonCode:     "billing_review",
			Confidence:     InspectionConfidenceMedium,
			Recommendation: InspectionRecommendationReview,
		}
	case "quota exhausted":
		return inspectionDecision{
			Health:              InspectionHealthQuotaLimited,
			ReasonCode:          "quota_exhausted",
			Confidence:          InspectionConfidenceHigh,
			Recommendation:      InspectionRecommendationDisable,
			AutoDisableEligible: true,
		}
	}
	if account.Unavailable || status == "transient upstream error" || status == "upstream temporarily unavailable" || status == "cloudflare challenge" {
		return inspectionDecision{
			Health:         InspectionHealthUnavailable,
			ReasonCode:     "native_unavailable",
			Confidence:     InspectionConfidenceMedium,
			Recommendation: InspectionRecommendationReview,
		}
	}
	if !record.Signal.LastSuccessAt.IsZero() && record.Signal.LastSuccessAt.After(record.Signal.LastFailureAt) {
		return inspectionDecision{
			Health:         InspectionHealthHealthy,
			ReasonCode:     "healthy_recent_success",
			Confidence:     InspectionConfidenceHigh,
			Recommendation: InspectionRecommendationKeep,
			HealthyCount:   boundedCounter(record.Signal.ConsecutiveSuccess),
		}
	}
	if recentAccountSuccess(account) || strings.EqualFold(strings.TrimSpace(account.Status), "ready") || strings.EqualFold(strings.TrimSpace(account.Status), "active") {
		return inspectionDecision{
			Health:         InspectionHealthHealthy,
			ReasonCode:     "healthy_recent_success",
			Confidence:     InspectionConfidenceHigh,
			Recommendation: InspectionRecommendationKeep,
		}
	}
	return inspectionDecision{
		Health:         InspectionHealthUnknown,
		ReasonCode:     "no_recent_evidence",
		Confidence:     InspectionConfidenceLow,
		Recommendation: InspectionRecommendationReview,
	}
}

func decisionFromModelProbe(probe inspectionProbeSignal, now time.Time) (inspectionDecision, bool) {
	if probe.TestedAt.IsZero() || now.Before(probe.TestedAt) || now.Sub(probe.TestedAt) > modelProbeEvidenceTTL {
		return inspectionDecision{}, false
	}
	switch probe.ReasonCode {
	case "model_response_ok":
		return inspectionDecision{
			Health: InspectionHealthHealthy, ReasonCode: probe.ReasonCode,
			Confidence: InspectionConfidenceHigh, Recommendation: InspectionRecommendationKeep,
		}, true
	case "authentication_failed":
		return inspectionDecision{
			Health: InspectionHealthInvalidCredentials, ReasonCode: probe.ReasonCode,
			Confidence: InspectionConfidenceHigh, Recommendation: InspectionRecommendationReauth,
			AutoDisableEligible: true,
		}, true
	case "quota_limited":
		return inspectionDecision{
			Health: InspectionHealthReview, ReasonCode: probe.ReasonCode,
			Confidence: InspectionConfidenceMedium, Recommendation: InspectionRecommendationReview, FailureCount: 1,
		}, true
	case "model_not_found", "request_timeout", "upstream_unavailable", "invalid_response":
		return inspectionDecision{
			Health: InspectionHealthReview, ReasonCode: probe.ReasonCode,
			Confidence: InspectionConfidenceLow, Recommendation: InspectionRecommendationReview, FailureCount: 1,
		}, true
	default:
		return inspectionDecision{}, false
	}
}

func activeInspectionSignal(signal inspectionSignal, now time.Time) bool {
	if signal.ReasonCode == "" || signal.LastFailureAt.IsZero() || signal.LastSuccessAt.After(signal.LastFailureAt) {
		return false
	}
	if now.Sub(signal.LastFailureAt) > inspectionEvidenceTTL {
		return false
	}
	if signal.ReasonCode == "quota_exhausted" && !signal.RecoverAfter.IsZero() && !signal.RecoverAfter.After(now) {
		return false
	}
	return true
}

func decisionFromSignal(signal inspectionSignal) inspectionDecision {
	decision := inspectionDecision{
		ReasonCode:          safeInspectionReason(signal.ReasonCode),
		Confidence:          normalizeInspectionConfidence(signal.Confidence),
		AutoDisableEligible: signal.AutoDisableEligible,
		RecoverAfter:        signal.RecoverAfter,
		FailureCount:        boundedCounter(signal.ConsecutiveFailures),
	}
	switch decision.ReasonCode {
	case "account_deactivated", "workspace_deactivated":
		decision.Health = InspectionHealthDeactivated
		decision.Recommendation = InspectionRecommendationDelete
	case "token_revoked", "invalid_credentials":
		decision.Health = InspectionHealthInvalidCredentials
		decision.Recommendation = InspectionRecommendationReauth
	case "quota_exhausted":
		decision.Health = InspectionHealthQuotaLimited
		decision.Recommendation = InspectionRecommendationDisable
	case "credential_permission_denied", "authentication_review":
		decision.Health = InspectionHealthReview
		decision.Recommendation = InspectionRecommendationReview
		if decision.ReasonCode == "authentication_review" {
			decision.AutoDisableEligible = false
		}
	case "transient_failure":
		decision.Health = InspectionHealthUnavailable
		decision.Recommendation = InspectionRecommendationReview
		decision.AutoDisableEligible = false
	default:
		decision.Health = InspectionHealthUnknown
		decision.Recommendation = InspectionRecommendationReview
		decision.AutoDisableEligible = false
	}
	return decision
}

func accountQuotaLimited(account Account, now time.Time) (bool, time.Time) {
	var recoverAfter time.Time
	limited := false
	if account.NextRetryAfter != nil && account.NextRetryAfter.After(now) {
		limited = true
		recoverAfter = account.NextRetryAfter.UTC()
	}
	if account.Usage == nil || account.Usage.Codex == nil {
		return limited, recoverAfter
	}
	for _, window := range []*UsageWindowSnapshot{account.Usage.Codex.FiveHour, account.Usage.Codex.SevenDay} {
		if window == nil || window.UsedPercent < 100 {
			continue
		}
		limited = true
		if window.ResetAt != nil && window.ResetAt.After(recoverAfter) {
			recoverAfter = window.ResetAt.UTC()
		}
	}
	return limited, recoverAfter
}

func recentAccountSuccess(account Account) bool {
	if account.Success > 0 && account.Failed == 0 {
		return true
	}
	for _, item := range account.RecentRequests {
		if item.Success > 0 && item.Failed == 0 {
			return true
		}
	}
	return false
}

func containsInspectionText(text string, values ...string) bool {
	for _, value := range values {
		if strings.Contains(text, value) {
			return true
		}
	}
	return false
}

func normalizeInspectionProvider(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.ReplaceAll(normalized, "-", "_")
	switch normalized {
	case "x_ai", "grok":
		return "xai"
	default:
		return normalized
	}
}
