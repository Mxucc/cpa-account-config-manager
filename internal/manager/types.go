package manager

import "time"

type Account struct {
	ID              string                     `json:"id"`
	AuthID          string                     `json:"auth_id,omitempty"`
	Name            string                     `json:"name"`
	Provider        string                     `json:"provider,omitempty"`
	Type            string                     `json:"type,omitempty"`
	Label           string                     `json:"label,omitempty"`
	Email           string                     `json:"email,omitempty"`
	ProjectID       string                     `json:"project_id,omitempty"`
	AccountType     string                     `json:"account_type,omitempty"`
	PlanType        string                     `json:"plan_type,omitempty"`
	Status          string                     `json:"status,omitempty"`
	StatusMessage   string                     `json:"status_message,omitempty"`
	Disabled        bool                       `json:"disabled"`
	Unavailable     bool                       `json:"unavailable"`
	RuntimeOnly     bool                       `json:"runtime_only"`
	Source          string                     `json:"source,omitempty"`
	Priority        *int                       `json:"priority,omitempty"`
	Note            string                     `json:"note,omitempty"`
	Prefix          string                     `json:"prefix,omitempty"`
	Proxy           string                     `json:"proxy,omitempty"`
	ProxyConfigured bool                       `json:"proxy_configured"`
	Websockets      *bool                      `json:"websockets,omitempty"`
	HeaderNames     []string                   `json:"header_names,omitempty"`
	HeaderCount     int                        `json:"header_count"`
	Editable        bool                       `json:"editable"`
	ReadOnlyReason  string                     `json:"read_only_reason,omitempty"`
	Success         int64                      `json:"success"`
	Failed          int64                      `json:"failed"`
	RecentRequests  []RecentRequestEntry       `json:"recent_requests,omitempty"`
	NextRetryAfter  *time.Time                 `json:"next_retry_after,omitempty"`
	Usage           *AccountUsageSnapshot      `json:"usage,omitempty"`
	CreatedAt       *time.Time                 `json:"created_at,omitempty"`
	DisabledAt      *time.Time                 `json:"disabled_at,omitempty"`
	UpdatedAt       *time.Time                 `json:"updated_at,omitempty"`
	LastRefresh     *time.Time                 `json:"last_refresh,omitempty"`
	Automation      *AccountAutomationSummary  `json:"automation,omitempty"`
	ModelPolicy     *AccountModelPolicySummary `json:"model_policy,omitempty"`
	Concurrency     AccountConcurrencySummary  `json:"concurrency"`

	detailAuthIndex string
	path            string
	revision        string
	usageIdentity   string
}

type RecentRequestEntry struct {
	Time    string `json:"time"`
	Success int64  `json:"success"`
	Failed  int64  `json:"failed"`
}

type AccountFilters struct {
	Provider    string `json:"provider,omitempty"`
	Type        string `json:"type,omitempty"`
	Status      string `json:"status,omitempty"`
	Disabled    *bool  `json:"disabled,omitempty"`
	Editability string `json:"editability,omitempty"`
	Source      string `json:"source,omitempty"`
	Search      string `json:"search,omitempty"`
}

type AccountSortField string

const (
	AccountSortAccount          AccountSortField = "account"
	AccountSortProvider         AccountSortField = "provider"
	AccountSortType             AccountSortField = "type"
	AccountSortUsage            AccountSortField = "usage"
	AccountSortActiveResetCount AccountSortField = "active_reset_count"
	AccountSortConcurrency      AccountSortField = "concurrency"
	AccountSortCreatedAt        AccountSortField = "created_at"
	AccountSortDisabledAt       AccountSortField = "disabled_at"
	AccountSortAccess           AccountSortField = "access"
	AccountSortStatus           AccountSortField = "status"
	AccountSortPriority         AccountSortField = "priority"
	AccountSortRouting          AccountSortField = "routing"
)

type AccountSortOrder string

const (
	AccountSortAscending  AccountSortOrder = "asc"
	AccountSortDescending AccountSortOrder = "desc"
)

type ListQuery struct {
	Page      int
	PageSize  int
	Filters   AccountFilters
	SortBy    AccountSortField
	SortOrder AccountSortOrder
}

type ListResponse struct {
	Accounts           []Account                      `json:"accounts"`
	Total              int                            `json:"total"`
	Page               int                            `json:"page"`
	PageSize           int                            `json:"page_size"`
	Pages              int                            `json:"pages"`
	AccountConcurrency AccountConcurrencyAvailability `json:"account_concurrency"`
}
