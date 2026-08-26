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
	quotaRecoveryEvidenceTTL = 2 * time.Hour
)

type inspectionEvidence struct {
	ReasonCode          string
	Confidence          string
	StatusCode          int
	AutoDisableEligible bool
	RecoverAfter        time.Time
	QuotaWindow         string
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
	SignalSource        string
	QuotaWindow         string
}

func classifyUsageFailure(record cpaapi.UsageRecord, now time.Time) inspectionEvidence {
	status := boundedHTTPStatus(record.Failure.StatusCode)
	text := normalizedFailureText(record.Failure.Body)
	shouldRetry := safeShouldRetry(record.ResponseHeaders)

	if usageFailureHasQuotaEvidence(record, text) {
		recoverAfter, quotaWindow := quotaRecoveryFromHeaders(record.ResponseHeaders, normalizeInspectionProvider(record.Provider), now)
		return inspectionEvidence{
			ReasonCode:          "quota_exhausted",
			Confidence:          InspectionConfidenceHigh,
			StatusCode:          status,
			AutoDisableEligible: true,
			RecoverAfter:        recoverAfter,
			QuotaWindow:         quotaWindow,
		}
	}
	if status == http.StatusTooManyRequests {
		return inspectionEvidence{ReasonCode: "transient_failure", Confidence: InspectionConfidenceLow, StatusCode: status}
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
		return inspectionEvidence{ReasonCode: "unconfirmed_upstream_response", Confidence: InspectionConfidenceLow, StatusCode: status}
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

func usageFailureHasQuotaEvidence(record cpaapi.UsageRecord, normalizedText string) bool {
	if containsInspectionText(normalizedText, "usage_limit_reached", "usage limit has been reached", "quota exhausted", "weekly limit reached") {
		return true
	}
	for _, header := range []string{"X-Codex-Primary-Used-Percent", "X-Codex-Secondary-Used-Percent"} {
		if value := parseUsagePercent(record.ResponseHeaders.Get(header)); value != nil && *value >= 100 {
			return true
		}
	}
	return false
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

func quotaRecoveryFromHeaders(headers http.Header, provider string, now time.Time) (time.Time, string) {
	primary := rawCodexWindow{
		usedPercent:   parseUsagePercent(headers.Get("x-codex-primary-used-percent")),
		resetAfter:    parseResetAfter(headers.Get("x-codex-primary-reset-after-seconds")),
		resetAt:       parseResetAt(headers.Get("x-codex-primary-reset-at"), now),
		windowMinutes: parseWindowMinutes(headers.Get("x-codex-primary-window-minutes")),
	}
	secondary := rawCodexWindow{
		usedPercent:   parseUsagePercent(headers.Get("x-codex-secondary-used-percent")),
		resetAfter:    parseResetAfter(headers.Get("x-codex-secondary-reset-after-seconds")),
		resetAt:       parseResetAt(headers.Get("x-codex-secondary-reset-at"), now),
		windowMinutes: parseWindowMinutes(headers.Get("x-codex-secondary-window-minutes")),
	}
	primaryReset := codexWindowResetAt(primary, now)
	secondaryReset := codexWindowResetAt(secondary, now)
	primaryFull := primary.usedPercent != nil && *primary.usedPercent >= 100
	secondaryFull := secondary.usedPercent != nil && *secondary.usedPercent >= 100

	switch {
	case primaryFull && secondaryFull:
		return laterInspectionReset(primaryReset, secondaryReset), InspectionQuotaWindowMultiple
	case primaryFull:
		return primaryReset, codexWindowKind(primary, InspectionQuotaWindowFiveHour)
	case secondaryFull:
		return secondaryReset, codexWindowKind(secondary, InspectionQuotaWindowSevenDay)
	}
	if !primaryReset.IsZero() {
		return primaryReset, codexWindowKind(primary, InspectionQuotaWindowFiveHour)
	}
	if !secondaryReset.IsZero() {
		return secondaryReset, codexWindowKind(secondary, InspectionQuotaWindowSevenDay)
	}
	if provider == "codex" {
		return now.Add(5 * time.Hour).UTC(), InspectionQuotaWindowFiveHourFallback
	}
	return time.Time{}, ""
}

func codexWindowResetAt(window rawCodexWindow, now time.Time) time.Time {
	if window.resetAfter != nil {
		return now.Add(*window.resetAfter).UTC()
	}
	if window.resetAt != nil {
		return window.resetAt.UTC()
	}
	return time.Time{}
}

func codexWindowKind(window rawCodexWindow, fallback string) string {
	if window.windowMinutes == nil {
		return fallback
	}
	if *window.windowMinutes <= 360 {
		return InspectionQuotaWindowFiveHour
	}
	return InspectionQuotaWindowSevenDay
}

func laterInspectionReset(left, right time.Time) time.Time {
	if right.After(left) {
		return right
	}
	return left
}

func applyUsageRecordToInspection(record *inspectionRecord, usage cpaapi.UsageRecord, policy InspectionPolicy, now time.Time) {
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
	window := time.Duration(normalizeInspectionPolicy(policy).PassiveFailureWindowMinutes) * time.Minute
	if record.Signal.LastFailureAt.IsZero() || now.Before(record.Signal.LastFailureAt) || now.Sub(record.Signal.LastFailureAt) > window ||
		record.Signal.ReasonCode != evidence.ReasonCode {
		record.Signal.ConsecutiveFailures = 1
	} else {
		record.Signal.ConsecutiveFailures = boundedCounter(record.Signal.ConsecutiveFailures + 1)
	}
	record.Signal.ConsecutiveSuccess = 0
	record.Signal.LastFailureAt = now
	record.Signal.StatusCode = evidence.StatusCode
	record.Signal.ReasonCode = evidence.ReasonCode
	record.Signal.Confidence = evidence.Confidence
	record.Signal.AutoDisableEligible = evidence.AutoDisableEligible
	record.Signal.RecoverAfter = evidence.RecoverAfter
	record.Signal.QuotaWindow = evidence.QuotaWindow
}

func decideInspection(account Account, record inspectionRecord, now time.Time) inspectionDecision {
	record = inspectionRecordForAccountIdentity(record, account)
	now = now.UTC()
	probeDecision, hasProbeDecision := decisionFromModelProbe(record.Probe, now)
	// Definitive credential and account-lifecycle failures remain the highest
	// priority. Ordinary model success or inconclusive probe output must not
	// hide a current CPA quota window, because routing can keep failing until
	// that window resets even when one isolated model probe succeeds.
	if hasProbeDecision && inspectionProbeDecisionOutranksQuota(probeDecision) {
		return probeDecision
	}
	if quotaWindow, recovered := accountQuotaRecoveredAfterDisable(account, record, now); recovered {
		return inspectionDecision{
			Health:         InspectionHealthHealthy,
			ReasonCode:     "quota_reset",
			Confidence:     InspectionConfidenceHigh,
			Recommendation: InspectionRecommendationEnable,
			HealthyCount:   1,
			SignalSource:   InspectionSignalNative,
			QuotaWindow:    quotaWindow,
		}
	}
	if limited, recoverAfter, quotaWindow := accountQuotaLimited(account, now); limited {
		return inspectionDecision{
			Health:              InspectionHealthQuotaLimited,
			ReasonCode:          "quota_exhausted",
			Confidence:          InspectionConfidenceHigh,
			Recommendation:      InspectionRecommendationDisable,
			AutoDisableEligible: true,
			RecoverAfter:        recoverAfter,
			SignalSource:        InspectionSignalNative,
			QuotaWindow:         quotaWindow,
		}
	}
	if hasProbeDecision {
		return probeDecision
	}
	if account.Disabled && !record.Result.OwnedDisable {
		return inspectionDecision{
			Health:         InspectionHealthDisabled,
			ReasonCode:     "manual_disabled",
			Confidence:     InspectionConfidenceHigh,
			Recommendation: InspectionRecommendationKeep,
			SignalSource:   InspectionSignalNative,
		}
	}
	if activeInspectionSignal(record.Signal, now) {
		return decisionFromSignal(record.Signal)
	}
	nativeStatusSupported := !isAgentIdentityProvider(account.Provider)
	status := strings.ToLower(strings.TrimSpace(account.StatusMessage))
	if status == "" {
		status = strings.ToLower(strings.TrimSpace(account.Status))
	}
	if nativeStatusSupported {
		switch status {
		case "invalid_grant":
			return inspectionDecision{
				Health:              InspectionHealthInvalidCredentials,
				ReasonCode:          "invalid_credentials",
				Confidence:          InspectionConfidenceHigh,
				Recommendation:      InspectionRecommendationReauth,
				AutoDisableEligible: true,
				SignalSource:        InspectionSignalNative,
			}
		case "unauthorized":
			return inspectionDecision{
				Health:         InspectionHealthReview,
				ReasonCode:     "authentication_review",
				Confidence:     InspectionConfidenceMedium,
				Recommendation: InspectionRecommendationReview,
				SignalSource:   InspectionSignalNative,
			}
		case "payment_required":
			return inspectionDecision{
				Health:         InspectionHealthReview,
				ReasonCode:     "billing_review",
				Confidence:     InspectionConfidenceMedium,
				Recommendation: InspectionRecommendationReview,
				SignalSource:   InspectionSignalNative,
			}
		case "quota exhausted":
			return inspectionDecision{
				Health:              InspectionHealthQuotaLimited,
				ReasonCode:          "quota_exhausted",
				Confidence:          InspectionConfidenceHigh,
				Recommendation:      InspectionRecommendationDisable,
				AutoDisableEligible: true,
				SignalSource:        InspectionSignalNative,
			}
		}
	}
	if nativeStatusSupported && (account.Unavailable || status == "transient upstream error" || status == "upstream temporarily unavailable" || status == "cloudflare challenge") {
		return inspectionDecision{
			Health:              InspectionHealthUnavailable,
			ReasonCode:          "native_unavailable",
			Confidence:          InspectionConfidenceMedium,
			Recommendation:      InspectionRecommendationDisable,
			AutoDisableEligible: true,
			SignalSource:        InspectionSignalNative,
		}
	}
	if !record.Signal.LastSuccessAt.IsZero() && record.Signal.LastSuccessAt.After(record.Signal.LastFailureAt) {
		return inspectionDecision{
			Health:         InspectionHealthHealthy,
			ReasonCode:     "healthy_recent_success",
			Confidence:     InspectionConfidenceHigh,
			Recommendation: InspectionRecommendationKeep,
			HealthyCount:   boundedCounter(record.Signal.ConsecutiveSuccess),
			SignalSource:   InspectionSignalPassive,
		}
	}
	if recentAccountSuccess(account) || (!nativeStatusSupported && observedAccountSuccess(account)) || strings.EqualFold(strings.TrimSpace(account.Status), "ready") || strings.EqualFold(strings.TrimSpace(account.Status), "active") {
		return inspectionDecision{
			Health:         InspectionHealthHealthy,
			ReasonCode:     "healthy_recent_success",
			Confidence:     InspectionConfidenceHigh,
			Recommendation: InspectionRecommendationKeep,
			SignalSource:   InspectionSignalNative,
		}
	}
	return inspectionDecision{
		Health:         InspectionHealthUnknown,
		ReasonCode:     "no_recent_evidence",
		Confidence:     InspectionConfidenceLow,
		Recommendation: InspectionRecommendationReview,
		SignalSource:   InspectionSignalNative,
	}
}

func accountQuotaRecoveredAfterDisable(account Account, record inspectionRecord, now time.Time) (string, bool) {
	if !account.Disabled || !account.Editable || !record.Result.OwnedDisable || record.DisableReason != "quota_exhausted" ||
		account.Usage == nil || account.Usage.Codex == nil || !isCodexInspectionProvider(account.Provider) {
		return "", false
	}
	snapshot := account.Usage.Codex
	if !freshQuotaRecoverySnapshot(snapshot, record.DisabledAt, now) || quotaRecoveryBlockedByFailure(account, record, now) {
		return "", false
	}
	quotaWindow := quotaRecoveryWindow(record)
	return quotaWindow, codexQuotaWindowRecovered(snapshot, quotaWindow)
}

func freshQuotaRecoverySnapshot(snapshot *CodexUsageSnapshot, disabledAt, now time.Time) bool {
	if snapshot == nil || snapshot.ObservedAt.IsZero() || disabledAt.IsZero() {
		return false
	}
	observedAt := snapshot.ObservedAt.UTC()
	now = now.UTC()
	return observedAt.After(disabledAt.UTC()) && !observedAt.After(now) && now.Sub(observedAt) <= quotaRecoveryEvidenceTTL
}

func quotaRecoveryWindow(record inspectionRecord) string {
	if value := normalizeInspectionQuotaWindow(record.Result.QuotaWindow); value != "" {
		return value
	}
	return normalizeInspectionQuotaWindow(record.Signal.QuotaWindow)
}

func codexQuotaWindowRecovered(snapshot *CodexUsageSnapshot, quotaWindow string) bool {
	if snapshot == nil {
		return false
	}
	recovered := func(window *UsageWindowSnapshot) bool {
		return window != nil && window.UsedPercent >= 0 && window.UsedPercent < 100
	}
	switch normalizeInspectionQuotaWindow(quotaWindow) {
	case InspectionQuotaWindowFiveHour, InspectionQuotaWindowFiveHourFallback:
		return recovered(snapshot.FiveHour)
	case InspectionQuotaWindowSevenDay:
		return recovered(snapshot.SevenDay)
	case InspectionQuotaWindowMultiple:
		return recovered(snapshot.FiveHour) && recovered(snapshot.SevenDay)
	default:
		if snapshot.FiveHour != nil {
			return recovered(snapshot.FiveHour)
		}
		return recovered(snapshot.SevenDay)
	}
}

func quotaRecoveryBlockedByFailure(account Account, record inspectionRecord, now time.Time) bool {
	status := strings.ToLower(strings.TrimSpace(firstNonEmpty(account.StatusMessage, account.Status)))
	if status == "invalid_grant" || status == "unauthorized" {
		return true
	}
	if decision, ok := decisionFromModelProbe(record.Probe, now); ok && inspectionProbeDecisionOutranksQuota(decision) &&
		record.Probe.TestedAt.After(record.DisabledAt) {
		return true
	}
	if activeInspectionSignal(record.Signal, now) && record.Signal.LastFailureAt.After(record.DisabledAt) {
		decision := decisionFromSignal(record.Signal)
		if decision.Health == InspectionHealthInvalidCredentials || decision.Health == InspectionHealthDeactivated {
			return true
		}
	}
	return record.Result.LastCheckedAt.After(record.DisabledAt) &&
		(record.Result.Health == InspectionHealthInvalidCredentials || record.Result.Health == InspectionHealthDeactivated)
}

func isCodexInspectionProvider(provider string) bool {
	return normalizeInspectionProvider(provider) == "codex" || isAgentIdentityProvider(provider)
}

func inspectionProbeDecisionOutranksQuota(decision inspectionDecision) bool {
	return decision.Health == InspectionHealthInvalidCredentials || decision.Health == InspectionHealthDeactivated
}

func decisionFromModelProbe(probe inspectionProbeSignal, now time.Time) (inspectionDecision, bool) {
	if probe.TestedAt.IsZero() || now.Before(probe.TestedAt) || now.Sub(probe.TestedAt) > modelProbeEvidenceTTL {
		return inspectionDecision{}, false
	}
	switch probe.ReasonCode {
	case "model_response_ok", "credential_response_ok":
		return inspectionDecision{
			Health: InspectionHealthHealthy, ReasonCode: probe.ReasonCode,
			Confidence: InspectionConfidenceHigh, Recommendation: InspectionRecommendationKeep,
			HealthyCount: probe.ConsecutiveSuccess, SignalSource: InspectionSignalActiveProbe,
		}, true
	case "authentication_failed":
		if !inspectionProbeConfirmsCredentialFailure(probe.Kind, probe.ReasonCode, probe.StatusCode) {
			return inspectionDecision{
				Health: InspectionHealthReview, ReasonCode: probe.ReasonCode,
				Confidence: InspectionConfidenceMedium, Recommendation: InspectionRecommendationReview,
				FailureCount: probe.ConsecutiveFailures, SignalSource: InspectionSignalActiveProbe,
			}, true
		}
		return inspectionDecision{
			Health: InspectionHealthInvalidCredentials, ReasonCode: probe.ReasonCode,
			Confidence: InspectionConfidenceHigh, Recommendation: InspectionRecommendationReauth,
			AutoDisableEligible: true, FailureCount: probe.ConsecutiveFailures, SignalSource: InspectionSignalActiveProbe,
		}, true
	case "workspace_deactivated", "account_deactivated":
		return inspectionDecision{
			Health: InspectionHealthDeactivated, ReasonCode: probe.ReasonCode,
			Confidence: InspectionConfidenceHigh, Recommendation: InspectionRecommendationDelete,
			AutoDisableEligible: true, FailureCount: probe.ConsecutiveFailures, SignalSource: InspectionSignalActiveProbe,
		}, true
	case "quota_limited":
		if probe.Kind != InspectionProbeKindCredential {
			return inspectionDecision{
				Health: InspectionHealthReview, ReasonCode: probe.ReasonCode,
				Confidence: InspectionConfidenceLow, Recommendation: InspectionRecommendationReview,
				FailureCount: probe.ConsecutiveFailures, SignalSource: InspectionSignalActiveProbe,
			}, true
		}
		reasonCode := probe.ReasonCode
		if probe.QuotaWindow != "" {
			reasonCode = "quota_exhausted"
		}
		return inspectionDecision{
			Health: InspectionHealthQuotaLimited, ReasonCode: reasonCode,
			Confidence: InspectionConfidenceMedium, Recommendation: InspectionRecommendationDisable,
			AutoDisableEligible: true, FailureCount: probe.ConsecutiveFailures,
			SignalSource: InspectionSignalActiveProbe, QuotaWindow: probe.QuotaWindow,
		}, true
	case "model_not_found":
		return inspectionDecision{
			Health: InspectionHealthReview, ReasonCode: probe.ReasonCode,
			Confidence: InspectionConfidenceLow, Recommendation: InspectionRecommendationReview,
			FailureCount: probe.ConsecutiveFailures, SignalSource: InspectionSignalActiveProbe,
		}, true
	case "request_timeout", "upstream_unavailable", "invalid_response", "transient_failure":
		return inspectionDecision{
			Health: InspectionHealthUnavailable, ReasonCode: probe.ReasonCode,
			Confidence: InspectionConfidenceLow, Recommendation: InspectionRecommendationReview,
			FailureCount: probe.ConsecutiveFailures, SignalSource: InspectionSignalActiveProbe,
		}, true
	default:
		return inspectionDecision{}, false
	}
}

func inspectionProbeConfirmsCredentialFailure(kind, reason string, statusCode int) bool {
	if safeModelProbeReason(reason) != "authentication_failed" {
		return false
	}
	return normalizeInspectionProbeKind(kind) == InspectionProbeKindCredential || statusCode == http.StatusUnauthorized
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
		QuotaWindow:         signal.QuotaWindow,
		FailureCount:        boundedCounter(signal.ConsecutiveFailures),
		SignalSource:        InspectionSignalPassive,
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

func accountQuotaLimited(account Account, now time.Time) (bool, time.Time, string) {
	var recoverAfter time.Time
	limited := false
	quotaWindow := ""
	if !isAgentIdentityProvider(account.Provider) && account.NextRetryAfter != nil && account.NextRetryAfter.After(now) {
		limited = true
		recoverAfter = account.NextRetryAfter.UTC()
	}
	if account.Usage == nil || account.Usage.Codex == nil {
		return limited, recoverAfter, quotaWindow
	}
	for index, window := range []*UsageWindowSnapshot{account.Usage.Codex.FiveHour, account.Usage.Codex.SevenDay} {
		if window == nil || window.UsedPercent < 100 {
			continue
		}
		limited = true
		windowKind := InspectionQuotaWindowFiveHour
		if index == 1 {
			windowKind = InspectionQuotaWindowSevenDay
		}
		if quotaWindow != "" && quotaWindow != windowKind {
			quotaWindow = InspectionQuotaWindowMultiple
		} else {
			quotaWindow = windowKind
		}
		windowRecoverAt := window.ResetAt
		// A live cycle (pending/passed/inconclusive) and a confirmed-failed
		// cycle both freeze the recovery deadline to the cycle RecoverAt,
		// mirroring the sub2api-overdraft pause-until-recover behavior. Only a
		// recovered window falls back to the mutable window reset time.
		if (window.OverdraftActive || window.OverdraftStatus == overdraftStatusFailed) && window.OverdraftRecoverAt != nil {
			windowRecoverAt = window.OverdraftRecoverAt
		}
		if windowRecoverAt != nil && windowRecoverAt.After(recoverAfter) {
			recoverAfter = windowRecoverAt.UTC()
		}
	}
	return limited, recoverAfter, quotaWindow
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
