package manager

import (
	"errors"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

const (
	maxUsageAccounts        = 10_000
	maxUsageResetAfter      = 31 * 24 * time.Hour
	maxUsageWindowMinutes   = 31 * 24 * 60
	usageWindowWithoutReset = 15 * time.Minute
	usageWindowResetDrift   = 2 * time.Minute
	usagePersistDelay       = 2 * time.Second
)

type CreditUsageSnapshot struct {
	AmountUSD        float64    `json:"amount_usd"`
	RatedRequests    int64      `json:"rated_requests"`
	UnratedRequests  int64      `json:"unrated_requests"`
	StartedAt        *time.Time `json:"started_at,omitempty"`
	PricingUpdatedAt *time.Time `json:"pricing_updated_at,omitempty"`
	PricingSource    string     `json:"pricing_source,omitempty"`
}

type AccountUsageSnapshot struct {
	InputTokens         int64                `json:"input_tokens"`
	OutputTokens        int64                `json:"output_tokens"`
	ReasoningTokens     int64                `json:"reasoning_tokens"`
	CachedTokens        int64                `json:"cached_tokens"`
	CacheReadTokens     int64                `json:"cache_read_tokens"`
	CacheCreationTokens int64                `json:"cache_creation_tokens"`
	TotalTokens         int64                `json:"total_tokens"`
	LastRequestAt       *time.Time           `json:"last_request_at,omitempty"`
	UpdatedAt           *time.Time           `json:"updated_at,omitempty"`
	Codex               *CodexUsageSnapshot  `json:"codex,omitempty"`
	Credit              *CreditUsageSnapshot `json:"credit,omitempty"`
}

type AccountLifecycleSnapshot struct {
	CreatedAt  *time.Time
	DisabledAt *time.Time
}

type CodexUsageSnapshot struct {
	FiveHour           *UsageWindowSnapshot `json:"five_hour,omitempty"`
	SevenDay           *UsageWindowSnapshot `json:"seven_day,omitempty"`
	PlanType           string               `json:"plan_type,omitempty"`
	ActiveResetCount   *int                 `json:"active_reset_count,omitempty"`
	MetadataObservedAt time.Time            `json:"metadata_observed_at,omitempty"`
	ObservedAt         time.Time            `json:"observed_at"`
}

type UsageWindowSnapshot struct {
	UsedPercent        float64    `json:"used_percent"`
	ResetAt            *time.Time `json:"reset_at,omitempty"`
	WindowMinutes      int        `json:"window_minutes,omitempty"`
	OverdraftActive    bool       `json:"overdraft_active,omitempty"`
	OverdraftTokens    int64      `json:"overdraft_tokens,omitempty"`
	OverdraftRequests  int64      `json:"overdraft_requests,omitempty"`
	OverdraftAmountUSD float64    `json:"overdraft_amount_usd,omitempty"`
	OverdraftRated     int64      `json:"overdraft_rated_requests,omitempty"`
	OverdraftUnrated   int64      `json:"overdraft_unrated_requests,omitempty"`
	OverdraftStartedAt *time.Time `json:"overdraft_started_at,omitempty"`
	OverdraftRecoverAt *time.Time `json:"overdraft_recover_at,omitempty"`
}

type usageAggregate struct {
	Identity               usageIdentityFingerprint `json:"identity,omitempty"`
	InputTokens            int64                    `json:"input_tokens"`
	OutputTokens           int64                    `json:"output_tokens"`
	ReasoningTokens        int64                    `json:"reasoning_tokens"`
	CachedTokens           int64                    `json:"cached_tokens"`
	CacheReadTokens        int64                    `json:"cache_read_tokens"`
	CacheCreationTokens    int64                    `json:"cache_creation_tokens"`
	TotalTokens            int64                    `json:"total_tokens"`
	SuccessfulTokens       int64                    `json:"successful_tokens,omitempty"`
	SuccessfulRequests     int64                    `json:"successful_requests,omitempty"`
	CreditAmountNanos      int64                    `json:"credit_amount_nanos,omitempty"`
	CreditRatedRequests    int64                    `json:"credit_rated_requests,omitempty"`
	CreditUnratedRequests  int64                    `json:"credit_unrated_requests,omitempty"`
	CreditStartedAt        time.Time                `json:"credit_started_at,omitempty"`
	CreditPricingUpdatedAt time.Time                `json:"credit_pricing_updated_at,omitempty"`
	CreditPricingSource    string                   `json:"credit_pricing_source,omitempty"`
	FiveHourOverdraft      *overdraftCycleState     `json:"five_hour_overdraft,omitempty"`
	SevenDayOverdraft      *overdraftCycleState     `json:"seven_day_overdraft,omitempty"`
	Lifecycle              *accountLifecycleState   `json:"lifecycle,omitempty"`
	LastRequestAt          time.Time                `json:"last_request_at,omitempty"`
	UpdatedAt              time.Time                `json:"updated_at,omitempty"`
	Codex                  *CodexUsageSnapshot      `json:"codex,omitempty"`
}

type accountLifecycleState struct {
	CreatedAt      time.Time `json:"created_at"`
	Disabled       bool      `json:"disabled"`
	DisabledAt     time.Time `json:"disabled_at,omitempty"`
	StateChangedAt time.Time `json:"state_changed_at"`
}

type overdraftCycleState struct {
	Active                    bool      `json:"active"`
	BaselineTokens            int64     `json:"baseline_tokens,omitempty"`
	BaselineRequests          int64     `json:"baseline_requests,omitempty"`
	BaselineCreditAmountNanos int64     `json:"baseline_credit_amount_nanos,omitempty"`
	BaselineCreditRated       int64     `json:"baseline_credit_rated_requests,omitempty"`
	BaselineCreditUnrated     int64     `json:"baseline_credit_unrated_requests,omitempty"`
	StartedAt                 time.Time `json:"started_at,omitempty"`
	RecoverAt                 time.Time `json:"recover_at,omitempty"`
	WindowMinutes             int       `json:"window_minutes,omitempty"`
	ChangedAt                 time.Time `json:"changed_at"`
}

type UsageTracker struct {
	mu               sync.RWMutex
	storeMu          sync.Mutex
	accounts         map[string]usageAggregate
	bindings         map[string]usageBinding
	bindingsReady    bool
	now              func() time.Time
	store            string
	durableStore     string
	allowDurable     bool
	loaded           bool
	dirty            bool
	generation       uint64
	persistDelay     time.Duration
	wake             chan struct{}
	stop             chan struct{}
	done             chan struct{}
	closeOnce        sync.Once
	creditCalculator UsageCreditCalculator
}

func NewUsageTracker() *UsageTracker {
	tracker := &UsageTracker{
		accounts:     make(map[string]usageAggregate),
		bindings:     make(map[string]usageBinding),
		now:          time.Now,
		persistDelay: usagePersistDelay,
		wake:         make(chan struct{}, 1),
		stop:         make(chan struct{}),
		done:         make(chan struct{}),
	}
	go tracker.run()
	return tracker
}

func (t *UsageTracker) SetCreditCalculator(calculator UsageCreditCalculator) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.creditCalculator = calculator
	t.mu.Unlock()
}

func (t *UsageTracker) Configure(config Config) {
	if t == nil {
		return
	}
	config = normalizeConfig(config)
	storePath := usageStorePath(config.DataDir)

	t.storeMu.Lock()
	defer t.storeMu.Unlock()
	t.mu.Lock()
	t.allowDurable = config.implicitDataDir
	if !t.allowDurable {
		t.durableStore = ""
	} else if t.durableStore != "" {
		storePath = t.durableStore
	}
	if t.loaded && t.store == storePath {
		t.mu.Unlock()
		return
	}
	if t.loaded && t.dirty && t.store != "" {
		if persisted, errSave := persistUsageState(t.store, t.accounts); errSave == nil {
			t.accounts = mergeUsageAggregates(t.accounts, persisted)
			t.dirty = false
		}
	}
	accounts, recovered, errLoad := loadUsageStateWithBackup(storePath)
	if errLoad != nil {
		accounts = make(map[string]usageAggregate)
	}
	t.accounts = accounts
	t.store = storePath
	t.loaded = true
	t.dirty = recovered
	t.generation++
	t.mu.Unlock()
	if recovered {
		t.requestPersist()
	}
}

func (t *UsageTracker) DiscoverAuthStorage(entries []cpaapi.HostAuthFileEntry) {
	if t == nil {
		return
	}
	authDir := discoverUsageAuthDir(entries)
	if authDir != "" {
		t.configureDurableStore(durableUsageStorePath(authDir))
	}
	t.bindUsageAccounts(entries)
}

func (t *UsageTracker) bindUsageAccounts(entries []cpaapi.HostAuthFileEntry) {
	bindings := buildUsageBindings(entries)
	now := t.currentTime()
	changed := false
	t.mu.Lock()
	t.bindings = bindings
	t.bindingsReady = true
	for authIndex, binding := range bindings {
		current, exists := t.accounts[binding.Key]
		if exists && usageIdentitiesConflict(current.Identity, binding.Identity) {
			if binding.Disabled && usageIdentityCanRebindByEmail(current.Identity, binding.Identity) {
				// Email is the durable primary identity. CPA may regenerate the
				// auth index and project a different Team workspace fingerprint
				// after writing the disabled field. Keep usage and active overdraft
				// cycles for that single account while refreshing the auxiliary ID.
				current.Identity = rebindUsageIdentity(current.Identity, binding.Identity)
			} else {
				current = usageAggregate{Identity: binding.Identity, UpdatedAt: now}
			}
			t.accounts[binding.Key] = current
			changed = true
		} else if exists {
			mergedIdentity := mergeUsageIdentity(current.Identity, binding.Identity)
			if mergedIdentity != current.Identity {
				current.Identity = mergedIdentity
				t.accounts[binding.Key] = current
				changed = true
			}
		}
		pendingKey := usagePendingKey(authIndex)
		pending, pendingExists := t.accounts[pendingKey]
		if pendingExists {
			delete(t.accounts, pendingKey)
			pending.Identity = mergeUsageIdentity(pending.Identity, binding.Identity)
			if exists && !usageIdentitiesConflict(current.Identity, pending.Identity) {
				pending = mergeUsageAggregate(pending, current)
			}
			pending.Identity = mergeUsageIdentity(pending.Identity, binding.Identity)
			current = pending
			changed = true
		}
		lifecycle := observeAccountLifecycle(current.Lifecycle, binding, now)
		if !sameAccountLifecycle(current.Lifecycle, lifecycle) {
			current.Lifecycle = lifecycle
			current.UpdatedAt = now
			changed = true
		}
		t.accounts[binding.Key] = current
	}
	if changed {
		t.dirty = true
		t.generation++
	}
	t.mu.Unlock()
	if changed {
		t.requestPersist()
	}
}

func (t *UsageTracker) AccountLifecycle(authIndex string) AccountLifecycleSnapshot {
	if t == nil {
		return AccountLifecycleSnapshot{}
	}
	authIndex = strings.TrimSpace(authIndex)
	if authIndex == "" {
		return AccountLifecycleSnapshot{}
	}
	t.mu.RLock()
	storageKey, identity := t.usageStorageKeyLocked(authIndex)
	if t.bindingsReady && identity == (usageIdentityFingerprint{}) {
		t.mu.RUnlock()
		return AccountLifecycleSnapshot{}
	}
	aggregate, exists := t.accounts[storageKey]
	t.mu.RUnlock()
	if !exists || usageIdentitiesConflict(aggregate.Identity, identity) || aggregate.Lifecycle == nil {
		return AccountLifecycleSnapshot{}
	}
	state := sanitizeAccountLifecycle(aggregate.Lifecycle)
	if state == nil {
		return AccountLifecycleSnapshot{}
	}
	createdAt := state.CreatedAt.UTC()
	snapshot := AccountLifecycleSnapshot{CreatedAt: &createdAt}
	if state.Disabled && !state.DisabledAt.IsZero() {
		disabledAt := state.DisabledAt.UTC()
		snapshot.DisabledAt = &disabledAt
	}
	return snapshot
}

func (t *UsageTracker) configureDurableStore(storePath string) {
	storePath = filepath.Clean(strings.TrimSpace(storePath))
	if t == nil || storePath == "." || !filepath.IsAbs(storePath) {
		return
	}
	t.storeMu.Lock()
	defer t.storeMu.Unlock()

	t.mu.RLock()
	if !t.allowDurable || t.store == storePath {
		t.mu.RUnlock()
		return
	}
	generation := t.generation
	current := cloneUsageAggregates(t.accounts)
	t.mu.RUnlock()

	stored, recovered, errLoad := loadUsageStateWithBackup(storePath)
	if errLoad != nil && !errors.Is(errLoad, os.ErrNotExist) {
		return
	}
	merged := mergeUsageAggregates(current, stored)
	if len(merged) > 0 || recovered {
		persisted, errPersist := persistUsageState(storePath, merged)
		if errPersist != nil {
			return
		}
		merged = persisted
	}

	t.mu.Lock()
	if !t.allowDurable {
		t.mu.Unlock()
		return
	}
	t.accounts = mergeUsageAggregates(t.accounts, merged)
	t.store = storePath
	t.durableStore = storePath
	t.loaded = true
	t.dirty = t.generation != generation
	t.generation++
	dirty := t.dirty
	t.mu.Unlock()
	if dirty {
		t.requestPersist()
	}
}

func (t *UsageTracker) Observe(record cpaapi.UsageRecord) {
	if t == nil {
		return
	}
	authIndex := strings.TrimSpace(record.AuthIndex)
	if authIndex == "" {
		return
	}
	now := t.currentTime()
	t.mu.RLock()
	calculator := t.creditCalculator
	t.mu.RUnlock()
	creditCharge := CreditCharge{}
	if calculator != nil {
		creditCharge = calculator.Calculate(record)
	}
	requestedAt := record.RequestedAt.UTC()
	if requestedAt.IsZero() || requestedAt.After(now.Add(24*time.Hour)) {
		requestedAt = now
	}

	t.mu.Lock()
	storageKey, identity := t.usageStorageKeyLocked(authIndex)
	if _, exists := t.accounts[storageKey]; !exists && len(t.accounts) >= maxUsageAccounts {
		t.evictOldestLocked()
	}
	aggregate := t.accounts[storageKey]
	aggregate.Identity = mergeUsageIdentity(aggregate.Identity, identity)
	aggregate.InputTokens = saturatingAdd(aggregate.InputTokens, nonNegative(record.Detail.InputTokens))
	aggregate.OutputTokens = saturatingAdd(aggregate.OutputTokens, nonNegative(record.Detail.OutputTokens))
	aggregate.ReasoningTokens = saturatingAdd(aggregate.ReasoningTokens, nonNegative(record.Detail.ReasoningTokens))
	aggregate.CachedTokens = saturatingAdd(aggregate.CachedTokens, nonNegative(record.Detail.CachedTokens))
	aggregate.CacheReadTokens = saturatingAdd(aggregate.CacheReadTokens, nonNegative(record.Detail.CacheReadTokens))
	aggregate.CacheCreationTokens = saturatingAdd(aggregate.CacheCreationTokens, nonNegative(record.Detail.CacheCreationTokens))
	totalTokens := nonNegative(record.Detail.TotalTokens)
	if totalTokens == 0 {
		totalTokens = saturatingAdd(nonNegative(record.Detail.InputTokens), nonNegative(record.Detail.OutputTokens))
		totalTokens = saturatingAdd(totalTokens, nonNegative(record.Detail.ReasoningTokens))
	}
	aggregate.TotalTokens = saturatingAdd(aggregate.TotalTokens, totalTokens)
	if !record.Failed {
		aggregate.SuccessfulTokens = saturatingAdd(aggregate.SuccessfulTokens, totalTokens)
		aggregate.SuccessfulRequests = saturatingAdd(aggregate.SuccessfulRequests, 1)
		if creditCharge.Enabled {
			if aggregate.CreditStartedAt.IsZero() {
				aggregate.CreditStartedAt = now
			}
			if creditCharge.Rated {
				aggregate.CreditAmountNanos = saturatingAdd(aggregate.CreditAmountNanos, creditCharge.AmountNanos)
				aggregate.CreditRatedRequests = saturatingAdd(aggregate.CreditRatedRequests, 1)
			} else {
				aggregate.CreditUnratedRequests = saturatingAdd(aggregate.CreditUnratedRequests, 1)
			}
			if creditCharge.PricingUpdatedAt.After(aggregate.CreditPricingUpdatedAt) {
				aggregate.CreditPricingUpdatedAt = creditCharge.PricingUpdatedAt
				aggregate.CreditPricingSource = strings.TrimSpace(creditCharge.PricingSource)
			}
		}
	}
	if aggregate.LastRequestAt.IsZero() || requestedAt.After(aggregate.LastRequestAt) {
		aggregate.LastRequestAt = requestedAt
	}
	aggregate.UpdatedAt = now
	if codex := parseCodexUsageHeaders(record.ResponseHeaders, now); codex != nil {
		if aggregate.Codex == nil {
			aggregate.Codex = &CodexUsageSnapshot{}
		}
		if codex.FiveHour != nil {
			if codex.FiveHour.UsedPercent == 0 {
				aggregate.FiveHourOverdraft = stoppedOverdraftCycle(aggregate.FiveHourOverdraft, now)
			}
			aggregate.Codex.FiveHour = mergeObservedUsageWindow(aggregate.Codex.FiveHour, codex.FiveHour)
		}
		if codex.SevenDay != nil {
			if codex.SevenDay.UsedPercent == 0 {
				aggregate.SevenDayOverdraft = stoppedOverdraftCycle(aggregate.SevenDayOverdraft, now)
			}
			aggregate.Codex.SevenDay = mergeObservedUsageWindow(aggregate.Codex.SevenDay, codex.SevenDay)
		}
		aggregate.Codex.ObservedAt = codex.ObservedAt
	}
	t.accounts[storageKey] = aggregate
	t.dirty = true
	t.generation++
	t.mu.Unlock()
	t.requestPersist()
}

func (t *UsageTracker) ObserveCredentialUsage(authIndex string, snapshot *CodexUsageSnapshot) {
	if t == nil || snapshot == nil {
		return
	}
	authIndex = safeOperationIdentifier(authIndex, 256)
	if authIndex == "" {
		return
	}
	now := t.currentTime()
	cloned := cloneCodexUsage(snapshot)
	if cloned == nil || !hasCodexUsageData(cloned) {
		return
	}
	cloned.ObservedAt = now
	t.mu.Lock()
	storageKey, identity := t.usageStorageKeyLocked(authIndex)
	if _, exists := t.accounts[storageKey]; !exists && len(t.accounts) >= maxUsageAccounts {
		t.evictOldestLocked()
	}
	aggregate := t.accounts[storageKey]
	aggregate.Identity = mergeUsageIdentity(aggregate.Identity, identity)
	if aggregate.Codex == nil {
		aggregate.Codex = &CodexUsageSnapshot{}
	}
	if cloned.FiveHour != nil {
		if cloned.FiveHour.UsedPercent == 0 {
			aggregate.FiveHourOverdraft = stoppedOverdraftCycle(aggregate.FiveHourOverdraft, now)
		}
		aggregate.Codex.FiveHour = mergeObservedUsageWindow(aggregate.Codex.FiveHour, cloned.FiveHour)
	}
	if cloned.SevenDay != nil {
		if cloned.SevenDay.UsedPercent == 0 {
			aggregate.SevenDayOverdraft = stoppedOverdraftCycle(aggregate.SevenDayOverdraft, now)
		}
		aggregate.Codex.SevenDay = mergeObservedUsageWindow(aggregate.Codex.SevenDay, cloned.SevenDay)
	}
	if cloned.FiveHour != nil || cloned.SevenDay != nil {
		aggregate.Codex.ObservedAt = now
	}
	metadataObserved := !cloned.MetadataObservedAt.IsZero() || cloned.PlanType != "" || cloned.ActiveResetCount != nil
	if metadataObserved {
		aggregate.Codex.PlanType = cloned.PlanType
		aggregate.Codex.ActiveResetCount = cloneIntPointer(cloned.ActiveResetCount)
		aggregate.Codex.MetadataObservedAt = now
	}
	aggregate.UpdatedAt = now
	t.accounts[storageKey] = aggregate
	t.dirty = true
	t.generation++
	t.mu.Unlock()
	t.requestPersist()
}

func (t *UsageTracker) BeginOverdraftCycle(authIndex, quotaWindow string, exhaustedAt time.Time) {
	if t == nil {
		return
	}
	authIndex = safeOperationIdentifier(authIndex, 256)
	if authIndex == "" {
		return
	}
	now := t.currentTime()
	exhaustedAt = exhaustedAt.UTC()
	if exhaustedAt.IsZero() || exhaustedAt.After(now.Add(time.Minute)) {
		exhaustedAt = now
	}
	t.mu.Lock()
	storageKey, identity := t.usageStorageKeyLocked(authIndex)
	aggregate, exists := t.accounts[storageKey]
	if !exists || aggregate.Codex == nil {
		t.mu.Unlock()
		return
	}
	aggregate.Identity = mergeUsageIdentity(aggregate.Identity, identity)
	changed := false
	start := func(window *UsageWindowSnapshot, cycle **overdraftCycleState, fallbackMinutes int) {
		window = currentUsageWindow(window, aggregate.Codex.ObservedAt, exhaustedAt)
		if window == nil || window.UsedPercent < 100 || *cycle != nil && (*cycle).Active {
			return
		}
		minutes := window.WindowMinutes
		if minutes <= 0 {
			minutes = fallbackMinutes
		}
		recoverAt := exhaustedAt.Add(time.Duration(minutes) * time.Minute).UTC()
		*cycle = &overdraftCycleState{
			Active: true, BaselineTokens: aggregate.SuccessfulTokens, BaselineRequests: aggregate.SuccessfulRequests,
			BaselineCreditAmountNanos: aggregate.CreditAmountNanos,
			BaselineCreditRated:       aggregate.CreditRatedRequests,
			BaselineCreditUnrated:     aggregate.CreditUnratedRequests,
			StartedAt:                 exhaustedAt, RecoverAt: recoverAt, WindowMinutes: minutes, ChangedAt: now,
		}
		changed = true
	}
	switch normalizeInspectionQuotaWindow(quotaWindow) {
	case InspectionQuotaWindowFiveHour, InspectionQuotaWindowFiveHourFallback:
		start(aggregate.Codex.FiveHour, &aggregate.FiveHourOverdraft, 5*60)
	case InspectionQuotaWindowSevenDay:
		start(aggregate.Codex.SevenDay, &aggregate.SevenDayOverdraft, 7*24*60)
	default:
		start(aggregate.Codex.FiveHour, &aggregate.FiveHourOverdraft, 5*60)
		start(aggregate.Codex.SevenDay, &aggregate.SevenDayOverdraft, 7*24*60)
	}
	if changed {
		aggregate.UpdatedAt = now
		t.accounts[storageKey] = aggregate
		t.dirty = true
		t.generation++
	}
	t.mu.Unlock()
	if changed {
		t.requestPersist()
	}
}

func (t *UsageTracker) StopOverdraftCycle(authIndex string) {
	if t == nil {
		return
	}
	authIndex = safeOperationIdentifier(authIndex, 256)
	if authIndex == "" {
		return
	}
	now := t.currentTime()
	t.mu.Lock()
	storageKey, _ := t.usageStorageKeyLocked(authIndex)
	aggregate, exists := t.accounts[storageKey]
	if !exists {
		t.mu.Unlock()
		return
	}
	fiveHour := stoppedOverdraftCycle(aggregate.FiveHourOverdraft, now)
	sevenDay := stoppedOverdraftCycle(aggregate.SevenDayOverdraft, now)
	changed := fiveHour != aggregate.FiveHourOverdraft || sevenDay != aggregate.SevenDayOverdraft
	if changed {
		aggregate.FiveHourOverdraft = fiveHour
		aggregate.SevenDayOverdraft = sevenDay
		aggregate.UpdatedAt = now
		t.accounts[storageKey] = aggregate
		t.dirty = true
		t.generation++
	}
	t.mu.Unlock()
	if changed {
		t.requestPersist()
	}
}

func stoppedOverdraftCycle(cycle *overdraftCycleState, now time.Time) *overdraftCycleState {
	if cycle == nil || !cycle.Active {
		return cycle
	}
	return &overdraftCycleState{Active: false, ChangedAt: now.UTC()}
}

func (t *UsageTracker) Snapshot(authIndex string) *AccountUsageSnapshot {
	if t == nil {
		return nil
	}
	authIndex = strings.TrimSpace(authIndex)
	if authIndex == "" {
		return nil
	}
	t.mu.RLock()
	storageKey, identity := t.usageStorageKeyLocked(authIndex)
	if t.bindingsReady && identity == (usageIdentityFingerprint{}) {
		t.mu.RUnlock()
		return nil
	}
	aggregate, exists := t.accounts[storageKey]
	t.mu.RUnlock()
	if !exists || usageIdentitiesConflict(aggregate.Identity, identity) {
		return nil
	}
	return publicUsageSnapshot(aggregate, t.currentTime())
}

func (t *UsageTracker) UsageIdentity(authIndex string) string {
	if t == nil {
		return ""
	}
	authIndex = strings.TrimSpace(authIndex)
	if authIndex == "" {
		return ""
	}
	t.mu.RLock()
	binding, exists := t.bindings[authIndex]
	t.mu.RUnlock()
	if !exists {
		return ""
	}
	return binding.Key
}

func (t *UsageTracker) usageStorageKeyLocked(authIndex string) (string, usageIdentityFingerprint) {
	if binding, exists := t.bindings[authIndex]; exists {
		return binding.Key, binding.Identity
	}
	return usagePendingKey(authIndex), usageIdentityFingerprint{}
}

func (t *UsageTracker) Close() {
	if t == nil {
		return
	}
	t.closeOnce.Do(func() { close(t.stop) })
	<-t.done
}

func (t *UsageTracker) currentTime() time.Time {
	now := time.Now
	if t != nil && t.now != nil {
		now = t.now
	}
	return now().UTC()
}

func (t *UsageTracker) evictOldestLocked() {
	oldestKey := ""
	var oldest time.Time
	for storageKey, aggregate := range t.accounts {
		candidate := aggregate.UpdatedAt
		if candidate.IsZero() {
			candidate = aggregate.LastRequestAt
		}
		if oldestKey == "" || candidate.Before(oldest) || candidate.Equal(oldest) && storageKey < oldestKey {
			oldestKey = storageKey
			oldest = candidate
		}
	}
	if oldestKey != "" {
		delete(t.accounts, oldestKey)
	}
}

func (t *UsageTracker) requestPersist() {
	select {
	case t.wake <- struct{}{}:
	default:
	}
}

func (t *UsageTracker) run() {
	defer close(t.done)
	for {
		select {
		case <-t.wake:
			delay := t.persistDelay
			if delay <= 0 {
				delay = usagePersistDelay
			}
			timer := time.NewTimer(delay)
			select {
			case <-timer.C:
				t.persist()
			case <-t.stop:
				if !timer.Stop() {
					<-timer.C
				}
				t.persist()
				return
			}
		case <-t.stop:
			t.persist()
			return
		}
	}
}

func (t *UsageTracker) persist() {
	if t == nil {
		return
	}
	t.storeMu.Lock()
	defer t.storeMu.Unlock()
	t.mu.RLock()
	if !t.dirty || t.store == "" {
		t.mu.RUnlock()
		return
	}
	storePath := t.store
	generation := t.generation
	accounts := cloneUsageAggregates(t.accounts)
	t.mu.RUnlock()
	persisted, errSave := persistUsageState(storePath, accounts)
	if errSave != nil {
		return
	}
	t.mu.Lock()
	if t.store == storePath {
		t.accounts = mergeUsageAggregates(t.accounts, persisted)
	}
	if t.generation == generation && t.store == storePath {
		t.dirty = false
	}
	t.mu.Unlock()
}

func publicUsageSnapshot(aggregate usageAggregate, now time.Time) *AccountUsageSnapshot {
	codex := cloneCodexUsage(aggregate.Codex)
	if codex != nil {
		codex.FiveHour = publicUsageWindow(codex.FiveHour, codex.ObservedAt, now, aggregate.FiveHourOverdraft, aggregate)
		codex.SevenDay = publicUsageWindow(codex.SevenDay, codex.ObservedAt, now, aggregate.SevenDayOverdraft, aggregate)
		if !hasCodexUsageData(codex) {
			codex = nil
		}
	}
	credit := publicCreditUsageSnapshot(aggregate)
	if aggregate.InputTokens == 0 && aggregate.OutputTokens == 0 && aggregate.ReasoningTokens == 0 &&
		aggregate.CachedTokens == 0 && aggregate.CacheReadTokens == 0 && aggregate.CacheCreationTokens == 0 &&
		aggregate.TotalTokens == 0 && aggregate.LastRequestAt.IsZero() && codex == nil && credit == nil {
		return nil
	}
	snapshot := &AccountUsageSnapshot{
		InputTokens:         aggregate.InputTokens,
		OutputTokens:        aggregate.OutputTokens,
		ReasoningTokens:     aggregate.ReasoningTokens,
		CachedTokens:        aggregate.CachedTokens,
		CacheReadTokens:     aggregate.CacheReadTokens,
		CacheCreationTokens: aggregate.CacheCreationTokens,
		TotalTokens:         aggregate.TotalTokens,
		Codex:               codex,
		Credit:              credit,
	}
	if !aggregate.LastRequestAt.IsZero() {
		value := aggregate.LastRequestAt.UTC()
		snapshot.LastRequestAt = &value
	}
	if !aggregate.UpdatedAt.IsZero() {
		value := aggregate.UpdatedAt.UTC()
		snapshot.UpdatedAt = &value
	}
	return snapshot
}

func publicCreditUsageSnapshot(aggregate usageAggregate) *CreditUsageSnapshot {
	if aggregate.CreditStartedAt.IsZero() && aggregate.CreditRatedRequests == 0 && aggregate.CreditUnratedRequests == 0 && aggregate.CreditAmountNanos == 0 {
		return nil
	}
	snapshot := &CreditUsageSnapshot{
		AmountUSD:       float64(nonNegative(aggregate.CreditAmountNanos)) / creditNanosPerUSD,
		RatedRequests:   nonNegative(aggregate.CreditRatedRequests),
		UnratedRequests: nonNegative(aggregate.CreditUnratedRequests),
		PricingSource:   strings.TrimSpace(aggregate.CreditPricingSource),
	}
	if !aggregate.CreditStartedAt.IsZero() {
		value := aggregate.CreditStartedAt.UTC()
		snapshot.StartedAt = &value
	}
	if !aggregate.CreditPricingUpdatedAt.IsZero() {
		value := aggregate.CreditPricingUpdatedAt.UTC()
		snapshot.PricingUpdatedAt = &value
	}
	return snapshot
}

func publicUsageWindow(window *UsageWindowSnapshot, observedAt, now time.Time, cycle *overdraftCycleState, aggregate usageAggregate) *UsageWindowSnapshot {
	var snapshot *UsageWindowSnapshot
	if cycle != nil && cycle.Active {
		snapshot = cloneUsageWindow(window)
	} else {
		snapshot = currentUsageWindow(window, observedAt, now)
	}
	if snapshot == nil {
		return nil
	}
	snapshot.OverdraftActive = cycle != nil && cycle.Active
	snapshot.OverdraftTokens = 0
	snapshot.OverdraftRequests = 0
	snapshot.OverdraftAmountUSD = 0
	snapshot.OverdraftRated = 0
	snapshot.OverdraftUnrated = 0
	snapshot.OverdraftStartedAt = nil
	snapshot.OverdraftRecoverAt = nil
	if cycle == nil || !cycle.Active {
		return snapshot
	}
	snapshot.OverdraftTokens = nonNegative(aggregate.SuccessfulTokens - cycle.BaselineTokens)
	snapshot.OverdraftRequests = nonNegative(aggregate.SuccessfulRequests - cycle.BaselineRequests)
	snapshot.OverdraftAmountUSD = float64(nonNegative(aggregate.CreditAmountNanos-cycle.BaselineCreditAmountNanos)) / creditNanosPerUSD
	snapshot.OverdraftRated = nonNegative(aggregate.CreditRatedRequests - cycle.BaselineCreditRated)
	snapshot.OverdraftUnrated = nonNegative(aggregate.CreditUnratedRequests - cycle.BaselineCreditUnrated)
	snapshot.OverdraftStartedAt = timePointer(cycle.StartedAt)
	snapshot.OverdraftRecoverAt = timePointer(cycle.RecoverAt)
	return snapshot
}

func currentUsageWindow(window *UsageWindowSnapshot, observedAt, now time.Time) *UsageWindowSnapshot {
	if window == nil {
		return nil
	}
	if window.ResetAt != nil && !window.ResetAt.After(now) {
		return nil
	}
	if window.ResetAt == nil && !observedAt.IsZero() && now.Sub(observedAt) > usageWindowWithoutReset {
		return nil
	}
	return cloneUsageWindow(window)
}

func mergeObservedUsageWindow(current, observed *UsageWindowSnapshot) *UsageWindowSnapshot {
	if observed == nil {
		return cloneUsageWindow(current)
	}
	return cloneUsageWindow(observed)
}

func sameUsageWindow(left, right *UsageWindowSnapshot) bool {
	if left == nil || right == nil {
		return false
	}
	if left.WindowMinutes > 0 && right.WindowMinutes > 0 && left.WindowMinutes != right.WindowMinutes {
		return false
	}
	if left.ResetAt == nil || right.ResetAt == nil {
		return left.ResetAt == nil && right.ResetAt == nil
	}
	drift := left.ResetAt.Sub(*right.ResetAt)
	if drift < 0 {
		drift = -drift
	}
	return drift <= usageWindowResetDrift
}

type rawCodexWindow struct {
	usedPercent   *float64
	resetAfter    *time.Duration
	resetAt       *time.Time
	windowMinutes *int
}

func parseCodexUsageHeaders(headers http.Header, now time.Time) *CodexUsageSnapshot {
	if len(headers) == 0 {
		return nil
	}
	primary := rawCodexWindow{
		usedPercent:   parseUsagePercent(headers.Get("x-codex-primary-used-percent")),
		resetAfter:    parseResetAfter(headers.Get("x-codex-primary-reset-after-seconds")),
		resetAt:       parseResetAt(headers.Get("x-codex-primary-reset-at"), now),
		windowMinutes: parseWindowMinutes(headers.Get("x-codex-primary-window-minutes")),
	}
	secondary := rawCodexWindow{
		usedPercent:   parseUsagePercent(headers.Get("x-codex-secondary-used-percent")),
		resetAfter:    parseResetAfter(headers.Get("x-codex-secondary-reset-after-seconds")),
		resetAt:       parseResetAt(headers.Get("x-codex-secondary-reset-at"), now),
		windowMinutes: parseWindowMinutes(headers.Get("x-codex-secondary-window-minutes")),
	}
	if primary.usedPercent == nil && secondary.usedPercent == nil {
		return nil
	}
	var fiveHour, sevenDay rawCodexWindow
	switch {
	case primary.windowMinutes != nil && secondary.windowMinutes != nil:
		if *primary.windowMinutes <= *secondary.windowMinutes {
			fiveHour, sevenDay = primary, secondary
		} else {
			fiveHour, sevenDay = secondary, primary
		}
	case primary.windowMinutes != nil:
		if *primary.windowMinutes <= 360 {
			fiveHour, sevenDay = primary, secondary
		} else {
			fiveHour, sevenDay = secondary, primary
		}
	case secondary.windowMinutes != nil:
		if *secondary.windowMinutes <= 360 {
			fiveHour, sevenDay = secondary, primary
		} else {
			fiveHour, sevenDay = primary, secondary
		}
	default:
		fiveHour, sevenDay = secondary, primary
	}
	snapshot := &CodexUsageSnapshot{
		FiveHour:   usageWindowFromRaw(fiveHour, now),
		SevenDay:   usageWindowFromRaw(sevenDay, now),
		ObservedAt: now.UTC(),
	}
	if snapshot.FiveHour == nil && snapshot.SevenDay == nil {
		return nil
	}
	return snapshot
}

func usageWindowFromRaw(raw rawCodexWindow, now time.Time) *UsageWindowSnapshot {
	if raw.usedPercent == nil {
		return nil
	}
	window := &UsageWindowSnapshot{UsedPercent: *raw.usedPercent}
	if raw.resetAfter != nil {
		resetAt := now.Add(*raw.resetAfter).UTC()
		window.ResetAt = &resetAt
	} else if raw.resetAt != nil {
		window.ResetAt = cloneTimePointer(raw.resetAt)
	}
	if raw.windowMinutes != nil {
		window.WindowMinutes = *raw.windowMinutes
	}
	return window
}

func parseUsagePercent(value string) *float64 {
	parsed, errParse := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if errParse != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) || parsed < 0 || parsed > 10_000 {
		return nil
	}
	return &parsed
}

func parseResetAfter(value string) *time.Duration {
	seconds, errParse := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if errParse != nil || seconds < 0 {
		return nil
	}
	duration := time.Duration(seconds) * time.Second
	if duration > maxUsageResetAfter {
		return nil
	}
	return &duration
}

func parseResetAt(value string, now time.Time) *time.Time {
	seconds, errParse := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if errParse != nil || seconds <= 0 {
		return nil
	}
	resetAt := time.Unix(seconds, 0).UTC()
	if resetAt.Before(now.Add(-time.Minute)) || resetAt.After(now.Add(maxUsageResetAfter)) {
		return nil
	}
	return &resetAt
}

func parseWindowMinutes(value string) *int {
	minutes, errParse := strconv.Atoi(strings.TrimSpace(value))
	if errParse != nil || minutes <= 0 || minutes > maxUsageWindowMinutes {
		return nil
	}
	return &minutes
}

func nonNegative(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

func saturatingAdd(left, right int64) int64 {
	if right <= 0 {
		return left
	}
	if left > math.MaxInt64-right {
		return math.MaxInt64
	}
	return left + right
}

func cloneUsageAggregates(accounts map[string]usageAggregate) map[string]usageAggregate {
	cloned := make(map[string]usageAggregate, len(accounts))
	for storageKey, aggregate := range accounts {
		aggregate.Codex = cloneCodexUsage(aggregate.Codex)
		aggregate.FiveHourOverdraft = cloneOverdraftCycle(aggregate.FiveHourOverdraft)
		aggregate.SevenDayOverdraft = cloneOverdraftCycle(aggregate.SevenDayOverdraft)
		aggregate.Lifecycle = cloneAccountLifecycle(aggregate.Lifecycle)
		cloned[storageKey] = aggregate
	}
	return cloned
}

func cloneAccountLifecycle(state *accountLifecycleState) *accountLifecycleState {
	if state == nil {
		return nil
	}
	cloned := *state
	return &cloned
}

func cloneOverdraftCycle(cycle *overdraftCycleState) *overdraftCycleState {
	if cycle == nil {
		return nil
	}
	cloned := *cycle
	return &cloned
}

func cloneCodexUsage(snapshot *CodexUsageSnapshot) *CodexUsageSnapshot {
	if snapshot == nil {
		return nil
	}
	cloned := *snapshot
	cloned.FiveHour = cloneUsageWindow(snapshot.FiveHour)
	cloned.SevenDay = cloneUsageWindow(snapshot.SevenDay)
	if snapshot.ActiveResetCount != nil {
		count := *snapshot.ActiveResetCount
		cloned.ActiveResetCount = &count
	}
	return &cloned
}

func hasCodexUsageData(snapshot *CodexUsageSnapshot) bool {
	return snapshot != nil && (snapshot.FiveHour != nil || snapshot.SevenDay != nil || snapshot.PlanType != "" || snapshot.ActiveResetCount != nil || !snapshot.MetadataObservedAt.IsZero())
}

func cloneUsageWindow(window *UsageWindowSnapshot) *UsageWindowSnapshot {
	if window == nil {
		return nil
	}
	cloned := *window
	if window.ResetAt != nil {
		resetAt := window.ResetAt.UTC()
		cloned.ResetAt = &resetAt
	}
	return &cloned
}
