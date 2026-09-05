package manager

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

const (
	providerRuntimeStoreVersion  = 2
	providerRuntimeStoreFileName = "ai-provider-runtime.json"
	providerRuntimePersistDelay  = 500 * time.Millisecond
	providerRuntimeMaxIdentities = 10000
	providerRuntimeMaxModels     = 512
	providerRuntimeMaxEvents     = 10000
	providerRuntimeMaxWindow     = time.Hour
	// A missing completion callback must not leave the runtime dashboard's
	// active count (or request map) growing forever. CPA requests can be long
	// lived, so use a generous lease and prune at a lower cadence.
	providerRuntimeRequestLease  = 30 * time.Minute
	providerRuntimePruneInterval = time.Minute
)

// ProviderRuntimeSnapshot is intentionally redacted. It contains no API key,
// token, cookie, header, or provider credential material.
type ProviderRuntimeSnapshot struct {
	Provider                string `json:"provider"`
	AuthIndex               string `json:"auth_index,omitempty"`
	Identity                string `json:"identity"`
	Supported               bool   `json:"supported"`
	Reason                  string `json:"reason,omitempty"`
	ConcurrencyConfigurable bool   `json:"concurrency_configurable"`
	Active                  int    `json:"active"`
	Waiting                 int    `json:"waiting"`
	Limit                   int    `json:"limit"`
	RequestLimit            int    `json:"request_limit"`
	RequestWindowSeconds    int    `json:"request_window_seconds"`
	UsedRequests            int    `json:"used_requests"`
	// Legacy aliases retained for older clients.
	Limit15s        int                  `json:"limit_15s"`
	Used60s         int                  `json:"used_60s"`
	Used15s         int                  `json:"used_15s"`
	InputTokens     int64                `json:"input_tokens"`
	OutputTokens    int64                `json:"output_tokens"`
	ReasoningTokens int64                `json:"reasoning_tokens"`
	CachedTokens    int64                `json:"cached_tokens"`
	TotalTokens     int64                `json:"total_tokens"`
	AmountUSD       float64              `json:"amount_usd"`
	RatedRequests   int64                `json:"rated_requests"`
	UnratedRequests int64                `json:"unrated_requests"`
	Quota           ProviderRuntimeQuota `json:"quota"`
	Models          []ProviderModelUsage `json:"models,omitempty"`
	UpdatedAt       time.Time            `json:"updated_at"`
}

type ProviderRuntimeQuota struct {
	FiveHourAmountUSD float64 `json:"five_hour_amount_usd"`
	SevenDayAmountUSD float64 `json:"seven_day_amount_usd"`
	FiveHourPercent   float64 `json:"five_hour_percent,omitempty"`
	SevenDayPercent   float64 `json:"seven_day_percent,omitempty"`
}

type ProviderModelUsage struct {
	Model           string  `json:"model"`
	InputTokens     int64   `json:"input_tokens"`
	OutputTokens    int64   `json:"output_tokens"`
	ReasoningTokens int64   `json:"reasoning_tokens"`
	CachedTokens    int64   `json:"cached_tokens"`
	TotalTokens     int64   `json:"total_tokens"`
	AmountUSD       float64 `json:"amount_usd"`
	Rated           bool    `json:"rated"`
	RatedRequests   int64   `json:"rated_requests"`
	UnratedRequests int64   `json:"unrated_requests"`
}

type providerRuntimeRequest struct {
	AggregateKey string
	AdmittedAt   time.Time
}

type providerRuntimeModel struct {
	ProviderModelUsage
}

type providerRuntimeEvent struct {
	At          time.Time
	AmountNanos int64
}

type providerRuntimeAggregate struct {
	Provider        string
	AuthIndex       string
	Identity        string
	Active          int
	InputTokens     int64
	OutputTokens    int64
	ReasoningTokens int64
	CachedTokens    int64
	TotalTokens     int64
	AmountNanos     int64
	RatedRequests   int64
	UnratedRequests int64
	Models          map[string]*providerRuntimeModel
	Events          []providerRuntimeEvent
	RequestEvents   []time.Time `json:"request_events,omitempty"`
	UpdatedAt       time.Time
}

type persistedProviderRuntimeState struct {
	Version    int                                 `json:"version"`
	Aggregates map[string]providerRuntimeAggregate `json:"aggregates"`
	// Aliases map volatile CPA auth indexes to a stable, redacted aggregate
	// identity. CPA may regenerate auth-index values when a provider channel is
	// rewritten; the alias keeps historical usage attached to that credential
	// without storing the credential itself.
	Aliases map[string]string `json:"aliases,omitempty"`
}

// ProviderRuntimeTracker observes request lifecycle and usage callbacks without
// participating in routing or admission. Missing CPA identities are exposed as
// unsupported rather than being guessed into a configured channel.
type ProviderRuntimeTracker struct {
	mu            sync.RWMutex
	storeMu       sync.Mutex
	requests      map[string]providerRuntimeRequest
	aggregates    map[string]*providerRuntimeAggregate
	calculator    UsageCreditCalculator
	quotaPolicies *QuotaPolicyService
	now           func() time.Time
	nextPrune     time.Time
	store         string
	durableStore  string
	allowDurable  bool
	loaded        bool
	dirty         bool
	// storageBlocked prevents a corrupt primary/backup pair from being
	// overwritten by an empty snapshot. New observations remain available in
	// memory, while the non-sensitive storage error tells the operator that
	// recovery or manual repair is required.
	storageBlocked bool
	storageErr     string
	aliases        map[string]string
	persistTimer   *time.Timer
}

func NewProviderRuntimeTracker(calculator UsageCreditCalculator) *ProviderRuntimeTracker {
	return &ProviderRuntimeTracker{
		requests:   make(map[string]providerRuntimeRequest),
		aggregates: make(map[string]*providerRuntimeAggregate),
		aliases:    make(map[string]string),
		calculator: calculator,
		now:        time.Now,
	}
}

// Configure restores redacted provider runtime aggregates from the configured
// plugin data directory. In-flight request state is intentionally reset because
// requests cannot safely be resumed across a process restart.
func (t *ProviderRuntimeTracker) Configure(config Config) {
	if t == nil {
		return
	}
	config = normalizeConfig(config)
	path := providerRuntimeStorePath(config.DataDir)
	t.storeMu.Lock()
	defer t.storeMu.Unlock()
	t.allowDurable = config.implicitDataDir
	if !t.allowDurable {
		t.durableStore = ""
	}
	if t.allowDurable && t.durableStore != "" {
		path = t.durableStore
	}
	if t.loaded && t.store == path {
		return
	}
	if t.persistTimer != nil {
		t.persistTimer.Stop()
		t.persistTimer = nil
	}
	if t.loaded && t.dirty && t.store != "" {
		if errPersist := t.persistLocked(); errPersist != nil {
			return
		}
	}
	state, errLoad := loadProviderRuntimeState(path)
	aggregates := providerRuntimeAggregatePointers(state.Aggregates)
	storageErr := ""
	storageBlocked := false
	if errLoad != nil {
		// Runtime metrics must never prevent the plugin from loading, but retain a
		// diagnosable error and do not pretend that a corrupt file was an empty
		// successful store. A backup is attempted before giving up.
		if backupState, backupErr := loadProviderRuntimeState(providerRuntimeBackupPath(path)); backupErr == nil {
			state = backupState
			aggregates = providerRuntimeAggregatePointers(state.Aggregates)
			errLoad = nil
			storageErr = "provider runtime state was recovered from backup"
		} else if errors.Is(errLoad, os.ErrNotExist) {
			errLoad = nil
		} else {
			storageErr = "provider runtime state could not be loaded"
			storageBlocked = true
		}
	}
	pruned := pruneProviderRuntimeState(&state, t.now().UTC())
	aggregates = providerRuntimeAggregatePointers(state.Aggregates)
	if aggregates == nil {
		aggregates = make(map[string]*providerRuntimeAggregate)
	}
	t.mu.Lock()
	t.requests = make(map[string]providerRuntimeRequest)
	t.aggregates = aggregates
	t.aliases = state.Aliases
	if t.aliases == nil {
		t.aliases = make(map[string]string)
	}
	t.nextPrune = time.Time{}
	t.storageErr = storageErr
	t.storageBlocked = storageBlocked
	t.dirty = storageErr == "provider runtime state was recovered from backup" || pruned
	t.mu.Unlock()
	t.store = path
	t.loaded = true
	if storageErr == "provider runtime state was recovered from backup" {
		_ = t.persistLocked()
	}
}

// DiscoverAuthStorage selects the same durable, host-adjacent store used by
// account usage when the plugin is using its implicit data directory. This is
// important for CPA restarts: a relative plugin data directory may be rebuilt
// or mounted differently while the auth directory remains stable.
func (t *ProviderRuntimeTracker) DiscoverAuthStorage(entries []cpaapi.HostAuthFileEntry) {
	if t == nil || !t.allowDurable {
		return
	}
	authDir := discoverUsageAuthDir(entries)
	if authDir == "" {
		return
	}
	path := providerRuntimeStorePath(filepath.Join(authDir, usageDurableDirName))
	t.storeMu.Lock()
	defer t.storeMu.Unlock()
	if t.durableStore == path && t.store == path {
		return
	}
	if t.loaded && t.store == path {
		t.durableStore = path
		return
	}
	if t.persistTimer != nil {
		t.persistTimer.Stop()
		t.persistTimer = nil
	}
	if t.loaded && t.dirty && t.store != "" {
		if errPersist := t.persistLocked(); errPersist != nil {
			return
		}
	}
	currentAggregates := t.snapshotAggregates()
	currentAliases := t.snapshotAliases()
	state, errLoad := loadProviderRuntimeState(path)
	recovered := false
	if errLoad != nil {
		if backup, backupErr := loadProviderRuntimeState(providerRuntimeBackupPath(path)); backupErr == nil {
			state = backup
			errLoad = nil
			recovered = true
		} else if !errors.Is(errLoad, os.ErrNotExist) {
			t.mu.Lock()
			t.storageErr = "provider runtime state could not be loaded"
			t.mu.Unlock()
			return
		}
	}
	// The fallback store may have received usage before the first account list
	// discovered CPA's absolute auth directory. Merge it with the durable
	// store instead of blindly replacing either side. Counters are monotonic,
	// so merging by maximum preserves new observations without double-counting
	// the copy that was already loaded from the fallback store.
	mergedAggregates := mergeProviderRuntimeAggregates(
		currentAggregates,
		providerRuntimeAggregatePointers(state.Aggregates),
	)
	mergedAliases := mergeProviderRuntimeAliases(currentAliases, state.Aliases)
	mergedPointers := providerRuntimeAggregatePointers(mergedAggregates)
	pruned := pruneProviderRuntimeAggregates(mergedPointers, t.now().UTC())
	mergedAggregates = providerRuntimeAggregateValues(mergedPointers)
	t.mu.Lock()
	t.aggregates = providerRuntimeAggregatePointers(mergedAggregates)
	t.aliases = mergedAliases
	t.requests = make(map[string]providerRuntimeRequest)
	t.nextPrune = time.Time{}
	t.store = path
	t.durableStore = path
	t.loaded = true
	t.storageErr = ""
	t.storageBlocked = false
	t.dirty = len(currentAggregates) > 0 || recovered || pruned
	t.mu.Unlock()
	if t.dirty {
		_ = t.persistLocked()
	}
}

func (t *ProviderRuntimeTracker) StorageError() string {
	if t == nil {
		return "provider runtime state is unavailable"
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.storageErr
}

func (t *ProviderRuntimeTracker) markDirty() {
	if t == nil {
		return
	}
	t.storeMu.Lock()
	defer t.storeMu.Unlock()
	if !t.loaded || t.store == "" {
		return
	}
	t.mu.Lock()
	t.dirty = true
	t.mu.Unlock()
	if t.persistTimer == nil {
		t.persistTimer = time.AfterFunc(providerRuntimePersistDelay, func() {
			t.persistNow()
		})
	}
}

func (t *ProviderRuntimeTracker) persistNow() {
	if t == nil {
		return
	}
	t.storeMu.Lock()
	defer t.storeMu.Unlock()
	t.persistTimer = nil
	_ = t.persistLocked()
}

func (t *ProviderRuntimeTracker) persistLocked() error {
	if t == nil || !t.loaded || t.store == "" {
		return nil
	}
	t.mu.RLock()
	blocked := t.storageBlocked
	t.mu.RUnlock()
	if blocked {
		return errors.New("provider runtime storage is blocked after load failure")
	}
	aggregates := t.snapshotAggregates()
	aliases := t.snapshotAliases()
	if errSave := saveProviderRuntimeState(t.store, aggregates, aliases); errSave != nil {
		t.mu.Lock()
		if !t.storageBlocked {
			t.storageErr = "provider runtime state could not be persisted"
		}
		t.mu.Unlock()
		return errSave
	}
	t.mu.Lock()
	t.dirty = false
	t.storageErr = ""
	t.mu.Unlock()
	return nil
}

func (t *ProviderRuntimeTracker) snapshotAggregates() map[string]*providerRuntimeAggregate {
	t.mu.RLock()
	defer t.mu.RUnlock()
	aggregates := make(map[string]*providerRuntimeAggregate, len(t.aggregates))
	for key, aggregate := range t.aggregates {
		if aggregate == nil {
			continue
		}
		clone := *aggregate
		clone.Active = 0
		clone.Models = make(map[string]*providerRuntimeModel, len(aggregate.Models))
		for model, value := range aggregate.Models {
			if value == nil {
				continue
			}
			modelClone := *value
			clone.Models[model] = &modelClone
		}
		clone.Events = append([]providerRuntimeEvent(nil), aggregate.Events...)
		aggregates[key] = &clone
	}
	return aggregates
}

func (t *ProviderRuntimeTracker) snapshotAliases() map[string]string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	aliases := make(map[string]string, len(t.aliases))
	for key, value := range t.aliases {
		aliases[key] = value
	}
	return aliases
}

func providerRuntimeStorePath(dataDir string) string {
	return filepath.Join(dataDir, providerRuntimeStoreFileName)
}

func providerRuntimeBackupPath(path string) string { return path + ".bak" }

func loadProviderRuntimeState(path string) (persistedProviderRuntimeState, error) {
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		return persistedProviderRuntimeState{}, errRead
	}
	var persisted persistedProviderRuntimeState
	if errDecode := json.Unmarshal(raw, &persisted); errDecode != nil {
		return persistedProviderRuntimeState{}, fmt.Errorf("decode provider runtime state: %w", errDecode)
	}
	if persisted.Version != 1 && persisted.Version != providerRuntimeStoreVersion {
		return persistedProviderRuntimeState{}, fmt.Errorf("unsupported provider runtime store version %d", persisted.Version)
	}
	aggregates := make(map[string]providerRuntimeAggregate, len(persisted.Aggregates))
	for key, value := range persisted.Aggregates {
		if strings.TrimSpace(key) == "" || strings.TrimSpace(value.Provider) == "" || strings.TrimSpace(value.Identity) == "" {
			continue
		}
		value.Provider = normalizeRuntimeProvider(value.Provider)
		value.Models = normalizeProviderRuntimeModels(value.Models)
		if len(value.Events) > providerRuntimeMaxEvents {
			value.Events = value.Events[len(value.Events)-providerRuntimeMaxEvents:]
		}
		value.Active = 0
		aggregates[key] = value
	}
	persisted.Aggregates = aggregates
	persisted.Aliases = normalizeProviderRuntimeAliases(persisted.Aliases)
	return persisted, nil
}

func providerRuntimeAggregatePointers(values map[string]providerRuntimeAggregate) map[string]*providerRuntimeAggregate {
	result := make(map[string]*providerRuntimeAggregate, len(values))
	for key, value := range values {
		clone := value
		result[key] = &clone
	}
	return result
}

func providerRuntimeAggregateValues(values map[string]*providerRuntimeAggregate) map[string]providerRuntimeAggregate {
	result := make(map[string]providerRuntimeAggregate, len(values))
	for key, value := range values {
		if value != nil {
			result[key] = *value
		}
	}
	return result
}

// pruneProviderRuntimeState removes rolling-window data that can no longer
// affect a dashboard or quota calculation. Doing this during load keeps a
// long-lived provider state file bounded even when no new request arrives
// after a restart.
func pruneProviderRuntimeState(state *persistedProviderRuntimeState, now time.Time) bool {
	if state == nil {
		return false
	}
	pointers := providerRuntimeAggregatePointers(state.Aggregates)
	changed := pruneProviderRuntimeAggregates(pointers, now)
	state.Aggregates = providerRuntimeAggregateValues(pointers)
	return changed
}

func pruneProviderRuntimeAggregates(aggregates map[string]*providerRuntimeAggregate, now time.Time) bool {
	changed := false
	for _, aggregate := range aggregates {
		if aggregate == nil {
			continue
		}
		requestEvents := pruneProviderRequestEvents(aggregate.RequestEvents, now)
		if len(requestEvents) != len(aggregate.RequestEvents) {
			aggregate.RequestEvents = requestEvents
			changed = true
		}
		costEvents := pruneProviderRuntimeCostEvents(aggregate.Events, now)
		if len(costEvents) != len(aggregate.Events) {
			aggregate.Events = costEvents
			changed = true
		}
	}
	return changed
}

func pruneProviderRuntimeCostEvents(events []providerRuntimeEvent, now time.Time) []providerRuntimeEvent {
	if len(events) == 0 {
		return nil
	}
	cutoff := now.Add(-7 * 24 * time.Hour)
	kept := events[:0]
	for _, event := range events {
		if event.At.After(cutoff) && !event.At.After(now) {
			kept = append(kept, event)
		}
	}
	return kept
}

func normalizeProviderRuntimeModels(models map[string]*providerRuntimeModel) map[string]*providerRuntimeModel {
	if len(models) == 0 {
		return make(map[string]*providerRuntimeModel)
	}
	capacity := len(models)
	if capacity > providerRuntimeMaxModels {
		capacity = providerRuntimeMaxModels
	}
	result := make(map[string]*providerRuntimeModel, capacity)
	keys := make([]string, 0, len(models))
	for key := range models {
		if strings.TrimSpace(key) != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	if len(keys) > providerRuntimeMaxModels {
		keys = keys[:providerRuntimeMaxModels]
	}
	for _, key := range keys {
		value := models[key]
		if value == nil {
			continue
		}
		clone := *value
		clone.Model = strings.TrimSpace(clone.Model)
		if clone.Model != "" {
			result[key] = &clone
		}
	}
	return result
}

// mergeProviderRuntimeAggregates combines two snapshots of cumulative
// provider metrics. The fallback store and the auth-adjacent store can both
// contain the same history, so counters use the maximum rather than addition.
// Window events are de-duplicated by timestamp and amount before being capped.
func mergeProviderRuntimeAggregates(left, right map[string]*providerRuntimeAggregate) map[string]providerRuntimeAggregate {
	merged := make(map[string]providerRuntimeAggregate, len(left)+len(right))
	for key, aggregate := range left {
		if aggregate == nil {
			continue
		}
		merged[key] = cloneProviderRuntimeAggregate(*aggregate)
	}
	for key, aggregate := range right {
		if aggregate == nil {
			continue
		}
		if current, exists := merged[key]; exists {
			merged[key] = mergeProviderRuntimeAggregate(current, *aggregate)
		} else {
			merged[key] = cloneProviderRuntimeAggregate(*aggregate)
		}
	}
	return merged
}

func mergeProviderRuntimeAggregate(left, right providerRuntimeAggregate) providerRuntimeAggregate {
	merged := cloneProviderRuntimeAggregate(left)
	merged.Active = 0
	if merged.Provider == "" {
		merged.Provider = normalizeRuntimeProvider(right.Provider)
	}
	if merged.Identity == "" || strings.HasPrefix(right.Identity, "credential:") && !strings.HasPrefix(merged.Identity, "credential:") {
		merged.Identity = right.Identity
	}
	if right.UpdatedAt.After(merged.UpdatedAt) || merged.AuthIndex == "" {
		merged.AuthIndex = right.AuthIndex
	}
	merged.InputTokens = maxInt64(merged.InputTokens, right.InputTokens)
	merged.OutputTokens = maxInt64(merged.OutputTokens, right.OutputTokens)
	merged.ReasoningTokens = maxInt64(merged.ReasoningTokens, right.ReasoningTokens)
	merged.CachedTokens = maxInt64(merged.CachedTokens, right.CachedTokens)
	merged.TotalTokens = maxInt64(merged.TotalTokens, right.TotalTokens)
	merged.AmountNanos = maxInt64(merged.AmountNanos, right.AmountNanos)
	merged.RatedRequests = maxInt64(merged.RatedRequests, right.RatedRequests)
	merged.UnratedRequests = maxInt64(merged.UnratedRequests, right.UnratedRequests)
	merged.Models = mergeProviderRuntimeModels(merged.Models, right.Models)
	merged.Events = mergeProviderRuntimeEvents(merged.Events, right.Events)
	merged.RequestEvents = mergeProviderRuntimeRequestEvents(merged.RequestEvents, right.RequestEvents)
	if right.UpdatedAt.After(merged.UpdatedAt) {
		merged.UpdatedAt = right.UpdatedAt
	}
	return merged
}

func cloneProviderRuntimeAggregate(value providerRuntimeAggregate) providerRuntimeAggregate {
	clone := value
	clone.Active = 0
	clone.Models = normalizeProviderRuntimeModels(value.Models)
	clone.Events = append([]providerRuntimeEvent(nil), value.Events...)
	clone.RequestEvents = append([]time.Time(nil), value.RequestEvents...)
	return clone
}

func mergeProviderRuntimeModels(left, right map[string]*providerRuntimeModel) map[string]*providerRuntimeModel {
	merged := normalizeProviderRuntimeModels(left)
	for model, value := range right {
		if value == nil || strings.TrimSpace(model) == "" {
			continue
		}
		if current := merged[model]; current != nil {
			current.InputTokens = maxInt64(current.InputTokens, value.InputTokens)
			current.OutputTokens = maxInt64(current.OutputTokens, value.OutputTokens)
			current.ReasoningTokens = maxInt64(current.ReasoningTokens, value.ReasoningTokens)
			current.CachedTokens = maxInt64(current.CachedTokens, value.CachedTokens)
			current.TotalTokens = maxInt64(current.TotalTokens, value.TotalTokens)
			if value.AmountUSD > current.AmountUSD {
				current.AmountUSD = value.AmountUSD
			}
			current.Rated = current.Rated || value.Rated
			current.RatedRequests = maxInt64(current.RatedRequests, value.RatedRequests)
			current.UnratedRequests = maxInt64(current.UnratedRequests, value.UnratedRequests)
			continue
		}
		if len(merged) < providerRuntimeMaxModels {
			copy := *value
			merged[model] = &copy
		}
	}
	return merged
}

func mergeProviderRuntimeEvents(left, right []providerRuntimeEvent) []providerRuntimeEvent {
	seen := make(map[string]struct{}, len(left)+len(right))
	merged := make([]providerRuntimeEvent, 0, len(left)+len(right))
	for _, event := range append(append([]providerRuntimeEvent(nil), left...), right...) {
		key := fmt.Sprintf("%d:%d", event.At.UnixNano(), event.AmountNanos)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		merged = append(merged, event)
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i].At.Before(merged[j].At) })
	if len(merged) > providerRuntimeMaxEvents {
		merged = merged[len(merged)-providerRuntimeMaxEvents:]
	}
	return merged
}

func mergeProviderRuntimeRequestEvents(left, right []time.Time) []time.Time {
	seen := make(map[int64]struct{}, len(left)+len(right))
	merged := make([]time.Time, 0, len(left)+len(right))
	for _, event := range append(append([]time.Time(nil), left...), right...) {
		key := event.UnixNano()
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		merged = append(merged, event)
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i].Before(merged[j]) })
	if len(merged) > providerRuntimeMaxEvents {
		merged = merged[len(merged)-providerRuntimeMaxEvents:]
	}
	return merged
}

func mergeProviderRuntimeAliases(left, right map[string]string) map[string]string {
	merged := normalizeProviderRuntimeAliases(left)
	for key, value := range normalizeProviderRuntimeAliases(right) {
		if _, exists := merged[key]; !exists {
			merged[key] = value
		}
	}
	return normalizeProviderRuntimeAliases(merged)
}

func normalizeProviderRuntimeAliases(values map[string]string) map[string]string {
	if len(values) == 0 {
		return make(map[string]string)
	}
	keys := make([]string, 0, len(values))
	for key, value := range values {
		if strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" || len(key) > 512 || len(value) > 512 {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) > providerRuntimeMaxIdentities*2 {
		keys = keys[:providerRuntimeMaxIdentities*2]
	}
	result := make(map[string]string, len(keys))
	for _, key := range keys {
		result[key] = strings.TrimSpace(values[key])
	}
	return result
}

func saveProviderRuntimeState(path string, aggregates map[string]*providerRuntimeAggregate, aliases map[string]string) error {
	values := make(map[string]providerRuntimeAggregate, len(aggregates))
	for key, aggregate := range aggregates {
		if aggregate == nil {
			continue
		}
		clone := *aggregate
		clone.Active = 0
		clone.Models = make(map[string]*providerRuntimeModel, len(aggregate.Models))
		for model, value := range aggregate.Models {
			if value == nil {
				continue
			}
			modelClone := *value
			clone.Models[model] = &modelClone
		}
		clone.Events = append([]providerRuntimeEvent(nil), aggregate.Events...)
		values[key] = clone
	}
	state := persistedProviderRuntimeState{
		Version:    providerRuntimeStoreVersion,
		Aggregates: values,
		Aliases:    normalizeProviderRuntimeAliases(aliases),
	}
	if errSave := savePrivateJSON(path, state); errSave != nil {
		return errSave
	}
	return savePrivateJSON(providerRuntimeBackupPath(path), state)
}

// SetQuotaPolicies attaches the provider policy store used for request-window
// admission and runtime summaries.
func (t *ProviderRuntimeTracker) SetQuotaPolicies(service *QuotaPolicyService) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.quotaPolicies = service
	t.mu.Unlock()
}

// RequestInterceptionActive keeps the lifecycle observer attached even when no
// mutating request experiment is enabled. It never changes request bodies.
func (t *ProviderRuntimeTracker) RequestInterceptionActive() bool              { return t != nil }
func (t *ProviderRuntimeTracker) RequestInterceptionAcceptsFormat(string) bool { return t != nil }
func (t *ProviderRuntimeTracker) InterceptRequest(request cpaapi.RequestInterceptRequest) (cpaapi.RequestInterceptResponse, bool) {
	if t == nil || strings.TrimSpace(request.RequestID) == "" {
		return cpaapi.RequestInterceptResponse{}, false
	}
	identity, authIndex := runtimeIdentityFromMetadata(request.Metadata)
	if identity == "" {
		return cpaapi.RequestInterceptResponse{}, false
	}
	provider := runtimeProviderFromMetadata(request.Metadata)
	if provider == "" {
		provider = normalizeRuntimeProvider(request.ToFormat)
	}
	now := t.now().UTC()
	t.mu.Lock()
	t.pruneExpiredLocked(now)
	aggregateKey, aggregateIdentity := t.resolveAggregateKeyLocked(provider, identity, authIndex, "")
	if current, exists := t.requests[request.RequestID]; exists {
		if current.AggregateKey == aggregateKey {
			t.mu.Unlock()
			return cpaapi.RequestInterceptResponse{}, false
		}
		delete(t.requests, request.RequestID)
		if previous := t.aggregates[current.AggregateKey]; previous != nil {
			if previous.Active > 0 {
				previous.Active--
			}
			previous.UpdatedAt = now
		}
	}
	aggregate := t.ensureAggregateLocked(aggregateKey, aggregateIdentity, provider, authIndex)
	if aggregate == nil {
		t.mu.Unlock()
		return cpaapi.RequestInterceptResponse{}, false
	}
	aggregate.RequestEvents = pruneProviderRequestEvents(aggregate.RequestEvents, now)
	// This tracker is observational. Account admission is the only concurrency
	// gate that can wait for a slot in CPA's request lifecycle. Returning a
	// synthetic 429 here makes CPA/sub2api classify an internal dashboard policy
	// as an upstream provider failure and stop scheduling the account. Provider
	// policies remain available in Snapshot for display and external scheduling.
	t.requests[request.RequestID] = providerRuntimeRequest{AggregateKey: aggregateKey, AdmittedAt: now}
	aggregate.Active++
	aggregate.RequestEvents = append(aggregate.RequestEvents, now)
	aggregate.UpdatedAt = now
	t.mu.Unlock()
	t.markDirty()
	return cpaapi.RequestInterceptResponse{}, false
}

func (t *ProviderRuntimeTracker) ObserveRequest(request cpaapi.RequestInterceptRequest) {
	_, _ = t.InterceptRequest(request)
}

func (t *ProviderRuntimeTracker) Complete(completion cpaapi.RequestCompletion) {
	if t == nil || strings.TrimSpace(completion.RequestID) == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pruneExpiredLocked(t.now().UTC())
	admission, exists := t.requests[completion.RequestID]
	if !exists {
		return
	}
	delete(t.requests, completion.RequestID)
	if aggregate := t.aggregates[admission.AggregateKey]; aggregate != nil {
		if aggregate.Active > 0 {
			aggregate.Active--
		}
		aggregate.UpdatedAt = t.now().UTC()
	}
}

func (t *ProviderRuntimeTracker) ObserveUsage(record cpaapi.UsageRecord) {
	if t == nil {
		return
	}
	identity, authIndex := runtimeIdentityFromUsage(record)
	if identity == "" {
		return
	}
	now := t.now().UTC()
	charge := CreditCharge{}
	if t.calculator != nil {
		charge = t.calculator.Calculate(record)
	}
	t.mu.Lock()
	provider := normalizeRuntimeProvider(record.Provider)
	credentialIdentity := runtimeCredentialIdentity(record)
	aggregateKey, aggregateIdentity := t.resolveAggregateKeyLocked(provider, identity, authIndex, credentialIdentity)
	aggregate := t.ensureAggregateLocked(aggregateKey, aggregateIdentity, provider, authIndex)
	if aggregate == nil {
		t.mu.Unlock()
		return
	}
	input := nonNegative(record.Detail.InputTokens)
	output := nonNegative(record.Detail.OutputTokens)
	reasoning := nonNegative(record.Detail.ReasoningTokens)
	cached := nonNegative(record.Detail.CachedTokens)
	total := nonNegative(record.Detail.TotalTokens)
	if total == 0 {
		total = saturatingAdd(saturatingAdd(input, output), reasoning)
	}
	aggregate.InputTokens = saturatingAdd(aggregate.InputTokens, input)
	aggregate.OutputTokens = saturatingAdd(aggregate.OutputTokens, output)
	aggregate.ReasoningTokens = saturatingAdd(aggregate.ReasoningTokens, reasoning)
	aggregate.CachedTokens = saturatingAdd(aggregate.CachedTokens, cached)
	aggregate.TotalTokens = saturatingAdd(aggregate.TotalTokens, total)
	if !record.Failed && charge.Rated && charge.AmountNanos > 0 {
		aggregate.Events = append(aggregate.Events, providerRuntimeEvent{At: now, AmountNanos: charge.AmountNanos})
		if len(aggregate.Events) > providerRuntimeMaxEvents {
			aggregate.Events = aggregate.Events[len(aggregate.Events)-providerRuntimeMaxEvents:]
		}
	}
	model := strings.TrimSpace(record.Model)
	if model != "" {
		if aggregate.Models == nil {
			aggregate.Models = make(map[string]*providerRuntimeModel)
		}
		if entry := aggregate.Models[model]; entry != nil || len(aggregate.Models) < providerRuntimeMaxModels {
			if entry == nil {
				entry = &providerRuntimeModel{ProviderModelUsage: ProviderModelUsage{Model: model}}
				aggregate.Models[model] = entry
			}
			entry.InputTokens = saturatingAdd(entry.InputTokens, input)
			entry.OutputTokens = saturatingAdd(entry.OutputTokens, output)
			entry.ReasoningTokens = saturatingAdd(entry.ReasoningTokens, reasoning)
			entry.CachedTokens = saturatingAdd(entry.CachedTokens, cached)
			entry.TotalTokens = saturatingAdd(entry.TotalTokens, total)
			if !record.Failed {
				if charge.Rated {
					entry.Rated = true
					entry.RatedRequests++
				} else if charge.Enabled {
					entry.UnratedRequests++
				}
			}
			if charge.Rated {
				entry.AmountUSD += float64(charge.AmountNanos) / creditNanosPerUSD
			}
		}
	}
	if !record.Failed {
		if charge.Rated {
			aggregate.RatedRequests++
			aggregate.AmountNanos = saturatingAdd(aggregate.AmountNanos, charge.AmountNanos)
		} else if charge.Enabled {
			aggregate.UnratedRequests++
		}
	}
	aggregate.UpdatedAt = now
	if authIndex != "" {
		aggregate.AuthIndex = authIndex
	}
	t.mu.Unlock()
	t.markDirty()
}

func (t *ProviderRuntimeTracker) Snapshot() []ProviderRuntimeSnapshot {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	t.pruneExpiredLocked(t.now().UTC())
	defer t.mu.Unlock()
	out := make([]ProviderRuntimeSnapshot, 0, len(t.aggregates))
	now := t.now().UTC()
	for _, aggregate := range t.aggregates {
		fiveHourAmountNanos, sevenDayAmountNanos := runtimeWindowAmounts(aggregate.Events, now)
		models := make([]ProviderModelUsage, 0, len(aggregate.Models))
		for _, model := range aggregate.Models {
			models = append(models, model.ProviderModelUsage)
		}
		sort.Slice(models, func(i, j int) bool { return models[i].Model < models[j].Model })
		supported := aggregate.Identity != ""
		reason := ""
		if !supported {
			reason = "provider_runtime_identity_unavailable"
		}
		policy, configurable := ProviderQuotaPolicy{}, false
		if t.quotaPolicies != nil {
			policy, configurable = t.quotaPolicies.ResolveProviderPolicy(aggregate.Provider, aggregate.AuthIndex, aggregate.Identity)
		}
		aggregate.RequestEvents = pruneProviderRequestEvents(aggregate.RequestEvents, now)
		windowSeconds := providerConcurrencyWindowSeconds(policy, configurable)
		usedRequests := countProviderRequestEvents(aggregate.RequestEvents, now.Add(-time.Duration(windowSeconds)*time.Second), now)
		limit, requestLimit := 0, 0
		if configurable && policy.Concurrency != nil {
			limit = *policy.Concurrency
		}
		if configurable && policy.Concurrency15s != nil {
			requestLimit = *policy.Concurrency15s
		}
		out = append(out, ProviderRuntimeSnapshot{
			Provider:                aggregate.Provider,
			AuthIndex:               aggregate.AuthIndex,
			Identity:                aggregate.Identity,
			Supported:               supported,
			Reason:                  reason,
			ConcurrencyConfigurable: configurable,
			Active:                  aggregate.Active,
			Waiting:                 0, // Provider queueing belongs to the CPA/sub2api scheduler.
			Limit:                   limit,
			RequestLimit:            requestLimit,
			RequestWindowSeconds:    windowSeconds,
			UsedRequests:            usedRequests,
			Limit15s:                requestLimit,
			Used60s:                 len(aggregate.RequestEvents),
			Used15s:                 countProviderRequestEvents(aggregate.RequestEvents, now.Add(-15*time.Second), now),
			InputTokens:             aggregate.InputTokens,
			OutputTokens:            aggregate.OutputTokens,
			ReasoningTokens:         aggregate.ReasoningTokens,
			CachedTokens:            aggregate.CachedTokens,
			TotalTokens:             aggregate.TotalTokens,
			AmountUSD:               float64(aggregate.AmountNanos) / creditNanosPerUSD,
			RatedRequests:           aggregate.RatedRequests,
			UnratedRequests:         aggregate.UnratedRequests,
			Quota:                   providerRuntimeQuota(policy, configurable, fiveHourAmountNanos, sevenDayAmountNanos),
			Models:                  models,
			UpdatedAt:               aggregate.UpdatedAt,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Provider == out[j].Provider {
			return out[i].AuthIndex < out[j].AuthIndex
		}
		return out[i].Provider < out[j].Provider
	})
	return out
}

func runtimeWindowAmounts(events []providerRuntimeEvent, now time.Time) (int64, int64) {
	if len(events) == 0 {
		return 0, 0
	}
	fiveHourCutoff := now.Add(-5 * time.Hour)
	sevenDayCutoff := now.Add(-7 * 24 * time.Hour)
	var fiveHour, sevenDay int64
	for _, event := range events {
		if event.At.After(now) {
			continue
		}
		if event.At.After(sevenDayCutoff) {
			sevenDay = saturatingAdd(sevenDay, event.AmountNanos)
		}
		if event.At.After(fiveHourCutoff) {
			fiveHour = saturatingAdd(fiveHour, event.AmountNanos)
		}
	}
	return fiveHour, sevenDay
}

func providerRuntimeQuota(policy ProviderQuotaPolicy, configured bool, fiveHourAmountNanos, sevenDayAmountNanos int64) ProviderRuntimeQuota {
	fiveHourAmount := float64(fiveHourAmountNanos) / creditNanosPerUSD
	sevenDayAmount := float64(sevenDayAmountNanos) / creditNanosPerUSD
	quota := ProviderRuntimeQuota{FiveHourAmountUSD: fiveHourAmount, SevenDayAmountUSD: sevenDayAmount}
	if !configured {
		return quota
	}
	if policy.FiveHour.BudgetAmountUSD != nil && *policy.FiveHour.BudgetAmountUSD > 0 {
		quota.FiveHourPercent = fiveHourAmount / *policy.FiveHour.BudgetAmountUSD * 100
	}
	if policy.SevenDay.BudgetAmountUSD != nil && *policy.SevenDay.BudgetAmountUSD > 0 {
		quota.SevenDayPercent = sevenDayAmount / *policy.SevenDay.BudgetAmountUSD * 100
	}
	return quota
}

func (t *ProviderRuntimeTracker) Shutdown() {
	if t == nil {
		return
	}
	t.storeMu.Lock()
	if t.persistTimer != nil {
		t.persistTimer.Stop()
		t.persistTimer = nil
	}
	_ = t.persistLocked()
	// Mark the tracker unloaded after the final flush. App instances are
	// normally discarded on shutdown, but keeping this explicit also makes a
	// later Configure call on the same tracker reload the persisted aggregates
	// instead of returning early for the same data directory.
	t.loaded = false
	t.store = ""
	t.dirty = false
	t.storeMu.Unlock()
	t.mu.Lock()
	t.requests = make(map[string]providerRuntimeRequest)
	t.aggregates = make(map[string]*providerRuntimeAggregate)
	t.mu.Unlock()
}

func (t *ProviderRuntimeTracker) pruneExpiredLocked(now time.Time) {
	if t == nil || (!t.nextPrune.IsZero() && now.Before(t.nextPrune)) {
		return
	}
	t.nextPrune = now.Add(providerRuntimePruneInterval)
	cutoff := now.Add(-providerRuntimeRequestLease)
	for requestID, request := range t.requests {
		if request.AdmittedAt.IsZero() || request.AdmittedAt.After(cutoff) {
			continue
		}
		delete(t.requests, requestID)
		if aggregate := t.aggregates[request.AggregateKey]; aggregate != nil {
			if aggregate.Active > 0 {
				aggregate.Active--
			}
			aggregate.UpdatedAt = now
		}
	}
	for _, aggregate := range t.aggregates {
		if aggregate != nil {
			aggregate.RequestEvents = pruneProviderRequestEvents(aggregate.RequestEvents, now)
		}
	}
}

func pruneProviderRequestEvents(events []time.Time, now time.Time) []time.Time {
	if len(events) == 0 {
		return nil
	}
	cutoff := now.Add(-providerRuntimeMaxWindow)
	kept := events[:0]
	for _, event := range events {
		if event.After(cutoff) && !event.After(now) {
			kept = append(kept, event)
		}
	}
	return kept
}

func providerConcurrencyWindowSeconds(policy ProviderQuotaPolicy, configured bool) int {
	if configured && policy.WindowSeconds != nil && *policy.WindowSeconds >= 1 && *policy.WindowSeconds <= 3600 {
		return *policy.WindowSeconds
	}
	return 15
}

func countProviderRequestEvents(events []time.Time, cutoff, now time.Time) int {
	count := 0
	for _, event := range events {
		if event.After(cutoff) && !event.After(now) {
			count++
		}
	}
	return count
}

func normalizeRuntimeProvider(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func runtimeAggregateKey(provider, identity string) string {
	return normalizeRuntimeProvider(provider) + "\x00" + identity
}

func runtimeAliasKey(provider, authIndex string) string {
	authIndex = strings.TrimSpace(authIndex)
	if authIndex == "" {
		return ""
	}
	return runtimeAggregateKey(provider, "auth-index:"+authIndex)
}

// resolveAggregateKeyLocked prefers a credential digest when CPA supplies the
// key with a usage callback. The digest is one-way and redacted; it lets a
// provider keep the same aggregate when CPA regenerates auth-index metadata
// after a channel update. Callers without the key reuse the persisted alias.
func (t *ProviderRuntimeTracker) resolveAggregateKeyLocked(provider, identity, authIndex, credentialIdentity string) (string, string) {
	provider = normalizeRuntimeProvider(provider)
	identity = strings.TrimSpace(identity)
	credentialIdentity = strings.TrimSpace(credentialIdentity)
	if t.aliases == nil {
		t.aliases = make(map[string]string)
	}
	aliasKey := runtimeAliasKey(provider, authIndex)
	if credentialIdentity != "" {
		stableKey := runtimeAggregateKey(provider, credentialIdentity)
		legacyKey := runtimeAggregateKey(provider, identity)
		if stableKey != legacyKey {
			if legacy := t.aggregates[legacyKey]; legacy != nil {
				if stable := t.aggregates[stableKey]; stable == nil {
					delete(t.aggregates, legacyKey)
					legacy.Identity = credentialIdentity
					t.aggregates[stableKey] = legacy
				} else {
					merged := mergeProviderRuntimeAggregate(*stable, *legacy)
					merged.Identity = credentialIdentity
					merged.Active += legacy.Active
					*stable = merged
					delete(t.aggregates, legacyKey)
				}
				for requestID, request := range t.requests {
					if request.AggregateKey == legacyKey {
						request.AggregateKey = stableKey
						t.requests[requestID] = request
					}
				}
			}
		}
		if aliasKey != "" && len(t.aliases) < providerRuntimeMaxIdentities*2 {
			t.aliases[aliasKey] = stableKey
		}
		return stableKey, credentialIdentity
	}
	if aliasKey != "" {
		if stableKey := strings.TrimSpace(t.aliases[aliasKey]); stableKey != "" {
			if aggregate := t.aggregates[stableKey]; aggregate != nil {
				return stableKey, aggregate.Identity
			}
			delete(t.aliases, aliasKey)
		}
	}
	return runtimeAggregateKey(provider, identity), identity
}

func (t *ProviderRuntimeTracker) evictIdleAggregateLocked() bool {
	var oldestKey string
	var oldest time.Time
	for key, aggregate := range t.aggregates {
		if aggregate == nil || aggregate.Active > 0 {
			continue
		}
		if oldestKey == "" || aggregate.UpdatedAt.Before(oldest) {
			oldestKey, oldest = key, aggregate.UpdatedAt
		}
	}
	if oldestKey == "" {
		return false
	}
	delete(t.aggregates, oldestKey)
	return true
}

func (t *ProviderRuntimeTracker) ensureAggregateLocked(key, identity, provider, authIndex string) *providerRuntimeAggregate {
	provider = normalizeRuntimeProvider(provider)
	aggregate := t.aggregates[key]
	if aggregate == nil {
		if len(t.aggregates) >= providerRuntimeMaxIdentities && !t.evictIdleAggregateLocked() {
			return nil
		}
		aggregate = &providerRuntimeAggregate{Provider: provider, AuthIndex: authIndex, Identity: identity, Models: make(map[string]*providerRuntimeModel)}
		t.aggregates[key] = aggregate
	}
	if aggregate.Provider == "" {
		aggregate.Provider = provider
	}
	if aggregate.AuthIndex == "" {
		aggregate.AuthIndex = authIndex
	}
	return aggregate
}

func runtimeProviderFromMetadata(metadata map[string]any) string {
	for _, key := range []string{"provider", "selected_provider", "provider_name", "auth_provider"} {
		if value, ok := metadata[key].(string); ok && strings.TrimSpace(value) != "" {
			return normalizeRuntimeProvider(value)
		}
	}
	return ""
}

func runtimeIdentityFromMetadata(metadata map[string]any) (string, string) {
	for _, key := range []string{"selected_auth_index", "selected_auth_id", "auth_index", "auth_id"} {
		if value, ok := metadata[key].(string); ok && strings.TrimSpace(value) != "" {
			value = strings.TrimSpace(value)
			if strings.Contains(key, "index") {
				return "auth-index:" + value, value
			}
			return "auth-id:" + value, ""
		}
	}
	return "", ""
}

func runtimeIdentityFromUsage(record cpaapi.UsageRecord) (string, string) {
	if authIndex := strings.TrimSpace(record.AuthIndex); authIndex != "" {
		return "auth-index:" + authIndex, authIndex
	}
	if authID := strings.TrimSpace(record.AuthID); authID != "" {
		return "auth-id:" + authID, ""
	}
	identity := runtimeCredentialIdentity(record)
	if identity == "" {
		return "", ""
	}
	return identity, ""
}

func runtimeCredentialIdentity(record cpaapi.UsageRecord) string {
	provider, key := normalizeRuntimeProvider(record.Provider), strings.TrimSpace(record.APIKey)
	if provider == "" || key == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(provider + "\x00" + key))
	return "credential:" + hex.EncodeToString(digest[:])
}

func (a *App) handleAIProviderRuntime() cpaapi.ManagementResponse {
	if a == nil || a.providerRuntime == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "provider runtime metrics are unavailable"})
	}
	return jsonResponse(http.StatusOK, map[string]any{
		"snapshots":     a.providerRuntime.Snapshot(),
		"updated_at":    time.Now().UTC(),
		"storage_error": a.providerRuntime.StorageError(),
	})
}
