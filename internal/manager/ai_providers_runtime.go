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
	// The provider channel index is host-derived and rebuilt from the next channel
	// list, so it is bounded well below the aggregate cap: it only has to hold the
	// auth indexes CPA currently reports for the operator's channels.
	providerRuntimeMaxProviderIndexEntries = 4096
	// One repair pass inspects at most this many stranded aggregates and folds at
	// most this many of them, so the pass is cheap and always terminates even on a
	// full aggregate map.
	providerRuntimeMaxOrphanCandidates = 1024
	providerRuntimeMaxOrphanAdoptions  = 256
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
	CredentialBacked        bool   `json:"credential_backed"`
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
	Provider         string
	AuthIndex        string
	Identity         string
	CredentialBacked bool
	Active           int
	InputTokens      int64
	OutputTokens     int64
	ReasoningTokens  int64
	CachedTokens     int64
	TotalTokens      int64
	AmountNanos      int64
	RatedRequests    int64
	UnratedRequests  int64
	Models           map[string]*providerRuntimeModel
	Events           []providerRuntimeEvent
	RequestEvents    []time.Time `json:"request_events,omitempty"`
	UpdatedAt        time.Time
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
	// accountAuthIndexes is populated from the host auth listing. CPA exposes
	// account and API-key provider usage through the same callback, and older
	// hosts can omit AuthType. Keeping the known native account indexes here
	// lets us reject ambiguous provider aggregates instead of copying account
	// usage into the provider dashboard.
	accountAuthIndexes map[string]struct{}
	// providerChannelIndex maps the volatile auth index CPA assigned to a
	// provider channel row to that row's credential. CPA's usage callback for an
	// openai-compatibility or codex-api-key channel names the auth index but
	// carries no API key, so without this index such a record cannot be mapped to
	// a channel credential. Every entry is derived from a live channel list; the
	// raw key stays in memory, is hashed into a redacted identity on the way in,
	// and is never logged or persisted.
	providerChannelIndex map[string]providerChannelIndexEntry
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
		requests:             make(map[string]providerRuntimeRequest),
		aggregates:           make(map[string]*providerRuntimeAggregate),
		accountAuthIndexes:   make(map[string]struct{}),
		providerChannelIndex: make(map[string]providerChannelIndexEntry),
		calculator:           calculator,
		now:                  time.Now,
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
	t.mu.Lock()
	pendingDirty := t.dirty
	t.mu.Unlock()
	if t.loaded && pendingDirty && t.store != "" {
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
	if t == nil {
		return
	}
	accountIndexes := make(map[string]struct{}, len(entries)*2)
	for _, entry := range entries {
		if authIndex := strings.TrimSpace(entry.AuthIndex); authIndex != "" {
			accountIndexes[authIndex] = struct{}{}
		}
		if authID := strings.TrimSpace(entry.ID); authID != "" {
			accountIndexes[authID] = struct{}{}
		}
	}
	// Do this before the durable-store switch below. The first account list is
	// also the point at which we can safely remove historical auth-index-only
	// aggregates written by versions that observed OAuth usage as provider
	// usage. A later explicit API-key callback will create a fresh, credential-
	// backed aggregate.
	t.mu.Lock()
	t.accountAuthIndexes = accountIndexes
	// An index a native account owns is never a provider channel index, even when
	// a channel list read the same index earlier.
	t.dropAccountClaimedProviderIndexesLocked(accountIndexes)
	removed := pruneProviderRuntimeAccountCollisionsLocked(t.aggregates, accountIndexes)
	if removed {
		t.dirty = true
	}
	t.mu.Unlock()
	if !t.allowDurable {
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
	t.mu.Lock()
	pendingDirty := t.dirty
	t.mu.Unlock()
	if t.loaded && pendingDirty && t.store != "" {
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
	if pruneProviderRuntimeAccountCollisionsLocked(mergedPointers, accountIndexes) {
		removed = true
	}
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
	t.dirty = len(currentAggregates) > 0 || recovered || pruned || removed
	needsPersist := t.dirty
	t.mu.Unlock()
	if needsPersist {
		_ = t.persistLocked()
	}
}

func pruneProviderRuntimeAccountCollisionsLocked(aggregates map[string]*providerRuntimeAggregate, accountIndexes map[string]struct{}) bool {
	changed := false
	for key, aggregate := range aggregates {
		if aggregate == nil {
			continue
		}
		if authIndex := strings.TrimSpace(aggregate.AuthIndex); authIndex != "" && !strings.HasPrefix(aggregate.Identity, "credential:") {
			if _, exists := accountIndexes[authIndex]; exists {
				delete(aggregates, key)
				changed = true
			}
		}
	}
	return changed
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
	if t == nil {
		return nil
	}
	if !t.loaded || t.store == "" {
		// Best-effort background state: report that there is nothing to write to instead of
		// pretending the write succeeded, but keep the caller's control flow unchanged.
		t.mu.Lock()
		t.storageErr = "provider runtime state has no storage path yet"
		t.mu.Unlock()
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
	merged.CredentialBacked = merged.CredentialBacked || right.CredentialBacked || strings.HasPrefix(merged.Identity, "credential:") || strings.HasPrefix(right.Identity, "credential:")
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

// providerChannelIndexEntry is one credential of a live CPA provider channel
// row, keyed by the volatile auth index CPA assigned to it. The raw API key is
// held in memory only: it is hashed into the redacted credential identity as
// soon as the channel list is read, is never logged, and is never part of the
// persisted runtime state.
type providerChannelIndexEntry struct {
	apiKey   string
	identity string
	provider string
	baseURL  string
	kind     string
	// primary marks the auth index CPA reports for the whole channel row (as
	// opposed to one weighted key entry of it). It is the index a usage callback
	// names for that row, so it is the representative index when one is needed.
	primary bool
}

// providerChannelCredential is one auth index paired with the credential of the
// channel row it belongs to, as read from a CPA channel list.
type providerChannelCredential struct {
	AuthIndex string
	APIKey    string
	BaseURL   string
	Kind      string
	Provider  string
	// Primary reports that AuthIndex is the row-level index of the channel.
	Primary bool
}

// providerChannelCredentialRef is one distinct channel credential of a provider
// together with a representative auth index for it.
type providerChannelCredentialRef struct {
	identity  string
	authIndex string
}

// providerRuntimeOrphan is one aggregate stranded under the volatile identity
// "auth-index:<hex>" that a channel list read has now explained.
type providerRuntimeOrphan struct {
	key       string
	provider  string
	authIndex string
	aggregate *providerRuntimeAggregate
}

// providerRuntimeOrphanRepair reports what one repair pass did.
type providerRuntimeOrphanRepair struct {
	// Adopted counts the stranded aggregates folded into a channel's stable
	// credential aggregate.
	Adopted int
	// Ambiguous names the providers whose stranded aggregates were left alone
	// because more than one live channel row could own them.
	Ambiguous []string
}

// providerRuntimeSuperseded is one aggregate a live channel row has already
// replaced, so its history belongs in that row's credential aggregate. Two shapes
// qualify: a volatile "auth-index:" shell whose index a live row still reports
// (the aggregate the fix that keyed accounting by CPA's provider name left
// behind), and a credential-backed aggregate an earlier release filed under the
// channel KIND's name ("openai") instead of the row's CPA provider name
// ("openai-compatible-<name>").
type providerRuntimeSuperseded struct {
	key string
	// row is the live channel row the aggregate's auth index belongs to. Its
	// credential identity is the aggregate the row's new records write to.
	row providerChannelIndexEntry
	// authIndex is the live row index the aggregate was matched by, and therefore
	// the index the merged aggregate is published under.
	authIndex string
	// legacyKindName reports that the aggregate sits under the channel kind's name
	// rather than the row's CPA provider name, which is the shape the previous
	// release wrote for an OpenAI-compatible channel.
	legacyKindName bool
	aggregate      *providerRuntimeAggregate
}

// SetProviderChannelCredentials replaces the credential index of one CPA channel
// kind with the rows of a freshly read channel list. CPA's usage callback for an
// openai-compatibility or codex-api-key channel carries the auth index of the
// channel row but no API key, so this index is what maps such a record back to
// the channel credential (and therefore to the credential identity the whole
// downstream path uses).
//
// Only the entries of the given kind are replaced: every kind is read on its own,
// and a successful read of one kind must not drop another kind's entries. The map
// is bounded, a native account index always wins, and a later account listing
// still overrules an index recorded here.
func (t *ProviderRuntimeTracker) SetProviderChannelCredentials(kind string, credentials []providerChannelCredential) {
	if t == nil {
		return
	}
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.providerChannelIndex == nil {
		t.providerChannelIndex = make(map[string]providerChannelIndexEntry)
	}
	for authIndex, entry := range t.providerChannelIndex {
		if entry.kind == kind {
			delete(t.providerChannelIndex, authIndex)
		}
	}
	for _, credential := range credentials {
		authIndex := strings.TrimSpace(credential.AuthIndex)
		entry := providerChannelIndexEntry{
			apiKey:   credential.APIKey,
			identity: aiProviderRuntimeCredentialIdentity(credential.Provider, credential.APIKey),
			provider: normalizeRuntimeProvider(credential.Provider),
			baseURL:  strings.TrimSpace(credential.BaseURL),
			kind:     kind,
			primary:  credential.Primary,
		}
		if authIndex == "" || entry.identity == "" || entry.provider == "" {
			continue
		}
		if current, exists := t.providerChannelIndex[authIndex]; exists {
			// The row-level index is the more useful attribution of the two, so it
			// wins over a key-entry index that happens to carry the same value.
			if current.primary && !entry.primary {
				continue
			}
		} else if len(t.providerChannelIndex) >= providerRuntimeMaxProviderIndexEntries {
			continue
		}
		if _, isAccount := t.accountAuthIndexes[authIndex]; isAccount {
			continue
		}
		t.providerChannelIndex[authIndex] = entry
	}
}

// ProviderCredentialForAuthIndex resolves the provider channel credential CPA
// assigned one auth index to. It returns an empty key for an unknown index and
// for an index a native account owns, so account telemetry keeps its current
// behaviour. The caller must use the returned key only to derive the redacted
// credential identity: it is never logged or persisted.
func (t *ProviderRuntimeTracker) ProviderCredentialForAuthIndex(authIndex string) (apiKey, provider string) {
	authIndex = strings.TrimSpace(authIndex)
	if t == nil || authIndex == "" {
		return "", ""
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	if _, isAccount := t.accountAuthIndexes[authIndex]; isAccount {
		return "", ""
	}
	entry, exists := t.providerChannelIndex[authIndex]
	if !exists || entry.apiKey == "" {
		return "", ""
	}
	return entry.apiKey, entry.provider
}

// dropAccountClaimedProviderIndexesLocked removes provider channel entries whose
// auth index a native account now owns. CPA regenerates auth indexes, so an index
// read from a channel row can later belong to an account; the tracker must never
// attribute that account's usage to a provider channel. The caller must hold the
// write lock.
func (t *ProviderRuntimeTracker) dropAccountClaimedProviderIndexesLocked(accountIndexes map[string]struct{}) {
	for authIndex := range t.providerChannelIndex {
		if _, isAccount := accountIndexes[authIndex]; isAccount {
			delete(t.providerChannelIndex, authIndex)
		}
	}
}

// RepairOrphanedAuthIndexAggregates folds provider aggregates that no longer
// belong where they are into the stable credential aggregate of the channel row
// that now owns that provider. It is called after a channel list was read, because
// that is the only moment at which the plugin knows which auth index a live row
// uses and which credential owns it.
//
// Two shapes are recovered:
//
//   - An aggregate stranded under the volatile identity "auth-index:<hex>" that no
//     live row and no native account claims.
//   - An aggregate the live path has already moved on from: a volatile shell whose
//     auth index a live row still reports, or a credential-backed aggregate an
//     earlier release filed under the channel KIND's name ("openai") instead of
//     CPA's per-channel provider key ("openai-compatible-<name>"). Both are folded
//     only while the channel index already holds the row's credential-backed
//     aggregate: that existing target is the proof the row's new records are being
//     written elsewhere, so the aggregate in hand will never see another one. The
//     target is never created here.
//
// An aggregate is only adopted when the provider maps to exactly ONE live channel
// credential: with two rows the provenance is unknown, and moving real usage onto
// the wrong row is worse than leaving it stranded, so those providers are reported
// instead. An index a native account owns is never touched. The number of
// candidates inspected and the number of aggregates folded per pass are both
// bounded, and the pass is idempotent: an adopted aggregate is deleted and counters
// merge by maximum.
func (t *ProviderRuntimeTracker) RepairOrphanedAuthIndexAggregates() providerRuntimeOrphanRepair {
	repair := providerRuntimeOrphanRepair{}
	if t == nil {
		return repair
	}
	t.mu.Lock()
	orphans := t.orphanedAuthIndexAggregatesLocked()
	ambiguous := make(map[string]struct{})
	for _, orphan := range orphans {
		if repair.Adopted >= providerRuntimeMaxOrphanAdoptions {
			break
		}
		if _, reported := ambiguous[orphan.provider]; reported {
			continue
		}
		candidates := t.providerChannelCredentialsLocked(orphan.provider)
		if len(candidates) == 0 {
			// No live channel row for this provider: there is nothing to adopt the
			// history into yet, so it stays where it is.
			continue
		}
		if len(candidates) > 1 {
			ambiguous[orphan.provider] = struct{}{}
			continue
		}
		if t.adoptOrphanLocked(candidates[0], orphan) {
			repair.Adopted++
		}
	}
	// Second pass: the aggregates a live channel row has already replaced, which
	// the first pass deliberately skips because their auth index is still live.
	t.repairSupersededAggregatesLocked(&repair, ambiguous)
	for provider := range ambiguous {
		repair.Ambiguous = append(repair.Ambiguous, provider)
	}
	sort.Strings(repair.Ambiguous)
	t.mu.Unlock()
	if repair.Adopted > 0 {
		t.markDirty()
	}
	return repair
}

// orphanedAuthIndexAggregatesLocked collects the aggregates a channel list read
// has explained: provider aggregates whose identity is still the volatile
// "auth-index:" one and whose index no live channel row or account uses. The
// scan is bounded and the result is ordered so a pass is reproducible.
func (t *ProviderRuntimeTracker) orphanedAuthIndexAggregatesLocked() []providerRuntimeOrphan {
	orphans := make([]providerRuntimeOrphan, 0, 4)
	for key, aggregate := range t.aggregates {
		if aggregate == nil {
			continue
		}
		if len(orphans) >= providerRuntimeMaxOrphanCandidates {
			// Bounded pass: the rest is recovered by the next channel read.
			break
		}
		identity := strings.TrimSpace(aggregate.Identity)
		if !strings.HasPrefix(identity, "auth-index:") {
			// Only the volatile identity this mapping bug produced is recovered. A
			// credential-backed or auth-id aggregate is never merged anywhere.
			continue
		}
		identityIndex := strings.TrimSpace(strings.TrimPrefix(identity, "auth-index:"))
		authIndex := strings.TrimSpace(aggregate.AuthIndex)
		if authIndex == "" {
			authIndex = identityIndex
		}
		provider := normalizeRuntimeProvider(aggregate.Provider)
		if provider == "" || authIndex == "" {
			continue
		}
		if _, live := t.providerChannelIndex[authIndex]; live {
			continue
		}
		if _, isAccount := t.accountAuthIndexes[authIndex]; isAccount {
			continue
		}
		if identityIndex != "" && identityIndex != authIndex {
			if _, live := t.providerChannelIndex[identityIndex]; live {
				continue
			}
		}
		orphans = append(orphans, providerRuntimeOrphan{
			key: key, provider: provider, authIndex: authIndex, aggregate: aggregate,
		})
	}
	sort.Slice(orphans, func(i, j int) bool {
		if orphans[i].provider == orphans[j].provider {
			return orphans[i].authIndex < orphans[j].authIndex
		}
		return orphans[i].provider < orphans[j].provider
	})
	return orphans
}

// providerChannelCredentialsLocked lists the distinct credentials the channel
// index holds for one provider. Distinct credentials, not rows: two rows that
// share a credential are one credential, and therefore one unambiguous target.
func (t *ProviderRuntimeTracker) providerChannelCredentialsLocked(provider string) []providerChannelCredentialRef {
	provider = normalizeRuntimeProvider(provider)
	type choice struct {
		primary string
		lowest  string
	}
	choices := make(map[string]choice, 2)
	for authIndex, entry := range t.providerChannelIndex {
		if entry.provider != provider || entry.identity == "" {
			continue
		}
		current := choices[entry.identity]
		if current.lowest == "" || authIndex < current.lowest {
			current.lowest = authIndex
		}
		if entry.primary && (current.primary == "" || authIndex < current.primary) {
			current.primary = authIndex
		}
		choices[entry.identity] = current
	}
	refs := make([]providerChannelCredentialRef, 0, len(choices))
	for identity, current := range choices {
		authIndex := current.primary
		if authIndex == "" {
			authIndex = current.lowest
		}
		refs = append(refs, providerChannelCredentialRef{identity: identity, authIndex: authIndex})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].identity < refs[j].identity })
	return refs
}

// adoptOrphanLocked folds one stranded aggregate into the stable credential
// aggregate of the single credential that owns the provider. The caller must hold
// the write lock and must have already proven the credential is unambiguous.
func (t *ProviderRuntimeTracker) adoptOrphanLocked(credential providerChannelCredentialRef, orphan providerRuntimeOrphan) bool {
	key := runtimeAggregateKey(orphan.provider, credential.identity)
	if key == orphan.key {
		return false
	}
	target := t.aggregates[key]
	if target == nil {
		if len(t.aggregates) >= providerRuntimeMaxIdentities && !t.evictIdleAggregateLocked() {
			return false
		}
		target = &providerRuntimeAggregate{
			Provider:  orphan.provider,
			AuthIndex: credential.authIndex,
			Identity:  credential.identity,
			Models:    make(map[string]*providerRuntimeModel),
		}
		t.aggregates[key] = target
	}
	// Both aggregates are cumulative views of the same channel's history, so
	// counters merge by maximum (the convention the store merge already uses)
	// instead of being added, which keeps a repeated pass from double counting.
	merged := mergeProviderRuntimeAggregate(*target, *orphan.aggregate)
	merged.Identity = credential.identity
	merged.CredentialBacked = true
	if credential.authIndex != "" {
		// The row's current auth index replaces the stale index the orphan was
		// stranded under, so the dashboard row follows the live index.
		merged.AuthIndex = credential.authIndex
	}
	merged.Active = target.Active + orphan.aggregate.Active
	*target = merged
	delete(t.aggregates, orphan.key)
	for requestID, request := range t.requests {
		if request.AggregateKey == orphan.key {
			request.AggregateKey = key
			t.requests[requestID] = request
		}
	}
	return true
}

// repairSupersededAggregatesLocked folds the aggregates a live channel row has
// already replaced into that row's credential-backed aggregate (see
// supersededAggregatesLocked for which ones qualify). The caller must hold the
// write lock and passes the pass-wide ambiguity report along so a provider is
// named at most once per pass.
func (t *ProviderRuntimeTracker) repairSupersededAggregatesLocked(repair *providerRuntimeOrphanRepair, ambiguous map[string]struct{}) {
	for _, candidate := range t.supersededAggregatesLocked() {
		if repair.Adopted >= providerRuntimeMaxOrphanAdoptions {
			// Bounded pass: the rest is recovered by the next channel read.
			return
		}
		provider := candidate.row.provider
		if _, reported := ambiguous[provider]; reported {
			continue
		}
		if candidate.legacyKindName {
			// The KIND-derived name is shared by every channel of that kind, so only
			// the auth index can prove which row owns the aggregate. It must resolve
			// exactly one distinct credential of the row's provider, otherwise the
			// owner is unknown and is reported instead of guessed.
			credentials := t.providerChannelCredentialsLocked(provider)
			if len(credentials) > 1 {
				ambiguous[provider] = struct{}{}
				continue
			}
			if len(credentials) == 0 || credentials[0].identity != candidate.row.identity {
				continue
			}
		}
		if t.adoptSupersededAggregateLocked(candidate) {
			repair.Adopted++
		}
	}
}

// adoptSupersededAggregateLocked folds one superseded aggregate into the row's
// existing credential-backed aggregate. The target must already exist: its absence
// is exactly the proof that the live path has not settled on that row yet, in which
// case the aggregate in hand may still receive records and has to be left alone.
// The caller must hold the write lock and must have already proven the row's
// credential, so no aggregate is ever created here.
func (t *ProviderRuntimeTracker) adoptSupersededAggregateLocked(candidate providerRuntimeSuperseded) bool {
	key := runtimeAggregateKey(candidate.row.provider, candidate.row.identity)
	if key == candidate.key {
		return false
	}
	target := t.aggregates[key]
	if target == nil {
		return false
	}
	// Both aggregates are cumulative views of the same channel's history, so
	// counters merge by maximum - the convention adoptOrphanLocked and the store
	// merge already use - which keeps a repeated pass from double counting.
	merged := mergeProviderRuntimeAggregate(*target, *candidate.aggregate)
	merged.Provider = candidate.row.provider
	merged.Identity = candidate.row.identity
	merged.CredentialBacked = true
	if candidate.authIndex != "" {
		// The live index of the row replaces the stale index the superseded
		// aggregate was stranded under, so the dashboard row follows the live one.
		merged.AuthIndex = candidate.authIndex
	}
	merged.Active = target.Active + candidate.aggregate.Active
	*target = merged
	delete(t.aggregates, candidate.key)
	for requestID, request := range t.requests {
		if request.AggregateKey == candidate.key {
			request.AggregateKey = key
			t.requests[requestID] = request
		}
	}
	return true
}

// supersededAggregatesLocked collects the aggregates a live channel row has
// already replaced:
//
//   - a volatile "auth-index:<hex>" shell whose index that row still reports, and
//   - a credential-backed aggregate filed under the channel KIND's name while the
//     row is served under CPA's per-channel provider name.
//
// Both are only returned when the channel index already holds the row's
// credential-backed aggregate - the target the row's new records write to. That
// existing target is the evidence the live path has moved on, so the aggregate in
// hand can never receive another record; without it the aggregate may still be
// live, and creating the target here would attribute history nobody has proven.
// The scan is bounded and the result is ordered so a pass is reproducible.
func (t *ProviderRuntimeTracker) supersededAggregatesLocked() []providerRuntimeSuperseded {
	candidates := make([]providerRuntimeSuperseded, 0, 4)
	for key, aggregate := range t.aggregates {
		if aggregate == nil {
			continue
		}
		if len(candidates) >= providerRuntimeMaxOrphanCandidates {
			// Bounded pass: the rest is recovered by the next channel read.
			break
		}
		identity := strings.TrimSpace(aggregate.Identity)
		authIndex := strings.TrimSpace(aggregate.AuthIndex)
		identityIndex := ""
		legacyKindName := false
		switch {
		case strings.HasPrefix(identity, "auth-index:"):
			identityIndex = strings.TrimSpace(strings.TrimPrefix(identity, "auth-index:"))
			if authIndex == "" {
				authIndex = identityIndex
			}
		case strings.HasPrefix(identity, "credential:"):
			// A credential-backed aggregate is only superseded when it sits under
			// the KIND-derived name: the current release always writes the row's CPA
			// provider name, so that name is the row's own aggregate.
			legacyKindName = true
		default:
			continue
		}
		if authIndex == "" {
			// Without an index nothing proves which row owns the aggregate.
			continue
		}
		row, live := t.providerChannelIndex[authIndex]
		if !live {
			// Not an index of a live row: either a real orphan, which the first
			// pass owns, or history that belongs to nobody.
			continue
		}
		if _, isAccount := t.accountAuthIndexes[authIndex]; isAccount {
			continue
		}
		if identityIndex != "" && identityIndex != authIndex {
			if _, isAccount := t.accountAuthIndexes[identityIndex]; isAccount {
				continue
			}
		}
		if legacyKindName {
			provider := normalizeRuntimeProvider(aggregate.Provider)
			if provider == row.provider {
				// Already filed under the row's CPA provider name.
				continue
			}
			if provider != normalizeRuntimeProvider(aiProviderRuntimeProviderName(row.kind)) {
				// Only the name the channel kind itself used to publish is migrated:
				// any other name is a different aggregate, not a legacy spelling of
				// this row.
				continue
			}
		}
		if t.aggregates[runtimeAggregateKey(row.provider, row.identity)] == nil {
			continue
		}
		candidates = append(candidates, providerRuntimeSuperseded{
			key: key, row: row, authIndex: authIndex, legacyKindName: legacyKindName, aggregate: aggregate,
		})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].row.provider == candidates[j].row.provider {
			if candidates[i].aggregate.Identity == candidates[j].aggregate.Identity {
				return candidates[i].key < candidates[j].key
			}
			return candidates[i].aggregate.Identity < candidates[j].aggregate.Identity
		}
		return candidates[i].row.provider < candidates[j].row.provider
	})
	return candidates
}

// RequestInterceptionActive keeps the lifecycle observer attached even when no
// mutating request experiment is enabled. It never changes request bodies.
func (t *ProviderRuntimeTracker) RequestInterceptionActive() bool              { return t != nil }
func (t *ProviderRuntimeTracker) RequestInterceptionAcceptsFormat(string) bool { return t != nil }
func (t *ProviderRuntimeTracker) InterceptRequest(request cpaapi.RequestInterceptRequest) (cpaapi.RequestInterceptResponse, bool) {
	return t.interceptRequest(request, false)
}

// ObserveRequest is a direct observer for callers that already know the
// request belongs to a provider. It retains the format fallback for legacy
// direct callers; the host request-interceptor path uses InterceptRequest and
// requires explicit provider metadata to avoid classifying OAuth account
// traffic as AI-provider traffic.
func (t *ProviderRuntimeTracker) ObserveRequest(request cpaapi.RequestInterceptRequest) {
	_, _ = t.interceptRequest(request, true)
}

func (t *ProviderRuntimeTracker) interceptRequest(request cpaapi.RequestInterceptRequest, allowFormatFallback bool) (cpaapi.RequestInterceptResponse, bool) {
	if t == nil || strings.TrimSpace(request.RequestID) == "" {
		return cpaapi.RequestInterceptResponse{}, false
	}
	identity, authIndex := runtimeIdentityFromMetadata(request.Metadata)
	if identity == "" {
		return cpaapi.RequestInterceptResponse{}, false
	}
	// The host invokes request interceptors for both native OAuth accounts and
	// API-key provider channels. ToFormat identifies the upstream protocol, not
	// the credential class, so it must never be used as a provider fallback for
	// the shared lifecycle path. Accept explicit API-key metadata, or an auth
	// index already proven to belong to a credential-based provider aggregate.
	if !allowFormatFallback && !requestMetadataIndicatesAPIKey(request.Metadata) && !t.knownProviderAuthIndex(authIndex) {
		return cpaapi.RequestInterceptResponse{}, false
	}
	provider := runtimeProviderFromMetadata(request.Metadata)
	if provider == "" && allowFormatFallback {
		provider = normalizeRuntimeProvider(request.ToFormat)
	}
	if provider == "" {
		return cpaapi.RequestInterceptResponse{}, false
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
	if t == nil || strings.EqualFold(strings.TrimSpace(record.AuthType), "oauth") || strings.EqualFold(strings.TrimSpace(record.AuthType), "oauth2") {
		return
	}
	if strings.TrimSpace(record.AuthType) == "" && t.isKnownAccountIdentity(strings.TrimSpace(record.AuthIndex), strings.TrimSpace(record.AuthID)) {
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
	// A request metadata hint is not sufficient provenance: CPA can expose
	// protocol-level api_key metadata for native OAuth requests too. Mark an
	// aggregate as provider-backed only when the usage callback carries an
	// actual API key, which is immediately hashed and never persisted.
	if runtimeCredentialIdentity(record) != "" {
		aggregate.CredentialBacked = true
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

func (t *ProviderRuntimeTracker) isKnownAccountIdentity(authIndex, authID string) bool {
	if t == nil {
		return false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	for _, value := range []string{authIndex, authID} {
		if value == "" {
			continue
		}
		if _, ok := t.accountAuthIndexes[value]; ok {
			return true
		}
	}
	return false
}

// Reset removes locally persisted runtime usage for the requested provider
// identity. It never contacts an upstream provider or changes its quota.
func (t *ProviderRuntimeTracker) Reset(provider, identity string) bool {
	if t == nil {
		return false
	}
	provider = normalizeRuntimeProvider(provider)
	identity = strings.TrimSpace(identity)
	if provider == "" || identity == "" {
		return false
	}
	removed := false
	t.mu.Lock()
	matchedKeys := make(map[string]struct{})
	for key, aggregate := range t.aggregates {
		if aggregate == nil || normalizeRuntimeProvider(aggregate.Provider) != provider {
			continue
		}
		if strings.TrimSpace(aggregate.Identity) != identity && strings.TrimSpace(aggregate.AuthIndex) != identity {
			continue
		}
		// Keep the aggregate and its Active count so in-flight requests remain
		// accounted for, but clear all locally accumulated usage history.
		aggregate.InputTokens = 0
		aggregate.OutputTokens = 0
		aggregate.ReasoningTokens = 0
		aggregate.CachedTokens = 0
		aggregate.TotalTokens = 0
		aggregate.AmountNanos = 0
		aggregate.RatedRequests = 0
		aggregate.UnratedRequests = 0
		aggregate.Models = make(map[string]*providerRuntimeModel)
		aggregate.Events = nil
		aggregate.RequestEvents = nil
		aggregate.UpdatedAt = t.now().UTC()
		matchedKeys[key] = struct{}{}
		removed = true
	}
	for key, target := range t.aliases {
		if _, matched := matchedKeys[target]; matched {
			delete(t.aliases, key)
		}
	}
	t.mu.Unlock()
	if removed {
		t.storeMu.Lock()
		if t.loaded && t.store != "" {
			t.mu.Lock()
			t.dirty = true
			t.mu.Unlock()
			_ = t.persistLocked()
		}
		t.storeMu.Unlock()
	}
	return removed
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
		if authIndex := strings.TrimSpace(aggregate.AuthIndex); authIndex != "" && !strings.HasPrefix(aggregate.Identity, "credential:") {
			if _, isAccount := t.accountAuthIndexes[authIndex]; isAccount {
				// Account and provider callbacks share CPA's auth-index namespace.
				// Never expose a provider aggregate for an index currently owned by
				// a native account.
				continue
			}
		}
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
		usedRequests := 0
		if windowSeconds > 0 {
			usedRequests = countProviderRequestEvents(aggregate.RequestEvents, now.Add(-time.Duration(windowSeconds)*time.Second), now)
		}
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
			CredentialBacked:        aggregate.CredentialBacked || strings.HasPrefix(aggregate.Identity, "credential:"),
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
	t.mu.Lock()
	t.dirty = false
	t.mu.Unlock()
	t.storeMu.Unlock()
	t.mu.Lock()
	t.requests = make(map[string]providerRuntimeRequest)
	t.aggregates = make(map[string]*providerRuntimeAggregate)
	// The channel index is host-derived, so it is dropped with the rest of the
	// in-memory state instead of surviving a retired instance.
	t.providerChannelIndex = make(map[string]providerChannelIndexEntry)
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
	return 0
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

func requestMetadataIndicatesAPIKey(metadata map[string]any) bool {
	for _, key := range []string{"auth_type", "authType", "account_type", "accountType", "credential_type", "credentialType"} {
		value, ok := metadata[key].(string)
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "apikey", "api_key", "api-key":
			return true
		case "oauth", "oauth2":
			return false
		}
	}
	for _, key := range []string{"is_api_key", "isApiKey", "provider_api_key", "providerApiKey"} {
		if value, ok := metadata[key].(bool); ok && value {
			return true
		}
	}
	return false
}

func (t *ProviderRuntimeTracker) knownProviderAuthIndex(authIndex string) bool {
	authIndex = strings.TrimSpace(authIndex)
	if t == nil || authIndex == "" {
		return false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	for _, aggregate := range t.aggregates {
		if aggregate == nil || aggregate.AuthIndex != authIndex {
			continue
		}
		// Credential-derived identities are only created from API-key usage
		// records. OAuth account aggregates use auth-index/auth-id identities.
		if strings.HasPrefix(aggregate.Identity, "credential:") {
			return true
		}
	}
	return false
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
	// API-key callbacks may carry an auth index that CPA regenerates when a
	// provider channel is edited. Prefer the redacted credential digest as the
	// aggregate identity while retaining authIndex for display/policy lookup.
	// This also prevents an API-key provider request from merging into an OAuth
	// account aggregate that happens to use the same auth index.
	if credentialIdentity := runtimeCredentialIdentity(record); credentialIdentity != "" {
		return credentialIdentity, strings.TrimSpace(record.AuthIndex)
	}
	if authIndex := strings.TrimSpace(record.AuthIndex); authIndex != "" {
		return "auth-index:" + authIndex, authIndex
	}
	if authID := strings.TrimSpace(record.AuthID); authID != "" {
		return "auth-id:" + authID, ""
	}
	return "", ""
}

// aiProviderRuntimeCredentialIdentity derives the runtime aggregate identity CPA
// reports for a provider credential. It is the durable usage key: it survives an
// auth-index regeneration, so usage history follows the channel rather than the
// volatile index, and it never exposes the credential itself.
func aiProviderRuntimeCredentialIdentity(provider, apiKey string) string {
	provider, key := normalizeRuntimeProvider(provider), strings.TrimSpace(apiKey)
	if provider == "" || key == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(provider + "\x00" + key))
	return "credential:" + hex.EncodeToString(digest[:])
}

func runtimeCredentialIdentity(record cpaapi.UsageRecord) string {
	return aiProviderRuntimeCredentialIdentity(record.Provider, record.APIKey)
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
