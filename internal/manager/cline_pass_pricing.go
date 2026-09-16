package manager

import (
	"math"
	"strings"
	"time"
)

// Cline Pass is a flat subscription gateway: the operator pays a fixed monthly
// price and is never billed per token, which makes it a different provider from
// Cline's usage-billed models. The official ClinePass documentation nevertheless
// publishes reference per-1M-token rates for the models the subscription serves
// "to help you understand how usage is measured against your ClinePass quota
// (2-5x quota compares to paying standard api rate)", and the Cline dashboard
// reports usage against three windows: a rolling 5-hour window, the calendar week
// and the calendar month.
//
// This file therefore holds reference rates, not charges. Its purpose is to let
// the plugin show what routed traffic would have cost at the standard API rate and
// compare that with the subscription, in the same "reference rates, not a charge"
// spirit as the OpenCode pricing service. The table is data only: it has no
// side effects, never reaches the network, and never stores a credential.
//
// Source of truth: Cline's official ClinePass documentation (reference rate table
// and the three documented quota windows).
const (
	// clinePassMonthlySubscriptionUSD is the documented flat subscription price of
	// Cline Pass. It is billed monthly and does not depend on token usage.
	clinePassMonthlySubscriptionUSD = 9.99

	// clinePassQuotaWindowFiveHour, clinePassQuotaWindowWeekly and
	// clinePassQuotaWindowMonthly are the three documented Cline Pass usage
	// windows, named the way the accounts payload publishes them.
	clinePassQuotaWindowFiveHour = "five_hour"
	clinePassQuotaWindowWeekly   = "weekly"
	clinePassQuotaWindowMonthly  = "monthly"

	// clinePassRollingWindow is the length of the documented rolling 5-hour
	// window. The window slides with the clock instead of resetting on a fixed
	// boundary, so every documented reference is measured against the last five
	// hours.
	clinePassRollingWindow = 5 * time.Hour

	// clinePassQwenLongContextThresholdTokens is the documented token boundary
	// above which Qwen3.7 Plus is measured at its long-context reference rate. The
	// documentation states it as ">256K tokens".
	clinePassQwenLongContextThresholdTokens int64 = 256_000

	// clinePassReferenceTokensPerUnit is the denominator of the documented
	// reference rates: every rate in the table is USD per 1M tokens.
	clinePassReferenceTokensPerUnit = 1_000_000

	// clinePassDeepSeekPeakDefault is the documented default choice for the two
	// DeepSeek models. Their documentation publishes a peak and an off-peak
	// reference rate but no time window, so the peak rate is the choice unless a
	// window is installed through clinePassDeepSeekOffPeak below.
	clinePassDeepSeekPeakDefault = true
)

// clinePassDeepSeekOffPeak is the injectable off-peak window predicate for the two
// DeepSeek models. Production leaves it nil, which keeps the documented peak
// default: the documentation publishes the off-peak *rates* but not the hours they
// apply to, so this plugin never guesses a window of its own. A deployment that
// learns the real window, or a test, can install a predicate here; it receives the
// request time at UTC and reports whether the off-peak rate applies.
var clinePassDeepSeekOffPeak func(time.Time) bool

// clinePassDeepSeekPeakRateAt reports whether a request at the supplied time is
// valued at the peak reference rate. Peak is the documented default
// (clinePassDeepSeekPeakDefault) and the only choice while no off-peak window is
// installed.
func clinePassDeepSeekPeakRateAt(at time.Time) bool {
	if clinePassDeepSeekOffPeak == nil {
		return clinePassDeepSeekPeakDefault
	}
	return !clinePassDeepSeekOffPeak(at.UTC())
}

// clinePassReferenceRateLevel is one documented context-length level of a Cline
// Pass reference rate, in USD per 1M tokens. CacheWrite is only meaningful when
// HasCacheWrite is true: the documentation publishes no cache-write rate for most
// models, and an absent rate must not be rendered as a free cache write.
type clinePassReferenceRateLevel struct {
	InputUSDPerMillion      float64
	OutputUSDPerMillion     float64
	CacheReadUSDPerMillion  float64
	CacheWriteUSDPerMillion float64
	HasCacheWrite           bool
}

// clinePassReferenceRate is the documented reference rate of one Cline Pass model.
// Peak is the applicable level unless an off-peak window applies (the two DeepSeek
// models), and LongContext replaces it when the model documents a long-context
// tier and the request's total tokens exceed the documented boundary (Qwen3.7
// Plus only).
type clinePassReferenceRate struct {
	Peak        clinePassReferenceRateLevel
	OffPeak     *clinePassReferenceRateLevel
	LongContext *clinePassReferenceRateLevel
}

// clinePassReferenceRates is the documented reference rate table, keyed by the
// normalized model id (see clinePassModelKey). A cache-write rate is only present
// for the models the documentation prices for cache writes.
var clinePassReferenceRates = map[string]clinePassReferenceRate{
	"glm-5.3": {
		Peak: clinePassReferenceRateLevel{InputUSDPerMillion: 1.40, OutputUSDPerMillion: 4.40, CacheReadUSDPerMillion: 0.26},
	},
	"glm-5.2": {
		Peak: clinePassReferenceRateLevel{InputUSDPerMillion: 1.40, OutputUSDPerMillion: 4.40, CacheReadUSDPerMillion: 0.26},
	},
	"kimi-k3": {
		Peak: clinePassReferenceRateLevel{InputUSDPerMillion: 3.00, OutputUSDPerMillion: 15.00, CacheReadUSDPerMillion: 0.30},
	},
	"kimi-k2.7-code": {
		Peak: clinePassReferenceRateLevel{InputUSDPerMillion: 0.95, OutputUSDPerMillion: 4.00, CacheReadUSDPerMillion: 0.19},
	},
	"kimi-k2.6": {
		Peak: clinePassReferenceRateLevel{InputUSDPerMillion: 0.95, OutputUSDPerMillion: 4.00, CacheReadUSDPerMillion: 0.16},
	},
	"deepseek-v4-pro": {
		Peak:    clinePassReferenceRateLevel{InputUSDPerMillion: 1.32, OutputUSDPerMillion: 3.96, CacheReadUSDPerMillion: 0.044},
		OffPeak: &clinePassReferenceRateLevel{InputUSDPerMillion: 0.66, OutputUSDPerMillion: 1.98, CacheReadUSDPerMillion: 0.022},
	},
	"deepseek-v4-flash": {
		Peak:    clinePassReferenceRateLevel{InputUSDPerMillion: 0.44, OutputUSDPerMillion: 1.32, CacheReadUSDPerMillion: 0.014},
		OffPeak: &clinePassReferenceRateLevel{InputUSDPerMillion: 0.22, OutputUSDPerMillion: 0.66, CacheReadUSDPerMillion: 0.007},
	},
	"mimo-v2.5": {
		Peak: clinePassReferenceRateLevel{InputUSDPerMillion: 0.14, OutputUSDPerMillion: 0.28, CacheReadUSDPerMillion: 0.0028},
	},
	"mimo-v2.5-pro": {
		Peak: clinePassReferenceRateLevel{InputUSDPerMillion: 1.74, OutputUSDPerMillion: 3.48, CacheReadUSDPerMillion: 0.0145},
	},
	"minimax-m3": {
		Peak: clinePassReferenceRateLevel{InputUSDPerMillion: 0.30, OutputUSDPerMillion: 1.20, CacheReadUSDPerMillion: 0.06},
	},
	"qwen3.8-max": {
		Peak: clinePassReferenceRateLevel{InputUSDPerMillion: 2.00, OutputUSDPerMillion: 6.00, CacheReadUSDPerMillion: 0.25, CacheWriteUSDPerMillion: 2.50, HasCacheWrite: true},
	},
	"qwen3.7-max": {
		Peak: clinePassReferenceRateLevel{InputUSDPerMillion: 2.50, OutputUSDPerMillion: 7.50, CacheReadUSDPerMillion: 0.50, CacheWriteUSDPerMillion: 3.125, HasCacheWrite: true},
	},
	"qwen3.7-plus": {
		Peak: clinePassReferenceRateLevel{InputUSDPerMillion: 0.40, OutputUSDPerMillion: 1.60, CacheReadUSDPerMillion: 0.04, CacheWriteUSDPerMillion: 0.50, HasCacheWrite: true},
		LongContext: &clinePassReferenceRateLevel{
			InputUSDPerMillion: 1.20, OutputUSDPerMillion: 4.80, CacheReadUSDPerMillion: 0.12, CacheWriteUSDPerMillion: 1.50, HasCacheWrite: true,
		},
	},
}

// clinePassReferenceRateAliases maps a model id Cline's gateway serves onto the
// documented reference rate row that prices it. The documented table prices
// "DeepSeek V4 Flash" under the cline-pass/deepseek-v4-flash id, while the gateway
// now publishes the same subscription model as cline-pass/deepseek-v4.1-flash:
// DeepSeek's own documentation states that the retired deepseek-v4-flash model is
// served by DeepSeek-V4.1-Flash and billed at the Flash price. Resolving both
// spellings to that one documented row keeps the model the gateway actually
// serves from being reported as unpriced, which would value a running account at
// zero. Every key is a model id, never a wildcard, so an unknown model still
// reports unpriced.
var clinePassReferenceRateAliases = map[string]string{
	"deepseek-v4.1-flash": "deepseek-v4-flash",
	"deepseek-v4-1-flash": "deepseek-v4-flash",
}

// clinePassModelKey normalizes a model id to the reference rate table key: the
// lowercased id with the literal cline-pass/ prefix removed. Both the full id the
// catalog publishes and the stripped id the strip_model_prefix switch publishes
// therefore resolve to the same rate.
func clinePassModelKey(model string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(model)), clinePassModelPrefix)
}

// clinePassReferencePrice returns the documented reference rate of one model. The
// lookup accepts both the full cline-pass/... id and the prefix-stripped form the
// strip_model_prefix switch publishes, and it resolves the documented aliases of
// clinePassReferenceRateAliases to the one row that prices them. A model the
// documentation does not price (including the free tier) reports ok=false so the
// caller can keep it unrated instead of showing a zero price as if it were free of
// charge.
func clinePassReferencePrice(model string) (clinePassReferenceRate, bool) {
	key := clinePassModelKey(model)
	if key == "" {
		return clinePassReferenceRate{}, false
	}
	if aliased, ok := clinePassReferenceRateAliases[key]; ok {
		key = aliased
	}
	rate, ok := clinePassReferenceRates[key]
	return rate, ok
}

// clinePassIsPublishedModel reports whether an id names a model this plugin
// publishes for Cline Pass: the curated allow-list (in its full or stripped form)
// or a documented reference rate. Unknown ids are not Cline Pass traffic.
func clinePassIsPublishedModel(model string) bool {
	key := clinePassModelKey(model)
	if key == "" {
		return false
	}
	if _, ok := clinePassReferenceRates[key]; ok {
		return true
	}
	for _, entry := range clinePassCatalog {
		if clinePassModelKey(entry.ID) == key {
			return true
		}
	}
	return false
}

// clinePassReferenceLevelFor returns the reference rate level that applies to one
// request: the long-context tier when the model documents one and the request's
// total tokens exceed the documented boundary, otherwise the peak or the off-peak
// DeepSeek level. An empty at means the request time is unknown, and an unknown
// time cannot be proven off-peak, so the documented peak default applies.
func clinePassReferenceLevelFor(rate clinePassReferenceRate, totalTokens int64, at time.Time) clinePassReferenceRateLevel {
	if rate.LongContext != nil && totalTokens > clinePassQwenLongContextThresholdTokens {
		return *rate.LongContext
	}
	if rate.OffPeak != nil && !at.IsZero() && !clinePassDeepSeekPeakRateAt(at) {
		return *rate.OffPeak
	}
	return rate.Peak
}

// clinePassReferenceCost returns the reference-priced value in USD of one request
// at the documented Cline Pass reference rates. The values are reference-priced
// list value, not a charge. An unknown or free model reports priced=false with no
// cost, so the caller keeps such usage unrated rather than pricing it at zero.
//
// The DeepSeek peak rate is used here because the documentation does not publish
// the off-peak hours; clinePassReferenceCostAt values a request at a known time
// instead. The Qwen3.7 Plus long-context tier is chosen by the request's total
// tokens (input + cache read + cache write + output).
func clinePassReferenceCost(model string, input, output, cacheRead, cacheWrite int64) (float64, bool) {
	return clinePassReferenceCostAt(model, input, output, cacheRead, cacheWrite, time.Time{})
}

// clinePassReferenceCostAt is clinePassReferenceCost for a request observed at a
// known time, so a deployment that installed an off-peak window
// (clinePassDeepSeekOffPeak) values the request in the right DeepSeek period. Every
// other model prices identically for any time.
func clinePassReferenceCostAt(model string, input, output, cacheRead, cacheWrite int64, at time.Time) (float64, bool) {
	rate, ok := clinePassReferencePrice(model)
	if !ok {
		return 0, false
	}
	uncached, output, cacheRead, cacheWrite := nonNegative(input), nonNegative(output), nonNegative(cacheRead), nonNegative(cacheWrite)
	level := clinePassReferenceLevelFor(rate, uncached+output+cacheRead+cacheWrite, at)
	usd := (float64(uncached)*level.InputUSDPerMillion +
		float64(output)*level.OutputUSDPerMillion +
		float64(cacheRead)*level.CacheReadUSDPerMillion +
		float64(cacheWrite)*level.CacheWriteUSDPerMillion) / clinePassReferenceTokensPerUnit
	if math.IsNaN(usd) || math.IsInf(usd, 0) || usd < 0 {
		return 0, true
	}
	return usd, true
}

// clinePassWeekStart returns the UTC Monday 00:00 of the calendar week containing t
// (ISO-8601 weeks, the common calendar-week definition).
func clinePassWeekStart(t time.Time) time.Time {
	t = t.UTC()
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	offset := (int(day.Weekday()) + 6) % 7 // Monday is the first day of the week.
	return day.AddDate(0, 0, -offset)
}

// clinePassMonthStart returns the UTC first-day 00:00 of the calendar month
// containing t.
func clinePassMonthStart(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// applyClinePassModelPrice copies the documented reference rate of one model onto
// a Cline Pass model row. An unpriced model keeps Priced=false and publishes no
// price field, so the UI can tell "the documentation does not price this model"
// apart from "the price is zero", and a model with no documented cache-write rate
// omits that field. It mirrors applyCodexModelPrice for the Codex model rows.
//
// The row carries the rate a request in the documented base tier is measured at:
// the peak rate for the two DeepSeek models, because the documentation publishes no
// off-peak hours, and the <=256K rate for Qwen3.7 Plus. The long-context tier and
// the off-peak level are what clinePassReferenceCostAt applies to a real request.
func applyClinePassModelPrice(row *clinePassModelView, model string) {
	if row == nil {
		return
	}
	rate, ok := clinePassReferencePrice(model)
	if !ok {
		return
	}
	row.Priced = true
	row.InputUSDPerMillion = rate.Peak.InputUSDPerMillion
	row.OutputUSDPerMillion = rate.Peak.OutputUSDPerMillion
	row.CacheReadUSDPerMillion = rate.Peak.CacheReadUSDPerMillion
	if rate.Peak.HasCacheWrite {
		row.CacheWriteUSDPerMillion = rate.Peak.CacheWriteUSDPerMillion
	}
}
