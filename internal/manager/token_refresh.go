package manager

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

var (
	ErrAccountTokenRefreshNotFound          = errors.New("account was not found")
	ErrAccountTokenRefreshReadOnly          = errors.New("account is read-only and cannot refresh credentials")
	ErrAccountTokenRefreshBusy              = errors.New("credential refresh is already running for this account")
	ErrAccountTokenRefreshUnsupported       = errors.New("manual token refresh requires a newer CPA version")
	ErrAccountTokenRefreshCredentialMissing = errors.New("refresh token is missing")
	ErrAccountTokenRefreshRejected          = errors.New("credential refresh was rejected; sign in again")
	ErrAccountTokenRefreshFailed            = errors.New("CPA failed to refresh the account credential")
)

type AccountTokenRefreshRequest struct {
	AccountID string `json:"account_id"`
}

type AccountTokenRefreshResult struct {
	AccountID           string     `json:"account_id"`
	Provider            string     `json:"provider,omitempty"`
	RefreshedAt         time.Time  `json:"refreshed_at"`
	ExpiresAt           *time.Time `json:"expires_at,omitempty"`
	RefreshTokenRotated bool       `json:"refresh_token_rotated"`
}

type accountTokenRefreshHost interface {
	RefreshHostAuth(context.Context, string) (cpaapi.HostAuthRefreshResponse, error)
}

type AccountTokenRefreshService struct {
	accounts *AccountService
	host     accountTokenRefreshHost
	locks    sync.Map
}

func NewAccountTokenRefreshService(accounts *AccountService, host AuthHost) *AccountTokenRefreshService {
	refreshHost, _ := host.(accountTokenRefreshHost)
	return &AccountTokenRefreshService{accounts: accounts, host: refreshHost}
}

func (s *AccountTokenRefreshService) Refresh(ctx context.Context, request AccountTokenRefreshRequest) (AccountTokenRefreshResult, error) {
	if s == nil || s.accounts == nil {
		return AccountTokenRefreshResult{}, fmt.Errorf("account token refresh service is unavailable")
	}
	accountID := strings.TrimSpace(request.AccountID)
	if accountID == "" || len(accountID) > 256 {
		return AccountTokenRefreshResult{}, fmt.Errorf("account_id is required and must be at most 256 characters")
	}
	resolved, errResolve := s.accounts.ResolveTargets(ctx, TargetScope{Mode: "selected", IDs: []string{accountID}})
	if errResolve != nil {
		return AccountTokenRefreshResult{}, fmt.Errorf("resolve account for token refresh: %w", errResolve)
	}
	if len(resolved.MissingIDs) != 0 || len(resolved.Accounts) != 1 {
		return AccountTokenRefreshResult{}, ErrAccountTokenRefreshNotFound
	}
	account := resolved.Accounts[0]
	if !account.Editable || account.path == "" {
		return AccountTokenRefreshResult{}, ErrAccountTokenRefreshReadOnly
	}
	if s.host == nil {
		return AccountTokenRefreshResult{}, ErrAccountTokenRefreshUnsupported
	}

	lockValue, _ := s.locks.LoadOrStore(account.ID, &sync.Mutex{})
	lock, _ := lockValue.(*sync.Mutex)
	if lock == nil || !lock.TryLock() {
		return AccountTokenRefreshResult{}, ErrAccountTokenRefreshBusy
	}
	defer lock.Unlock()

	response, errRefresh := s.host.RefreshHostAuth(ctx, account.ID)
	if errRefresh != nil {
		return AccountTokenRefreshResult{}, classifyHostTokenRefreshError(errRefresh)
	}
	refreshedAt := response.RefreshedAt.UTC()
	if refreshedAt.IsZero() {
		return AccountTokenRefreshResult{}, ErrAccountTokenRefreshFailed
	}
	provider := safeTechnicalValue(account.Provider, 64)
	if provider == "" {
		provider = safeTechnicalValue(response.Provider, 64)
	}
	return AccountTokenRefreshResult{
		AccountID:           account.ID,
		Provider:            provider,
		RefreshedAt:         refreshedAt,
		ExpiresAt:           normalizedOptionalTime(response.ExpiresAt),
		RefreshTokenRotated: response.RefreshTokenRotated,
	}, nil
}

func classifyHostTokenRefreshError(err error) error {
	message := strings.ToLower(strings.TrimSpace(err.Error()))
	switch {
	case strings.Contains(message, "unsupported host callback"), strings.Contains(message, "host auth refresh is unavailable"):
		return ErrAccountTokenRefreshUnsupported
	case strings.Contains(message, "refresh token") && strings.Contains(message, "missing"):
		return ErrAccountTokenRefreshCredentialMissing
	case strings.Contains(message, "invalid_grant"), strings.Contains(message, "unauthorized"), strings.Contains(message, "revoked"):
		return ErrAccountTokenRefreshRejected
	case strings.Contains(message, "auth not found"):
		return ErrAccountTokenRefreshNotFound
	default:
		return ErrAccountTokenRefreshFailed
	}
}

func normalizedOptionalTime(value *time.Time) *time.Time {
	if value == nil || value.IsZero() {
		return nil
	}
	normalized := value.UTC()
	return &normalized
}

func safeTechnicalValue(value string, limit int) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > limit {
		return ""
	}
	for _, character := range value {
		if character < 0x21 || character > 0x7e {
			return ""
		}
	}
	return value
}
