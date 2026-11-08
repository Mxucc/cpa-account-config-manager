package manager

import (
	"context"
	"strings"
	"time"

	"cpa-account-config-manager/internal/provider"
)

// providerSource is the manager-side half of the unified provider domain: it
// reads the existing Codex, OpenCode and Cline Pass services and hands their
// facts to the adapters. No business rule lives here; only translation.
type providerSource struct {
	app *App
}

func newProviderSource(app *App) provider.AccountSource {
	return providerSource{app: app}
}

// Accounts collects every account the plugin manages.
func (s providerSource) Accounts(ctx context.Context) ([]provider.Account, error) {
	now := time.Now().UTC()
	out := make([]provider.Account, 0)
	if s.app == nil {
		return out, nil
	}
	if s.app.accounts != nil {
		accounts, errAccounts := s.app.accounts.ListAllAccounts(ctx)
		if errAccounts != nil {
			return nil, errAccounts
		}
		for _, account := range accounts {
			if !isCodexAccount(account) {
				continue
			}
			out = append(out, provider.FromCodex(codexInput(account), now))
		}
	}
	if s.app.opencode != nil {
		snapshot := s.app.opencode.Snapshot()
		for _, view := range snapshot.Accounts {
			out = append(out, provider.FromOpenCodeGo(openCodeGoInput(view, snapshot.Results[view.ID]), now))
		}
	}
	if s.app.opencodeZen != nil {
		for _, view := range s.app.opencodeZen.ListAccounts() {
			out = append(out, provider.FromOpenCodeZen(openCodeZenInput(view), now))
		}
	}
	if s.app.clinePass != nil {
		for _, view := range s.app.clinePass.ListAccounts() {
			out = append(out, provider.FromClinePass(clinePassInput(view), now))
		}
	}
	// Imported Magpie/OpenCode plugins contribute accounts only when hook
	// execution is enabled; otherwise the registry still lists them, with no probe
	// and no accounts.
	out = append(out, s.app.importedPluginAccounts(ctx)...)
	return out, nil
}

// AccountByAuthIndex resolves the credential CPA names in a quota request. It
// answers only when one account really owns that index; guessing would put
// another account's allowance on the card.
func (s providerSource) AccountByAuthIndex(ctx context.Context, authIndex string) (provider.Account, bool, error) {
	if s.app == nil {
		return provider.Account{}, false, nil
	}
	now := time.Now().UTC()
	index := strings.TrimSpace(authIndex)
	if index == "" {
		return provider.Account{}, false, nil
	}
	if s.app.accounts != nil {
		accounts, errAccounts := s.app.accounts.ListAllAccounts(ctx)
		if errAccounts == nil {
			for _, account := range accounts {
				if !isCodexAccount(account) {
					continue
				}
				if strings.TrimSpace(account.AuthID) == index || strings.TrimSpace(account.ID) == index {
					return provider.FromCodex(codexInput(account), now), true, nil
				}
			}
		}
	}
	if s.app.clinePass != nil {
		if accountID := s.app.clinePass.RouteAccountForAuthIndex(index); accountID != "" {
			for _, view := range s.app.clinePass.ListAccounts() {
				if view.ID == accountID {
					return provider.FromClinePass(clinePassInput(view), now), true, nil
				}
			}
		}
	}
	return provider.Account{}, false, nil
}

func isCodexAccount(account Account) bool {
	for _, value := range []string{account.Provider, account.Type} {
		if strings.Contains(strings.ToLower(value), "codex") {
			return true
		}
	}
	return false
}

func codexInput(account Account) provider.CodexInput {
	input := provider.CodexInput{
		AccountID:   firstNonEmptyValue(account.AuthID, account.ID),
		DisplayName: firstNonEmptyValue(account.Label, account.Name),
		Email:       account.Email,
		PlanType:    account.PlanType,
		Disabled:    account.Disabled,
		Refreshable: true,
		Models:      provider.Models{Count: 0, Source: "cpa"},
	}
	if account.Usage != nil {
		input.Usage = provider.Usage{
			InputTokens:   account.Usage.InputTokens,
			OutputTokens:  account.Usage.OutputTokens,
			TotalTokens:   account.Usage.TotalTokens,
			LastRequestAt: account.Usage.LastRequestAt,
			UpdatedAt:     account.Usage.UpdatedAt,
		}
		if account.Usage.Codex != nil {
			codex := account.Usage.Codex
			input.PlanType = firstNonEmptyValue(codex.PlanType, input.PlanType)
			input.ActiveResets = codex.ActiveResetCount
			input.UpdatedAt = codex.ObservedAt
			if codex.FiveHour != nil {
				used := codex.FiveHour.UsedPercent
				input.FiveHour = &provider.WindowInput{UsedPercent: &used, ResetAt: codex.FiveHour.ResetAt}
			}
			if codex.SevenDay != nil {
				used := codex.SevenDay.UsedPercent
				input.SevenDay = &provider.WindowInput{UsedPercent: &used, ResetAt: codex.SevenDay.ResetAt}
			}
		}
	}
	if input.UpdatedAt.IsZero() && account.UpdatedAt != nil {
		input.UpdatedAt = *account.UpdatedAt
	}
	return input
}

func openCodeGoInput(view OpenCodeAccountView, result *OpenCodeQuotaResult) provider.OpenCodeGoInput {
	input := provider.OpenCodeGoInput{
		AccountID:   view.ID,
		DisplayName: view.WorkspaceID,
		BaseURL:     view.BaseURL,
		Models: provider.Models{
			Count:     len(view.Models),
			Source:    "upstream",
			Error:     view.ModelsError,
			FetchedAt: optionalTime(view.ModelsFetchedAt),
		},
		Credential: provider.Credential{
			Configured:  view.KeySet || view.CookieSet,
			Method:      openCodeCredentialMethod(view),
			Refreshable: view.KeySet || view.CookieSet,
		},
		UpdatedAt: view.ModelsFetchedAt,
	}
	if result != nil && result.Success {
		if result.Rolling != nil {
			used := result.Rolling.UsagePercent
			input.Rolling = &provider.WindowInput{UsedPercent: &used, ResetAt: optionalTime(result.Rolling.ResetAt)}
		}
		if result.Weekly != nil {
			used := result.Weekly.UsagePercent
			input.Weekly = &provider.WindowInput{UsedPercent: &used, ResetAt: optionalTime(result.Weekly.ResetAt)}
		}
		if result.Monthly != nil {
			used := result.Monthly.UsagePercent
			input.Monthly = &provider.WindowInput{UsedPercent: &used, ResetAt: optionalTime(result.Monthly.ResetAt)}
		}
		input.UpdatedAt = result.FetchedAt
	}
	return input
}

func openCodeZenInput(view OpenCodeZenAccountView) provider.OpenCodeZenInput {
	return provider.OpenCodeZenInput{
		AccountID:   view.ID,
		DisplayName: firstNonEmptyValue(view.Name, view.BaseURL),
		BaseURL:     view.BaseURL,
		Models: provider.Models{
			Count:     len(view.Models),
			Source:    "upstream",
			Error:     view.ModelsError,
			FetchedAt: optionalTime(view.ModelsFetchedAt),
		},
		Credential: provider.Credential{Configured: view.KeySet, Method: "api_key", Refreshable: view.KeySet},
		UpdatedAt:  view.ModelsFetchedAt,
	}
}

func clinePassInput(view ClinePassAccountView) provider.ClinePassInput {
	allowance := view.QuotaUsage.MonthlySubscriptionUSD
	monthlyUsed := view.QuotaUsage.Monthly.USD
	input := provider.ClinePassInput{
		AccountID:       view.ID,
		DisplayName:     firstNonEmptyValue(view.Name, view.BaseURL),
		BaseURL:         view.BaseURL,
		BalanceUSD:      nil,
		LimitedUntil:    view.QuotaLimitedUntil,
		Rejected:        view.ChannelCredentialRejected,
		RefUsedUSD:      optionalFloat(monthlyUsed),
		RefAllowanceUSD: optionalFloat(allowance),
		Models: provider.Models{
			Count:     len(view.Models),
			Source:    "upstream",
			Error:     view.ModelsError,
			FetchedAt: optionalTime(view.ModelsFetchedAt),
		},
		Credential: provider.Credential{
			Configured:  view.AccessTokenSet,
			Method:      view.AuthMethod,
			ExpiresAt:   view.ExpiresAt,
			Refreshable: view.RefreshTokenSet,
			Rejected:    view.ChannelCredentialRejected,
		},
		Binding: &provider.Binding{
			Kind:      "openai-compatibility",
			Published: view.ChannelBound,
			Models:    view.ChannelModels,
			Gaps:      view.ChannelModelGaps,
			Readable:  !view.ChannelStateUnreadable,
			Error:     view.ChannelBindingError,
		},
		UpdatedAt: view.CreatedAt,
	}
	if view.QuotaUsage.Reference {
		input.FiveHour = clinePassWindow(view.QuotaUsage.FiveHour, allowance*0.2)
		input.Weekly = clinePassWindow(view.QuotaUsage.Weekly, allowance*0.5)
		input.Monthly = clinePassWindow(view.QuotaUsage.Monthly, allowance)
	}
	return input
}

// clinePassWindow turns one reference-priced window into an amount window. The
// money is a list-value estimate, which the note says; the documented split only
// fills the limit, never a percentage the vendor did not report.
func clinePassWindow(usage ClinePassQuotaWindowUsage, limit float64) *provider.WindowInput {
	used := usage.USD
	window := &provider.WindowInput{Used: &used, Unit: "USD", Note: "Reference-priced usage"}
	if limit > 0 {
		window.Limit = &limit
	}
	return window
}

func openCodeCredentialMethod(view OpenCodeAccountView) string {
	switch {
	case view.KeySet && view.CookieSet:
		return "cookie+api_key"
	case view.KeySet:
		return "api_key"
	case view.CookieSet:
		return "cookie"
	default:
		return ""
	}
}

func optionalTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	copied := value
	return &copied
}

func optionalFloat(value float64) *float64 {
	if value == 0 {
		return nil
	}
	copied := value
	return &copied
}

func firstNonEmptyValue(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
