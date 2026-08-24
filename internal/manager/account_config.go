package manager

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"cpa-account-config-manager/internal/cpaapi"
)

const maxAccountConfigIDLength = 512

var (
	ErrAccountConfigNotFound = errors.New("account configuration was not found")
	ErrAccountConfigReadOnly = errors.New("account configuration is read-only")
)

type AccountConfigRequest struct {
	AccountID string `json:"account_id"`
}

type AccountEditableConfig struct {
	AccountID               string                         `json:"account_id"`
	Disabled                bool                           `json:"disabled"`
	Priority                *int                           `json:"priority"`
	Note                    string                         `json:"note"`
	Prefix                  string                         `json:"prefix"`
	Proxy                   string                         `json:"proxy"`
	ProxyConfigured         bool                           `json:"proxy_configured"`
	Websockets              *bool                          `json:"websockets"`
	HeaderNames             []string                       `json:"header_names"`
	ModelPolicy             *AccountModelPolicySummary     `json:"model_policy"`
	Concurrency             AccountConcurrencySummary      `json:"concurrency"`
	ConcurrencyAvailability AccountConcurrencyAvailability `json:"account_concurrency"`
	Credential              *CredentialSummary             `json:"credential,omitempty"`
}

func (s *AccountService) EditableConfig(ctx context.Context, rawAccountID string) (AccountEditableConfig, error) {
	accountID := strings.TrimSpace(rawAccountID)
	if accountID == "" || len(accountID) > maxAccountConfigIDLength {
		return AccountEditableConfig{}, ErrAccountConfigNotFound
	}
	scope, errScope := (TargetScope{Mode: "selected", IDs: []string{accountID}}).Validate()
	if errScope != nil {
		return AccountEditableConfig{}, ErrAccountConfigNotFound
	}
	resolved, errResolve := s.ResolveTargets(ctx, scope)
	if errResolve != nil {
		return AccountEditableConfig{}, errResolve
	}
	if len(resolved.Accounts) != 1 || len(resolved.MissingIDs) != 0 {
		return AccountEditableConfig{}, ErrAccountConfigNotFound
	}
	account := resolved.Accounts[0]
	if !account.Editable {
		return AccountEditableConfig{}, ErrAccountConfigReadOnly
	}
	credential := credentialSummaryFromAccount(account)
	s.enrichRuntimeCredential(ctx, &account, &credential)
	return AccountEditableConfig{
		AccountID:               account.ID,
		Disabled:                account.Disabled,
		Priority:                cloneIntPointer(account.Priority),
		Note:                    account.Note,
		Prefix:                  account.Prefix,
		Proxy:                   account.Proxy,
		ProxyConfigured:         account.ProxyConfigured,
		Websockets:              cloneBoolPointer(account.Websockets),
		HeaderNames:             append([]string{}, account.HeaderNames...),
		ModelPolicy:             cloneAccountModelPolicySummary(account.ModelPolicy),
		Concurrency:             account.Concurrency,
		ConcurrencyAvailability: s.accountConcurrencyAvailability(),
		Credential:              &credential,
	}, nil
}

func (a *App) handleAccountConfig(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	var request AccountConfigRequest
	if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errDecode.Error()})
	}
	config, errConfig := a.accounts.EditableConfig(ctx, request.AccountID)
	if errConfig != nil {
		switch {
		case errors.Is(errConfig, ErrAccountConfigNotFound):
			return jsonResponse(http.StatusNotFound, map[string]any{"error": ErrAccountConfigNotFound.Error()})
		case errors.Is(errConfig, ErrAccountConfigReadOnly):
			return jsonResponse(http.StatusConflict, map[string]any{"error": ErrAccountConfigReadOnly.Error()})
		default:
			return jsonResponse(http.StatusBadGateway, map[string]any{"error": "failed to load account configuration"})
		}
	}
	response := jsonResponse(http.StatusOK, config)
	response.Headers.Set("Cache-Control", "no-store")
	return response
}
