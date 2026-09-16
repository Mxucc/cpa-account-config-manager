package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

// Automatic retry is one operator setting that applies to every credential of
// the three product families this plugin manages: Codex accounts, OpenCode
// channels and Cline Pass channels. CPA owns the request loop and reads the
// per-credential budget from the credential itself, so the plugin only
// publishes that budget: a top-level request_retry key in an auth file, and the
// request-retry field of an OpenAI-compatible channel row. The host-wide retry
// knobs are raised only when a disabled value would defeat the setting, and an
// existing non-zero value is never lowered.
const (
	autoRetryDefaultAttempts = 5
	autoRetryMaxAttempts     = 10
	// autoRetryMinRetryIntervalSeconds is the host max-retry-interval the plugin
	// raises a disabled host to. CPA never retries while the host value is <= 0
	// and only waits for a cooled-down credential within this interval.
	autoRetryMinRetryIntervalSeconds = 30

	// autoRetryTransientCooldownFallbackSeconds is the cooldown CPA applies to a
	// transient upstream failure (408/500/502/503/504) while the host leaves
	// transient-error-cooldown-seconds at its default. A retry interval shorter than
	// the cooldown cannot retry such a failure at all, because CPA refuses a wait
	// longer than max-retry-interval, so the apply pass raises the interval to cover
	// it instead of leaving the client to see the error.
	autoRetryTransientCooldownFallbackSeconds = 60

	autoRetryStoreVersion  = 1
	autoRetryStoreFileName = "auto-retry.json"
)

// AutoRetrySettings is the persisted operator setting.
type AutoRetrySettings struct {
	Attempts int `json:"attempts"`
}

type autoRetryPersisted struct {
	Version  int               `json:"version"`
	Settings AutoRetrySettings `json:"settings"`
}

// AutoRetryHostState is the credential-free snapshot of the host retry knobs
// read by the last apply pass. Configured reports whether both per-request
// retry knobs were readable, so a caller can tell zeros from unknown values.
type AutoRetryHostState struct {
	RequestRetry        int  `json:"request_retry"`
	MaxRetryInterval    int  `json:"max_retry_interval"`
	MaxRetryCredentials int  `json:"max_retry_credentials"`
	BootstrapRetries    *int `json:"bootstrap_retries,omitempty"`
	// TransientCooldownSeconds is the host's transient-error cooldown, which is what
	// decides the smallest max-retry-interval that can still retry a 408/5xx.
	TransientCooldownSeconds *int `json:"transient_cooldown_seconds,omitempty"`
	Configured               bool `json:"configured"`
}

// AutoRetryAppliedState counts what the last apply pass changed, plus the host
// values it had to raise. Counts describe written credentials, not matched
// ones: a row that already carries the configured budget is left untouched.
type AutoRetryAppliedState struct {
	CodexAccounts          int    `json:"codex_accounts"`
	OpenCodeChannels       int    `json:"opencode_channels"`
	ClinePassChannels      int    `json:"cline_pass_channels"`
	Skipped                int    `json:"skipped"`
	HostRequestRetryRaised bool   `json:"host_request_retry_raised"`
	HostIntervalRaised     bool   `json:"host_interval_raised"`
	UpdatedAt              string `json:"updated_at"`
}

// AutoRetryState is the Management API payload of the automatic retry setting.
// It never carries a credential.
type AutoRetryState struct {
	Attempts        int                   `json:"attempts"`
	DefaultAttempts int                   `json:"default_attempts"`
	MaxAttempts     int                   `json:"max_attempts"`
	Enabled         bool                  `json:"enabled"`
	Host            AutoRetryHostState    `json:"host"`
	Applied         AutoRetryAppliedState `json:"applied"`
	StorageError    string                `json:"storage_error"`
}

// AutoRetryService persists the automatic retry budget and the outcome of the
// last apply pass. Only the number and non-sensitive host values are stored;
// credentials never reach this service.
type AutoRetryService struct {
	mu         sync.RWMutex
	storeMu    sync.Mutex
	store      string
	attempts   int
	host       AutoRetryHostState
	applied    AutoRetryAppliedState
	storageErr string
	configured bool
	loadFailed bool
}

func NewAutoRetryService() *AutoRetryService {
	config := normalizeConfig(Config{})
	return &AutoRetryService{store: autoRetryStorePath(config.DataDir), attempts: autoRetryDefaultAttempts}
}

func autoRetryStorePath(dataDir string) string {
	return filepath.Join(dataDir, autoRetryStoreFileName)
}

// Configure loads the persisted setting. A missing file is the documented
// default (5), so a first run reports the default without writing it; only an
// operator action persists a store file.
func (s *AutoRetryService) Configure(config Config) {
	if s == nil {
		return
	}
	config = normalizeConfig(config)
	storePath := autoRetryStorePath(config.DataDir)
	s.mu.RLock()
	sameStore := s.configured && s.store == storePath
	s.mu.RUnlock()
	if sameStore && !s.loadFailed {
		return
	}
	attempts := autoRetryDefaultAttempts
	storageErr := ""
	loaded, errLoad := loadAutoRetrySettings(storePath)
	if errLoad == nil {
		attempts = normalizeAutoRetryAttempts(loaded.Attempts)
	} else if !errors.Is(errLoad, os.ErrNotExist) {
		storageErr = "automatic retry settings could not be loaded"
	}
	s.mu.Lock()
	s.store = storePath
	s.attempts = attempts
	s.storageErr = storageErr
	s.loadFailed = storageErr != ""
	s.configured = true
	s.mu.Unlock()
}

// normalizeAutoRetryAttempts turns a malformed stored value into the default
// instead of propagating it to CPA.
func normalizeAutoRetryAttempts(attempts int) int {
	if attempts < 0 || attempts > autoRetryMaxAttempts {
		return autoRetryDefaultAttempts
	}
	return attempts
}

// Attempts reports the configured budget. A service that was never configured
// still reports the documented default, and a nil service reports it too so a
// channel bind never has to guard the call.
func (s *AutoRetryService) Attempts() int {
	if s == nil {
		return autoRetryDefaultAttempts
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.attempts
}

// SetAttempts persists a budget in 0..10. Zero disables retries for the
// credentials the plugin manages; anything outside the range is rejected.
func (s *AutoRetryService) SetAttempts(attempts int) error {
	if s == nil {
		return fmt.Errorf("automatic retry settings are unavailable")
	}
	if attempts < 0 || attempts > autoRetryMaxAttempts {
		return fmt.Errorf("attempts must be between 0 and %d", autoRetryMaxAttempts)
	}
	s.mu.RLock()
	storePath := s.store
	configured := s.configured
	s.mu.RUnlock()
	if !configured || strings.TrimSpace(storePath) == "" {
		return fmt.Errorf("automatic retry settings storage is unavailable")
	}
	s.storeMu.Lock()
	errSave := saveAutoRetrySettings(storePath, attempts)
	s.storeMu.Unlock()
	if errSave != nil {
		s.mu.Lock()
		s.storageErr = "automatic retry settings could not be persisted"
		s.mu.Unlock()
		return fmt.Errorf("save automatic retry settings: %w", errSave)
	}
	s.mu.Lock()
	s.attempts = attempts
	s.storageErr = ""
	s.loadFailed = false
	s.mu.Unlock()
	return nil
}

func (s *AutoRetryService) State() AutoRetryState {
	if s == nil {
		return AutoRetryState{
			Attempts:        autoRetryDefaultAttempts,
			DefaultAttempts: autoRetryDefaultAttempts,
			MaxAttempts:     autoRetryMaxAttempts,
			Enabled:         true,
			StorageError:    "automatic retry settings are unavailable",
		}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return AutoRetryState{
		Attempts:        s.attempts,
		DefaultAttempts: autoRetryDefaultAttempts,
		MaxAttempts:     autoRetryMaxAttempts,
		Enabled:         s.attempts > 0,
		Host:            s.host,
		Applied:         s.applied,
		StorageError:    s.storageErr,
	}
}

// hostState copies the last known host values. The caller starts from this
// snapshot so a partial read keeps the previous value instead of reporting a
// zero it did not observe.
func (s *AutoRetryService) hostState() AutoRetryHostState {
	if s == nil {
		return AutoRetryHostState{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.host
}

// recordApply stores one pass outcome. A nil host keeps the previous host
// snapshot, which is what a pass without a Management credential does.
func (s *AutoRetryService) recordApply(host *AutoRetryHostState, applied AutoRetryAppliedState) {
	if s == nil {
		return
	}
	applied.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	s.mu.Lock()
	if host != nil {
		s.host = *host
	}
	s.applied = applied
	s.mu.Unlock()
}

func loadAutoRetrySettings(path string) (AutoRetrySettings, error) {
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		return AutoRetrySettings{}, errRead
	}
	var state autoRetryPersisted
	if errDecode := json.Unmarshal(raw, &state); errDecode != nil {
		return AutoRetrySettings{}, fmt.Errorf("decode automatic retry settings: %w", errDecode)
	}
	if state.Version != autoRetryStoreVersion {
		return AutoRetrySettings{}, fmt.Errorf("unsupported automatic retry settings version %d", state.Version)
	}
	return state.Settings, nil
}

func saveAutoRetrySettings(path string, attempts int) error {
	return savePrivateJSON(path, autoRetryPersisted{
		Version:  autoRetryStoreVersion,
		Settings: AutoRetrySettings{Attempts: attempts},
	})
}

// handleAutoRetryGet reads the setting without running an apply pass.
func (a *App) handleAutoRetryGet(req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	if resolveManagementKey(req.Headers) == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	if a == nil || a.autoRetry == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "automatic retry service is unavailable"})
	}
	return jsonResponse(http.StatusOK, a.autoRetry.State())
}

// handleAutoRetryUpdate validates and persists the budget, applies it to every
// managed credential, and answers with the same payload as GET.
func (a *App) handleAutoRetryUpdate(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	managementKey := resolveManagementKey(req.Headers)
	if managementKey == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	if a == nil || a.autoRetry == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "automatic retry service is unavailable"})
	}
	var request struct {
		Attempts *int `json:"attempts"`
	}
	if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errDecode.Error()})
	}
	if request.Attempts == nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "attempts is required"})
	}
	if *request.Attempts < 0 || *request.Attempts > autoRetryMaxAttempts {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": fmt.Sprintf("attempts must be between 0 and %d", autoRetryMaxAttempts)})
	}
	if errSet := a.autoRetry.SetAttempts(*request.Attempts); errSet != nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "automatic retry setting could not be persisted"})
	}
	a.runAutoRetryApply(ctx, managementKey)
	managementKey = ""
	return jsonResponse(http.StatusOK, a.autoRetry.State())
}

// scheduleAutoRetryApply runs one best-effort apply pass shortly after a
// configuration so the setting reaches stored credentials without an operator
// action. It uses its own goroutine and its own deadline, ignores failures, and
// reports nothing: nothing here may block a request or surface an error.
func (a *App) scheduleAutoRetryApply() {
	if a == nil || a.autoRetry == nil {
		return
	}
	time.AfterFunc(autoRetryBootstrapDelay, func() {
		defer func() {
			// A background pass must never take down the host.
			_ = recover()
		}()
		if a.runtime == nil || !a.runtime.AllowsBackgroundWork() {
			return
		}
		a.runAutoRetryApply(context.Background(), resolveManagementKey(nil))
	})
}

// autoRetryRequiredRetryInterval is the smallest host max-retry-interval that lets
// the operator's budget reach the failures this plugin can make transparent: any
// recoverable failure cools its credential for at least the host's transient-error
// cooldown, and CPA only waits for a cooled-down credential while the wait fits
// inside max-retry-interval. A shorter interval therefore disables the very retry
// the setting promises.
func autoRetryRequiredRetryInterval(transientCooldownSeconds int) int {
	required := autoRetryMinRetryIntervalSeconds
	if transientCooldownSeconds <= 0 {
		transientCooldownSeconds = autoRetryTransientCooldownFallbackSeconds
	}
	if transientCooldownSeconds > required {
		required = transientCooldownSeconds
	}
	return required
}

// autoRetryTransientCooldown reports the host's transient-error cooldown from the
// last apply pass. An unreported value is returned as 0 so the caller falls back to
// the documented default.
func autoRetryTransientCooldown(host AutoRetryHostState) int {
	if host.TransientCooldownSeconds == nil {
		return 0
	}
	return *host.TransientCooldownSeconds
}
