package manager

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

var (
	ErrAccountTokenRefreshNotFound            = errors.New("account was not found")
	ErrAccountTokenRefreshReadOnly            = errors.New("account is read-only and cannot refresh credentials")
	ErrAccountTokenRefreshBusy                = errors.New("credential refresh is already running for this account")
	ErrAccountTokenRefreshUnsupported         = errors.New("manual token refresh requires a newer CPA version")
	ErrAccountTokenRefreshProviderUnsupported = errors.New("account provider does not support plugin-side token refresh")
	ErrAccountTokenRefreshCredentialMissing   = errors.New("refresh token is missing")
	ErrAccountTokenRefreshRejected            = errors.New("credential refresh was rejected; sign in again")
	ErrAccountTokenRefreshConflict            = errors.New("account credential changed while refresh was running; retry with the latest credential")
	ErrAccountTokenRefreshVerification        = errors.New("credential was saved but could not be verified; reload the account before retrying")
	ErrAccountTokenRefreshFailed              = errors.New("failed to refresh account credential")
)

const maxTokenRefreshAuthJSONBytes = 4 << 20

type AccountTokenRefreshRequest struct {
	AccountID string `json:"account_id"`
}

type AccountTokenRefreshResult struct {
	AccountID           string     `json:"account_id"`
	Provider            string     `json:"provider,omitempty"`
	RefreshSource       string     `json:"refresh_source"`
	RefreshedAt         time.Time  `json:"refreshed_at"`
	ExpiresAt           *time.Time `json:"expires_at,omitempty"`
	RefreshTokenRotated bool       `json:"refresh_token_rotated"`
}

type accountTokenRefreshHost interface {
	RefreshHostAuth(context.Context, string) (cpaapi.HostAuthRefreshResponse, error)
}

type AccountTokenRefreshService struct {
	accounts   *AccountService
	authHost   AuthHost
	nativeHost accountTokenRefreshHost
	exchanger  tokenRefreshExchanger
	now        func() time.Time
	locksMu    sync.Mutex
	active     map[string]struct{}
}

func NewAccountTokenRefreshService(accounts *AccountService, host AuthHost) *AccountTokenRefreshService {
	refreshHost, _ := host.(accountTokenRefreshHost)
	return &AccountTokenRefreshService{
		accounts: accounts, authHost: host, nativeHost: refreshHost, exchanger: newCodexTokenRefreshExchanger(),
		now: time.Now, active: make(map[string]struct{}),
	}
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
	if !s.acquire(account.ID) {
		return AccountTokenRefreshResult{}, ErrAccountTokenRefreshBusy
	}
	defer s.release(account.ID)

	if s.nativeHost != nil {
		response, errRefresh := s.nativeHost.RefreshHostAuth(ctx, account.ID)
		if errRefresh == nil {
			return nativeTokenRefreshResult(account, response)
		}
		if classified := classifyHostTokenRefreshError(errRefresh); !errors.Is(classified, ErrAccountTokenRefreshUnsupported) {
			return AccountTokenRefreshResult{}, classified
		}
	}
	return s.refreshWithProvider(ctx, account)
}

func nativeTokenRefreshResult(account Account, response cpaapi.HostAuthRefreshResponse) (AccountTokenRefreshResult, error) {
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
		RefreshSource:       "cpa_native",
		RefreshedAt:         refreshedAt,
		ExpiresAt:           normalizedOptionalTime(response.ExpiresAt),
		RefreshTokenRotated: response.RefreshTokenRotated,
	}, nil
}

func (s *AccountTokenRefreshService) refreshWithProvider(ctx context.Context, account Account) (AccountTokenRefreshResult, error) {
	if s.authHost == nil || s.exchanger == nil {
		return AccountTokenRefreshResult{}, ErrAccountTokenRefreshUnsupported
	}
	initial, errRead := s.readAuthDocument(ctx, account)
	if errRead != nil {
		return AccountTokenRefreshResult{}, ErrAccountTokenRefreshFailed
	}
	rawProvider := tokenRefreshMetadataString(initial.metadata, "type")
	provider := normalizeTokenRefreshProvider(firstNonEmpty(rawProvider, account.Provider, account.Type))
	if provider != "codex" {
		return AccountTokenRefreshResult{}, ErrAccountTokenRefreshProviderUnsupported
	}
	if authMode := strings.ToLower(tokenRefreshMetadataString(initial.metadata, "auth_mode")); authMode == "personal_access_token" || authMode == "agent_identity" {
		return AccountTokenRefreshResult{}, ErrAccountTokenRefreshProviderUnsupported
	}
	refreshToken := tokenRefreshMetadataString(initial.metadata, "refresh_token")
	if refreshToken == "" {
		return AccountTokenRefreshResult{}, ErrAccountTokenRefreshCredentialMissing
	}
	refreshed, errExchange := s.exchanger.Exchange(ctx, tokenRefreshExchangeInput{
		Provider: provider, RefreshToken: refreshToken,
		ClientID: tokenRefreshMetadataString(initial.metadata, "client_id"),
		ProxyURL: tokenRefreshMetadataString(initial.metadata, "proxy_url"),
	})
	if errExchange != nil {
		return AccountTokenRefreshResult{}, classifyProviderTokenRefreshError(errExchange)
	}

	latest, errLatest := s.readAuthDocument(ctx, account)
	if errLatest != nil {
		return AccountTokenRefreshResult{}, ErrAccountTokenRefreshFailed
	}
	if latest.name != initial.name {
		return AccountTokenRefreshResult{}, ErrAccountTokenRefreshConflict
	}
	if initial.tokenState != latest.tokenState {
		return AccountTokenRefreshResult{}, ErrAccountTokenRefreshConflict
	}
	nowFunc := s.now
	if nowFunc == nil {
		nowFunc = time.Now
	}
	now := nowFunc().UTC()
	expiresAt := now.Add(refreshed.ExpiresIn).UTC()
	merged := cloneTokenRefreshMetadata(latest.metadata)
	merged["access_token"] = refreshed.AccessToken
	if refreshed.RefreshToken != "" {
		merged["refresh_token"] = refreshed.RefreshToken
	}
	if refreshed.IDToken != "" {
		merged["id_token"] = refreshed.IDToken
		mergeCodexIdentityClaims(merged, refreshed.IDToken)
	}
	if refreshed.TokenType != "" {
		merged["token_type"] = refreshed.TokenType
	}
	if refreshed.Scope != "" {
		merged["scope"] = refreshed.Scope
	}
	merged["expired"] = expiresAt.Format(time.RFC3339)
	if _, exists := merged["expires_at"]; exists {
		merged["expires_at"] = expiresAt.Format(time.RFC3339)
	}
	if _, exists := merged["expires_in"]; exists {
		merged["expires_in"] = int64(refreshed.ExpiresIn / time.Second)
	}
	merged["last_refresh"] = now.Format(time.RFC3339)
	rawMerged, errMarshal := json.Marshal(merged)
	if errMarshal != nil || len(rawMerged) > maxTokenRefreshAuthJSONBytes {
		return AccountTokenRefreshResult{}, ErrAccountTokenRefreshFailed
	}
	response, errSave := s.authHost.SaveAuth(ctx, latest.name, rawMerged)
	if errSave != nil || (response.Name != "" && response.Name != latest.name) ||
		(response.Path != "" && account.path != "" && normalizedPath(response.Path) != account.path) {
		return AccountTokenRefreshResult{}, ErrAccountTokenRefreshFailed
	}
	verified, errVerify := s.readAuthDocument(ctx, account)
	if errVerify != nil || verified.tokenState != tokenRefreshStateFingerprint(merged) {
		return AccountTokenRefreshResult{}, ErrAccountTokenRefreshVerification
	}
	return AccountTokenRefreshResult{
		AccountID: account.ID, Provider: provider, RefreshSource: "plugin_codex",
		RefreshedAt: now, ExpiresAt: &expiresAt,
		RefreshTokenRotated: refreshed.RefreshToken != "" && refreshed.RefreshToken != refreshToken,
	}, nil
}

type tokenRefreshAuthDocument struct {
	name       string
	metadata   map[string]any
	tokenState string
}

func (s *AccountTokenRefreshService) readAuthDocument(ctx context.Context, account Account) (tokenRefreshAuthDocument, error) {
	detail, errGet := s.authHost.GetAuth(ctx, account.ID)
	if errGet != nil {
		return tokenRefreshAuthDocument{}, errGet
	}
	if returned := strings.TrimSpace(detail.AuthIndex); returned != "" && returned != account.ID {
		return tokenRefreshAuthDocument{}, fmt.Errorf("auth index changed")
	}
	if detailPath := normalizedPath(detail.Path); account.path != "" && detailPath != "" && detailPath != account.path {
		return tokenRefreshAuthDocument{}, fmt.Errorf("auth path changed")
	}
	name := strings.TrimSpace(firstNonEmpty(detail.Name, account.Name))
	if !safeAuthJSONName(name) {
		return tokenRefreshAuthDocument{}, fmt.Errorf("auth file name is invalid")
	}
	raw := bytes.TrimSpace(detail.JSON)
	if len(raw) == 0 || len(raw) > maxTokenRefreshAuthJSONBytes || !json.Valid(raw) {
		return tokenRefreshAuthDocument{}, fmt.Errorf("auth json is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	metadata := make(map[string]any)
	if errDecode := decoder.Decode(&metadata); errDecode != nil {
		return tokenRefreshAuthDocument{}, errDecode
	}
	return tokenRefreshAuthDocument{name: name, metadata: metadata, tokenState: tokenRefreshStateFingerprint(metadata)}, nil
}

func (s *AccountTokenRefreshService) acquire(accountID string) bool {
	s.locksMu.Lock()
	defer s.locksMu.Unlock()
	if s.active == nil {
		s.active = make(map[string]struct{})
	}
	if _, exists := s.active[accountID]; exists {
		return false
	}
	s.active[accountID] = struct{}{}
	return true
}

func (s *AccountTokenRefreshService) release(accountID string) {
	s.locksMu.Lock()
	delete(s.active, accountID)
	s.locksMu.Unlock()
}

func classifyProviderTokenRefreshError(err error) error {
	switch {
	case errors.Is(err, ErrAccountTokenRefreshProviderUnsupported):
		return ErrAccountTokenRefreshProviderUnsupported
	case errors.Is(err, ErrAccountTokenRefreshCredentialMissing):
		return ErrAccountTokenRefreshCredentialMissing
	case errors.Is(err, ErrAccountTokenRefreshRejected):
		return ErrAccountTokenRefreshRejected
	default:
		return ErrAccountTokenRefreshFailed
	}
}

func tokenRefreshMetadataString(metadata map[string]any, key string) string {
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}

func tokenRefreshStateFingerprint(metadata map[string]any) string {
	state := make(map[string]any)
	for _, key := range []string{
		"type", "auth_mode", "client_id", "access_token", "refresh_token", "id_token", "token_type", "scope",
		"expired", "expires_at", "expires_in", "last_refresh",
	} {
		if value, exists := metadata[key]; exists {
			state[key] = value
		}
	}
	raw, _ := json.Marshal(state)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func cloneTokenRefreshMetadata(metadata map[string]any) map[string]any {
	clone := make(map[string]any, len(metadata)+4)
	for key, value := range metadata {
		clone[key] = value
	}
	return clone
}

func mergeCodexIdentityClaims(metadata map[string]any, idToken string) {
	claims := parseImportJWTPayload(idToken)
	if len(claims) == 0 {
		return
	}
	if profile, ok := claims["https://api.openai.com/profile"].(map[string]any); ok {
		if email := boundedTokenRefreshIdentity(profile["email"], 320); email != "" {
			metadata["email"] = email
		}
	}
	if auth, ok := claims["https://api.openai.com/auth"].(map[string]any); ok {
		if accountID := boundedTokenRefreshIdentity(auth["chatgpt_account_id"], 512); accountID != "" {
			metadata["account_id"] = accountID
			if _, exists := metadata["chatgpt_account_id"]; exists {
				metadata["chatgpt_account_id"] = accountID
			}
		}
	}
}

func boundedTokenRefreshIdentity(value any, limit int) string {
	text, _ := value.(string)
	text = strings.TrimSpace(text)
	if text == "" || len(text) > limit || hasUnsafeControl(text, false) {
		return ""
	}
	return text
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
