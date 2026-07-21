package manager

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

type UpdateChecker struct {
	mu             sync.RWMutex
	storeMu        sync.Mutex
	config         Config
	store          string
	currentVersion string
	policy         UpdatePolicy
	checkedAt      time.Time
	error          string
	configured     bool
	closed         bool
	now            func() time.Time
}

func NewUpdateChecker(currentVersion string) *UpdateChecker {
	config := normalizeConfig(Config{})
	return &UpdateChecker{
		config:         config,
		store:          updateStorePath(config.DataDir),
		currentVersion: strings.TrimSpace(currentVersion),
		policy:         defaultUpdatePolicy(),
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
	sameStore := c.configured && c.store == storePath
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
	c.checkedAt = state.CheckedAt
	c.error = retainedUpdateStateError(state.Error)
	c.configured = true
	c.mu.Unlock()
}

func (c *UpdateChecker) Snapshot() UpdateSnapshot {
	if c == nil {
		return UpdateSnapshot{Policy: defaultUpdatePolicy()}
	}
	c.mu.RLock()
	policy := c.policy
	current := c.currentVersion
	checkedAt := c.checkedAt
	errMessage := c.error
	c.mu.RUnlock()
	if _, _, currentOK := parseReleaseVersion(current); !currentOK && errMessage == "" {
		errMessage = "current version is invalid"
	}
	return UpdateSnapshot{
		Policy:         policy,
		CurrentVersion: current,
		CheckedAt:      checkedAt,
		Error:          safeUpdateError(errMessage),
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
	return c.Snapshot(), nil
}

// RequestCheck records when the authenticated UI queried CPA's plugin store.
// Version discovery itself stays in CPA so this plugin never needs GitHub access.
func (c *UpdateChecker) RequestCheck() UpdateSnapshot {
	if c == nil {
		return UpdateSnapshot{Policy: defaultUpdatePolicy()}
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return c.Snapshot()
	}
	c.checkedAt = c.currentTime()
	c.error = ""
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
	return c.Snapshot()
}

func (c *UpdateChecker) Shutdown() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
}

func (c *UpdateChecker) persistedStateLocked() persistedUpdateState {
	return persistedUpdateState{
		Version:   updateStoreVersion,
		Policy:    c.policy,
		CheckedAt: c.checkedAt,
		Error:     c.error,
	}
}

func (c *UpdateChecker) currentTime() time.Time {
	now := time.Now
	if c != nil && c.now != nil {
		now = c.now
	}
	return now().UTC()
}

func retainedUpdateStateError(value string) string {
	switch safeUpdateError(value) {
	case "update state could not be loaded", "update state could not be persisted":
		return safeUpdateError(value)
	default:
		return ""
	}
}
