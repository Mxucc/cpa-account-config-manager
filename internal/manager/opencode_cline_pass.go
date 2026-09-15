package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// Cline Pass is a subscription gateway that speaks the OpenAI-compatible chat
// contract. The account, OAuth and model behavior in this file follow the
// community reference implementation github.com/fifidayone/pi-clinepass, which
// documents how the gateway authenticates (WorkOS device flow plus a Cline
// token exchange), how tokens rotate, and which model ids the gateway serves.
//
// Credentials never leave the plugin private data directory: the management API
// returns a redacted view and only reports whether a token is stored.
const (
	clinePassStoreVersion         = 1
	clinePassStoreFileName        = "cline-pass.json"
	clinePassDefaultBaseURL       = "https://api.cline.bot/api/v1"
	clinePassDefaultTimeout       = 20
	clinePassMaxTimeoutSeconds    = 60
	clinePassMaxAccounts          = 64
	clinePassMaxNameLength        = 120
	clinePassMaxModelIDLength     = 256
	clinePassMaxProbeBytes        = 1 << 20
	clinePassMaxResponseBytes     = 8 << 20
	clinePassErrorSummaryLength   = 180
	clinePassTokenLifetime        = 55 * time.Minute
	clinePassTokenRefreshMargin   = 5 * time.Minute
	clinePassRequestTimeout       = 20 * time.Second
	clinePassRefreshTimeout       = 15 * time.Second
	clinePassDeviceTimeout        = 20 * time.Second
	clinePassLoginSessionTTL      = 15 * time.Minute
	clinePassMaxLoginSessions     = 16
	clinePassClineVersionFallback = "3.0.61"
	clinePassVersionCacheTTL      = 24 * time.Hour
	clinePassVersionFetchTimeout  = 5 * time.Second
	clinePassNPMRegistryURL       = "https://registry.npmjs.org/cline/latest"
	clinePassBoundChannelName     = "Cline Pass"
)

// Authentication methods an account can be created with.
const (
	clinePassAuthMethodOAuth  = "oauth"
	clinePassAuthMethodAPIKey = "api_key"
	clinePassAuthMethodCLI    = "cli"
)

// clinePassCatalogModel is one allow-listed Cline Pass model. The catalog is
// curated from the reference implementation's measured catalog: the gateway's
// own listing drifts, and AGENTS.md requires public API models to be explicitly
// allow-listed, so an upstream /models response can validate a credential but
// can never expand what this plugin publishes.
type clinePassCatalogModel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Free bool   `json:"free"`
}

// clinePassCatalog is the curated Cline Pass model allow-list (paid models plus
// the free tier the gateway serves).
var clinePassCatalog = []clinePassCatalogModel{
	{ID: "cline-free/longcat-2.0", Name: "LongCat 2.0", Free: true},
	{ID: "z-ai/glm-5.3-flash", Name: "GLM-5.3 Flash", Free: true},
	{ID: "deepseek/deepseek-v4-flash", Name: "DeepSeek V4 Flash", Free: true},
	{ID: "poolside/laguna-s-2.1:free", Name: "Laguna S-2.1", Free: true},
	{ID: "cline-free/solar-pro4", Name: "Solar Pro 4", Free: true},
	{ID: "cline-free/muse-spark-1.3-contributor", Name: "Muse Spark 1.3 Contributor", Free: true},
	{ID: "cline-pass/glm-5.3-flash", Name: "GLM-5.3 Flash"},
	{ID: "cline-pass/glm-5.3", Name: "GLM-5.3"},
	{ID: "cline-pass/glm-5.2", Name: "GLM-5.2"},
	{ID: "cline-pass/kimi-k2.7-code", Name: "Kimi K2.7 Code"},
	{ID: "cline-pass/kimi-k2.6", Name: "Kimi K2.6"},
	{ID: "cline-pass/kimi-k3", Name: "Kimi K3"},
	{ID: "cline-pass/deepseek-v4-pro", Name: "DeepSeek V4 Pro"},
	{ID: "cline-pass/deepseek-v4.1-flash", Name: "DeepSeek V4.1 Flash"},
	{ID: "cline-pass/deepseek-v4-flash", Name: "DeepSeek V4 Flash"},
	{ID: "cline-pass/mimo-v2.5", Name: "MiMo-V2.5"},
	{ID: "cline-pass/mimo-v2.5-pro", Name: "MiMo-V2.5-Pro"},
	{ID: "cline-pass/minimax-m3", Name: "MiniMax M3"},
	{ID: "cline-pass/qwen3.7-plus", Name: "Qwen3.7 Plus"},
	{ID: "cline-pass/qwen3.7-max", Name: "Qwen3.7 Max"},
	{ID: "cline-pass/qwen3.8-max", Name: "Qwen3.8 Max"},
}

// clinePassCatalogIDs returns the allow-listed model ids in catalog order.
func clinePassCatalogIDs() []string {
	ids := make([]string, 0, len(clinePassCatalog))
	for _, model := range clinePassCatalog {
		ids = append(ids, model.ID)
	}
	return ids
}

// clinePassAllowsModel reports whether an id is on the allow-list. Comparison is
// exact: the gateway serves these ids verbatim.
func clinePassAllowsModel(id string) bool {
	trimmed := strings.TrimSpace(id)
	for _, model := range clinePassCatalog {
		if model.ID == trimmed {
			return true
		}
	}
	return false
}

// ClinePassAccount is one bound Cline Pass credential. Tokens are persisted only
// in the plugin private data directory and are never returned by the management
// API. An account created from a static API key carries the key in both the
// access and refresh fields and never expires.
type ClinePassAccount struct {
	ID           string    `json:"id"`
	Name         string    `json:"name,omitempty"`
	BaseURL      string    `json:"base_url"`
	AuthMethod   string    `json:"auth_method,omitempty"`
	AccessToken  string    `json:"access_token,omitempty"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`
	// Models caches the allow-listed catalog. Model ids are not secret, so the
	// redacted view exposes them.
	Models          []string  `json:"models,omitempty"`
	ModelsError     string    `json:"models_error,omitempty"`
	ModelsFetchedAt time.Time `json:"models_fetched_at,omitempty"`
	CreatedAt       time.Time `json:"created_at,omitempty"`
}

// ClinePassAccountView is the redacted public shape of a bound Cline Pass
// account. Tokens are replaced by booleans.
type ClinePassAccountView struct {
	ID              string     `json:"id"`
	Name            string     `json:"name,omitempty"`
	BaseURL         string     `json:"base_url"`
	AuthMethod      string     `json:"auth_method,omitempty"`
	AccessTokenSet  bool       `json:"access_token_set"`
	RefreshTokenSet bool       `json:"refresh_token_set"`
	ExpiresAt       *time.Time `json:"expires_at,omitempty"`
	Expired         bool       `json:"expired"`
	Models          []string   `json:"models,omitempty"`
	ModelsError     string     `json:"models_error,omitempty"`
	ModelsFetchedAt time.Time  `json:"models_fetched_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at,omitempty"`
}

type clinePassPersisted struct {
	Version  int                `json:"version"`
	Accounts []ClinePassAccount `json:"accounts"`
}

// ClinePassProbeResult reports whether the gateway accepted a credential.
type ClinePassProbeResult struct {
	Reachable  bool   `json:"reachable"`
	StatusCode int    `json:"status_code,omitempty"`
	Detail     string `json:"detail,omitempty"`
}

// clinePassLoginSession is one in-flight OAuth device authorization. Only the
// device code is secret; the user code and verification URI are shown to the
// operator by design.
type clinePassLoginSession struct {
	ID                      string
	Method                  string
	Name                    string
	DeviceCode              string
	UserCode                string
	VerificationURI         string
	VerificationURIComplete string
	IntervalSeconds         int
	ExpiresAt               time.Time
	NextPollAt              time.Time
	Status                  string
	Error                   string
	AccountID               string
}

// ClinePassLoginView is the public shape of one login session.
type ClinePassLoginView struct {
	SessionID               string                 `json:"session_id,omitempty"`
	Method                  string                 `json:"method,omitempty"`
	Status                  string                 `json:"status"`
	UserCode                string                 `json:"user_code,omitempty"`
	VerificationURI         string                 `json:"verification_uri,omitempty"`
	VerificationURIComplete string                 `json:"verification_uri_complete,omitempty"`
	IntervalSeconds         int                    `json:"interval_seconds,omitempty"`
	ExpiresInSeconds        int                    `json:"expires_in_seconds,omitempty"`
	Error                   string                 `json:"error,omitempty"`
	Account                 *ClinePassAccountView  `json:"account,omitempty"`
	Accounts                []ClinePassAccountView `json:"accounts,omitempty"`
}

// clinePassLogin statuses reported to the UI.
const (
	clinePassLoginPending   = "pending"
	clinePassLoginCompleted = "completed"
	clinePassLoginFailed    = "failed"
	clinePassLoginExpired   = "expired"
	clinePassLoginCancelled = "cancelled"
)

// ClinePassService provides Cline Pass credential management, the OAuth device
// login, token rotation, the model catalog and gateway probes.
type ClinePassService struct {
	mu          sync.RWMutex
	accounts    []ClinePassAccount
	dataDir     string
	loaded      bool
	loadFailed  bool
	storageErr  string
	now         func() time.Time
	doer        HTTPDoer
	logins      map[string]*clinePassLoginSession
	versionMu   sync.Mutex
	version     string
	versionedAt time.Time
}

func NewClinePassService() *ClinePassService {
	return &ClinePassService{
		now:    func() time.Time { return time.Now() },
		logins: map[string]*clinePassLoginSession{},
	}
}

// SetHTTPDoer injects the transport used for outbound calls. Tests rely on it;
// production leaves it nil and gets a per-request client.
func (s *ClinePassService) SetHTTPDoer(doer HTTPDoer) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.doer = doer
	s.mu.Unlock()
}

func (s *ClinePassService) httpDoer() HTTPDoer {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.doer
}

func clinePassStorePath(dataDir string) string {
	return filepath.Join(dataDir, clinePassStoreFileName)
}

// Configure loads persisted accounts. It is safe to call on a live service; the
// in-memory state is replaced by the on-disk state for the configured store.
func (s *ClinePassService) Configure(config Config) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sameStore := s.loaded && s.dataDir == config.DataDir
	if sameStore && !s.loadFailed {
		return
	}
	loaded, errLoad := loadClinePassState(clinePassStorePath(config.DataDir), config.DataDir != "")
	if errLoad != nil {
		s.loaded = true
		s.loadFailed = !errors.Is(errLoad, os.ErrNotExist)
		if s.loadFailed {
			s.storageErr = "Cline Pass state could not be loaded"
		} else {
			s.storageErr = ""
			if !sameStore {
				s.accounts = nil
			}
		}
		s.dataDir = config.DataDir
		return
	}
	s.dataDir = config.DataDir
	s.accounts = normalizeClinePassAccounts(loaded.Accounts)
	s.loaded = true
	s.loadFailed = false
	s.storageErr = ""
}

func loadClinePassState(storePath string, enabled bool) (clinePassPersisted, error) {
	var loaded clinePassPersisted
	if !enabled {
		return loaded, os.ErrNotExist
	}
	raw, errRead := os.ReadFile(storePath)
	if errRead != nil {
		return loaded, errRead
	}
	if errDecode := json.Unmarshal(raw, &loaded); errDecode != nil {
		return loaded, fmt.Errorf("decode Cline Pass state: %w", errDecode)
	}
	if loaded.Version != clinePassStoreVersion {
		return loaded, fmt.Errorf("unsupported Cline Pass state version")
	}
	return loaded, nil
}

func normalizeClinePassAccounts(accounts []ClinePassAccount) []ClinePassAccount {
	normalized := make([]ClinePassAccount, 0, len(accounts))
	for _, account := range accounts {
		account.ID = strings.TrimSpace(account.ID)
		account.Name = strings.TrimSpace(account.Name)
		account.BaseURL = normalizeClinePassBaseURL(account.BaseURL)
		account.AccessToken = strings.TrimSpace(account.AccessToken)
		account.RefreshToken = strings.TrimSpace(account.RefreshToken)
		account.AuthMethod = normalizeClinePassAuthMethod(account.AuthMethod)
		if account.ID == "" || account.AccessToken == "" {
			continue
		}
		account.Models = normalizeClinePassModels(account.Models)
		normalized = append(normalized, account)
	}
	return normalized
}

func normalizeClinePassAuthMethod(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case clinePassAuthMethodAPIKey:
		return clinePassAuthMethodAPIKey
	case clinePassAuthMethodCLI:
		return clinePassAuthMethodCLI
	default:
		return clinePassAuthMethodOAuth
	}
}

// normalizeClinePassBaseURL trims whitespace and trailing slashes and makes sure
// the API version segment is present, so the stored base always reaches
// {base}/chat/completions and {base}/models.
func normalizeClinePassBaseURL(value string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(value), "/")
	if trimmed == "" {
		return clinePassDefaultBaseURL
	}
	parsed, errParse := url.Parse(trimmed)
	if errParse != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return trimmed
	}
	path := strings.TrimRight(parsed.Path, "/")
	switch {
	case path == "":
		// A bare origin means the Cline API root; a custom deployment must give
		// its complete API base (for example http://bridge:8787/v1).
		path = "/api/v1"
	case !strings.HasSuffix(path, "/v1"):
		path += "/v1"
	}
	parsed.Path = path
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.TrimRight(parsed.String(), "/")
}

// validClinePassBaseURL reports whether a stored base URL is usable for outbound
// requests.
func validClinePassBaseURL(value string) bool {
	parsed, errParse := url.Parse(strings.TrimSpace(value))
	if errParse != nil {
		return false
	}
	return (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
}

func normalizeClinePassModels(models []string) []string {
	normalized := make([]string, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		trimmed := strings.TrimSpace(model)
		if trimmed == "" || len(trimmed) > clinePassMaxModelIDLength || !clinePassAllowsModel(trimmed) {
			continue
		}
		if _, exists := seen[trimmed]; exists {
			continue
		}
		seen[trimmed] = struct{}{}
		normalized = append(normalized, trimmed)
	}
	sort.Strings(normalized)
	return normalized
}

func (s *ClinePassService) persistLocked() error {
	if s.dataDir == "" {
		return nil
	}
	errPersist := savePrivateJSON(clinePassStorePath(s.dataDir), clinePassPersisted{
		Version:  clinePassStoreVersion,
		Accounts: append([]ClinePassAccount(nil), s.accounts...),
	})
	if errPersist != nil {
		s.storageErr = "Cline Pass state could not be persisted"
		return errPersist
	}
	s.loadFailed = false
	s.storageErr = ""
	return nil
}

// StorageError reports a fixed, non-sensitive storage failure string.
func (s *ClinePassService) StorageError() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.storageErr
}

// ListAccounts returns the redacted account list.
func (s *ClinePassService) ListAccounts() []ClinePassAccountView {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	views := make([]ClinePassAccountView, 0, len(s.accounts))
	for _, account := range s.accounts {
		views = append(views, s.clinePassViewOfLocked(account))
	}
	return views
}

func (s *ClinePassService) clinePassViewOfLocked(account ClinePassAccount) ClinePassAccountView {
	authMethod := normalizeClinePassAuthMethod(account.AuthMethod)
	view := ClinePassAccountView{
		ID:              account.ID,
		Name:            account.Name,
		BaseURL:         account.BaseURL,
		AuthMethod:      authMethod,
		AccessTokenSet:  strings.TrimSpace(account.AccessToken) != "",
		RefreshTokenSet: strings.TrimSpace(account.RefreshToken) != "",
		Models:          append([]string(nil), account.Models...),
		ModelsError:     account.ModelsError,
		ModelsFetchedAt: account.ModelsFetchedAt,
		CreatedAt:       account.CreatedAt,
	}
	if authMethod != clinePassAuthMethodAPIKey && !account.ExpiresAt.IsZero() {
		expires := account.ExpiresAt.UTC()
		view.ExpiresAt = &expires
		view.Expired = !expires.After(s.now().UTC())
	}
	return view
}

// AccountView returns one redacted account view.
func (s *ClinePassService) AccountView(id string) (ClinePassAccountView, bool) {
	if s == nil {
		return ClinePassAccountView{}, false
	}
	id = strings.TrimSpace(id)
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, account := range s.accounts {
		if account.ID == id {
			return s.clinePassViewOfLocked(account), true
		}
	}
	return ClinePassAccountView{}, false
}

// accountLocked returns a copy of one stored account.
func (s *ClinePassService) accountLocked(id string) (ClinePassAccount, bool) {
	id = strings.TrimSpace(id)
	for _, account := range s.accounts {
		if account.ID == id {
			return account, true
		}
	}
	return ClinePassAccount{}, false
}

// SaveAPIKeyAccount adds or replaces a credential submitted as a static API key.
// When accountID matches an existing entry only the name and base URL change and
// the key is replaced when apiKey is non-empty, so an existing OAuth account can
// be renamed without losing its tokens.
func (s *ClinePassService) SaveAPIKeyAccount(accountID, name, baseURL, apiKey string) (string, error) {
	if s == nil {
		return "", fmt.Errorf("Cline Pass service is unavailable")
	}
	accountID = strings.TrimSpace(accountID)
	name = strings.TrimSpace(name)
	apiKey = strings.TrimSpace(apiKey)
	if len(name) > clinePassMaxNameLength {
		return "", fmt.Errorf("name is too long")
	}
	normalizedBase := normalizeClinePassBaseURL(baseURL)
	if !validClinePassBaseURL(normalizedBase) {
		return "", fmt.Errorf("base_url must be a valid http(s) URL")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := range s.accounts {
		if s.accounts[index].ID != accountID {
			continue
		}
		s.accounts[index].Name = name
		s.accounts[index].BaseURL = normalizedBase
		if apiKey != "" {
			s.accounts[index].AuthMethod = clinePassAuthMethodAPIKey
			s.accounts[index].AccessToken = apiKey
			s.accounts[index].RefreshToken = apiKey
			s.accounts[index].ExpiresAt = time.Time{}
			s.accounts[index].Models = normalizeClinePassModels(clinePassCatalogIDs())
			s.accounts[index].ModelsError = ""
			s.accounts[index].ModelsFetchedAt = time.Time{}
		}
		if errPersist := s.persistLocked(); errPersist != nil {
			return "", errPersist
		}
		return s.accounts[index].ID, nil
	}
	if len(s.accounts) >= clinePassMaxAccounts {
		return "", fmt.Errorf("Cline Pass account limit reached")
	}
	if apiKey == "" {
		return "", fmt.Errorf("api_key is required when creating an account")
	}
	now := s.now().UTC()
	account := ClinePassAccount{
		ID:         fmt.Sprintf("cline_%d", s.now().UnixNano()),
		Name:       name,
		BaseURL:    normalizedBase,
		AuthMethod: clinePassAuthMethodAPIKey,
		// A static key never expires, so both fields carry it and the refresh
		// path passes it through untouched.
		AccessToken:  apiKey,
		RefreshToken: apiKey,
		Models:       normalizeClinePassModels(clinePassCatalogIDs()),
		CreatedAt:    now,
	}
	s.accounts = append(s.accounts, account)
	if errPersist := s.persistLocked(); errPersist != nil {
		s.accounts = s.accounts[:len(s.accounts)-1]
		return "", errPersist
	}
	return account.ID, nil
}

// RemoveAccount removes one bound account and persists the change.
func (s *ClinePassService) RemoveAccount(id string) error {
	if s == nil {
		return fmt.Errorf("Cline Pass service is unavailable")
	}
	id = strings.TrimSpace(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := range s.accounts {
		if s.accounts[index].ID != id {
			continue
		}
		removed := s.accounts[index]
		s.accounts = append(s.accounts[:index], s.accounts[index+1:]...)
		if errPersist := s.persistLocked(); errPersist != nil {
			s.accounts = append(s.accounts, ClinePassAccount{})
			copy(s.accounts[index+1:], s.accounts[index:])
			s.accounts[index] = removed
			return errPersist
		}
		s.cancelLoginsForAccountLocked(id)
		return nil
	}
	return fmt.Errorf("Cline Pass account was not found")
}

// credential returns a usable upstream credential for one account, refreshing a
// rotating OAuth token first when it is at or near expiry.
func (s *ClinePassService) credential(ctx context.Context, id string) (OpenCodeGoCredential, error) {
	if s == nil {
		return OpenCodeGoCredential{}, fmt.Errorf("Cline Pass service is unavailable")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return OpenCodeGoCredential{}, fmt.Errorf("account_id is required")
	}
	account, found, errLookup := s.accountByID(id)
	if errLookup != nil {
		return OpenCodeGoCredential{}, errLookup
	}
	if !found {
		return OpenCodeGoCredential{}, fmt.Errorf("Cline Pass account was not found")
	}
	if normalizeClinePassAuthMethod(account.AuthMethod) != clinePassAuthMethodAPIKey && s.tokenNeedsRefresh(account) {
		refreshed, errRefresh := s.refreshAccountToken(ctx, id)
		if errRefresh != nil {
			return OpenCodeGoCredential{}, errRefresh
		}
		account = refreshed
	}
	token := strings.TrimSpace(account.AccessToken)
	if token == "" {
		return OpenCodeGoCredential{}, fmt.Errorf("a Cline Pass credential is not stored for this account")
	}
	return OpenCodeGoCredential{
		ID:      account.ID,
		BaseURL: account.BaseURL,
		APIKey:  token,
		Models:  append([]string(nil), account.Models...),
	}, nil
}

func (s *ClinePassService) accountByID(id string) (ClinePassAccount, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	account, found := s.accountLocked(id)
	return account, found, nil
}

// tokenNeedsRefresh reports whether a rotating OAuth token is missing an expiry
// or expires inside the refresh margin.
func (s *ClinePassService) tokenNeedsRefresh(account ClinePassAccount) bool {
	if strings.TrimSpace(account.RefreshToken) == "" {
		return false
	}
	if account.ExpiresAt.IsZero() {
		return true
	}
	return !account.ExpiresAt.After(s.now().UTC().Add(clinePassTokenRefreshMargin))
}

// updateAccountTokensLocked writes rotated tokens back to the store. A rotated
// refresh token must be persisted immediately: the gateway invalidates the old
// one, so a failed persist would leave the stored credential dead.
func (s *ClinePassService) updateAccountTokensLocked(id, access, refresh string, expiresAt time.Time) bool {
	for index := range s.accounts {
		if s.accounts[index].ID != id {
			continue
		}
		if access != "" {
			s.accounts[index].AccessToken = access
		}
		if refresh != "" {
			s.accounts[index].RefreshToken = refresh
		}
		if !expiresAt.IsZero() {
			s.accounts[index].ExpiresAt = expiresAt.UTC()
		}
		s.accounts[index].AuthMethod = clinePassAuthMethodOAuth
		return true
	}
	return false
}

// RefreshToken forces a token rotation for one account and returns its view.
func (s *ClinePassService) RefreshToken(ctx context.Context, id string) (ClinePassAccountView, error) {
	if s == nil {
		return ClinePassAccountView{}, fmt.Errorf("Cline Pass service is unavailable")
	}
	account, found, _ := s.accountByID(strings.TrimSpace(id))
	if !found {
		return ClinePassAccountView{}, fmt.Errorf("Cline Pass account was not found")
	}
	if normalizeClinePassAuthMethod(account.AuthMethod) == clinePassAuthMethodAPIKey {
		view, _ := s.AccountView(account.ID)
		return view, nil
	}
	refreshed, errRefresh := s.refreshAccountToken(ctx, account.ID)
	if errRefresh != nil {
		return ClinePassAccountView{}, errRefresh
	}
	view, _ := s.AccountView(refreshed.ID)
	return view, nil
}

// RefreshModels validates the stored credential against the gateway model
// listing and records the outcome. The published catalog stays the curated
// allow-list: an upstream listing can confirm a credential but never expands
// what this plugin routes.
func (s *ClinePassService) RefreshModels(ctx context.Context, id string, timeoutSeconds int) (ClinePassAccountView, error) {
	credential, errCredential := s.credential(ctx, id)
	if errCredential != nil {
		return ClinePassAccountView{}, errCredential
	}
	_, _, errFetch := fetchClinePassModels(ctx, credential.BaseURL, credential.APIKey, clinePassTimeout(timeoutSeconds), s.httpDoer())
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := range s.accounts {
		if s.accounts[index].ID != credential.ID {
			continue
		}
		s.accounts[index].ModelsFetchedAt = s.now().UTC()
		if errFetch != nil {
			s.accounts[index].ModelsError = sanitizeClinePassError(errFetch.Error())
			s.accounts[index].Models = normalizeClinePassModels(clinePassCatalogIDs())
		} else {
			s.accounts[index].ModelsError = ""
			s.accounts[index].Models = normalizeClinePassModels(clinePassCatalogIDs())
		}
		_ = s.persistLocked()
		view := s.clinePassViewOfLocked(s.accounts[index])
		if errFetch != nil {
			return view, errFetch
		}
		return view, nil
	}
	return ClinePassAccountView{}, fmt.Errorf("Cline Pass account was not found")
}

// ProbeModel sends one minimal chat completion through the stored credential.
func (s *ClinePassService) ProbeModel(ctx context.Context, id, model string, timeoutSeconds int) (OpenCodeModelTestResult, error) {
	credential, errCredential := s.credential(ctx, id)
	if errCredential != nil {
		return OpenCodeModelTestResult{}, errCredential
	}
	if credential.APIKey == "" {
		return OpenCodeModelTestResult{
			Status: "unsupported", ReasonCode: "credential_incomplete", Model: strings.TrimSpace(model),
			Detail: "store a Cline Pass credential to test or route models", TestedAt: s.now().UTC(),
		}, nil
	}
	return probeClinePassModel(ctx, credential.BaseURL, credential.APIKey, model, clinePassTimeout(timeoutSeconds), s.httpDoer()), nil
}

// Probe queries a raw Cline Pass endpoint without saving any credential.
func (s *ClinePassService) Probe(ctx context.Context, baseURL, apiKey string, timeout time.Duration) ClinePassProbeResult {
	return probeClinePassEndpoint(ctx, baseURL, apiKey, timeout, s.httpDoer())
}

// ProbeAccount queries one saved account using its stored credential.
func (s *ClinePassService) ProbeAccount(ctx context.Context, id string, timeout time.Duration) (ClinePassAccountView, ClinePassProbeResult) {
	failed := ClinePassProbeResult{Reachable: false}
	credential, errCredential := s.credential(ctx, id)
	if errCredential != nil {
		failed.Detail = sanitizeClinePassError(errCredential.Error())
		return ClinePassAccountView{}, failed
	}
	view, _ := s.AccountView(credential.ID)
	return view, probeClinePassEndpoint(ctx, credential.BaseURL, credential.APIKey, timeout, s.httpDoer())
}

// clinePassTimeout resolves the request timeout used for catalog and probe calls.
func clinePassTimeout(seconds int) time.Duration {
	if seconds >= 1 && seconds <= clinePassMaxTimeoutSeconds {
		return time.Duration(seconds) * time.Second
	}
	return time.Duration(clinePassDefaultTimeout) * time.Second
}

// doerTransport adapts an injected doer into a transport so every outbound call
// honors test injection while production keeps the default transport.
func doerTransport(doer HTTPDoer) http.RoundTripper {
	if doer == nil {
		return nil
	}
	return doerRoundTripper{doer: doer}
}

type doerRoundTripper struct {
	doer HTTPDoer
}

func (t doerRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return t.doer.Do(request)
}

// sanitizeClinePassError bounds and flattens upstream error text before it is
// stored or returned.
func sanitizeClinePassError(value string) string {
	flattened := strings.Join(strings.Fields(stripOpenCodeTags(value)), " ")
	if len(flattened) > clinePassErrorSummaryLength {
		flattened = flattened[:clinePassErrorSummaryLength]
	}
	if flattened == "" {
		return "unknown error"
	}
	return flattened
}

// clinePassClientVersion returns the Cline CLI version advertised in the
// identifying headers. The value is fetched once per day from the npm registry;
// an unreachable registry falls back to the bundled version instead of failing.
func (s *ClinePassService) clinePassClientVersion(ctx context.Context) string {
	if s == nil {
		return clinePassClineVersionFallback
	}
	s.versionMu.Lock()
	cached := s.version
	fetchedAt := s.versionedAt
	s.versionMu.Unlock()
	if cached != "" && s.now().UTC().Sub(fetchedAt) < clinePassVersionCacheTTL {
		return cached
	}
	version := clinePassFetchClineVersion(ctx, s.httpDoer())
	s.versionMu.Lock()
	s.version = version
	s.versionedAt = s.now().UTC()
	s.versionMu.Unlock()
	return version
}

// clinePassClientVersionCached returns the cached version without any network
// access, for paths that must stay synchronous.
func (s *ClinePassService) clinePassClientVersionCached() string {
	if s == nil {
		return clinePassClineVersionFallback
	}
	s.versionMu.Lock()
	defer s.versionMu.Unlock()
	if s.version == "" {
		return clinePassClineVersionFallback
	}
	return s.version
}

// clinePassFetchClineVersion reads the published Cline version. The response is
// validated so a malformed registry answer can never reach a request header.
func clinePassFetchClineVersion(ctx context.Context, doer HTTPDoer) string {
	requestCtx, cancel := context.WithTimeout(ctx, clinePassVersionFetchTimeout)
	defer cancel()
	request, errRequest := http.NewRequestWithContext(requestCtx, http.MethodGet, clinePassNPMRegistryURL, nil)
	if errRequest != nil {
		return clinePassClineVersionFallback
	}
	request.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: clinePassVersionFetchTimeout, Transport: doerTransport(doer)}
	response, errDo := client.Do(request)
	if errDo != nil || response == nil || response.Body == nil {
		return clinePassClineVersionFallback
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return clinePassClineVersionFallback
	}
	body, errRead := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	if errRead != nil {
		return clinePassClineVersionFallback
	}
	var payload struct {
		Version string `json:"version"`
	}
	if errDecode := json.Unmarshal(body, &payload); errDecode != nil {
		return clinePassClineVersionFallback
	}
	version := strings.TrimSpace(payload.Version)
	if version == "" || len(version) > 64 || !clinePassSafeHeaderValue(version) {
		return clinePassClineVersionFallback
	}
	return version
}

// clinePassSafeHeaderValue rejects control characters so a malformed registry
// answer cannot break an outbound request.
func clinePassSafeHeaderValue(value string) bool {
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// clinePassChannelHeaders are the identifying headers the gateway expects from a
// Cline product surface. Free-tier models are gated behind them, and routed
// traffic carries the same identity as a probe.
func clinePassChannelHeaders(version string) map[string]string {
	if strings.TrimSpace(version) == "" {
		version = clinePassClineVersionFallback
	}
	return map[string]string{
		"x-client-type":    "cli",
		"x-client-version": version,
		"x-core-version":   version,
		"x-platform":       runtime.GOOS,
		"User-Agent":       "Cline/" + version,
	}
}

// clinePassChannelBaseURL is the CPA channel base URL for one stored account:
// CPA appends /chat/completions to it, and the normalized stored base already
// ends with the API version.
func clinePassChannelBaseURL(baseURL string) string {
	return normalizeClinePassBaseURL(baseURL)
}

// newClinePassRequest builds one authenticated gateway request with the Cline
// identifying headers.
func newClinePassRequest(ctx context.Context, method, target, accessToken string, body io.Reader, version string) (*http.Request, error) {
	request, errRequest := http.NewRequestWithContext(ctx, method, target, body)
	if errRequest != nil {
		return nil, errRequest
	}
	request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(accessToken))
	request.Header.Set("Accept", "application/json")
	for name, value := range clinePassChannelHeaders(version) {
		request.Header.Set(name, value)
	}
	return request, nil
}

// fetchClinePassModels validates a credential against the gateway model listing.
// The ids themselves are ignored on purpose: the curated allow-list is the only
// source of published models.
func fetchClinePassModels(ctx context.Context, baseURL, accessToken string, timeout time.Duration, doer HTTPDoer) ([]string, int, error) {
	base := normalizeClinePassBaseURL(baseURL)
	token := strings.TrimSpace(accessToken)
	if !validClinePassBaseURL(base) {
		return nil, 0, fmt.Errorf("Cline Pass base URL is invalid")
	}
	if token == "" {
		return nil, 0, fmt.Errorf("a Cline Pass credential is required")
	}
	request, errRequest := newClinePassRequest(ctx, http.MethodGet, base+"/models", token, nil, "")
	if errRequest != nil {
		return nil, 0, fmt.Errorf("create Cline Pass models request")
	}
	client := &http.Client{Timeout: timeout, Transport: doerTransport(doer)}
	response, errDo := client.Do(request)
	if errDo != nil {
		return nil, 0, fmt.Errorf("Cline Pass models request failed: %s", sanitizeClinePassError(errDo.Error()))
	}
	if response == nil || response.Body == nil {
		return nil, 0, fmt.Errorf("the Cline Pass models endpoint returned an empty response")
	}
	defer func() { _ = response.Body.Close() }()
	body, errRead := io.ReadAll(io.LimitReader(response.Body, clinePassMaxResponseBytes))
	if errRead != nil {
		return nil, response.StatusCode, fmt.Errorf("read Cline Pass models response: %s", sanitizeClinePassError(errRead.Error()))
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, response.StatusCode, fmt.Errorf("the Cline Pass models endpoint returned HTTP %d", response.StatusCode)
	}
	models, errParse := parseOpenCodeModelCatalog(body)
	if errParse != nil {
		return nil, response.StatusCode, errParse
	}
	return models, response.StatusCode, nil
}

// probeClinePassModel sends one minimal chat completion so an operator can verify
// that a credential really serves a model. The response body is never stored:
// only a bounded, redacted preview is returned.
func probeClinePassModel(ctx context.Context, baseURL, accessToken, model string, timeout time.Duration, doer HTTPDoer) OpenCodeModelTestResult {
	result := OpenCodeModelTestResult{Model: strings.TrimSpace(model), TestedAt: time.Now().UTC(), ProbeKind: "model", Endpoint: "chat"}
	base := normalizeClinePassBaseURL(baseURL)
	token := strings.TrimSpace(accessToken)
	if !validClinePassBaseURL(base) || token == "" {
		result.Status, result.ReasonCode = "unsupported", "credential_incomplete"
		result.Detail = "a Cline Pass base URL and credential are both required"
		return result
	}
	if result.Model == "" {
		result.Status, result.ReasonCode = "unsupported", "invalid_model"
		result.Detail = "a model id is required"
		return result
	}
	payload, errMarshal := json.Marshal(map[string]any{
		"model":      result.Model,
		"messages":   []map[string]string{{"role": "user", "content": "ping"}},
		"max_tokens": openCodeModelProbeMaxOutputTokens,
		"stream":     false,
	})
	if errMarshal != nil {
		result.Status, result.ReasonCode = "unavailable", "request_failed"
		result.Detail = "the probe request could not be encoded"
		return result
	}
	request, errRequest := newClinePassRequest(ctx, http.MethodPost, base+"/chat/completions", token, bytes.NewReader(payload), "")
	if errRequest != nil {
		result.Status, result.ReasonCode = "unavailable", "request_failed"
		result.Detail = "the probe request could not be created"
		return result
	}
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: timeout, Transport: doerTransport(doer)}
	startedAt := time.Now()
	response, errDo := client.Do(request)
	result.LatencyMS = time.Since(startedAt).Milliseconds()
	result.TriedEndpoints = []string{"chat"}
	if errDo != nil {
		result.Status, result.ReasonCode = "unavailable", "request_timeout"
		result.Detail = sanitizeClinePassError(errDo.Error())
		return result
	}
	if response == nil || response.Body == nil {
		result.Status, result.ReasonCode = "unavailable", "upstream_unavailable"
		result.Detail = "the upstream returned an empty response"
		return result
	}
	body, _ := io.ReadAll(io.LimitReader(response.Body, clinePassMaxProbeBytes))
	_ = response.Body.Close()
	result.StatusCode = response.StatusCode
	result.Reachable = response.StatusCode > 0
	result.Response = sanitizeModelTestResponsePreview(modelProbeHTTPResponse{
		StatusCode: response.StatusCode,
		Header:     response.Header,
		Body:       body,
	})
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		result.Status, result.ReasonCode = "available", "model_response_ok"
		result.Detail = ""
		return result
	}
	result.Detail = sanitizeClinePassError(string(body))
	result.Status, result.ReasonCode = classifyOpenCodeProbeFailure(response.StatusCode, string(body))
	return result
}

// probeClinePassEndpoint probes the model listing on a base URL. A 2xx answer
// means the endpoint is reachable; 401/403 means it rejected the credential.
func probeClinePassEndpoint(ctx context.Context, baseURL, accessToken string, timeout time.Duration, doer HTTPDoer) ClinePassProbeResult {
	base := normalizeClinePassBaseURL(baseURL)
	if !validClinePassBaseURL(base) {
		return ClinePassProbeResult{Reachable: false, Detail: "invalid base URL"}
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, errRequest := newClinePassRequest(probeCtx, http.MethodGet, base+"/models", accessToken, nil, "")
	if errRequest != nil {
		return ClinePassProbeResult{Reachable: false, Detail: "invalid endpoint URL"}
	}
	client := &http.Client{Timeout: timeout, Transport: doerTransport(doer)}
	response, errDo := client.Do(request)
	if errDo != nil {
		return ClinePassProbeResult{Reachable: false, Detail: sanitizeClinePassError(errDo.Error())}
	}
	if response == nil || response.Body == nil {
		return ClinePassProbeResult{Reachable: false, Detail: "upstream returned an empty response"}
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, clinePassMaxProbeBytes))
	response.Body.Close()
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return ClinePassProbeResult{Reachable: true, StatusCode: response.StatusCode, Detail: "reachable"}
	}
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return ClinePassProbeResult{Reachable: false, StatusCode: response.StatusCode, Detail: "the gateway rejected the credential"}
	}
	return ClinePassProbeResult{Reachable: false, StatusCode: response.StatusCode, Detail: aiProviderProbeStatusDetail(response.StatusCode)}
}
