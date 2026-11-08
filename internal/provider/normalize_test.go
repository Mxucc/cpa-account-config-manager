package provider

import (
	"math"
	"testing"
	"time"
)

func TestClampPercentKeepsUnknownAndClampsBounds(t *testing.T) {
	nan := nanFloat()
	inf := math.Inf(1)
	cases := []struct {
		name  string
		input *float64
		want  *float64
	}{
		{name: "nil stays unknown", input: nil, want: nil},
		{name: "nan stays unknown", input: &nan, want: nil},
		{name: "infinity stays unknown", input: &inf, want: nil},
		{name: "negative clamps to zero", input: FloatPtr(-5), want: FloatPtr(0)},
		{name: "above one hundred clamps", input: FloatPtr(140), want: FloatPtr(100)},
		{name: "inside is untouched", input: FloatPtr(42.5), want: FloatPtr(42.5)},
	}
	for _, testCase := range cases {
		got := ClampPercent(testCase.input)
		if testCase.want == nil {
			if got != nil {
				t.Errorf("%s: got %v, want nil", testCase.name, *got)
			}
			continue
		}
		if got == nil || *got != *testCase.want {
			t.Errorf("%s: got %v, want %v", testCase.name, got, *testCase.want)
		}
	}
}

func TestShareOfNeedsBothAMountsAndPositiveLimit(t *testing.T) {
	if got := ShareOf(nil, FloatPtr(10)); got != nil {
		t.Errorf("missing used amount must stay unknown, got %v", *got)
	}
	if got := ShareOf(FloatPtr(3), nil); got != nil {
		t.Errorf("missing limit must stay unknown, got %v", *got)
	}
	if got := ShareOf(FloatPtr(3), FloatPtr(0)); got != nil {
		t.Errorf("zero limit must stay unknown, got %v", *got)
	}
	got := ShareOf(FloatPtr(3), FloatPtr(10))
	if got == nil || *got != 30 {
		t.Fatalf("3 of 10 = %v, want 30", got)
	}
}

func TestWindowKindMappingCoversEveryProviderVocabulary(t *testing.T) {
	cases := map[string]CycleKind{
		"five_hour": CycleRolling, "5h": CycleRolling, "rolling": CycleRolling,
		"seven_day": CycleWeekly, "weekly": CycleWeekly, "7d": CycleWeekly,
		"monthly": CycleMonthly, "30-day": CycleMonthly,
		"yearly": CycleAnnual,
		"weird":  CycleNone, "": CycleNone,
	}
	for source, want := range cases {
		if got := WindowKind(source); got != want {
			t.Errorf("WindowKind(%q) = %q, want %q", source, got, want)
		}
	}
}

func TestStatusOrderingPrefersCredentialProblems(t *testing.T) {
	if got := StatusFromFacts(true, true, false, true); got != StatusCredentialInvalid {
		t.Errorf("credential invalid must outrank quota and disabled, got %q", got)
	}
	if got := StatusFromFacts(true, false, false, false); got != StatusDisabled {
		t.Errorf("disabled = %q", got)
	}
	if got := StatusFromFacts(false, false, false, true); got != StatusQuotaLimited {
		t.Errorf("quota limited = %q", got)
	}
	if got := StatusFromFacts(false, false, true, false); got != StatusRepairing {
		t.Errorf("repairing = %q", got)
	}
	if got := StatusFromFacts(false, false, false, false); got != StatusReady {
		t.Errorf("healthy = %q", got)
	}
}

func TestFromCodexKeepsUnknownWindowsUnknown(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	account := FromCodex(CodexInput{
		AccountID: "acct-1",
		Email:     "a@example.com",
		PlanType:  "plus",
		FiveHour:  &WindowInput{UsedPercent: FloatPtr(40)},
		SevenDay:  &WindowInput{},
	}, now)
	if account.ID != "provider:codex:acct-1" {
		t.Fatalf("id = %q", account.ID)
	}
	if len(account.Quota) != 2 {
		t.Fatalf("windows = %d", len(account.Quota))
	}
	fiveHour := account.Quota[0]
	if fiveHour.UsedPercent == nil || *fiveHour.UsedPercent != 40 || fiveHour.RemainingPercent == nil || *fiveHour.RemainingPercent != 60 {
		t.Fatalf("five hour = %#v", fiveHour)
	}
	sevenDay := account.Quota[1]
	if sevenDay.UsedPercent != nil || sevenDay.RemainingPercent != nil {
		t.Fatalf("an unreported window must stay nil, got %#v", sevenDay)
	}
	if account.Plan.Name != "ChatGPT Plus" || account.Plan.Cycle.Kind != CycleMonthly {
		t.Fatalf("plan = %#v", account.Plan)
	}
}

func TestFromOpenCodeGoSplitsAllowanceWithoutInventingShares(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	account := FromOpenCodeGo(OpenCodeGoInput{
		AccountID:           "go-1",
		MonthlyAllowanceUSD: FloatPtr(10),
		UsedUSD:             FloatPtr(2.5),
		Rolling:             &WindowInput{RemainingPercent: FloatPtr(80)},
		Weekly:              &WindowInput{},
	}, now)
	if account.Billing.Mode != BillingSubscription || account.Billing.Remaining == nil || *account.Billing.Remaining != 7.5 {
		t.Fatalf("billing = %#v", account.Billing)
	}
	rolling := account.Quota[0]
	if rolling.UsedPercent == nil || *rolling.UsedPercent != 20 {
		t.Fatalf("remaining 80 must become 20 used, got %#v", rolling)
	}
	if rolling.Limit == nil || *rolling.Limit != 2 {
		t.Fatalf("rolling limit = %v, want 20%% of $10", rolling.Limit)
	}
	weekly := account.Quota[1]
	if weekly.UsedPercent != nil {
		t.Fatalf("unknown weekly share must stay nil, got %#v", weekly)
	}
}

func TestFromOpenCodeZenIsAWalletWithNoMeter(t *testing.T) {
	account := FromOpenCodeZen(OpenCodeZenInput{AccountID: "zen-1", BalanceUSD: FloatPtr(4.25)}, time.Now())
	if account.Billing.Mode != BillingWallet || account.Billing.Remaining == nil || *account.Billing.Remaining != 4.25 {
		t.Fatalf("billing = %#v", account.Billing)
	}
	if len(account.Quota) != 1 || account.Quota[0].UsedPercent != nil || account.Quota[0].Balance == nil {
		t.Fatalf("wallet window = %#v", account.Quota)
	}
	empty := FromOpenCodeZen(OpenCodeZenInput{AccountID: "zen-2", BalanceUSD: FloatPtr(0)}, time.Now())
	if empty.Status != StatusQuotaLimited {
		t.Fatalf("an empty wallet must be limited, got %q", empty.Status)
	}
}

func TestFromClinePassReportsRepairAndLimitTogether(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	until := now.Add(30 * time.Minute)
	account := FromClinePass(ClinePassInput{
		AccountID:    "cline-1",
		Plan:         "pro",
		Rejected:     true,
		Repairing:    true,
		LimitedUntil: &until,
		BalanceUSD:   FloatPtr(1.5),
		FiveHour:     &WindowInput{UsedPercent: FloatPtr(70)},
	}, now)
	if account.Status != StatusCredentialInvalid {
		t.Fatalf("status = %q, want credential_invalid", account.Status)
	}
	if !account.Credential.Repairing || !account.Credential.Rejected {
		t.Fatalf("credential = %#v", account.Credential)
	}
	if len(account.Metrics) != 1 || account.Metrics[0].Key != "balance" {
		t.Fatalf("metrics = %#v", account.Metrics)
	}
	if account.Plan.Name == "" {
		t.Fatalf("plan = %#v", account.Plan)
	}
}

func TestFromAIProviderHasNoInventedQuotaWindow(t *testing.T) {
	account := FromAIProvider(AIProviderInput{
		AccountID: "chan-1", DisplayName: "openrouter", Models: []string{"m1", "m2"},
	}, time.Now())
	if len(account.Quota) != 0 {
		t.Fatalf("a plain channel must not show a quota window, got %#v", account.Quota)
	}
	if account.Models.Count != 2 {
		t.Fatalf("models = %#v", account.Models)
	}
}

func TestQuotaResponseCarriesWindowsSummaryAndPlan(t *testing.T) {
	reset := time.Date(2026, 11, 8, 18, 0, 0, 0, time.UTC)
	account := Account{
		Plan:    Plan{ID: "go", Name: "OpenCode Go", Tier: "go", Cycle: Cycle{Kind: CycleMonthly}},
		Quota:   []Window{{ID: "rolling", Label: "5 小时", UsedPercent: FloatPtr(40), RemainingPercent: FloatPtr(60), ResetAt: &reset}},
		Metrics: []Metric{{Key: "balance", Label: "余额", Value: 3, Format: "currency", Currency: "USD"}},
		Billing: Billing{Remaining: FloatPtr(6)},
	}
	response := QuotaResponse(account)
	if response.Subscription == nil || response.Subscription.TierName != "OpenCode Go" {
		t.Fatalf("subscription = %#v", response.Subscription)
	}
	if len(response.Groups) != 1 || len(response.Groups[0].Buckets) != 1 {
		t.Fatalf("groups = %#v", response.Groups)
	}
	bucket := response.Groups[0].Buckets[0]
	if bucket.RemainingFraction != 0.6 || bucket.ResetTime == "" || bucket.Window != "5 小时" {
		t.Fatalf("bucket = %#v", bucket)
	}
	if len(response.Summary) != 2 {
		t.Fatalf("summary = %#v", response.Summary)
	}
}

func TestQuotaResponseSkipsWalletMeter(t *testing.T) {
	account := Account{
		Plan:    Plan{Cycle: Cycle{Kind: CycleNone}},
		Quota:   []Window{{ID: "balance", Balance: FloatPtr(2), Unit: "USD"}},
		Metrics: []Metric{{Key: "balance", Label: "余额", Value: 2}},
	}
	response := QuotaResponse(account)
	if len(response.Groups) != 0 {
		t.Fatalf("a balance-only account must not render a meter, got %#v", response.Groups)
	}
	if len(response.Summary) != 1 {
		t.Fatalf("summary = %#v", response.Summary)
	}
}

func nanFloat() float64 {
	zero := 0.0
	return zero / zero
}
