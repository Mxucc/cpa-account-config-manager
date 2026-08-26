package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	openCodeZenStoreVersion       = 1
	openCodeZenDefaultTimeout     = 20
	openCodeZenMaxTimeoutSeconds  = 60
	openCodeZenDefaultBaseURL     = "https://opencode.ai/zen"
	openCodeZenMaxAccounts        = 64
	openCodeZenMaxNameLength      = 80
	openCodeZenMaxProbeBytes      = 1 << 20
	openCodeZenErrorSummaryLength = 180
)

// OpenCodeZenAccount is one bound OpenCode Zen credential. The Zen API key is
// persisted only in the plugin's private data directory and never returned by
// the management API. BaseURL may point at the Zen gateway directly
// (https://opencode.ai/zen) or at a self-hosted opencode-cc bridge.
type OpenCodeZenAccount struct {
	ID        string `json:"id"`
	Name      string `json:"name,omitempty"`
	BaseURL   string `json:"base_url"`
	ZenAPIKey string `json:"zen_api_key,omitempty"`
}

// OpenCodeZenAccountView is the redacted public shape of a bound Zen account.
// The API key itself is never exposed; KeySet reports whether one is stored.
type OpenCodeZenAccountView struct {
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	BaseURL string `json:"base_url"`
	KeySet  bool   `json:"key_set"`
}

type openCodeZenPersisted struct {
	Version  int                  `json:"version"`
	Accounts []OpenCodeZenAccount `json:"accounts"`
}

// OpenCodeZenProbeResult reports whether the Zen/bridge endpoint is reachable
// and how the upstream treated the supplied credential.
type OpenCodeZenProbeResult struct {
	Reachable  bool   `json:"reachable"`
	StatusCode int    `json:"status_code,omitempty"`
	Detail     string `json:"detail,omitempty"`
}

// OpenCodeZenService provides OpenCode Zen credential management and endpoint
// probing. Its behavior follows the opencode-cc bridge gateway conventions:
// the /v1/models endpoint is probed with both a Bearer token and an
// Anthropic-style x-api-key header so direct Zen and self-hosted bridge
// endpoints both accept the credential. See
// https://github.com/Kiowx/opencode-cc for the bridge reference.
type OpenCodeZenService struct {
	mu         sync.RWMutex
	accounts   []OpenCodeZenAccount
	dataDir    string
	loaded     bool
	loadFailed bool
	storageErr string
	now        func() time.Time
}

func NewOpenCodeZenService() *OpenCodeZenService {
	return &OpenCodeZenService{
		now: func() time.Time { return time.Now() },
	}
}

func openCodeZenStorePath(dataDir string) string {
	return filepath.Join(dataDir, "opencode-zen.json")
}

// Configure loads persisted accounts. It is safe to call on a live service;
// any in-memory state is replaced by the on-disk state.
func (s *OpenCodeZenService) Configure(config Config) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sameStore := s.loaded && s.dataDir == config.DataDir
	if sameStore && !s.loadFailed {
		return
	}
	loaded, errLoad := loadOpenCodeZenState(openCodeZenStorePath(config.DataDir), config.DataDir != "")
	if errLoad != nil {
		s.loaded = true
		s.loadFailed = !errors.Is(errLoad, os.ErrNotExist)
		if s.loadFailed {
			s.storageErr = "OpenCode Zen state could not be loaded"
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
	s.accounts = normalizeOpenCodeZenAccounts(loaded.Accounts)
	s.loaded = true
	s.loadFailed = false
	s.storageErr = ""
}

func loadOpenCodeZenState(storePath string, enabled bool) (openCodeZenPersisted, error) {
	var loaded openCodeZenPersisted
	if !enabled {
		return loaded, os.ErrNotExist
	}
	raw, errRead := os.ReadFile(storePath)
	if errRead != nil {
		return loaded, errRead
	}
	if errDecode := json.Unmarshal(raw, &loaded); errDecode != nil {
		return loaded, fmt.Errorf("decode OpenCode Zen state: %w", errDecode)
	}
	if loaded.Version != openCodeZenStoreVersion {
		return loaded, fmt.Errorf("unsupported OpenCode Zen state version")
	}
	return loaded, nil
}

func normalizeOpenCodeZenAccounts(accounts []OpenCodeZenAccount) []OpenCodeZenAccount {
	normalized := make([]OpenCodeZenAccount, 0, len(accounts))
	for _, account := range accounts {
		account.ID = strings.TrimSpace(account.ID)
		account.Name = strings.TrimSpace(account.Name)
		account.BaseURL = normalizeOpenCodeZenBaseURL(account.BaseURL)
		account.ZenAPIKey = strings.TrimSpace(account.ZenAPIKey)
		if account.ID == "" || account.BaseURL == "" || account.ZenAPIKey == "" {
			continue
		}
		normalized = append(normalized, account)
	}
	return normalized
}

func (s *OpenCodeZenService) persistLocked() error {
	if s.dataDir == "" {
		return nil
	}
	errPersist := savePrivateJSON(openCodeZenStorePath(s.dataDir), openCodeZenPersisted{
		Version:  openCodeZenStoreVersion,
		Accounts: append([]OpenCodeZenAccount(nil), s.accounts...),
	})
	if errPersist != nil {
		s.storageErr = "OpenCode Zen state could not be persisted"
		return errPersist
	}
	s.loadFailed = false
	s.storageErr = ""
	return nil
}

func (s *OpenCodeZenService) StorageError() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.storageErr
}

// ListAccounts returns the redacted account list.
func (s *OpenCodeZenService) ListAccounts() []OpenCodeZenAccountView {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	views := make([]OpenCodeZenAccountView, 0, len(s.accounts))
	for _, account := range s.accounts {
		views = append(views, openCodeZenViewOf(account))
	}
	return views
}

func openCodeZenViewOf(account OpenCodeZenAccount) OpenCodeZenAccountView {
	return OpenCodeZenAccountView{
		ID:      account.ID,
		Name:    account.Name,
		BaseURL: account.BaseURL,
		KeySet:  strings.TrimSpace(account.ZenAPIKey) != "",
	}
}

// SaveAccount adds or replaces a Zen credential. When accountID is non-empty
// and matches an existing entry, the entry is updated in place: name and
// base_url always apply, but the API key is only replaced when apiKey is
// non-empty. An unknown accountID creates a new entry.
func (s *OpenCodeZenService) SaveAccount(accountID, name, baseURL, apiKey string) (string, error) {
	if s == nil {
		return "", fmt.Errorf("OpenCode Zen service is unavailable")
	}
	accountID = strings.TrimSpace(accountID)
	name = strings.TrimSpace(name)
	baseURL = normalizeOpenCodeZenBaseURL(baseURL)
	apiKey = strings.TrimSpace(apiKey)
	if baseURL == "" {
		return "", fmt.Errorf("base_url is required")
	}
	if parsed, errParse := url.Parse(baseURL); errParse != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", fmt.Errorf("base_url must be a valid http(s) URL")
	}
	if len(name) > openCodeZenMaxNameLength {
		return "", fmt.Errorf("name is too long")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := range s.accounts {
		if s.accounts[index].ID == accountID {
			s.accounts[index].Name = name
			s.accounts[index].BaseURL = baseURL
			if apiKey != "" {
				s.accounts[index].ZenAPIKey = apiKey
			}
			if errPersist := s.persistLocked(); errPersist != nil {
				return "", errPersist
			}
			return s.accounts[index].ID, nil
		}
	}
	if len(s.accounts) >= openCodeZenMaxAccounts {
		return "", fmt.Errorf("OpenCode Zen account limit reached")
	}
	if apiKey == "" {
		return "", fmt.Errorf("zen_api_key is required when creating an account")
	}
	account := OpenCodeZenAccount{
		ID:        fmt.Sprintf("zen_%d", s.now().UnixNano()),
		Name:      name,
		BaseURL:   baseURL,
		ZenAPIKey: apiKey,
	}
	s.accounts = append(s.accounts, account)
	if errPersist := s.persistLocked(); errPersist != nil {
		s.accounts = s.accounts[:len(s.accounts)-1]
		return "", errPersist
	}
	return account.ID, nil
}

// RemoveAccount removes one bound Zen account and persists the change.
func (s *OpenCodeZenService) RemoveAccount(id string) error {
	if s == nil {
		return fmt.Errorf("OpenCode Zen service is unavailable")
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
			s.accounts = append(s.accounts, OpenCodeZenAccount{})
			copy(s.accounts[index+1:], s.accounts[index:])
			s.accounts[index] = removed
			return errPersist
		}
		return nil
	}
	return fmt.Errorf("OpenCode Zen account was not found")
}

// Probe queries one Zen/bridge endpoint without saving any credential.
func (s *OpenCodeZenService) Probe(ctx context.Context, baseURL, apiKey string, timeout time.Duration) OpenCodeZenProbeResult {
	return probeOpenCodeZenEndpoint(ctx, baseURL, apiKey, timeout)
}

// ProbeAccount queries one saved account's endpoint using its stored key.
func (s *OpenCodeZenService) ProbeAccount(ctx context.Context, id string, timeout time.Duration) (OpenCodeZenAccountView, OpenCodeZenProbeResult) {
	empty := OpenCodeZenAccountView{}
	failed := OpenCodeZenProbeResult{Reachable: false}
	if s == nil {
		failed.Detail = "OpenCode Zen service is unavailable"
		return empty, failed
	}
	id = strings.TrimSpace(id)
	s.mu.RLock()
	var found *OpenCodeZenAccount
	for index := range s.accounts {
		if s.accounts[index].ID == id {
			accountCopy := s.accounts[index]
			found = &accountCopy
			break
		}
	}
	s.mu.RUnlock()
	if found == nil {
		failed.Detail = "OpenCode Zen account was not found"
		return empty, failed
	}
	return openCodeZenViewOf(*found), probeOpenCodeZenEndpoint(ctx, found.BaseURL, found.ZenAPIKey, timeout)
}

// probeOpenCodeZenEndpoint probes the model listing on the base URL. The
// candidate order mirrors the opencode-cc gateway layout: /v1/models first
// (works for both a plain bridge root and a Zen root such as
// https://opencode.ai/zen), then the raw base URL, then the Zen-native paths
// /zen/v1/models and /zen/go/v1/models when the base looks like a plain
// origin.
func probeOpenCodeZenEndpoint(ctx context.Context, baseURL, apiKey string, timeout time.Duration) OpenCodeZenProbeResult {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if trimmed == "" {
		return OpenCodeZenProbeResult{Reachable: false, Detail: "invalid base URL"}
	}
	candidates := []string{trimmed + "/v1/models", trimmed}
	if parsed, errParse := url.Parse(trimmed); errParse == nil && (parsed.Path == "" || parsed.Path == "/") {
		candidates = append(candidates, trimmed+"/zen/v1/models", trimmed+"/zen/go/v1/models")
	}

	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var lastStatus int
	var lastDetail string
	for _, candidate := range candidates {
		if errProbe := probeCtx.Err(); errProbe != nil {
			lastDetail = sanitizeAIProviderProbeError(errProbe)
			break
		}
		request, errNew := http.NewRequestWithContext(probeCtx, http.MethodGet, candidate, nil)
		if errNew != nil {
			lastDetail = "invalid endpoint URL"
			continue
		}
		request.Header.Set("Accept", "application/json")
		request.Header.Set("User-Agent", "cpa-account-config-manager/opencode-zen")
		if strings.TrimSpace(apiKey) != "" {
			// Send both bearer styles so direct Zen (Bearer) and the
			// opencode-cc native path (Bearer + x-api-key) both accept it.
			request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
			request.Header.Set("x-api-key", strings.TrimSpace(apiKey))
		}
		client := &http.Client{Timeout: timeout}
		response, errDo := client.Do(request)
		if errDo != nil {
			lastDetail = sanitizeAIProviderProbeError(errDo)
			continue
		}
		if response == nil || response.Body == nil {
			lastDetail = "upstream returned an empty response"
			continue
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, openCodeZenMaxProbeBytes))
		response.Body.Close()
		lastStatus = response.StatusCode
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			return OpenCodeZenProbeResult{Reachable: true, StatusCode: response.StatusCode, Detail: "reachable"}
		}
		lastDetail = aiProviderProbeStatusDetail(response.StatusCode)
		if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
			break
		}
	}
	return OpenCodeZenProbeResult{Reachable: false, StatusCode: lastStatus, Detail: lastDetail}
}

// normalizeOpenCodeZenBaseURL trims whitespace and trailing slashes so probe
// URL building is stable; an empty result means the input was blank.
func normalizeOpenCodeZenBaseURL(value string) string {
	return strings.TrimRight(strings.TrimSpace(value), "/")
}
