package manager

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"html"
)

const (
	openCodeQuotaStoreVersion       = 1
	openCodeQuotaDefaultTimeout     = 30
	openCodeQuotaMaxTimeoutSeconds  = 60
	openCodeQuotaDashboardMaxBytes  = 4 << 20
	openCodeQuotaErrorSummaryLength = 180
)

// OpenCodeAccount is one bound OpenCode Go workspace credential. The auth
// cookie is persisted only in the plugin's private data directory and never
// returned by the management API.
type OpenCodeAccount struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	AuthCookie  string `json:"auth_cookie"`
}

// OpenCodeAccountView is the redacted public shape of a bound account.
type OpenCodeAccountView struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
}

type openCodeQuotaPersisted struct {
	Version        int               `json:"version"`
	Accounts       []OpenCodeAccount `json:"accounts"`
	TimeoutSeconds int               `json:"timeout_seconds"`
}

// OpenCodeWindowUsage is one quota window (rolling/weekly/monthly).
type OpenCodeWindowUsage struct {
	UsagePercent     float64   `json:"usage_percent"`
	PercentRemaining float64   `json:"percent_remaining"`
	ResetInSec       int64     `json:"reset_in_sec"`
	ResetAt          time.Time `json:"reset_at"`
}

// OpenCodeQuotaResult is the parsed quota for one workspace.
type OpenCodeQuotaResult struct {
	Success   bool                 `json:"success"`
	AccountID string               `json:"account_id,omitempty"`
	Workspace string               `json:"workspace,omitempty"`
	Rolling   *OpenCodeWindowUsage `json:"rolling,omitempty"`
	Weekly    *OpenCodeWindowUsage `json:"weekly,omitempty"`
	Monthly   *OpenCodeWindowUsage `json:"monthly,omitempty"`
	Source    string               `json:"source,omitempty"`
	FetchedAt time.Time            `json:"fetched_at,omitempty"`
	Error     string               `json:"error,omitempty"`
}

// OpenCodeQuotaSnapshot is the sanitized management-visible state.
type OpenCodeQuotaSnapshot struct {
	Accounts  []OpenCodeAccountView           `json:"accounts"`
	Results   map[string]*OpenCodeQuotaResult `json:"results"`
	FetchedAt time.Time                       `json:"fetched_at"`
}

// OpenCodeQuotaService provides OpenCode Go quota monitoring. Its behavior is
// ported from the community plugin
// cnb.cool/zcyoop/opencode-go-quota-cpa-plugin.
type OpenCodeQuotaService struct {
	mu             sync.RWMutex
	accounts       []OpenCodeAccount
	timeoutSeconds int
	cache          map[string]*OpenCodeQuotaResult
	fetchedAt      time.Time
	dataDir        string
	fetchMu        sync.Mutex
	now            func() time.Time
}

func NewOpenCodeQuotaService() *OpenCodeQuotaService {
	return &OpenCodeQuotaService{
		timeoutSeconds: openCodeQuotaDefaultTimeout,
		cache:          map[string]*OpenCodeQuotaResult{},
		now:            func() time.Time { return time.Now() },
	}
}

func openCodeQuotaStorePath(dataDir string) string {
	return filepath.Join(dataDir, "opencode-quota.json")
}

// Configure loads persisted accounts and settings. It is safe to call on a
// live service; the current in-memory cache is retained.
func (s *OpenCodeQuotaService) Configure(config Config) {
	if s == nil {
		return
	}
	timeout := openCodeQuotaDefaultTimeout
	var loaded openCodeQuotaPersisted
	if config.DataDir != "" {
		if raw, errRead := os.ReadFile(openCodeQuotaStorePath(config.DataDir)); errRead == nil {
			if errDecode := json.Unmarshal(raw, &loaded); errDecode == nil && loaded.Version == openCodeQuotaStoreVersion {
				if loaded.TimeoutSeconds >= 1 && loaded.TimeoutSeconds <= openCodeQuotaMaxTimeoutSeconds {
					timeout = loaded.TimeoutSeconds
				}
			}
		}
	}
	s.mu.Lock()
	s.dataDir = config.DataDir
	s.accounts = append([]OpenCodeAccount(nil), loaded.Accounts...)
	s.timeoutSeconds = timeout
	s.mu.Unlock()
}

func (s *OpenCodeQuotaService) persistLocked() error {
	if s.dataDir == "" {
		return nil
	}
	return savePrivateJSON(openCodeQuotaStorePath(s.dataDir), openCodeQuotaPersisted{
		Version:        openCodeQuotaStoreVersion,
		Accounts:       append([]OpenCodeAccount(nil), s.accounts...),
		TimeoutSeconds: s.timeoutSeconds,
	})
}

// ListAccounts returns the redacted account list.
func (s *OpenCodeQuotaService) ListAccounts() []OpenCodeAccountView {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	views := make([]OpenCodeAccountView, 0, len(s.accounts))
	for _, account := range s.accounts {
		views = append(views, OpenCodeAccountView{ID: account.ID, WorkspaceID: account.WorkspaceID})
	}
	return views
}

// SaveAccount adds or replaces a workspace credential and persists it.
func (s *OpenCodeQuotaService) SaveAccount(workspaceID, authCookie string) (string, error) {
	if s == nil {
		return "", fmt.Errorf("OpenCode quota service is unavailable")
	}
	workspaceID = strings.TrimSpace(workspaceID)
	authCookie = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(authCookie), "auth="))
	if workspaceID == "" || authCookie == "" {
		return "", fmt.Errorf("workspace_id and auth_cookie are both required")
	}
	id := fmt.Sprintf("%s_%d", workspaceID, s.now().UnixNano())
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := range s.accounts {
		if s.accounts[index].WorkspaceID == workspaceID {
			s.accounts[index].AuthCookie = authCookie
			id = s.accounts[index].ID
			if errPersist := s.persistLocked(); errPersist != nil {
				return "", errPersist
			}
			return id, nil
		}
	}
	s.accounts = append(s.accounts, OpenCodeAccount{ID: id, WorkspaceID: workspaceID, AuthCookie: authCookie})
	if errPersist := s.persistLocked(); errPersist != nil {
		s.accounts = s.accounts[:len(s.accounts)-1]
		return "", errPersist
	}
	return id, nil
}

// RemoveAccount removes one bound account and persists the change.
func (s *OpenCodeQuotaService) RemoveAccount(id string) error {
	if s == nil {
		return fmt.Errorf("OpenCode quota service is unavailable")
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
			s.accounts = append(s.accounts, OpenCodeAccount{})
			copy(s.accounts[index+1:], s.accounts[index:])
			s.accounts[index] = removed
			return errPersist
		}
		delete(s.cache, id)
		return nil
	}
	return fmt.Errorf("OpenCode account was not found")
}

// ClearAccounts removes every bound account.
func (s *OpenCodeQuotaService) ClearAccounts() error {
	if s == nil {
		return fmt.Errorf("OpenCode quota service is unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := s.accounts
	s.accounts = nil
	if errPersist := s.persistLocked(); errPersist != nil {
		s.accounts = previous
		return errPersist
	}
	s.cache = map[string]*OpenCodeQuotaResult{}
	s.fetchedAt = s.now().UTC()
	return nil
}

func (s *OpenCodeQuotaService) snapshotAccountsLocked() []OpenCodeAccount {
	return append([]OpenCodeAccount(nil), s.accounts...)
}

func (s *OpenCodeQuotaService) timeoutLocked() time.Duration {
	timeout := s.timeoutSeconds
	if timeout < 1 {
		timeout = openCodeQuotaDefaultTimeout
	}
	return time.Duration(timeout) * time.Second
}

// Snapshot returns the redacted account list plus the cached results.
func (s *OpenCodeQuotaService) Snapshot() OpenCodeQuotaSnapshot {
	if s == nil {
		return OpenCodeQuotaSnapshot{Accounts: []OpenCodeAccountView{}, Results: map[string]*OpenCodeQuotaResult{}}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	snapshot := OpenCodeQuotaSnapshot{
		Accounts:  make([]OpenCodeAccountView, 0, len(s.accounts)),
		Results:   make(map[string]*OpenCodeQuotaResult, len(s.cache)),
		FetchedAt: s.fetchedAt,
	}
	for _, account := range s.accounts {
		snapshot.Accounts = append(snapshot.Accounts, OpenCodeAccountView{ID: account.ID, WorkspaceID: account.WorkspaceID})
	}
	for id, result := range s.cache {
		cloned := *result
		snapshot.Results[id] = &cloned
	}
	return snapshot
}

// RefreshAll fetches quota for every bound account (or only the cache when
// force is false and a fetch already happened) and returns the fresh results.
func (s *OpenCodeQuotaService) RefreshAll(force bool) map[string]*OpenCodeQuotaResult {
	if s == nil {
		return map[string]*OpenCodeQuotaResult{}
	}
	s.mu.RLock()
	accounts := s.snapshotAccountsLocked()
	timeout := s.timeoutLocked()
	s.mu.RUnlock()

	if len(accounts) == 0 {
		s.mu.Lock()
		s.cache = map[string]*OpenCodeQuotaResult{}
		s.fetchedAt = s.now().UTC()
		s.mu.Unlock()
		return map[string]*OpenCodeQuotaResult{}
	}
	if !force {
		s.mu.RLock()
		_, fetched := s.cache, s.fetchedAt
		hasCache := !fetched.IsZero()
		s.mu.RUnlock()
		if hasCache {
			return s.Snapshot().Results
		}
	}

	s.fetchMu.Lock()
	defer s.fetchMu.Unlock()

	results := make(map[string]*OpenCodeQuotaResult, len(accounts))
	for _, account := range accounts {
		result := queryOpenCodeGoQuota(account.WorkspaceID, account.AuthCookie, timeout)
		result.AccountID = account.ID
		results[account.ID] = &result
	}
	s.mu.Lock()
	s.cache = results
	s.fetchedAt = s.now().UTC()
	s.mu.Unlock()
	return results
}

// RefreshAccount refreshes a single bound account and updates its cache entry.
func (s *OpenCodeQuotaService) RefreshAccount(id string) *OpenCodeQuotaResult {
	empty := &OpenCodeQuotaResult{Success: false, AccountID: id, FetchedAt: s.now().UTC()}
	if s == nil {
		empty.Error = "OpenCode quota service is unavailable"
		return empty
	}
	id = strings.TrimSpace(id)
	s.mu.RLock()
	var found *OpenCodeAccount
	for index := range s.accounts {
		if s.accounts[index].ID == id {
			account := s.accounts[index]
			found = &account
			break
		}
	}
	timeout := s.timeoutLocked()
	s.mu.RUnlock()
	if found == nil {
		empty.Error = "OpenCode account was not found"
		return empty
	}
	result := queryOpenCodeGoQuota(found.WorkspaceID, found.AuthCookie, timeout)
	result.AccountID = found.ID
	s.mu.Lock()
	if s.cache == nil {
		s.cache = map[string]*OpenCodeQuotaResult{}
	}
	s.cache[found.ID] = &result
	s.fetchedAt = s.now().UTC()
	s.mu.Unlock()
	return &result
}

// Probe queries one workspace without saving any credential.
func (s *OpenCodeQuotaService) Probe(workspaceID, authCookie string, timeoutSeconds int) OpenCodeQuotaResult {
	timeout := time.Duration(openCodeQuotaDefaultTimeout) * time.Second
	if timeoutSeconds >= 1 && timeoutSeconds <= openCodeQuotaMaxTimeoutSeconds {
		timeout = time.Duration(timeoutSeconds) * time.Second
	}
	return queryOpenCodeGoQuota(workspaceID, authCookie, timeout)
}

func queryOpenCodeGoQuota(workspaceID, authCookie string, timeout time.Duration) OpenCodeQuotaResult {
	now := time.Now().UTC()
	target := "https://opencode.ai/workspace/" + url.PathEscape(workspaceID) + "/go"
	client := &http.Client{Timeout: timeout}
	request, errNew := http.NewRequest(http.MethodGet, target, nil)
	if errNew != nil {
		return OpenCodeQuotaResult{Success: false, Workspace: workspaceID, Error: sanitizeOpenCodeError(errNew.Error()), FetchedAt: now}
	}
	request.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) Gecko/20100101 Firefox/148.0")
	request.Header.Set("Accept", "text/html")
	request.Header.Set("Cookie", "auth="+strings.TrimSpace(strings.TrimPrefix(authCookie, "auth=")))
	response, errDo := client.Do(request)
	if errDo != nil {
		return OpenCodeQuotaResult{Success: false, Workspace: workspaceID, Error: "OpenCode Go dashboard request failed: " + sanitizeOpenCodeError(errDo.Error()), FetchedAt: now}
	}
	defer response.Body.Close()
	body, errRead := io.ReadAll(io.LimitReader(response.Body, openCodeQuotaDashboardMaxBytes))
	if errRead != nil {
		return OpenCodeQuotaResult{Success: false, Workspace: workspaceID, Error: "read dashboard: " + sanitizeOpenCodeError(errRead.Error()), FetchedAt: now}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return OpenCodeQuotaResult{Success: false, Workspace: workspaceID, Error: fmt.Sprintf("OpenCode Go dashboard HTTP %d: %s", response.StatusCode, sanitizeOpenCodeError(string(body))), FetchedAt: now}
	}
	rolling, weekly, monthly, source := parseOpenCodeDashboard(string(body), now)
	if rolling == nil && weekly == nil && monthly == nil {
		return OpenCodeQuotaResult{Success: false, Workspace: workspaceID, Error: "could not parse rollingUsage, weeklyUsage, or monthlyUsage from OpenCode Go dashboard", FetchedAt: now}
	}
	return OpenCodeQuotaResult{
		Success:   true,
		Workspace: workspaceID,
		Rolling:   rolling,
		Weekly:    weekly,
		Monthly:   monthly,
		Source:    source,
		FetchedAt: now,
	}
}

var openCodeScrapedNumberPattern = `(-?\d+(?:\.\d+)?)`

var openCodeSSRRegex = map[string][2]*regexp.Regexp{
	"rolling": {
		regexp.MustCompile(`rollingUsage:\$R\[\d+\]=\{[^}]*usagePercent:` + openCodeScrapedNumberPattern + `[^}]*resetInSec:` + openCodeScrapedNumberPattern + `[^}]*\}`),
		regexp.MustCompile(`rollingUsage:\$R\[\d+\]=\{[^}]*resetInSec:` + openCodeScrapedNumberPattern + `[^}]*usagePercent:` + openCodeScrapedNumberPattern + `[^}]*\}`),
	},
	"weekly": {
		regexp.MustCompile(`weeklyUsage:\$R\[\d+\]=\{[^}]*usagePercent:` + openCodeScrapedNumberPattern + `[^}]*resetInSec:` + openCodeScrapedNumberPattern + `[^}]*\}`),
		regexp.MustCompile(`weeklyUsage:\$R\[\d+\]=\{[^}]*resetInSec:` + openCodeScrapedNumberPattern + `[^}]*usagePercent:` + openCodeScrapedNumberPattern + `[^}]*\}`),
	},
	"monthly": {
		regexp.MustCompile(`monthlyUsage:\$R\[\d+\]=\{[^}]*usagePercent:` + openCodeScrapedNumberPattern + `[^}]*resetInSec:` + openCodeScrapedNumberPattern + `[^}]*\}`),
		regexp.MustCompile(`monthlyUsage:\$R\[\d+\]=\{[^}]*resetInSec:` + openCodeScrapedNumberPattern + `[^}]*usagePercent:` + openCodeScrapedNumberPattern + `[^}]*\}`),
	},
}

var (
	openCodeReUsageLabel = regexp.MustCompile(`data-slot="usage-label">([^<]+)<`)
	openCodeReUsageValue = regexp.MustCompile(`data-slot="usage-value">[^0-9]*(\d+(?:\.\d+)?)`)
	openCodeReReset      = regexp.MustCompile(`data-slot="(reset-time|reset-now)">([\s\S]*?)</span>`)
	openCodeReSolidOpen  = regexp.MustCompile(`<!--\$-->`)
	openCodeReSolidClose = regexp.MustCompile(`<!--/-->`)
	openCodeReResetsIn   = regexp.MustCompile(`(?i)Resets?\s*in\s*`)
)

func parseOpenCodeDashboard(body string, now time.Time) (*OpenCodeWindowUsage, *OpenCodeWindowUsage, *OpenCodeWindowUsage, string) {
	rolling := parseOpenCodeSSRWindow(body, "rolling", now)
	weekly := parseOpenCodeSSRWindow(body, "weekly", now)
	monthly := parseOpenCodeSSRWindow(body, "monthly", now)
	if rolling != nil || weekly != nil || monthly != nil {
		return rolling, weekly, monthly, "dashboard_ssr"
	}
	slots := parseOpenCodeDataSlotFormat(body, now)
	return slots["rolling"], slots["weekly"], slots["monthly"], "dashboard_data_slot"
}

func parseOpenCodeSSRWindow(body, name string, now time.Time) *OpenCodeWindowUsage {
	pair, ok := openCodeSSRRegex[name]
	if !ok {
		return nil
	}
	if match := pair[0].FindStringSubmatch(body); len(match) == 3 {
		pct, errPercent := strconv.ParseFloat(match[1], 64)
		sec, errSeconds := strconv.ParseFloat(match[2], 64)
		if errPercent == nil && errSeconds == nil {
			return normalizeOpenCodeWindow(pct, sec, now)
		}
	}
	if match := pair[1].FindStringSubmatch(body); len(match) == 3 {
		sec, errSeconds := strconv.ParseFloat(match[1], 64)
		pct, errPercent := strconv.ParseFloat(match[2], 64)
		if errSeconds == nil && errPercent == nil {
			return normalizeOpenCodeWindow(pct, sec, now)
		}
	}
	return nil
}

func parseOpenCodeDataSlotFormat(body string, now time.Time) map[string]*OpenCodeWindowUsage {
	result := map[string]*OpenCodeWindowUsage{}
	parts := strings.Split(body, `data-slot="usage-item"`)
	for index := 1; index < len(parts); index++ {
		content := parts[index]
		labelMatch := openCodeReUsageLabel.FindStringSubmatch(content)
		valueMatch := openCodeReUsageValue.FindStringSubmatch(content)
		resetMatch := openCodeReReset.FindStringSubmatch(content)
		if len(labelMatch) != 2 || len(valueMatch) != 2 || len(resetMatch) != 3 {
			continue
		}
		label := strings.ToLower(strings.TrimSpace(html.UnescapeString(labelMatch[1])))
		percent, errPercent := strconv.ParseFloat(valueMatch[1], 64)
		if errPercent != nil {
			continue
		}
		resetText := openCodeReSolidOpen.ReplaceAllString(resetMatch[2], "")
		resetText = openCodeReSolidClose.ReplaceAllString(resetText, "")
		resetText = openCodeReResetsIn.ReplaceAllString(resetText, "")
		resetText = strings.TrimSpace(stripOpenCodeTags(resetText))
		var resetSeconds *float64
		if resetMatch[1] == "reset-now" {
			zero := 0.0
			resetSeconds = &zero
		} else if parsed, ok := parseOpenCodeHumanDuration(resetText); ok {
			resetSeconds = &parsed
		}
		if resetSeconds == nil {
			continue
		}
		var key string
		switch {
		case strings.Contains(label, "rolling"):
			key = "rolling"
		case strings.Contains(label, "weekly"):
			key = "weekly"
		case strings.Contains(label, "monthly"):
			key = "monthly"
		default:
			continue
		}
		result[key] = normalizeOpenCodeWindow(percent, *resetSeconds, now)
	}
	return result
}

func parseOpenCodeHumanDuration(value string) (float64, bool) {
	value = strings.ToLower(strings.Join(strings.Fields(value), " "))
	if value == "reset-now" || value == "reset now" || value == "now" || value == "resets now" {
		return 0, true
	}
	units := []struct {
		expression *regexp.Regexp
		multiplier float64
	}{
		{regexp.MustCompile(`(\d+(?:\.\d+)?)\s*days?`), 86400},
		{regexp.MustCompile(`(\d+(?:\.\d+)?)\s*hours?`), 3600},
		{regexp.MustCompile(`(\d+(?:\.\d+)?)\s*minutes?`), 60},
		{regexp.MustCompile(`(\d+(?:\.\d+)?)\s*seconds?`), 1},
	}
	total := 0.0
	found := false
	for _, unit := range units {
		if match := unit.expression.FindStringSubmatch(value); len(match) == 2 {
			parsed, errParse := strconv.ParseFloat(match[1], 64)
			if errParse == nil {
				total += parsed * unit.multiplier
				found = true
			}
		}
	}
	return total, found
}

func normalizeOpenCodeWindow(percent, resetSeconds float64, now time.Time) *OpenCodeWindowUsage {
	if math.IsNaN(percent) || math.IsInf(percent, 0) || math.IsNaN(resetSeconds) || math.IsInf(resetSeconds, 0) {
		return nil
	}
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	if resetSeconds < 0 {
		resetSeconds = 0
	}
	seconds := int64(math.Round(resetSeconds))
	return &OpenCodeWindowUsage{
		UsagePercent:     percent,
		PercentRemaining: 100 - percent,
		ResetInSec:       seconds,
		ResetAt:          now.Add(time.Duration(seconds) * time.Second),
	}
}

func stripOpenCodeTags(value string) string {
	strip := regexp.MustCompile(`<[^>]*>`)
	return html.UnescapeString(strip.ReplaceAllString(value, ""))
}

func sanitizeOpenCodeError(value string) string {
	value = strings.Join(strings.Fields(stripOpenCodeTags(value)), " ")
	if len(value) > openCodeQuotaErrorSummaryLength {
		value = value[:openCodeQuotaErrorSummaryLength]
	}
	if value == "" {
		return "unknown error"
	}
	return value
}

func sortedOpenCodeAccountIDs(results map[string]*OpenCodeQuotaResult) []string {
	keys := make([]string, 0, len(results))
	for key := range results {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
