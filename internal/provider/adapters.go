package provider

import "time"

// Adapters. Each input struct is what the manager's existing services already
// know; an adapter only maps it into the shared shape and never calls a provider
// itself. Keeping data collection out of this package is what lets the whole
// normalization be tested without CPA or the network.

// WindowInput is one upstream window before normalization. Every fact is a
// pointer so an adapter can tell "not reported" from zero.
type WindowInput struct {
	UsedPercent      *float64
	RemainingPercent *float64
	Used             *float64
	Limit            *float64
	Balance          *float64
	ResetAt          *time.Time
	Unit             string
	Note             string
	Aside            bool
}

// usedPercent resolves the share from whichever fact the provider reported:
// a used percentage, a remaining percentage, or an amount against a limit.
func (w WindowInput) usedPercent() *float64 {
	if result := ClampPercent(w.UsedPercent); result != nil {
		return result
	}
	if result := UsedFromRemaining(w.RemainingPercent); result != nil {
		return result
	}
	return ShareOf(w.Used, w.Limit)
}

func (w WindowInput) toWindow(id, sourceWindow, label string) Window {
	window := NewWindow(id, sourceWindow, w.usedPercent())
	if label != "" {
		window.Label = label
	}
	window.Limit = Money(w.Limit)
	window.Balance = Money(w.Balance)
	window.ResetAt = w.ResetAt
	window.Aside = w.Aside
	window.Note = w.Note
	if w.Unit != "" {
		window.Unit = w.Unit
	}
	return window
}

// CodexInput is one CPA Codex credential.
type CodexInput struct {
	AccountID    string
	DisplayName  string
	Email        string
	PlanType     string
	Disabled     bool
	FiveHour     *WindowInput
	SevenDay     *WindowInput
	ActiveResets *int
	ExpiresAt    *time.Time
	Refreshable  bool
	Usage        Usage
	Models       Models
	UpdatedAt    time.Time
}

// FromCodex maps a Codex credential into the shared shape. Codex reports both
// windows as percentages; the plan is the ChatGPT plan type the host learned.
func FromCodex(input CodexInput, now time.Time) Account {
	definition, _ := DefinitionFor("codex")
	account := Account{
		ID:          AccountID(definition.ID, input.AccountID),
		Provider:    ProviderRef{ID: definition.ID, Kind: definition.Kind, DisplayName: definition.DisplayName},
		DisplayName: firstNonEmpty(input.DisplayName, input.Email, input.AccountID),
		Status:      StatusFromFacts(input.Disabled, false, false, false),
		Credential: Credential{
			Configured:  true,
			Method:      "oauth",
			ExpiresAt:   input.ExpiresAt,
			Expired:     Expired(input.ExpiresAt, now),
			Refreshable: input.Refreshable,
		},
		Usage:     input.Usage,
		Models:    input.Models,
		Source:    Source{Kind: "cpa-auth", Ref: input.AccountID},
		UpdatedAt: input.UpdatedAt,
	}
	account.Plan = Plan{
		ID:   input.PlanType,
		Name: planName(input.PlanType),
		Tier: input.PlanType,
		Cycle: Cycle{
			Kind: CycleMonthly,
		},
	}
	if input.FiveHour != nil {
		account.Quota = append(account.Quota, input.FiveHour.toWindow("five_hour", "five_hour", "5 hours"))
	}
	if input.SevenDay != nil {
		account.Quota = append(account.Quota, input.SevenDay.toWindow("seven_day", "seven_day", "7 days"))
	}
	if input.ActiveResets != nil {
		account.Metrics = append(account.Metrics, Metric{
			Key: "active_reset_count", Label: "Active reset credits", Value: float64(*input.ActiveResets),
		})
	}
	// The plan is a subscription only when the host named one; an unknown plan
	// type must not be presented as a paid cycle.
	if input.PlanType == "" {
		account.Plan.Cycle.Kind = CycleNone
	}
	account.Billing = Billing{Mode: BillingSubscription, Period: "month"}
	return account
}

// OpenCodeGoInput is one OpenCode Go workspace.
type OpenCodeGoInput struct {
	AccountID           string
	DisplayName         string
	BaseURL             string
	Rolling             *WindowInput
	Weekly              *WindowInput
	Monthly             *WindowInput
	MonthlyAllowanceUSD *float64
	UsedUSD             *float64
	Models              Models
	Credential          Credential
	UpdatedAt           time.Time
}

// FromOpenCodeGo maps a Go workspace. Go is a $10/month subscription whose
// allowance is split across the three windows; the split is reported as limits
// on each window, not invented from the price.
func FromOpenCodeGo(input OpenCodeGoInput, now time.Time) Account {
	definition, _ := DefinitionFor("opencode-go")
	credential := input.Credential
	if credential.ExpiresAt != nil {
		credential.Expired = Expired(credential.ExpiresAt, now)
	}
	account := Account{
		ID:          AccountID(definition.ID, input.AccountID),
		Provider:    ProviderRef{ID: definition.ID, Kind: definition.Kind, DisplayName: definition.DisplayName},
		DisplayName: firstNonEmpty(input.DisplayName, input.AccountID),
		Status:      StatusFromFacts(false, false, false, false),
		Credential:  credential,
		Models:      input.Models,
		Source:      Source{Kind: "plugin-store", Ref: input.AccountID},
		UpdatedAt:   input.UpdatedAt,
		Plan: Plan{
			ID:    "go",
			Name:  "OpenCode Go",
			Tier:  "go",
			Cycle: Cycle{Kind: CycleMonthly},
			Price: &Price{Amount: 10, Currency: "USD", Period: "month", Source: "official-pricing"},
		},
	}
	if input.Rolling != nil {
		account.Quota = append(account.Quota, withAllowanceShare(input.Rolling.toWindow("rolling", "rolling", "5 hours"), input.MonthlyAllowanceUSD, 0.2))
	}
	if input.Weekly != nil {
		account.Quota = append(account.Quota, withAllowanceShare(input.Weekly.toWindow("weekly", "weekly", "7 days"), input.MonthlyAllowanceUSD, 0.5))
	}
	if input.Monthly != nil {
		account.Quota = append(account.Quota, withAllowanceShare(input.Monthly.toWindow("monthly", "monthly", "Monthly"), input.MonthlyAllowanceUSD, 1))
	}
	account.Billing = Billing{
		Mode:      BillingSubscription,
		Currency:  "USD",
		Period:    "month",
		Allowance: Money(input.MonthlyAllowanceUSD),
		Used:      Money(input.UsedUSD),
	}
	if account.Billing.Allowance != nil && account.Billing.Used != nil {
		remaining := *account.Billing.Allowance - *account.Billing.Used
		account.Billing.Remaining = Money(&remaining)
	}
	return account
}

// withAllowanceShare records the monthly allowance a window can spend, when the
// provider's documented split is known. It never overrides a reported share.
func withAllowanceShare(window Window, monthlyAllowance *float64, fraction float64) Window {
	if monthlyAllowance == nil || *monthlyAllowance <= 0 {
		return window
	}
	limit := *monthlyAllowance * fraction
	window.Limit = Money(&limit)
	if window.Unit == "%" {
		window.Unit = "USD"
	}
	return window
}

// OpenCodeZenInput is one OpenCode Zen credential: a prepaid wallet.
type OpenCodeZenInput struct {
	AccountID   string
	DisplayName string
	BaseURL     string
	BalanceUSD  *float64
	Models      Models
	Credential  Credential
	UpdatedAt   time.Time
}

// FromOpenCodeZen maps a Zen credential. A wallet has a balance and no share of
// anything, so no percentage window is created when only a balance is known.
func FromOpenCodeZen(input OpenCodeZenInput, now time.Time) Account {
	definition, _ := DefinitionFor("opencode-zen")
	credential := input.Credential
	if credential.ExpiresAt != nil {
		credential.Expired = Expired(credential.ExpiresAt, now)
	}
	account := Account{
		ID:          AccountID(definition.ID, input.AccountID),
		Provider:    ProviderRef{ID: definition.ID, Kind: definition.Kind, DisplayName: definition.DisplayName},
		DisplayName: firstNonEmpty(input.DisplayName, input.AccountID),
		Status:      StatusFromFacts(false, false, false, false),
		Credential:  credential,
		Models:      input.Models,
		Source:      Source{Kind: "plugin-store", Ref: input.AccountID},
		UpdatedAt:   input.UpdatedAt,
		Plan:        Plan{Cycle: Cycle{Kind: CycleNone}},
		Billing: Billing{
			Mode:      BillingWallet,
			Currency:  "USD",
			Remaining: Money(input.BalanceUSD),
		},
	}
	if balance := Money(input.BalanceUSD); balance != nil {
		account.Quota = append(account.Quota, Window{
			ID:      "balance",
			Label:   "Balance",
			Kind:    CycleNone,
			Balance: balance,
			Unit:    "USD",
			Source:  "upstream",
		})
	}
	if input.BalanceUSD != nil && *input.BalanceUSD <= 0 {
		account.Status = Worse(account.Status, StatusQuotaLimited)
	}
	return account
}

// ClinePassInput is one Cline Pass account.
type ClinePassInput struct {
	AccountID       string
	DisplayName     string
	BaseURL         string
	Plan            string
	BalanceUSD      *float64
	FiveHour        *WindowInput
	Weekly          *WindowInput
	Monthly         *WindowInput
	LimitedUntil    *time.Time
	Credential      Credential
	Rejected        bool
	Repairing       bool
	Binding         *Binding
	Models          Models
	RefUsedUSD      *float64
	RefAllowanceUSD *float64
	UpdatedAt       time.Time
}

// FromClinePass maps a Cline Pass account, including the automatic-repair state
// that the old page had to explain itself: a rejected credential is invalid even
// while a repair is running, and both facts stay visible.
func FromClinePass(input ClinePassInput, now time.Time) Account {
	definition, _ := DefinitionFor("cline-pass")
	quotaLimited := input.LimitedUntil != nil && input.LimitedUntil.After(now)
	credential := input.Credential
	credential.Rejected = credential.Rejected || input.Rejected
	credential.Repairing = credential.Repairing || input.Repairing
	if credential.ExpiresAt != nil {
		credential.Expired = Expired(credential.ExpiresAt, now)
	}
	account := Account{
		ID:          AccountID(definition.ID, input.AccountID),
		Provider:    ProviderRef{ID: definition.ID, Kind: definition.Kind, DisplayName: definition.DisplayName},
		DisplayName: firstNonEmpty(input.DisplayName, input.AccountID),
		Status:      StatusFromFacts(false, input.Rejected, input.Repairing, quotaLimited),
		Credential:  credential,
		Models:      input.Models,
		Binding:     input.Binding,
		Source:      Source{Kind: "plugin-store", Ref: input.AccountID},
		UpdatedAt:   input.UpdatedAt,
		Plan: Plan{
			ID:    input.Plan,
			Tier:  input.Plan,
			Name:  planName(input.Plan),
			Cycle: Cycle{Kind: CycleMonthly},
		},
		Billing: Billing{
			Mode:      BillingSubscription,
			Currency:  "USD",
			Period:    "month",
			Allowance: Money(input.RefAllowanceUSD),
			Used:      Money(input.RefUsedUSD),
		},
	}
	if input.Plan == "" {
		account.Plan.Cycle.Kind = CycleNone
	}
	if account.Billing.Allowance != nil && account.Billing.Used != nil {
		remaining := *account.Billing.Allowance - *account.Billing.Used
		account.Billing.Remaining = Money(&remaining)
	}
	if input.FiveHour != nil {
		account.Quota = append(account.Quota, input.FiveHour.toWindow("five_hour", "5h", "5 hours"))
	}
	if input.Weekly != nil {
		account.Quota = append(account.Quota, input.Weekly.toWindow("weekly", "weekly", "7 days"))
	}
	if input.Monthly != nil {
		account.Quota = append(account.Quota, input.Monthly.toWindow("monthly", "monthly", "Monthly"))
	}
	if balance := Money(input.BalanceUSD); balance != nil {
		account.Metrics = append(account.Metrics, Metric{
			Key: "balance", Label: "Balance", Value: *balance, Unit: "USD", Format: "currency", Currency: "USD",
		})
	}
	if quotaLimited {
		account.StatusNote = "The gateway limited this account; it recovers after the window ends"
	}
	if input.Rejected {
		account.StatusNote = "The gateway rejected this credential; automatic repair is running"
	}
	return account
}

// AIProviderInput is one CPA AI-provider channel that is not a managed product.
type AIProviderInput struct {
	AccountID   string
	DisplayName string
	Kind        string
	Models      []string
	Usage       Usage
	UpdatedAt   time.Time
}

// FromAIProvider maps a plain channel. It has measured usage but no account-level
// quota window, so its quota list stays empty instead of showing a fake one.
func FromAIProvider(input AIProviderInput, now time.Time) Account {
	definition, _ := DefinitionFor("ai-provider")
	account := Account{
		ID:          AccountID(definition.ID, input.AccountID),
		Provider:    ProviderRef{ID: definition.ID, Kind: definition.Kind, DisplayName: definition.DisplayName},
		DisplayName: firstNonEmpty(input.DisplayName, input.AccountID),
		Status:      StatusUnknown,
		Credential:  Credential{Configured: true, Method: "api_key"},
		Models:      Models{Count: len(input.Models), Source: "channel"},
		Usage:       input.Usage,
		Source:      Source{Kind: "channel", Ref: input.Kind},
		UpdatedAt:   input.UpdatedAt,
		Plan:        Plan{Cycle: Cycle{Kind: CycleNone}},
		Billing:     Billing{Mode: BillingMetered, Currency: "USD"},
	}
	return account
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func planName(planType string) string {
	switch planType {
	case "plus":
		return "ChatGPT Plus"
	case "pro":
		return "ChatGPT Pro"
	case "team":
		return "ChatGPT Team"
	case "enterprise":
		return "ChatGPT Enterprise"
	case "free":
		return "ChatGPT Free"
	case "go":
		return "OpenCode Go"
	case "":
		return ""
	default:
		return planType
	}
}
