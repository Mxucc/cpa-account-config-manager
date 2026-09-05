package manager

import (
	"testing"

	"cpa-account-config-manager/internal/cpaapi"
)

func quotaGuardRequest(authIndex, format string) cpaapi.RequestInterceptRequest {
	return cpaapi.RequestInterceptRequest{
		ToFormat: format,
		Metadata: map[string]any{"selected_auth_index": authIndex},
	}
}

func configuredQuotaGuard(t *testing.T, policy AccountQuotaPolicy, usage *UsageTracker) *AccountQuotaGuard {
	t.Helper()
	policies := NewQuotaPolicyService()
	policies.Configure(Config{DataDir: t.TempDir()})
	if err := policies.SetAccountPolicy("auth-a", policy); err != nil {
		t.Fatal(err)
	}
	return NewAccountQuotaGuard(usage, policies)
}

func TestAccountQuotaGuardPassesThroughAtFiveHourLimit(t *testing.T) {
	usage := NewUsageTracker()
	t.Cleanup(func() { usage.Close() })
	used := 80.0
	usage.ObserveCredentialUsage("auth-a", &CodexUsageSnapshot{FiveHour: &UsageWindowSnapshot{UsedPercent: used}})
	limit := 80
	guard := configuredQuotaGuard(t, AccountQuotaPolicy{FiveHour: QuotaWindowPolicy{LimitPercent: &limit}}, usage)
	response, changed := guard.InterceptRequest(quotaGuardRequest("auth-a", "codex"))
	if changed || response.Terminate || response.StatusCode != 0 {
		t.Fatalf("five-hour quota request was rejected = %#v, changed=%v", response, changed)
	}
}

func TestAccountQuotaGuardPassesThroughAtSevenDayLimit(t *testing.T) {
	usage := NewUsageTracker()
	t.Cleanup(func() { usage.Close() })
	used := 100.0
	usage.ObserveCredentialUsage("auth-a", &CodexUsageSnapshot{SevenDay: &UsageWindowSnapshot{UsedPercent: used}})
	limit := 95
	guard := configuredQuotaGuard(t, AccountQuotaPolicy{SevenDay: QuotaWindowPolicy{LimitPercent: &limit}}, usage)
	response, changed := guard.InterceptRequest(quotaGuardRequest("auth-a", "codex"))
	if changed || response.Terminate || response.StatusCode != 0 {
		t.Fatalf("seven-day quota request was rejected = %#v, changed=%v", response, changed)
	}
}

func TestAccountQuotaGuardPassesBelowLimitAndWithoutUsage(t *testing.T) {
	usage := NewUsageTracker()
	t.Cleanup(func() { usage.Close() })
	used := 49.9
	limit := 50
	usage.ObserveCredentialUsage("auth-a", &CodexUsageSnapshot{FiveHour: &UsageWindowSnapshot{UsedPercent: used}})
	guard := configuredQuotaGuard(t, AccountQuotaPolicy{FiveHour: QuotaWindowPolicy{LimitPercent: &limit}}, usage)
	if response, changed := guard.InterceptRequest(quotaGuardRequest("auth-a", "codex")); changed || response.Terminate {
		t.Fatalf("below-limit request rejected = %#v, changed=%v", response, changed)
	}
	if response, changed := guard.InterceptRequest(quotaGuardRequest("missing", "codex")); changed || response.Terminate {
		t.Fatalf("missing-usage request rejected = %#v, changed=%v", response, changed)
	}
}

func TestAccountQuotaGuardUsesFallbackAuthMetadataAndFormatGate(t *testing.T) {
	usage := NewUsageTracker()
	t.Cleanup(func() { usage.Close() })
	used := 100.0
	usage.ObserveCredentialUsage("auth-a", &CodexUsageSnapshot{FiveHour: &UsageWindowSnapshot{UsedPercent: used}})
	limit := 0
	guard := configuredQuotaGuard(t, AccountQuotaPolicy{FiveHour: QuotaWindowPolicy{LimitPercent: &limit}}, usage)
	request := quotaGuardRequest("auth-a", "codex")
	request.Metadata = map[string]any{"auth_id": "auth-a"}
	if response, changed := guard.InterceptRequest(request); changed || response.Terminate || response.StatusCode != 0 {
		t.Fatalf("fallback auth metadata was rejected = %#v, changed=%v", response, changed)
	}
	if response, changed := guard.InterceptRequest(quotaGuardRequest("auth-a", "openai")); changed || response.Terminate {
		t.Fatalf("non-Codex format was rejected = %#v, changed=%v", response, changed)
	}
}

func TestAccountQuotaGuardFiltersLimitedSchedulerCandidates(t *testing.T) {
	usage := NewUsageTracker()
	t.Cleanup(func() { usage.Close() })
	used := 100.0
	usage.ObserveCredentialUsage("auth-limited", &CodexUsageSnapshot{FiveHour: &UsageWindowSnapshot{UsedPercent: used}})
	limit := 90
	policies := NewQuotaPolicyService()
	policies.Configure(Config{DataDir: t.TempDir()})
	if err := policies.SetAccountPolicy("auth-limited", AccountQuotaPolicy{FiveHour: QuotaWindowPolicy{LimitPercent: &limit}}); err != nil {
		t.Fatal(err)
	}
	guard := NewAccountQuotaGuard(usage, policies)
	request := cpaapi.SchedulerPickRequest{Candidates: []cpaapi.SchedulerAuthCandidate{
		{ID: "auth-limited"},
		{ID: "auth-eligible"},
	}}
	filtered, changed := guard.FilterSchedulerCandidates(request)
	if !changed || len(filtered) != 1 || filtered[0].ID != "auth-eligible" {
		t.Fatalf("filtered candidates = %#v, changed=%v", filtered, changed)
	}

	allLimited := request
	allLimited.Candidates = append(allLimited.Candidates, cpaapi.SchedulerAuthCandidate{ID: "auth-limited"})
	allLimited.Candidates[1].ID = "auth-limited"
	filtered, changed = guard.FilterSchedulerCandidates(allLimited)
	if !changed || len(filtered) != 0 {
		t.Fatalf("all limited candidates = %#v, changed=%v", filtered, changed)
	}
}
