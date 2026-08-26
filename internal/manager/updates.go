package manager

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

var updatePersistRetryDelay = 30 * time.Second

type UpdateChecker struct {
	mu             sync.RWMutex
	storeMu        sync.Mutex
	config         Config
	store          string
	currentVersion string
	runtime        *RuntimeOwnership
	policy         UpdatePolicy
	checkedAt      time.Time
	error          string
	configured     bool
	loadFailed     bool
	dirty          bool
	retryTimer     *time.Timer
	retryScheduled bool
	closed         bool
	now            func() time.Time
}

func (c *UpdateChecker) SetRuntimeOwnership(runtime *RuntimeOwnership) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.runtime = runtime
	c.mu.Unlock()
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
	configuredPolicy, hasConfiguredPolicy, errConfiguredPolicy := updatePolicyFromConfig(config)
	// Serialize the complete read-modify-write cycle.  The update state is
	// shared by the UI check endpoint, policy updates, and configuration
	// reloads; protecting only the file write allowed an older in-memory
	// snapshot to overwrite a newer policy or checked_at value.
	c.storeMu.Lock()
	defer c.storeMu.Unlock()
	c.mu.RLock()
	sameStore := c.configured && c.store == storePath && !c.loadFailed
	c.mu.RUnlock()
	if sameStore {
		c.mu.RLock()
		dirty := c.dirty
		c.mu.RUnlock()
		if dirty {
			c.persistDirtyLocked()
			c.mu.RLock()
			stillDirty := c.dirty
			c.mu.RUnlock()
			if stillDirty {
				return
			}
		}
		c.mu.Lock()
		c.config = config
		currentPolicy := c.policy
		if hasConfiguredPolicy && errConfiguredPolicy != nil {
			c.error = "update state could not be loaded"
		} else if hasConfiguredPolicy && c.error == "update state could not be loaded" {
			c.error = ""
		}
		if hasConfiguredPolicy && errConfiguredPolicy == nil && currentPolicy != configuredPolicy {
			state := c.persistedStateLocked()
			state.Policy = configuredPolicy
			state.Error = ""
			if errSave := saveUpdateState(storePath, state); errSave != nil {
				c.error = "update state could not be persisted"
			} else {
				c.policy = configuredPolicy
				c.loadFailed = false
				if c.error == "update state could not be persisted" {
					c.error = ""
				}
			}
		}
		c.mu.Unlock()
		return
	}

	c.mu.RLock()
	needsFlush := c.configured && c.store != storePath && c.dirty
	c.mu.RUnlock()
	if needsFlush {
		c.persistDirtyLocked()
		c.mu.RLock()
		stillDirty := c.dirty
		c.mu.RUnlock()
		if stillDirty {
			return
		}
	}

	state := persistedUpdateState{Version: updateStoreVersion, Policy: defaultUpdatePolicy()}
	loadFailed := false
	loaded, errLoad := loadUpdateState(storePath)
	if errLoad == nil {
		state = loaded
	} else if !errors.Is(errLoad, os.ErrNotExist) {
		state.Error = "update state could not be loaded"
		loadFailed = true
	}
	if hasConfiguredPolicy {
		if errConfiguredPolicy != nil {
			state.Error = "update state could not be loaded"
		} else {
			state.Policy = configuredPolicy
			state.Error = ""
			if errSave := saveUpdateState(storePath, state); errSave != nil {
				state.Error = "update state could not be persisted"
			}
		}
	}
	c.mu.RLock()
	configuredSameStore := c.configured && c.store == storePath
	c.mu.RUnlock()
	if loadFailed && configuredSameStore {
		c.mu.Lock()
		c.config = config
		c.loadFailed = true
		c.error = "update state could not be loaded"
		c.mu.Unlock()
		return
	}
	c.mu.Lock()
	c.config = config
	c.store = storePath
	c.policy = state.Policy
	c.checkedAt = state.CheckedAt
	c.error = retainedUpdateStateError(state.Error)
	c.loadFailed = loadFailed
	c.dirty = c.error == "update state could not be persisted"
	c.configured = true
	if c.dirty {
		c.schedulePersistRetryLocked()
	}
	c.mu.Unlock()
}

func updatePolicyFromConfig(config Config) (UpdatePolicy, bool, error) {
	if config.UpdatePolicy == nil {
		return UpdatePolicy{}, false, nil
	}
	policy, errValidate := validateUpdatePolicy(*config.UpdatePolicy)
	if errValidate != nil {
		return UpdatePolicy{}, true, errValidate
	}
	return policy, true, nil
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
	runtime := c.runtime
	c.mu.RUnlock()
	if _, _, currentOK := parseReleaseVersion(current); !currentOK && errMessage == "" {
		errMessage = "current version is invalid"
	}
	snapshot := UpdateSnapshot{
		Policy:         policy,
		CurrentVersion: current,
		CheckedAt:      checkedAt,
		Error:          safeUpdateError(errMessage),
	}
	if runtime != nil {
		snapshot.Runtime = runtime.Snapshot()
	}
	return snapshot
}

func (c *UpdateChecker) SetPolicy(policy UpdatePolicy) (UpdateSnapshot, error) {
	if c == nil {
		return UpdateSnapshot{}, fmt.Errorf("update checker is unavailable")
	}
	normalized, errValidate := validateUpdatePolicy(policy)
	if errValidate != nil {
		return UpdateSnapshot{}, errValidate
	}
	c.storeMu.Lock()
	defer c.storeMu.Unlock()
	c.mu.Lock()
	storePath := c.store
	state := c.persistedStateLocked()
	closed := c.closed
	if closed || strings.TrimSpace(storePath) == "" {
		c.mu.Unlock()
		return UpdateSnapshot{}, fmt.Errorf("update storage is unavailable")
	}
	state.Policy = normalized
	state.Error = ""
	errSave := saveUpdateState(storePath, state)
	if errSave != nil {
		c.mu.Unlock()
		return UpdateSnapshot{}, fmt.Errorf("save update policy: %w", errSave)
	}
	c.policy = normalized
	c.error = ""
	c.loadFailed = false
	c.dirty = false
	c.stopPersistRetryLocked()
	c.mu.Unlock()
	return c.Snapshot(), nil
}

// RequestCheck records when the authenticated UI queried CPA's plugin store.
// Version discovery itself stays in CPA so this plugin never needs GitHub access.
func (c *UpdateChecker) RequestCheck() UpdateSnapshot {
	if c == nil {
		return UpdateSnapshot{Policy: defaultUpdatePolicy()}
	}
	c.storeMu.Lock()
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		c.storeMu.Unlock()
		return c.Snapshot()
	}
	c.checkedAt = c.currentTime()
	c.error = ""
	c.dirty = true
	c.mu.Unlock()
	c.persistDirtyLocked()
	c.storeMu.Unlock()
	return c.Snapshot()
}

func (c *UpdateChecker) Shutdown() {
	if c == nil {
		return
	}
	c.storeMu.Lock()
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		c.storeMu.Unlock()
		return
	}
	c.closed = true
	c.mu.Unlock()
	c.persistDirtyLocked()
	c.mu.Lock()
	c.stopPersistRetryLocked()
	c.mu.Unlock()
	c.storeMu.Unlock()
}

// persistDirtyLocked writes the latest check timestamp after transient storage
// failures. The caller must hold storeMu so policy saves and Configure cannot
// be overwritten by an older retry snapshot.
func (c *UpdateChecker) persistDirtyLocked() {
	if c == nil {
		return
	}
	c.mu.RLock()
	if !c.dirty || strings.TrimSpace(c.store) == "" {
		c.mu.RUnlock()
		return
	}
	storePath := c.store
	state := c.persistedStateLocked()
	state.Error = ""
	c.mu.RUnlock()
	if errSave := saveUpdateState(storePath, state); errSave != nil {
		c.mu.Lock()
		c.error = "update state could not be persisted"
		c.schedulePersistRetryLocked()
		c.mu.Unlock()
		return
	}
	c.mu.Lock()
	if c.store == storePath {
		c.dirty = false
		c.loadFailed = false
		if c.error == "update state could not be persisted" || c.error == "update state could not be loaded" {
			c.error = ""
		}
		c.stopPersistRetryLocked()
	}
	c.mu.Unlock()
}

func (c *UpdateChecker) schedulePersistRetryLocked() {
	if c == nil || c.closed || c.retryScheduled || !c.dirty {
		return
	}
	c.retryScheduled = true
	c.retryTimer = time.AfterFunc(updatePersistRetryDelay, func() {
		c.storeMu.Lock()
		c.mu.Lock()
		c.retryScheduled = false
		c.retryTimer = nil
		closed := c.closed
		c.mu.Unlock()
		if !closed {
			c.persistDirtyLocked()
		}
		c.storeMu.Unlock()
	})
}

func (c *UpdateChecker) stopPersistRetryLocked() {
	if c.retryTimer != nil {
		c.retryTimer.Stop()
		c.retryTimer = nil
	}
	c.retryScheduled = false
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
