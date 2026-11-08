package provider

import (
	"math"
	"strings"
	"time"
)

// Normalization helpers. Every provider adapter calls these, so the "unknown is
// not zero" and "clamp to a valid share" rules exist in one place and are
// covered by one table test.

// ClampPercent turns any number into a valid percentage.
//
// A NaN or an infinity is treated as not reported (nil): a broken upstream value
// must not become a 0% bar that looks like a full allowance. Values slightly
// outside [0,100] are clamped, because vendors round and a 100.4% reading is a
// full window, not an error.
func ClampPercent(value *float64) *float64 {
	if value == nil {
		return nil
	}
	if math.IsNaN(*value) || math.IsInf(*value, 0) {
		return nil
	}
	clamped := math.Max(0, math.Min(100, *value))
	return &clamped
}

// PercentFromUsed converts a used share to a used percentage, keeping nil.
func PercentFromUsed(used *float64) *float64 {
	if used == nil {
		return nil
	}
	if math.IsNaN(*used) || math.IsInf(*used, 0) {
		return nil
	}
	asPercent := *used * 100
	return ClampPercent(&asPercent)
}

// RemainingFromUsed derives the remaining percentage. The pair is always
// consistent: remaining = 100 - used, and a missing used share stays nil.
func RemainingFromUsed(used *float64) *float64 {
	if used == nil {
		return nil
	}
	remaining := 100 - *used
	return ClampPercent(&remaining)
}

// UsedFromRemaining is the inverse for providers that report what is left.
func UsedFromRemaining(remaining *float64) *float64 {
	if remaining == nil {
		return nil
	}
	used := 100 - *remaining
	return ClampPercent(&used)
}

// ShareOf computes the used percentage from an amount and its limit. It returns
// nil unless both are present and the limit is positive, because dividing by a
// missing limit would invent a 0% reading.
func ShareOf(used, limit *float64) *float64 {
	if used == nil || limit == nil || *limit <= 0 {
		return nil
	}
	if math.IsNaN(*used) || math.IsInf(*used, 0) {
		return nil
	}
	percent := (*used / *limit) * 100
	return ClampPercent(&percent)
}

// Money keeps a finite, non-negative amount. A negative balance is clamped to
// zero (an overdrawn wallet shows 0), while an unreported amount stays nil.
func Money(value *float64) *float64 {
	if value == nil {
		return nil
	}
	if math.IsNaN(*value) || math.IsInf(*value, 0) {
		return nil
	}
	clamped := math.Max(0, *value)
	return &clamped
}

// FloatPtr is a convenience for adapters and tests.
func FloatPtr(value float64) *float64 { return &value }

// TimePtr is a convenience for adapters and tests.
func TimePtr(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	copied := value
	return &copied
}

// WindowKind maps a provider's own window name to the shared vocabulary while
// keeping the original name on the window.
func WindowKind(source string) CycleKind {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "5h", "5h_window", "five_hour", "fivehours", "rolling", "session", "5-hour":
		return CycleRolling
	case "weekly", "week", "7d", "seven_day", "sevendays", "7-day":
		return CycleWeekly
	case "monthly", "month", "30d", "30-day":
		return CycleMonthly
	case "annual", "yearly", "year":
		return CycleAnnual
	default:
		return CycleNone
	}
}

// WindowLabel is the human label for a shared window kind. A provider with its
// own label keeps it; this is only the fallback.
func WindowLabel(kind CycleKind) string {
	switch kind {
	case CycleRolling:
		return "5 hours"
	case CycleWeekly:
		return "7 days"
	case CycleMonthly:
		return "Monthly"
	case CycleAnnual:
		return "Annual"
	default:
		return "Quota"
	}
}

// NewWindow builds a window with a consistent used/remaining pair. It never
// fills a missing share with zero: the caller passes what the provider reported.
func NewWindow(id string, sourceWindow string, usedPercent *float64) Window {
	used := ClampPercent(usedPercent)
	kind := WindowKind(sourceWindow)
	return Window{
		ID:               id,
		Label:            WindowLabel(kind),
		Kind:             kind,
		SourceWindow:     sourceWindow,
		UsedPercent:      used,
		RemainingPercent: RemainingFromUsed(used),
		Unit:             "%",
		Source:           "upstream",
	}
}

// ApplyLimit records an amount limit on a window and derives the used share when
// the provider gave amounts rather than a percentage.
func ApplyLimit(window Window, used, limit *float64, unit string) Window {
	window.Limit = Money(limit)
	window.Unit = unit
	if window.UsedPercent == nil {
		window.UsedPercent = ShareOf(used, limit)
		window.RemainingPercent = RemainingFromUsed(window.UsedPercent)
	}
	return window
}

// StatusFromFacts picks the status from raw provider facts. It exists so every
// adapter orders the same facts the same way (see Status.rank).
func StatusFromFacts(disabled, credentialRejected, repairing, quotaLimited bool) Status {
	status := StatusReady
	if quotaLimited {
		status = Worse(status, StatusQuotaLimited)
	}
	if repairing {
		status = Worse(status, StatusRepairing)
	}
	if credentialRejected {
		status = Worse(status, StatusCredentialInvalid)
	}
	if disabled {
		status = Worse(status, StatusDisabled)
	}
	return status
}

// Expired reports whether a credential expiry has passed. A nil expiry is not
// expired: unknown is not the same as invalid.
func Expired(expiresAt *time.Time, now time.Time) bool {
	return expiresAt != nil && !expiresAt.After(now)
}
