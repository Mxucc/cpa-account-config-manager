package manager

import (
	"encoding/json"
	"net/http"
	"strings"
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

func TestAccountQuotaGuardRejectsFiveHourLimit(t *testing.T) {
	usage := NewUsageTracker()
	t.Cleanup(func() { usage.Close() })
	used := 80.0
	usage.ObserveCredentialUsage("auth-a", &CodexUsageSnapshot{FiveHour: &UsageWindowSnapshot{UsedPercent: used}})
	limit := 80
	guard := configuredQuotaGuard(t, AccountQuotaPolicy{FiveHour: QuotaWindowPolicy{LimitPercent: &limit}}, usage)
	response, changed := guard.InterceptRequest(quotaGuardRequest("auth-a", "codex"))
	if !changed || !response.Terminate || response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("five-hour rejection = %#v, changed=%v", response, changed)
	}
	if response.ResponseHeaders.Get("Retry-After") != "60" || !json.Valid(response.ResponseBody) || !strings.Contains(string(response.ResponseBody), "five_hour") {
		t.Fatalf("rejection response = %#v body=%s", response.ResponseHeaders, response.ResponseBody)
	}
}

func TestAccountQuotaGuardRejectsSevenDayLimit(t *testing.T) {
	usage := NewUsageTracker()
	t.Cleanup(func() { usage.Close() })
	used := 100.0
	usage.ObserveCredentialUsage("auth-a", &CodexUsageSnapshot{SevenDay: &UsageWindowSnapshot{UsedPercent: used}})
	limit := 95
	guard := configuredQuotaGuard(t, AccountQuotaPolicy{SevenDay: QuotaWindowPolicy{LimitPercent: &limit}}, usage)
	response, changed := guard.InterceptRequest(quotaGuardRequest("auth-a", "codex"))
	if !changed || !response.Terminate || response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("seven-day rejection = %#v, changed=%v", response, changed)
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
	if response, changed := guard.InterceptRequest(request); !changed || !response.Terminate {
		t.Fatalf("fallback auth metadata was not enforced = %#v, changed=%v", response, changed)
	}
	if response, changed := guard.InterceptRequest(quotaGuardRequest("auth-a", "openai")); changed || response.Terminate {
		t.Fatalf("non-Codex format was rejected = %#v, changed=%v", response, changed)
	}
}
