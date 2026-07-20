package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

const maxReleaseMetadataBytes = 1 << 20

type HTTPHost interface {
	DoHTTP(context.Context, cpaapi.HTTPRequest) (cpaapi.HTTPResponse, error)
}

type UpdateChecker struct {
	mu             sync.RWMutex
	storeMu        sync.Mutex
	wait           sync.WaitGroup
	host           HTTPHost
	config         Config
	store          string
	repository     string
	currentVersion string
	policy         UpdatePolicy
	latestVersion  string
	checkedAt      time.Time
	error          string
	checking       bool
	pending        bool
	wake           chan struct{}
	cancel         context.CancelFunc
	started        bool
	closed         bool
	now            func() time.Time
}

func NewUpdateChecker(host HTTPHost, currentVersion, repository string) *UpdateChecker {
	config := normalizeConfig(Config{})
	return &UpdateChecker{
		host:           host,
		config:         config,
		store:          updateStorePath(config.DataDir),
		repository:     strings.TrimSpace(repository),
		currentVersion: strings.TrimSpace(currentVersion),
		policy:         defaultUpdatePolicy(),
		wake:           make(chan struct{}, 1),
		now:            time.Now,
	}
}

func (c *UpdateChecker) Configure(config Config) {
	if c == nil {
		return
	}
	config = normalizeConfig(config)
	storePath := updateStorePath(config.DataDir)
	c.mu.RLock()
	sameStore := c.started && c.store == storePath
	c.mu.RUnlock()
	if sameStore {
		c.mu.Lock()
		c.config = config
		c.mu.Unlock()
		return
	}

	state := persistedUpdateState{Version: updateStoreVersion, Policy: defaultUpdatePolicy()}
	loaded, errLoad := loadUpdateState(storePath)
	if errLoad == nil {
		state = loaded
	} else if !errors.Is(errLoad, os.ErrNotExist) {
		state.Error = "update state could not be loaded"
	}
	c.mu.Lock()
	c.config = config
	c.store = storePath
	c.policy = state.Policy
	c.latestVersion = state.LatestVersion
	c.checkedAt = state.CheckedAt
	c.error = safeUpdateError(state.Error)
	start := !c.started && !c.closed
	if start {
		ctx, cancel := context.WithCancel(context.Background())
		c.cancel = cancel
		c.started = true
		c.wait.Add(1)
		go c.run(ctx)
	}
	checkNow := start && c.host != nil && c.policy.CheckEnabled
	c.mu.Unlock()
	if checkNow {
		c.RequestCheck()
	}
}

func (c *UpdateChecker) Snapshot() UpdateSnapshot {
	if c == nil {
		return UpdateSnapshot{Policy: defaultUpdatePolicy()}
	}
	c.mu.RLock()
	policy := c.policy
	current := c.currentVersion
	latest := c.latestVersion
	checking := c.checking
	pending := c.pending
	checkedAt := c.checkedAt
	errMessage := c.error
	repository := c.repository
	c.mu.RUnlock()
	available, normalizedLatest, versionsOK := releaseVersionNewer(current, latest)
	if latest != "" && versionsOK {
		latest = normalizedLatest
	}
	if _, _, currentOK := parseReleaseVersion(current); !currentOK && errMessage == "" {
		errMessage = "current version is invalid"
	}
	releaseURL := ""
	if latest != "" {
		if owner, repo, ok := parseGitHubRepository(repository); ok {
			releaseURL = fmt.Sprintf("https://github.com/%s/%s/releases/tag/v%s", owner, repo, url.PathEscape(latest))
		}
	}
	return UpdateSnapshot{
		Policy:          policy,
		CurrentVersion:  current,
		LatestVersion:   latest,
		UpdateAvailable: versionsOK && available,
		ReleaseURL:      releaseURL,
		Checking:        checking,
		Pending:         pending,
		CheckedAt:       checkedAt,
		Error:           safeUpdateError(errMessage),
	}
}

func (c *UpdateChecker) SetPolicy(policy UpdatePolicy) (UpdateSnapshot, error) {
	if c == nil {
		return UpdateSnapshot{}, fmt.Errorf("update checker is unavailable")
	}
	normalized, errValidate := validateUpdatePolicy(policy)
	if errValidate != nil {
		return UpdateSnapshot{}, errValidate
	}
	c.mu.RLock()
	storePath := c.store
	state := c.persistedStateLocked()
	closed := c.closed
	c.mu.RUnlock()
	if closed || strings.TrimSpace(storePath) == "" {
		return UpdateSnapshot{}, fmt.Errorf("update storage is unavailable")
	}
	state.Policy = normalized
	c.storeMu.Lock()
	errSave := saveUpdateState(storePath, state)
	c.storeMu.Unlock()
	if errSave != nil {
		return UpdateSnapshot{}, fmt.Errorf("save update policy: %w", errSave)
	}
	c.mu.Lock()
	c.policy = normalized
	c.error = ""
	c.mu.Unlock()
	if normalized.CheckEnabled {
		c.RequestCheck()
	}
	return c.Snapshot(), nil
}

func (c *UpdateChecker) RequestCheck() UpdateSnapshot {
	if c == nil {
		return UpdateSnapshot{Policy: defaultUpdatePolicy()}
	}
	c.mu.Lock()
	started := c.started && !c.closed
	if started {
		c.pending = true
	}
	c.mu.Unlock()
	if started {
		select {
		case c.wake <- struct{}{}:
		default:
		}
	}
	return c.Snapshot()
}

func (c *UpdateChecker) Shutdown() {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	cancel := c.cancel
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	c.wait.Wait()
}

func (c *UpdateChecker) run(ctx context.Context) {
	defer c.wait.Done()
	timer := time.NewTimer(c.checkInterval())
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.wake:
			c.check(ctx)
		case <-timer.C:
			if c.checkEnabled() {
				c.check(ctx)
			}
		}
		resetInspectionTimer(timer, c.checkInterval())
	}
}

func (c *UpdateChecker) check(ctx context.Context) {
	c.mu.Lock()
	if c.checking || c.closed {
		c.mu.Unlock()
		return
	}
	c.checking = true
	c.pending = false
	repository := c.repository
	host := c.host
	c.mu.Unlock()

	latest := ""
	errMessage := ""
	owner, repo, validRepository := parseGitHubRepository(repository)
	if !validRepository {
		errMessage = "repository metadata is invalid"
	} else if host == nil {
		errMessage = "update check is unavailable"
	} else {
		request := cpaapi.HTTPRequest{
			Method: http.MethodGet,
			URL:    fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", owner, repo),
			Headers: http.Header{
				"Accept":     []string{"application/vnd.github+json"},
				"User-Agent": []string{"cpa-account-config-manager-update-check"},
			},
		}
		response, errRequest := host.DoHTTP(ctx, request)
		if errRequest != nil || response.StatusCode != http.StatusOK {
			errMessage = "release metadata request failed"
		} else if len(response.Body) == 0 || len(response.Body) > maxReleaseMetadataBytes {
			errMessage = "release metadata response was invalid"
		} else {
			var release struct {
				TagName    string `json:"tag_name"`
				Draft      bool   `json:"draft"`
				Prerelease bool   `json:"prerelease"`
			}
			if errDecode := json.Unmarshal(response.Body, &release); errDecode != nil || release.Draft || release.Prerelease {
				errMessage = "release metadata response was invalid"
			} else if _, normalized, okVersion := parseReleaseVersion(release.TagName); !okVersion {
				errMessage = "release metadata response was invalid"
			} else {
				latest = normalized
			}
		}
	}

	c.mu.Lock()
	c.checking = false
	c.latestVersion = latest
	c.checkedAt = c.currentTime()
	c.error = errMessage
	state := c.persistedStateLocked()
	storePath := c.store
	c.mu.Unlock()
	if strings.TrimSpace(storePath) != "" {
		c.storeMu.Lock()
		errSave := saveUpdateState(storePath, state)
		c.storeMu.Unlock()
		if errSave != nil {
			c.mu.Lock()
			c.error = "update state could not be persisted"
			c.mu.Unlock()
		}
	}
}

func (c *UpdateChecker) persistedStateLocked() persistedUpdateState {
	return persistedUpdateState{
		Version:       updateStoreVersion,
		Policy:        c.policy,
		LatestVersion: c.latestVersion,
		CheckedAt:     c.checkedAt,
		Error:         c.error,
	}
}

func (c *UpdateChecker) checkEnabled() bool {
	c.mu.RLock()
	enabled := c.policy.CheckEnabled && !c.closed
	c.mu.RUnlock()
	return enabled
}

func (c *UpdateChecker) checkInterval() time.Duration {
	c.mu.RLock()
	hours := c.policy.CheckIntervalHours
	c.mu.RUnlock()
	if hours < minUpdateCheckIntervalHours || hours > maxUpdateCheckIntervalHours {
		hours = defaultUpdateCheckIntervalHours
	}
	return time.Duration(hours) * time.Hour
}

func (c *UpdateChecker) currentTime() time.Time {
	now := time.Now
	if c != nil && c.now != nil {
		now = c.now
	}
	return now().UTC()
}

func parseGitHubRepository(value string) (string, string, bool) {
	parsed, errParse := url.Parse(strings.TrimSpace(value))
	if errParse != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Host, "github.com") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", "", false
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 2 || !safeGitHubPathPart(parts[0]) || !safeGitHubPathPart(parts[1]) {
		return "", "", false
	}
	return parts[0], strings.TrimSuffix(parts[1], ".git"), true
}

func safeGitHubPathPart(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 100 {
		return false
	}
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '-' || char == '_' || char == '.' {
			continue
		}
		return false
	}
	return true
}
