package manager

import (
	"encoding/json"
	"net/http"
	"strings"

	"cpa-account-config-manager/internal/cpaapi"
)

// AccountQuotaGuard enforces plugin-owned percentage limits using the official
// Codex window percentages CPA has already reported for the selected account.
// It intentionally does not infer a quota from token counts or mutate CPA auth
// files; when CPA has no usage snapshot the request is passed through.
type AccountQuotaGuard struct {
	usage    *UsageTracker
	policies *QuotaPolicyService
}

func NewAccountQuotaGuard(usage *UsageTracker, policies *QuotaPolicyService) *AccountQuotaGuard {
	return &AccountQuotaGuard{usage: usage, policies: policies}
}

func (g *AccountQuotaGuard) RequestInterceptionActive() bool {
	return g != nil && g.policies != nil && g.policies.HasAccountPolicies()
}

func (g *AccountQuotaGuard) RequestInterceptionAcceptsFormat(format string) bool {
	return g.RequestInterceptionActive() && strings.EqualFold(strings.TrimSpace(format), "codex")
}

func (g *AccountQuotaGuard) InterceptRequest(request cpaapi.RequestInterceptRequest) (cpaapi.RequestInterceptResponse, bool) {
	if g == nil || g.usage == nil || g.policies == nil || !g.RequestInterceptionAcceptsFormat(request.ToFormat) {
		return cpaapi.RequestInterceptResponse{}, false
	}
	authIndex := requestAuthIdentifier(request.Metadata)
	if authIndex == "" {
		return cpaapi.RequestInterceptResponse{}, false
	}
	policy := g.policies.AccountPolicy(authIndex)
	if quotaPolicyEmpty(policy) {
		return cpaapi.RequestInterceptResponse{}, false
	}
	usage := g.usage.Snapshot(authIndex)
	if usage == nil || usage.Codex == nil {
		return cpaapi.RequestInterceptResponse{}, false
	}
	if policy.FiveHour.LimitPercent != nil && usage.Codex.FiveHour != nil && usage.Codex.FiveHour.UsedPercent >= float64(*policy.FiveHour.LimitPercent) {
		return accountQuotaRejectedResponse(authIndex, "five_hour", usage.Codex.FiveHour.UsedPercent, *policy.FiveHour.LimitPercent), true
	}
	if policy.SevenDay.LimitPercent != nil && usage.Codex.SevenDay != nil && usage.Codex.SevenDay.UsedPercent >= float64(*policy.SevenDay.LimitPercent) {
		return accountQuotaRejectedResponse(authIndex, "seven_day", usage.Codex.SevenDay.UsedPercent, *policy.SevenDay.LimitPercent), true
	}
	return cpaapi.RequestInterceptResponse{}, false
}

func requestAuthIdentifier(metadata map[string]any) string {
	for _, key := range []string{"selected_auth_index", "auth_index", "selected_auth_id", "auth_id"} {
		if value, ok := metadata[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func accountQuotaRejectedResponse(authIndex, window string, used float64, limit int) cpaapi.RequestInterceptResponse {
	body, _ := json.Marshal(map[string]any{"error": map[string]any{
		"type":          "account_quota_limit_reached",
		"message":       "the selected account reached its configured quota percentage limit",
		"auth_index":    authIndex,
		"quota_window":  window,
		"used_percent":  used,
		"limit_percent": limit,
	}})
	return cpaapi.RequestInterceptResponse{
		Terminate:       true,
		StatusCode:      http.StatusTooManyRequests,
		ResponseHeaders: http.Header{"Content-Type": {"application/json"}, "Retry-After": {"60"}},
		ResponseBody:    body,
	}
}
