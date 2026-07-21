package manager

import (
	"math"
	"net/http"
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
	usagePersistDelay       = 2 * time.Second
)

type AccountUsageSnapshot struct {
	InputTokens         int64               `json:"input_tokens"`
	OutputTokens        int64               `json:"output_tokens"`
	ReasoningTokens     int64               `json:"reasoning_tokens"`
	CachedTokens        int64               `json:"cached_tokens"`
	CacheReadTokens     int64               `json:"cache_read_tokens"`
	CacheCreationTokens int64               `json:"cache_creation_tokens"`
	TotalTokens         int64               `json:"total_tokens"`
	LastRequestAt       *time.Time          `json:"last_request_at,omitempty"`
	UpdatedAt           *time.Time          `json:"updated_at,omitempty"`
	Codex               *CodexUsageSnapshot `json:"codex,omitempty"`
}

type CodexUsageSnapshot struct {
	FiveHour   *UsageWindowSnapshot `json:"five_hour,omitempty"`
	SevenDay   *UsageWindowSnapshot `json:"seven_day,omitempty"`
	ObservedAt time.Time            `json:"observed_at"`
}

type UsageWindowSnapshot struct {
	UsedPercent   float64    `json:"used_percent"`
	ResetAt       *time.Time `json:"reset_at,omitempty"`
	WindowMinutes int        `json:"window_minutes,omitempty"`
}

type usageAggregate struct {
	InputTokens         int64               `json:"input_tokens"`
	OutputTokens        int64               `json:"output_tokens"`
	ReasoningTokens     int64               `json:"reasoning_tokens"`
	CachedTokens        int64               `json:"cached_tokens"`
	CacheReadTokens     int64               `json:"cache_read_tokens"`
	CacheCreationTokens int64               `json:"cache_creation_tokens"`
	TotalTokens         int64               `json:"total_tokens"`
	LastRequestAt       time.Time           `json:"last_request_at,omitempty"`
	UpdatedAt           time.Time           `json:"updated_at,omitempty"`
	Codex               *CodexUsageSnapshot `json:"codex,omitempty"`
}

type UsageTracker struct {
	mu           sync.RWMutex
	accounts     map[string]usageAggregate
	now          func() time.Time
	store        string
	loaded       bool
	dirty        bool
	generation   uint64
	persistDelay time.Duration
	wake         chan struct{}
	stop         chan struct{}
	done         chan struct{}
	closeOnce    sync.Once
}

func NewUsageTracker() *UsageTracker {
	tracker := &UsageTracker{
		accounts:     make(map[string]usageAggregate),
		now:          time.Now,
		persistDelay: usagePersistDelay,
		wake:         make(chan struct{}, 1),
		stop:         make(chan struct{}),
		done:         make(chan struct{}),
	}
	go tracker.run()
	return tracker
}

func (t *UsageTracker) Configure(config Config) {
	if t == nil {
		return
	}
	config = normalizeConfig(config)
	storePath := usageStorePath(config.DataDir)

	t.mu.Lock()
	defer t.mu.Unlock()
	if t.loaded && t.store == storePath {
		return
	}
	if t.loaded && t.dirty && t.store != "" {
		_ = saveUsageState(t.store, t.accounts)
	}
	accounts, errLoad := loadUsageState(storePath)
	if errLoad != nil {
		accounts = make(map[string]usageAggregate)
	}
	t.accounts = accounts
	t.store = storePath
	t.loaded = true
	t.dirty = false
	t.generation++
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
	requestedAt := record.RequestedAt.UTC()
	if requestedAt.IsZero() || requestedAt.After(now.Add(24*time.Hour)) {
		requestedAt = now
	}

	t.mu.Lock()
	if _, exists := t.accounts[authIndex]; !exists && len(t.accounts) >= maxUsageAccounts {
		t.evictOldestLocked()
	}
	aggregate := t.accounts[authIndex]
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
	if aggregate.LastRequestAt.IsZero() || requestedAt.After(aggregate.LastRequestAt) {
		aggregate.LastRequestAt = requestedAt
	}
	aggregate.UpdatedAt = now
	if codex := parseCodexUsageHeaders(record.ResponseHeaders, now); codex != nil {
		if aggregate.Codex == nil {
			aggregate.Codex = &CodexUsageSnapshot{}
		}
		if codex.FiveHour != nil {
			aggregate.Codex.FiveHour = cloneUsageWindow(codex.FiveHour)
		}
		if codex.SevenDay != nil {
			aggregate.Codex.SevenDay = cloneUsageWindow(codex.SevenDay)
		}
		aggregate.Codex.ObservedAt = codex.ObservedAt
	}
	t.accounts[authIndex] = aggregate
	t.dirty = true
	t.generation++
	t.mu.Unlock()
	t.requestPersist()
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
	aggregate, exists := t.accounts[authIndex]
	t.mu.RUnlock()
	if !exists {
		return nil
	}
	return publicUsageSnapshot(aggregate, t.currentTime())
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
	for authIndex, aggregate := range t.accounts {
		candidate := aggregate.UpdatedAt
		if candidate.IsZero() {
			candidate = aggregate.LastRequestAt
		}
		if oldestKey == "" || candidate.Before(oldest) || candidate.Equal(oldest) && authIndex < oldestKey {
			oldestKey = authIndex
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
	t.mu.RLock()
	if !t.dirty || t.store == "" {
		t.mu.RUnlock()
		return
	}
	storePath := t.store
	generation := t.generation
	accounts := cloneUsageAggregates(t.accounts)
	t.mu.RUnlock()
	if errSave := saveUsageState(storePath, accounts); errSave != nil {
		return
	}
	t.mu.Lock()
	if t.generation == generation && t.store == storePath {
		t.dirty = false
	}
	t.mu.Unlock()
}

func publicUsageSnapshot(aggregate usageAggregate, now time.Time) *AccountUsageSnapshot {
	codex := cloneCodexUsage(aggregate.Codex)
	if codex != nil {
		codex.FiveHour = currentUsageWindow(codex.FiveHour, codex.ObservedAt, now)
		codex.SevenDay = currentUsageWindow(codex.SevenDay, codex.ObservedAt, now)
		if codex.FiveHour == nil && codex.SevenDay == nil {
			codex = nil
		}
	}
	if aggregate.InputTokens == 0 && aggregate.OutputTokens == 0 && aggregate.ReasoningTokens == 0 &&
		aggregate.CachedTokens == 0 && aggregate.CacheReadTokens == 0 && aggregate.CacheCreationTokens == 0 &&
		aggregate.TotalTokens == 0 && aggregate.LastRequestAt.IsZero() && codex == nil {
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
	for authIndex, aggregate := range accounts {
		aggregate.Codex = cloneCodexUsage(aggregate.Codex)
		cloned[authIndex] = aggregate
	}
	return cloned
}

func cloneCodexUsage(snapshot *CodexUsageSnapshot) *CodexUsageSnapshot {
	if snapshot == nil {
		return nil
	}
	cloned := *snapshot
	cloned.FiveHour = cloneUsageWindow(snapshot.FiveHour)
	cloned.SevenDay = cloneUsageWindow(snapshot.SevenDay)
	return &cloned
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
