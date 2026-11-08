package provider

import (
	"context"
	"sort"
	"strings"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

// Source is the manager-side data collection this package needs. The manager
// implements it over its existing Codex, OpenCode and Cline Pass services; the
// provider package stays free of host and network access so every normalization
// rule can be tested directly.
type AccountSource interface {
	// Accounts returns every account the plugin manages, already normalized by
	// the per-provider adapters.
	Accounts(ctx context.Context) ([]Account, error)
	// AccountByAuthIndex resolves the credential CPA named in a quota request.
	AccountByAuthIndex(ctx context.Context, authIndex string) (Account, bool, error)
}

// Service is the single read model behind the new provider API and the CPA
// quota capability.
type Service struct {
	source AccountSource
	// extra lists providers registered at runtime (imported Magpie/OpenCode
	// plugins). It is read on every call so a new import appears without a
	// restart and a removal disappears.
	extra func() []Definition
}

// NewService builds the read model. A nil source yields an empty service, which
// keeps handlers safe before the host has configured anything.
func NewService(source AccountSource) *Service {
	return &Service{source: source}
}

// SetExtraDefinitions registers the dynamic part of the registry.
func (s *Service) SetExtraDefinitions(extra func() []Definition) {
	if s != nil {
		s.extra = extra
	}
}

// Registry is the static definitions plus anything registered at runtime.
func (s *Service) Registry() []Definition {
	out := make([]Definition, 0, len(Definitions)+4)
	out = append(out, Definitions...)
	if s != nil && s.extra != nil {
		out = append(out, s.extra()...)
	}
	return out
}

// Providers lists every registered provider with its live account counts.
func (s *Service) Providers(ctx context.Context) []ProviderView {
	definitions := s.Registry()
	views := make([]ProviderView, 0, len(definitions))
	accounts := s.accounts(ctx)
	counts := map[string]map[Status]int{}
	for _, account := range accounts {
		if counts[account.Provider.ID] == nil {
			counts[account.Provider.ID] = map[Status]int{}
		}
		counts[account.Provider.ID][account.Status]++
	}
	for _, definition := range definitions {
		view := ProviderView{Definition: definition, ByStatus: counts[definition.ID]}
		if view.ByStatus == nil {
			view.ByStatus = map[Status]int{}
		}
		for _, count := range view.ByStatus {
			view.Accounts += count
		}
		views = append(views, view)
	}
	return views
}

// Accounts lists every normalized account, newest update first and stable by id.
func (s *Service) Accounts(ctx context.Context) []Account {
	accounts := s.accounts(ctx)
	sort.SliceStable(accounts, func(left, right int) bool {
		if !accounts[left].UpdatedAt.Equal(accounts[right].UpdatedAt) {
			return accounts[left].UpdatedAt.After(accounts[right].UpdatedAt)
		}
		return accounts[left].ID < accounts[right].ID
	})
	return accounts
}

// AccountsForProvider filters by provider id, accepting the registry ids only.
func (s *Service) AccountsForProvider(ctx context.Context, providerID string) []Account {
	filtered := make([]Account, 0)
	for _, account := range s.Accounts(ctx) {
		if account.Provider.ID == providerID {
			filtered = append(filtered, account)
		}
	}
	return filtered
}

// Account returns one account by its canonical id.
func (s *Service) Account(ctx context.Context, id string) (Account, bool) {
	for _, account := range s.accounts(ctx) {
		if account.ID == id {
			return account, true
		}
	}
	return Account{}, false
}

// Overview aggregates the dashboard numbers.
func (s *Service) Overview(ctx context.Context) Overview {
	accounts := s.accounts(ctx)
	overview := Overview{
		Accounts:  len(accounts),
		Providers: len(s.Registry()),
		ByStatus:  map[Status]int{},
		UpdatedAt: time.Now().UTC(),
	}
	for _, account := range accounts {
		overview.ByStatus[account.Status]++
		if account.Plan.Price != nil && account.Plan.Price.Currency == "USD" && account.Plan.Cycle.Kind != CycleNone {
			overview.PublicationUSD += account.Plan.Price.Amount
		}
	}
	return overview
}

// QuotaForAuthIndex answers CPA's quota.fetch. It never invents a window: an
// account with no reported quota still answers with its plan and summary, which
// is a valid Management Center card.
func (s *Service) QuotaForAuthIndex(ctx context.Context, authIndex string) (cpaapi.QuotaFetchResponse, bool, error) {
	if s == nil || s.source == nil || strings.TrimSpace(authIndex) == "" {
		return cpaapi.QuotaFetchResponse{}, false, nil
	}
	account, ok, errLookup := s.source.AccountByAuthIndex(ctx, strings.TrimSpace(authIndex))
	if errLookup != nil || !ok {
		return cpaapi.QuotaFetchResponse{}, false, errLookup
	}
	return QuotaResponse(account), true, nil
}

// SupportedQuotaProviders lists the provider keys this plugin can answer a
// quota request for. It is what quota.describe advertises.
func SupportedQuotaProviders() []string {
	return []string{"codex", "opencode-go", "opencode-zen", "cline-pass"}
}

func (s *Service) accounts(ctx context.Context) []Account {
	if s == nil || s.source == nil {
		return nil
	}
	accounts, errAccounts := s.source.Accounts(ctx)
	if errAccounts != nil {
		return nil
	}
	out := make([]Account, 0, len(accounts))
	for _, account := range accounts {
		if account.ID == "" {
			continue
		}
		out = append(out, account)
	}
	return out
}
