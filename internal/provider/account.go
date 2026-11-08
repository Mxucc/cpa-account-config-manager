// Package provider is the plugin's single provider domain: one registry of
// provider definitions and one normalized Account shape that every product
// surface (Codex, OpenCode Go, OpenCode Zen, Cline Pass, AI-provider channels and
// later Magpie plugins) is mapped into.
//
// The point is that billing, subscription cycles and quota windows are computed
// once, here, instead of in each page. Three rules keep the shape honest:
//
//   - A percentage and an amount are different facts. used_percent is present
//     only when the upstream reported a share; allowance/used/remaining are
//     present only when an amount is known. Unknown is null, never 0.
//   - A window keeps the provider's own window name in source_window, so a
//     consumer can still show "rolling" when the shared kind is "rolling".
//   - A subscription cycle is reported only when the provider states one. No
//     cycle is invented from a price or from a reset time.
package provider

import "time"

// Status is the single health vocabulary every provider maps into. The order is
// intentional: a credential problem outranks a quota limit, because the account
// is not merely slow, it cannot authenticate at all.
type Status string

const (
	StatusReady             Status = "ready"
	StatusQuotaLimited      Status = "quota_limited"
	StatusCredentialInvalid Status = "credential_invalid"
	StatusRepairing         Status = "repairing"
	StatusDisabled          Status = "disabled"
	StatusUnknown           Status = "unknown"
)

// Higher rank wins when several raw facts disagree.
func (s Status) rank() int {
	switch s {
	case StatusCredentialInvalid:
		return 5
	case StatusQuotaLimited:
		return 4
	case StatusRepairing:
		return 3
	case StatusDisabled:
		return 2
	case StatusReady:
		return 1
	default:
		return 0
	}
}

// Worse returns the status that should be shown when both facts hold.
func Worse(left, right Status) Status {
	if left.rank() >= right.rank() {
		return left
	}
	return right
}

// CycleKind is the subscription period as the provider describes it.
type CycleKind string

const (
	CycleNone    CycleKind = "none"
	CycleRolling CycleKind = "rolling"
	CycleWeekly  CycleKind = "weekly"
	CycleMonthly CycleKind = "monthly"
	CycleAnnual  CycleKind = "annual"
)

// BillingMode says how usage is charged.
type BillingMode string

const (
	BillingSubscription BillingMode = "subscription"
	BillingMetered      BillingMode = "metered"
	BillingWallet       BillingMode = "wallet"
	BillingUnknown      BillingMode = "unknown"
)

// ProviderRef identifies the provider an account belongs to.
type ProviderRef struct {
	ID          string `json:"id"`
	Kind        string `json:"kind,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
}

// Credential is everything a page may show about a credential. The credential
// itself is never part of this struct: only whether one exists, when it expires
// and whether the plugin can refresh it.
type Credential struct {
	Configured  bool       `json:"configured"`
	Method      string     `json:"method,omitempty"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	Expired     bool       `json:"expired,omitempty"`
	Refreshable bool       `json:"refreshable,omitempty"`
	Repairing   bool       `json:"repairing,omitempty"`
	Rejected    bool       `json:"rejected,omitempty"`
	// Reason is a bounded, credential-free explanation shown beside the state.
	Reason string `json:"reason,omitempty"`
}

// Price is a list price, not a charge. Source says where it came from so an
// operator can tell a catalog value from one the vendor reported.
type Price struct {
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
	Period   string  `json:"period,omitempty"`
	Source   string  `json:"source,omitempty"`
}

// Cycle is the subscription period. Every field is optional because providers
// differ: a monthly plan states renews_at, a rolling quota states resets_at, and
// a wallet states neither.
type Cycle struct {
	Kind     CycleKind  `json:"kind"`
	StartsAt *time.Time `json:"starts_at,omitempty"`
	RenewsAt *time.Time `json:"renews_at,omitempty"`
	ResetsAt *time.Time `json:"resets_at,omitempty"`
}

// Plan is the subscription behind an account.
type Plan struct {
	ID    string `json:"id,omitempty"`
	Name  string `json:"name,omitempty"`
	Tier  string `json:"tier,omitempty"`
	Cycle Cycle  `json:"cycle"`
	Price *Price `json:"price,omitempty"`
}

// Window is one quota window. UsedPercent and Limit are pointers so "not
// reported" cannot be confused with zero: the UI renders null as "not reported".
type Window struct {
	ID               string     `json:"id"`
	Label            string     `json:"label"`
	Kind             CycleKind  `json:"kind"`
	SourceWindow     string     `json:"source_window,omitempty"`
	UsedPercent      *float64   `json:"used_percent,omitempty"`
	RemainingPercent *float64   `json:"remaining_percent,omitempty"`
	Limit            *float64   `json:"limit,omitempty"`
	Unit             string     `json:"unit,omitempty"`
	Balance          *float64   `json:"balance,omitempty"`
	ResetAt          *time.Time `json:"reset_at,omitempty"`
	// Aside marks a window the routing layer ignores but a page still shows.
	Aside  bool   `json:"aside,omitempty"`
	Source string `json:"source,omitempty"`
	Note   string `json:"note,omitempty"`
}

// Billing is the money side of an account.
type Billing struct {
	Mode       BillingMode `json:"mode"`
	Currency   string      `json:"currency,omitempty"`
	Allowance  *float64    `json:"allowance,omitempty"`
	Used       *float64    `json:"used,omitempty"`
	Remaining  *float64    `json:"remaining,omitempty"`
	Period     string      `json:"period,omitempty"`
	RateSource string      `json:"rate_source,omitempty"`
}

// Usage is measured traffic, not allowance.
type Usage struct {
	InputTokens   int64      `json:"input_tokens"`
	OutputTokens  int64      `json:"output_tokens"`
	TotalTokens   int64      `json:"total_tokens"`
	CostUSD       float64    `json:"cost_usd"`
	LastRequestAt *time.Time `json:"last_request_at,omitempty"`
	UpdatedAt     *time.Time `json:"updated_at,omitempty"`
}

// Models is the catalog an account can reach.
type Models struct {
	Count     int        `json:"count"`
	Source    string     `json:"source,omitempty"`
	Error     string     `json:"error,omitempty"`
	FetchedAt *time.Time `json:"fetched_at,omitempty"`
}

// Binding is the CPA channel an account is published through, when it has one.
type Binding struct {
	Kind      string `json:"kind,omitempty"`
	Channel   string `json:"channel,omitempty"`
	Published bool   `json:"published"`
	Models    int    `json:"models,omitempty"`
	Gaps      int    `json:"gaps,omitempty"`
	Readable  bool   `json:"readable,omitempty"`
	Error     string `json:"error,omitempty"`
}

// Source says which CPA object backs an account.
type Source struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref,omitempty"`
}

// Metric is a bounded, provider-defined number a page may show beside the
// windows (a reset counter, a balance). It is not a quota window.
type Metric struct {
	Key      string  `json:"key"`
	Label    string  `json:"label"`
	Value    float64 `json:"value"`
	Unit     string  `json:"unit,omitempty"`
	Format   string  `json:"format,omitempty"`
	Currency string  `json:"currency,omitempty"`
}

// Account is the normalized account every page reads.
type Account struct {
	ID          string      `json:"id"`
	Provider    ProviderRef `json:"provider"`
	DisplayName string      `json:"display_name"`
	Status      Status      `json:"status"`
	StatusNote  string      `json:"status_note,omitempty"`
	Credential  Credential  `json:"credential"`
	Plan        Plan        `json:"plan"`
	Quota       []Window    `json:"quota"`
	Metrics     []Metric    `json:"metrics,omitempty"`
	Billing     Billing     `json:"billing"`
	Usage       Usage       `json:"usage"`
	Models      Models      `json:"models"`
	Binding     *Binding    `json:"binding,omitempty"`
	Source      Source      `json:"source"`
	UpdatedAt   time.Time   `json:"updated_at"`
}

// AccountID builds the stable, provider-scoped id a page may deep-link.
func AccountID(providerID, accountID string) string {
	return "provider:" + providerID + ":" + accountID
}

// Overview is the dashboard aggregate. Counts are by status, so an operator sees
// how many accounts cannot serve at a glance.
type Overview struct {
	Accounts       int            `json:"accounts"`
	ByStatus       map[Status]int `json:"by_status"`
	Providers      int            `json:"providers"`
	PublicationUSD float64        `json:"publication_usd"`
	UpdatedAt      time.Time      `json:"updated_at"`
}
