package provider

import (
	"time"
)

// MagpiePluginInput is one imported plugin whose hooks were executed.
type MagpiePluginInput struct {
	ProviderID  string
	DisplayName string
	Models      []string
	Methods     []string
	Windows     []MagpieWindow
	Warnings    []string
	UpdatedAt   time.Time
}

// MagpieWindow mirrors the sidecar's window shape.
type MagpieWindow struct {
	Name    string
	Used    *float64
	Limit   *float64
	Balance *float64
	Unit    string
	Reset   string
	Aside   bool
}

// FromMagpiePlugin maps a plugin probe into the shared account shape. It is the
// only place a plugin-derived account is built, so a plugin cannot report a
// percentage the UI would confuse with an amount: the same used/limit/balance
// rules as every built-in adapter apply here.
func FromMagpiePlugin(input MagpiePluginInput) Account {
	displayName := firstNonEmpty(input.DisplayName, input.ProviderID)
	account := Account{
		ID:          AccountID(input.ProviderID, "plugin"),
		Provider:    ProviderRef{ID: input.ProviderID, Kind: "plugin", DisplayName: displayName},
		DisplayName: displayName,
		Status:      StatusReady,
		Credential: Credential{
			Configured: false,
			Method:     joinMethods(input.Methods),
		},
		Plan: Plan{Cycle: Cycle{Kind: CycleNone}},
		Billing: Billing{
			Mode: BillingUnknown,
		},
		Models:    Models{Count: len(input.Models), Source: "plugin"},
		Source:    Source{Kind: "magpie-plugin", Ref: input.ProviderID},
		UpdatedAt: input.UpdatedAt,
	}
	walletBalance := 0.0
	hasWallet := false
	for _, window := range input.Windows {
		normalized := Window{
			ID:           safeWindowID(window.Name),
			Label:        window.Name,
			Kind:         CycleNone,
			SourceWindow: window.Name,
			Limit:        Money(window.Limit),
			Balance:      Money(window.Balance),
			Aside:        window.Aside,
			Source:       "plugin",
		}
		if window.Unit != "" {
			normalized.Unit = window.Unit
		}
		// A plugin may report a share (used of limit) or a bare balance. Both are
		// kept; only the share becomes a percentage.
		if window.Used != nil && window.Limit != nil && *window.Limit > 0 {
			normalized.UsedPercent = ShareOf(window.Used, window.Limit)
			normalized.RemainingPercent = RemainingFromUsed(normalized.UsedPercent)
		} else if window.Used != nil {
			// No denominator: read the number as an already-normalized percentage.
			normalized.UsedPercent = ClampPercent(window.Used)
			normalized.RemainingPercent = RemainingFromUsed(normalized.UsedPercent)
		}
		if window.Balance != nil {
			hasWallet = true
			walletBalance += *window.Balance
		}
		if resetAt, ok := parsePluginReset(window.Reset); ok {
			normalized.ResetAt = &resetAt
		}
		account.Quota = append(account.Quota, normalized)
	}
	if hasWallet {
		account.Billing = Billing{Mode: BillingWallet, Currency: "USD", Remaining: FloatPtr(walletBalance)}
	} else if len(account.Quota) > 0 {
		account.Billing = Billing{Mode: BillingMetered, Currency: "USD"}
	}
	// A warning is context, not a measurement: it rides on the status note so the
	// page can explain a partial probe without inventing a metric.
	if len(input.Warnings) > 0 {
		account.StatusNote = input.Warnings[0]
	}
	return account
}

func joinMethods(methods []string) string {
	joined := ""
	for _, method := range methods {
		if method == "" {
			continue
		}
		if joined != "" {
			joined += "+"
		}
		joined += method
	}
	return joined
}

func safeWindowID(name string) string {
	id := make([]rune, 0, len(name))
	for _, character := range name {
		switch {
		case character >= 'a' && character <= 'z', character >= '0' && character <= '9':
			id = append(id, character)
		case character >= 'A' && character <= 'Z':
			id = append(id, character+('a'-'A'))
		case character == '-' || character == '_':
			id = append(id, character)
		case character == ' ':
			id = append(id, '_')
		}
	}
	if len(id) == 0 {
		return "window"
	}
	return string(id)
}

func parsePluginReset(value string) (time.Time, bool) {
	if value == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339, time.RFC3339Nano, "2006-01-02T15:04:05Z0700"} {
		if parsed, errParse := time.Parse(layout, value); errParse == nil {
			return parsed.UTC(), true
		}
	}
	return time.Time{}, false
}
