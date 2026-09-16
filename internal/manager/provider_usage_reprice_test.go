package manager

import (
	"math"
	"testing"

	"cpa-account-config-manager/internal/cpaapi"
)

// flipCreditCalculator prices nothing until valueOf is set, which models a gateway
// that served a model before the price table learned it.
type flipCreditCalculator struct {
	valueOf int64
}

func (c *flipCreditCalculator) Enabled() bool { return true }

func (c *flipCreditCalculator) Calculate(record cpaapi.UsageRecord) CreditCharge {
	if c.valueOf <= 0 {
		return CreditCharge{Enabled: true}
	}
	return CreditCharge{Enabled: true, Rated: true, AmountNanos: c.valueOf}
}

func (c *flipCreditCalculator) Snapshot() CreditPricingSnapshot { return CreditPricingSnapshot{} }

func (c *flipCreditCalculator) RepriceAggregate(record cpaapi.UsageRecord) (int64, bool) {
	if c.valueOf <= 0 {
		return 0, false
	}
	return c.valueOf, true
}

// providerRuntimeSnapshotForProvider reads the one row a test recorded.
func providerRuntimeSnapshotForProvider(t *testing.T, tracker *ProviderRuntimeTracker, provider string) ProviderRuntimeSnapshot {
	t.Helper()
	for _, snapshot := range tracker.Snapshot() {
		if snapshot.Provider == provider {
			return snapshot
		}
	}
	t.Fatalf("no runtime row for provider %q", provider)
	return ProviderRuntimeSnapshot{}
}

// A model a channel served before any price table could price it stayed unrated
// forever, which made a running channel report millions of tokens as free usage.
// The re-price pass values those totals once a table can price the model, moves the
// counters to the rated side without touching a token count, keeps history out of
// the rolling windows, and survives a restart.
func TestProviderRuntimeRepricesHistoryTheCurrentPriceTableCanValue(t *testing.T) {
	dataDir := t.TempDir()
	calculator := &flipCreditCalculator{}
	tracker := NewProviderRuntimeTracker(calculator)
	tracker.Configure(Config{DataDir: dataDir})
	tracker.ObserveUsage(cpaapi.UsageRecord{
		Provider:  "openai-compatible-warrior",
		AuthType:  "apikey",
		AuthIndex: "auth-warrior",
		Model:     "deepseek-v4.1-flash",
		Detail:    cpaapi.UsageDetail{InputTokens: 1_000_000, CachedTokens: 400_000, OutputTokens: 100_000, TotalTokens: 1_100_000},
	})
	snapshot := providerRuntimeSnapshotForProvider(t, tracker, "openai-compatible-warrior")
	if snapshot.AmountUSD != 0 || snapshot.RatedRequests != 0 || snapshot.UnratedRequests != 1 {
		t.Fatalf("usage recorded before the model was priced = %#v", snapshot)
	}
	if len(snapshot.Models) != 1 || snapshot.Models[0].UnratedRequests != 1 {
		t.Fatalf("model row of unrated usage = %#v", snapshot.Models)
	}

	// The same totals become valuable once the price table prices the model.
	calculator.valueOf = 250_000_000
	result := tracker.RepriceUnratedModelUsage()
	if result.Models != 1 || result.Aggregates != 1 || result.Requests != 1 || math.Abs(result.AmountUSD-0.25) > 1e-9 {
		t.Fatalf("reprice result = %#v", result)
	}
	snapshot = providerRuntimeSnapshotForProvider(t, tracker, "openai-compatible-warrior")
	if math.Abs(snapshot.AmountUSD-0.25) > 1e-9 || snapshot.RatedRequests != 1 || snapshot.UnratedRequests != 0 {
		t.Fatalf("aggregate after the re-price = %#v", snapshot)
	}
	model := snapshot.Models[0]
	if !model.Rated || model.RatedRequests != 1 || model.UnratedRequests != 0 || math.Abs(model.AmountUSD-0.25) > 1e-9 {
		t.Fatalf("model row after the re-price = %#v", model)
	}
	// The counters moved; the recorded tokens did not.
	if model.InputTokens != 1_000_000 || model.CachedTokens != 400_000 || model.OutputTokens != 100_000 {
		t.Fatalf("re-priced tokens = %#v", model)
	}
	// A rolling window is built from recorded events, and history is not an event.
	if snapshot.Quota.FiveHourAmountUSD != 0 || snapshot.Quota.SevenDayAmountUSD != 0 {
		t.Fatalf("the re-price entered a rolling window: %#v", snapshot.Quota)
	}
	// The pass is idempotent: nothing is left to value and nothing is counted twice.
	if again := tracker.RepriceUnratedModelUsage(); again.Models != 0 || again.AmountUSD != 0 {
		t.Fatalf("second reprice = %#v", again)
	}
	if after := providerRuntimeSnapshotForProvider(t, tracker, "openai-compatible-warrior"); math.Abs(after.AmountUSD-0.25) > 1e-9 {
		t.Fatalf("second reprice changed the amount: %#v", after)
	}
	tracker.Shutdown()

	restored := NewProviderRuntimeTracker(calculator)
	defer restored.Shutdown()
	restored.Configure(Config{DataDir: dataDir})
	after := providerRuntimeSnapshotForProvider(t, restored, "openai-compatible-warrior")
	if math.Abs(after.AmountUSD-0.25) > 1e-9 || after.RatedRequests != 1 || after.UnratedRequests != 0 {
		t.Fatalf("restored aggregate = %#v", after)
	}
}

// The re-price pass may only accept a rate it can apply to a sum of requests: the
// Flash card the retired DeepSeek slug now resolves to is a single flat rate, while
// a long-context or priority card would value a whole month at one request's tier.
func TestCreditPricingRepricesOnlyFlatRateHistory(t *testing.T) {
	service := NewSub2APICreditUsage()
	defer service.Close()
	service.SetEnabled(true)
	service.table.Store(parsedCreditPricingForTest(t, map[string]creditModelPricing{
		"deepseek-v4-flash": {Input: 0.44e-6, Output: 1.32e-6, CacheRead: 0.014e-6},
		"tiered-model":      {Input: 1e-6, Output: 1e-6, LongContextThreshold: 272_000, LongContextInputMultiplier: 2, LongContextOutputMultiplier: 1.5},
		"priority-model":    {Input: 1e-6, Output: 1e-6, InputPriority: 2e-6},
	}))

	historical := cpaapi.UsageRecord{
		Model:  "deepseek-v4.1-flash",
		Detail: cpaapi.UsageDetail{InputTokens: 1_000_000, CachedTokens: 400_000, OutputTokens: 100_000},
	}
	nanos, ok := service.RepriceAggregate(historical)
	// 600K uncached input at 0.44 + 400K cache reads at 0.014 + 100K output at 1.32.
	want := int64(math.Round((0.6*0.44 + 0.4*0.014 + 0.1*1.32) * float64(creditNanosPerUSD)))
	if !ok || nanos <= 0 || math.Abs(float64(nanos-want)) > 1 {
		t.Fatalf("RepriceAggregate = %d (ok=%v), want %d", nanos, ok, want)
	}
	if charge := service.Calculate(historical); charge.AmountNanos != nanos {
		t.Fatalf("re-price %d differs from the live charge %d", nanos, charge.AmountNanos)
	}

	for _, model := range []string{"tiered-model", "priority-model", "unknown-model"} {
		usage := historical
		usage.Model = model
		if amount, priced := service.RepriceAggregate(usage); priced || amount != 0 {
			t.Fatalf("RepriceAggregate(%q) = %d, priced=%v, want no amount", model, amount, priced)
		}
	}

	service.SetEnabled(false)
	if amount, priced := service.RepriceAggregate(historical); priced || amount != 0 {
		t.Fatalf("a disabled calculator re-priced history: %d, priced=%v", amount, priced)
	}
}
