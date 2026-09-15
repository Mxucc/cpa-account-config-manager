package manager

import (
	"math"
	"testing"
	"time"
)

// clinePassDocumentedRate is one row of the reference rate table as the ClinePass
// documentation publishes it. The tests below assert the plugin's table against
// these numbers, so a transcription mistake cannot slip through.
type clinePassDocumentedRate struct {
	model      string
	input      float64
	output     float64
	cacheRead  float64
	cacheWrite float64
}

func TestClinePassReferencePriceMatchesDocumentation(t *testing.T) {
	documented := []clinePassDocumentedRate{
		{model: "cline-pass/glm-5.3", input: 1.40, output: 4.40, cacheRead: 0.26},
		{model: "cline-pass/glm-5.2", input: 1.40, output: 4.40, cacheRead: 0.26},
		{model: "cline-pass/kimi-k3", input: 3.00, output: 15.00, cacheRead: 0.30},
		{model: "cline-pass/kimi-k2.7-code", input: 0.95, output: 4.00, cacheRead: 0.19},
		{model: "cline-pass/kimi-k2.6", input: 0.95, output: 4.00, cacheRead: 0.16},
		{model: "cline-pass/deepseek-v4-pro", input: 1.32, output: 3.96, cacheRead: 0.044},
		{model: "cline-pass/deepseek-v4-flash", input: 0.44, output: 1.32, cacheRead: 0.014},
		{model: "cline-pass/mimo-v2.5", input: 0.14, output: 0.28, cacheRead: 0.0028},
		{model: "cline-pass/mimo-v2.5-pro", input: 1.74, output: 3.48, cacheRead: 0.0145},
		{model: "cline-pass/minimax-m3", input: 0.30, output: 1.20, cacheRead: 0.06},
		{model: "cline-pass/qwen3.8-max", input: 2.00, output: 6.00, cacheRead: 0.25, cacheWrite: 2.50},
		{model: "cline-pass/qwen3.7-max", input: 2.50, output: 7.50, cacheRead: 0.50, cacheWrite: 3.125},
		{model: "cline-pass/qwen3.7-plus", input: 0.40, output: 1.60, cacheRead: 0.04, cacheWrite: 0.50},
	}
	for _, want := range documented {
		// Both the full catalog id and the stripped id the strip_model_prefix
		// switch publishes must resolve to the same documented rate.
		for _, id := range []string{want.model, clinePassModelKey(want.model)} {
			rate, ok := clinePassReferencePrice(id)
			if !ok {
				t.Fatalf("clinePassReferencePrice(%q) reported the documented model as unpriced", id)
			}
			if rate.Peak.InputUSDPerMillion != want.input || rate.Peak.OutputUSDPerMillion != want.output || rate.Peak.CacheReadUSDPerMillion != want.cacheRead {
				t.Fatalf("%q peak rate = %+v, want in=%v out=%v cache_read=%v", id, rate.Peak, want.input, want.output, want.cacheRead)
			}
			if want.cacheWrite == 0 {
				if rate.Peak.HasCacheWrite {
					t.Fatalf("%q reports a cache-write rate the documentation does not publish: %+v", id, rate.Peak)
				}
				continue
			}
			if !rate.Peak.HasCacheWrite || rate.Peak.CacheWriteUSDPerMillion != want.cacheWrite {
				t.Fatalf("%q cache-write rate = %+v, want %v", id, rate.Peak, want.cacheWrite)
			}
		}
	}

	// Qwen3.7 Plus is the only model with a documented long-context tier.
	longContext, ok := clinePassReferencePrice("cline-pass/qwen3.7-plus")
	if !ok || longContext.LongContext == nil {
		t.Fatalf("qwen3.7-plus lost its long-context tier: %+v", longContext)
	}
	if longContext.LongContext.InputUSDPerMillion != 1.20 || longContext.LongContext.OutputUSDPerMillion != 4.80 ||
		longContext.LongContext.CacheReadUSDPerMillion != 0.12 || longContext.LongContext.CacheWriteUSDPerMillion != 1.50 {
		t.Fatalf("qwen3.7-plus long-context rate = %+v", *longContext.LongContext)
	}
	// The two DeepSeek models are the only ones with a documented off-peak pair.
	for _, id := range []string{"cline-pass/deepseek-v4-pro", "cline-pass/deepseek-v4-flash"} {
		rate, ok := clinePassReferencePrice(id)
		if !ok || rate.OffPeak == nil {
			t.Fatalf("%q lost its off-peak rate: %+v", id, rate)
		}
	}
	if rate, _ := clinePassReferencePrice("cline-pass/kimi-k3"); rate.OffPeak != nil || rate.LongContext != nil {
		t.Fatalf("kimi-k3 gained an undocumented tier: %+v", rate)
	}

	// A model the documentation does not price, including the free tier and an
	// unknown id, reports unpriced instead of a zero price.
	for _, id := range []string{"cline-free/longcat-2.0", "cline-pass/glm-5.3-flash", "cline-pass/deepseek-v4.1-flash", "unknown/model", ""} {
		if rate, ok := clinePassReferencePrice(id); ok {
			t.Fatalf("clinePassReferencePrice(%q) priced an undocumented model: %+v", id, rate)
		}
		if usd, priced := clinePassReferenceCost(id, 1_000_000, 1_000_000, 0, 0); priced || usd != 0 {
			t.Fatalf("clinePassReferenceCost(%q) = %v, priced=%v, want no cost", id, usd, priced)
		}
	}
}

// The documented reference rates are per 1M tokens, so the cost maths is asserted
// against hand-computed values: GLM-5.3 with 1M input, 500K output and 200K cache
// reads is 1.40 + 0.5*4.40 + 0.2*0.26 = 3.652 USD, and Qwen3.8 Max with 1M of every
// token class is 2.00 + 6.00 + 0.25 + 2.50 = 10.75 USD.
func TestClinePassReferenceCostMaths(t *testing.T) {
	if usd, priced := clinePassReferenceCost("cline-pass/glm-5.3", 1_000_000, 500_000, 200_000, 0); !priced || math.Abs(usd-3.652) > 1e-9 {
		t.Fatalf("glm-5.3 cost = %v priced=%v, want 3.652", usd, priced)
	}
	if usd, priced := clinePassReferenceCost("glm-5.3", 1_000_000, 500_000, 200_000, 0); !priced || math.Abs(usd-3.652) > 1e-9 {
		t.Fatalf("stripped glm-5.3 cost = %v priced=%v, want 3.652", usd, priced)
	}
	if usd, priced := clinePassReferenceCost("cline-pass/qwen3.8-max", 1_000_000, 1_000_000, 1_000_000, 1_000_000); !priced || math.Abs(usd-10.75) > 1e-9 {
		t.Fatalf("qwen3.8-max cost = %v priced=%v, want 10.75", usd, priced)
	}
	// A model with no documented cache-write rate must not charge for a cache write.
	if usd, priced := clinePassReferenceCost("cline-pass/kimi-k3", 0, 0, 0, 1_000_000); !priced || usd != 0 {
		t.Fatalf("kimi-k3 cache write cost = %v priced=%v, want 0", usd, priced)
	}
	// Negative token counts are clamped instead of crediting the account.
	if usd, _ := clinePassReferenceCost("cline-pass/kimi-k3", -5_000_000, 0, 0, 0); usd != 0 {
		t.Fatalf("negative input tokens produced %v", usd)
	}
}

// Qwen3.7 Plus switches to its documented long-context rate above 256K total
// tokens: 100K input plus 200K output is 300K total and costs
// 0.1*1.20 + 0.2*4.80 = 1.08 USD, while exactly 256K total stays on the base rate
// (0.256 * 0.40 = 0.1024 USD).
func TestClinePassQwenLongContextTierSwitchesOnTotalTokens(t *testing.T) {
	if usd, priced := clinePassReferenceCost("cline-pass/qwen3.7-plus", 100_000, 200_000, 0, 0); !priced || math.Abs(usd-1.08) > 1e-9 {
		t.Fatalf("long-context cost = %v priced=%v, want 1.08", usd, priced)
	}
	if usd, priced := clinePassReferenceCost("cline-pass/qwen3.7-plus", 256_000, 0, 0, 0); !priced || math.Abs(usd-0.1024) > 1e-9 {
		t.Fatalf("boundary cost = %v priced=%v, want the base rate 0.1024", usd, priced)
	}
	// A cache read alone can push a request over the documented boundary.
	if usd, priced := clinePassReferenceCost("cline-pass/qwen3.7-plus", 0, 0, 300_000, 0); !priced || math.Abs(usd-0.036) > 1e-9 {
		t.Fatalf("cache-read long-context cost = %v priced=%v, want 0.036", usd, priced)
	}
	// The other documented models keep a single tier however large the request is.
	if usd, priced := clinePassReferenceCost("cline-pass/glm-5.3", 1_000_000, 0, 0, 0); !priced || math.Abs(usd-1.40) > 1e-9 {
		t.Fatalf("glm-5.3 large request cost = %v priced=%v, want 1.40", usd, priced)
	}
}

// The DeepSeek helper is exercised both ways: the documented peak rate is the
// default, and an installed off-peak window (the injectable predicate, because the
// documentation publishes the off-peak rates without their hours) selects the
// off-peak level for a request inside that window.
func TestClinePassDeepSeekPeakAndOffPeakRates(t *testing.T) {
	t.Cleanup(func() { clinePassDeepSeekOffPeak = nil })
	peakHour := time.Date(2026, 3, 12, 12, 0, 0, 0, time.UTC)
	offPeakHour := time.Date(2026, 3, 12, 2, 0, 0, 0, time.UTC)

	// Default: no window is installed, so peak applies and is documented as the
	// default choice.
	clinePassDeepSeekOffPeak = nil
	if !clinePassDeepSeekPeakRateAt(offPeakHour) {
		t.Fatal("the default DeepSeek choice is not the documented peak rate")
	}
	if usd, priced := clinePassReferenceCostAt("cline-pass/deepseek-v4-pro", 1_000_000, 0, 0, 0, offPeakHour); !priced || math.Abs(usd-1.32) > 1e-9 {
		t.Fatalf("deepseek-v4-pro default cost = %v priced=%v, want the peak 1.32", usd, priced)
	}
	// An unknown request time cannot be proven off-peak, so cost keeps the peak rate.
	if usd, _ := clinePassReferenceCost("cline-pass/deepseek-v4-pro", 1_000_000, 0, 0, 0); math.Abs(usd-1.32) > 1e-9 {
		t.Fatalf("timeless deepseek cost = %v, want the peak 1.32", usd)
	}

	// Install the documented off-peak window shape: 00:00-08:00 UTC.
	clinePassDeepSeekOffPeak = func(at time.Time) bool {
		hour := at.UTC().Hour()
		return hour >= 0 && hour < 8
	}
	if clinePassDeepSeekPeakRateAt(offPeakHour) {
		t.Fatal("a request inside the installed off-peak window was valued at the peak rate")
	}
	if !clinePassDeepSeekPeakRateAt(peakHour) {
		t.Fatal("a request outside the installed off-peak window lost the peak rate")
	}
	// deepseek-v4-pro off-peak: 0.66 input, 1.98 output, 0.022 cache read.
	if usd, priced := clinePassReferenceCostAt("cline-pass/deepseek-v4-pro", 1_000_000, 1_000_000, 1_000_000, 0, offPeakHour); !priced || math.Abs(usd-2.662) > 1e-9 {
		t.Fatalf("deepseek-v4-pro off-peak cost = %v priced=%v, want 2.662", usd, priced)
	}
	if usd, _ := clinePassReferenceCostAt("cline-pass/deepseek-v4-pro", 1_000_000, 0, 0, 0, peakHour); math.Abs(usd-1.32) > 1e-9 {
		t.Fatalf("deepseek-v4-pro peak cost = %v, want 1.32", usd)
	}
	// deepseek-v4-flash off-peak: 0.22 input, 0.66 output, 0.007 cache read.
	if usd, _ := clinePassReferenceCostAt("cline-pass/deepseek-v4-flash", 1_000_000, 1_000_000, 1_000_000, 0, offPeakHour); math.Abs(usd-0.887) > 1e-9 {
		t.Fatalf("deepseek-v4-flash off-peak cost = %v, want 0.887", usd)
	}
	// A model without an off-peak pair is priced identically in both periods.
	peakCost, _ := clinePassReferenceCostAt("cline-pass/kimi-k3", 1_000_000, 0, 0, 0, peakHour)
	offPeakCost, _ := clinePassReferenceCostAt("cline-pass/kimi-k3", 1_000_000, 0, 0, 0, offPeakHour)
	if peakCost != offPeakCost {
		t.Fatalf("kimi-k3 cost changed with the clock: %v vs %v", peakCost, offPeakCost)
	}
}

// The documented subscription figure and the three window names are the contract
// the accounts payload publishes.
func TestClinePassDocumentedSubscriptionAndWindows(t *testing.T) {
	if clinePassMonthlySubscriptionUSD != 9.99 {
		t.Fatalf("monthly subscription = %v, want 9.99", clinePassMonthlySubscriptionUSD)
	}
	if clinePassQuotaWindowFiveHour != "five_hour" || clinePassQuotaWindowWeekly != "weekly" || clinePassQuotaWindowMonthly != "monthly" {
		t.Fatalf("window names = %q %q %q", clinePassQuotaWindowFiveHour, clinePassQuotaWindowWeekly, clinePassQuotaWindowMonthly)
	}
	if clinePassRollingWindow != 5*time.Hour {
		t.Fatalf("rolling window = %v", clinePassRollingWindow)
	}
}

// The model page carries the reference price of a priced model and no price field
// at all for one the documentation does not price.
func TestClinePassModelRowCarriesReferencePrice(t *testing.T) {
	row := clinePassModelView{ID: "cline-pass/qwen3.7-max", Name: "Qwen3.7 Max"}
	applyClinePassModelPrice(&row, row.ID)
	if !row.Priced || row.InputUSDPerMillion != 2.50 || row.OutputUSDPerMillion != 7.50 || row.CacheReadUSDPerMillion != 0.50 || row.CacheWriteUSDPerMillion != 3.125 {
		t.Fatalf("priced row1 = %+v", row)
	}

	// A model without a documented cache-write rate keeps that field at zero, and
	// the omitempty tag keeps it out of the payload.
	unpricedCacheWrite := clinePassModelView{ID: "cline-pass/kimi-k3"}
	applyClinePassModelPrice(&unpricedCacheWrite, unpricedCacheWrite.ID)
	if !unpricedCacheWrite.Priced || unpricedCacheWrite.CacheWriteUSDPerMillion != 0 {
		t.Fatalf("priced row2 = %+v", unpricedCacheWrite)
	}

	free := clinePassModelView{ID: "cline-free/longcat-2.0"}
	applyClinePassModelPrice(&free, free.ID)
	if free.Priced || free.InputUSDPerMillion != 0 {
		t.Fatalf("unpriced row = %+v", free)
	}
}
