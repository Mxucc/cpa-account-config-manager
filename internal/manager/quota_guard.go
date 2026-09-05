package manager

import (
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

// FilterSchedulerCandidates removes accounts that have reached a configured CPA quota
// percentage before the host admits the request. Accounts without a usable usage
// snapshot remain eligible so a missing observation never accidentally drains a pool.
func (g *AccountQuotaGuard) FilterSchedulerCandidates(request cpaapi.SchedulerPickRequest) ([]cpaapi.SchedulerAuthCandidate, bool) {
	if g == nil || g.usage == nil || g.policies == nil || len(request.Candidates) == 0 {
		return request.Candidates, false
	}
	filtered := make([]cpaapi.SchedulerAuthCandidate, 0, len(request.Candidates))
	changed := false
	for _, candidate := range request.Candidates {
		if g.schedulerCandidateQuotaLimited(candidate) {
			changed = true
			continue
		}
		filtered = append(filtered, candidate)
	}
	return filtered, changed
}

func (g *AccountQuotaGuard) schedulerCandidateQuotaLimited(candidate cpaapi.SchedulerAuthCandidate) bool {
	identifiers := []string{strings.TrimSpace(candidate.ID)}
	for _, key := range []string{"selected_auth_index", "auth_index", "selected_auth_id", "auth_id", "account_id"} {
		if value, ok := candidate.Metadata[key].(string); ok {
			identifiers = append(identifiers, strings.TrimSpace(value))
		}
	}
	for _, key := range []string{"auth_index", "auth_id", "account_id"} {
		if value := strings.TrimSpace(candidate.Attributes[key]); value != "" {
			identifiers = append(identifiers, value)
		}
	}
	seen := make(map[string]struct{}, len(identifiers))
	for _, identifier := range identifiers {
		if identifier == "" {
			continue
		}
		if _, ok := seen[identifier]; ok {
			continue
		}
		seen[identifier] = struct{}{}
		policy := g.policies.AccountPolicy(identifier)
		if quotaPolicyEmpty(policy) {
			continue
		}
		usage := g.usage.Snapshot(identifier)
		if usage != nil && usage.Codex != nil && accountQuotaLimitReached(policy, usage.Codex) {
			return true
		}
	}
	return false
}

func accountQuotaLimitReached(policy AccountQuotaPolicy, usage *CodexUsageSnapshot) bool {
	if usage == nil {
		return false
	}
	return (policy.FiveHour.LimitPercent != nil && usage.FiveHour != nil && usage.FiveHour.UsedPercent >= float64(*policy.FiveHour.LimitPercent)) ||
		(policy.SevenDay.LimitPercent != nil && usage.SevenDay != nil && usage.SevenDay.UsedPercent >= float64(*policy.SevenDay.LimitPercent))
}

func (g *AccountQuotaGuard) InterceptRequest(cpaapi.RequestInterceptRequest) (cpaapi.RequestInterceptResponse, bool) {
	// Quota exhaustion is handled before scheduler admission. Never emit a plugin
	// 429/503 here: a request-level rejection would stop sub2api scheduling
	// instead of allowing the next eligible account to run.
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
