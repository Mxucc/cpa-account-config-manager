package manager

import (
	"context"
	"strings"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

// RuntimeAuthHost is an optional host capability. Older CPA versions may not
// implement host.auth.get_runtime; callers must gracefully fall back to the
// metadata returned by host.auth.list.
type RuntimeAuthHost interface {
	GetAuthRuntime(context.Context, string) (cpaapi.HostAuthFileEntry, error)
}

// CredentialSummary is the redacted credential view exposed to the management
// UI. It intentionally contains no auth JSON, access/refresh token, API key,
// cookies, authorization headers, or proxy credentials.
type CredentialSummary struct {
	ID             string     `json:"id"`
	AuthID         string     `json:"auth_id,omitempty"`
	Name           string     `json:"name,omitempty"`
	Provider       string     `json:"provider,omitempty"`
	Type           string     `json:"type,omitempty"`
	AccountType    string     `json:"account_type,omitempty"`
	PlanType       string     `json:"plan_type,omitempty"`
	Label          string     `json:"label,omitempty"`
	Email          string     `json:"email,omitempty"`
	ProjectID      string     `json:"project_id,omitempty"`
	AccountID      string     `json:"account_id,omitempty"`
	Status         string     `json:"status,omitempty"`
	StatusMessage  string     `json:"status_message,omitempty"`
	Disabled       bool       `json:"disabled"`
	Unavailable    bool       `json:"unavailable"`
	RuntimeOnly    bool       `json:"runtime_only"`
	Editable       bool       `json:"editable"`
	Source         string     `json:"source,omitempty"`
	PathAvailable  bool       `json:"path_available"`
	RuntimeLoaded  bool       `json:"runtime_loaded"`
	RuntimeError   string     `json:"runtime_error,omitempty"`
	Success        int64      `json:"success"`
	Failed         int64      `json:"failed"`
	Priority       *int       `json:"priority,omitempty"`
	Websockets     *bool      `json:"websockets,omitempty"`
	CreatedAt      *time.Time `json:"created_at,omitempty"`
	UpdatedAt      *time.Time `json:"updated_at,omitempty"`
	LastRefresh    *time.Time `json:"last_refresh,omitempty"`
	NextRetryAfter *time.Time `json:"next_retry_after,omitempty"`
}

func credentialSummaryFromAccount(account Account) CredentialSummary {
	return CredentialSummary{
		ID: account.ID, AuthID: account.AuthID, Name: account.Name,
		Provider: account.Provider, Type: account.Type, AccountType: account.AccountType,
		PlanType: account.PlanType, Label: account.Label, Email: account.Email, ProjectID: account.ProjectID,
		// Account.ID is CPA's stable auth index, not the upstream account ID.
		// Leave AccountID empty unless host metadata/runtime data supplied it.
		Status: account.Status, StatusMessage: account.StatusMessage,
		Disabled: account.Disabled, Unavailable: account.Unavailable, RuntimeOnly: account.RuntimeOnly,
		Editable: account.Editable, Source: account.Source, PathAvailable: account.path != "",
		Success: account.Success, Failed: account.Failed, Priority: cloneIntPointer(account.Priority),
		Websockets: cloneBoolPointer(account.Websockets), CreatedAt: cloneTimePointer(account.CreatedAt),
		UpdatedAt: cloneTimePointer(account.UpdatedAt), LastRefresh: cloneTimePointer(account.LastRefresh),
		NextRetryAfter: cloneTimePointer(account.NextRetryAfter),
	}
}

func (s *AccountService) CredentialSummary(ctx context.Context, rawID string) (CredentialSummary, error) {
	id := strings.TrimSpace(rawID)
	if id == "" {
		return CredentialSummary{}, ErrAccountConfigNotFound
	}
	accounts, err := s.baseAccounts(ctx)
	if err != nil {
		return CredentialSummary{}, err
	}
	var account *Account
	for i := range accounts {
		if accounts[i].ID == id || accounts[i].AuthID == id || accounts[i].Name == id {
			account = &accounts[i]
			break
		}
	}
	if account == nil {
		return CredentialSummary{}, ErrAccountConfigNotFound
	}
	// Credential details are an explicit, on-demand operation. Do not make
	// account-list requests fan out into one runtime callback per account.
	s.enrichAccountDetail(ctx, account)
	summary := credentialSummaryFromAccount(*account)
	s.enrichRuntimeCredential(ctx, account, &summary)
	return summary, nil
}

func (s *AccountService) enrichRuntimeCredential(ctx context.Context, account *Account, summary *CredentialSummary) {
	if summary == nil || account == nil {
		return
	}
	if runtimeHost, ok := s.host.(RuntimeAuthHost); ok && strings.TrimSpace(account.detailAuthIndex) != "" {
		runtime, runtimeErr := runtimeHost.GetAuthRuntime(ctx, account.detailAuthIndex)
		if runtimeErr != nil {
			summary.RuntimeError = sanitizeCredentialRuntimeError(runtimeErr)
			return
		}
		summary.RuntimeLoaded = true
		applyRuntimeCredentialSummary(summary, runtime)
		return
	}
	summary.RuntimeError = "runtime credential details require CPA schema v2"
}

func applyRuntimeCredentialSummary(summary *CredentialSummary, runtime cpaapi.HostAuthFileEntry) {
	if summary == nil {
		return
	}
	if value := strings.TrimSpace(runtime.ID); value != "" {
		summary.ID = value
	}
	if value := strings.TrimSpace(runtime.AuthIndex); value != "" {
		summary.AuthID = value
	}
	if value := strings.TrimSpace(runtime.Name); value != "" {
		summary.Name = value
	}
	if value := strings.TrimSpace(runtime.Provider); value != "" {
		summary.Provider = value
	}
	if value := strings.TrimSpace(runtime.Type); value != "" {
		summary.Type = value
	}
	if value := strings.TrimSpace(runtime.AccountType); value != "" {
		summary.AccountType = value
	}
	if value := strings.TrimSpace(runtime.PlanType); value != "" {
		summary.PlanType = value
	}
	if value := strings.TrimSpace(runtime.Label); value != "" {
		summary.Label = value
	}
	if value := strings.TrimSpace(runtime.Email); value != "" {
		summary.Email = value
	}
	if value := strings.TrimSpace(runtime.ProjectID); value != "" {
		summary.ProjectID = value
	}
	if value := strings.TrimSpace(runtime.Account); value != "" {
		summary.AccountID = value
	}
	if value := strings.TrimSpace(runtime.Status); value != "" {
		summary.Status = value
	}
	if value := strings.TrimSpace(runtime.StatusMessage); value != "" {
		summary.StatusMessage = value
	}
	summary.Disabled, summary.Unavailable, summary.RuntimeOnly = runtime.Disabled, runtime.Unavailable, runtime.RuntimeOnly
	summary.Source, summary.PathAvailable = runtime.Source, strings.TrimSpace(runtime.Path) != ""
	summary.Success, summary.Failed, summary.Priority, summary.Websockets = runtime.Success, runtime.Failed, intPointerIfNonZero(runtime.Priority), runtimeBoolPointer(runtime.Websockets)
	summary.CreatedAt, summary.UpdatedAt, summary.LastRefresh, summary.NextRetryAfter = timePointer(runtime.CreatedAt), timePointer(runtime.UpdatedAt), timePointer(runtime.LastRefresh), timePointer(runtime.NextRetryAfter)
}

func sanitizeCredentialRuntimeError(err error) string {
	if err == nil {
		return ""
	}
	message := strings.TrimSpace(err.Error())
	if len(message) > 240 {
		message = message[:240]
	}
	// Never echo a token-like value if a host accidentally includes one in an error.
	if strings.Contains(strings.ToLower(message), "token") || strings.Contains(strings.ToLower(message), "secret") || strings.Contains(strings.ToLower(message), "apikey") {
		return "runtime credential details unavailable"
	}
	return message
}

func intPointerIfNonZero(value int) *int {
	if value == 0 {
		return nil
	}
	return &value
}
func runtimeBoolPointer(value bool) *bool { return &value }
