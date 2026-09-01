package manager

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	providerRuntimeStoreVersion  = 1
	providerRuntimeStoreFileName = "ai-provider-runtime.json"
	providerRuntimePersistDelay  = 500 * time.Millisecond
	providerRuntimeMaxIdentities = 10000
	providerRuntimeMaxModels     = 512
	providerRuntimeMaxEvents     = 10000
	// A missing completion callback must not leave the runtime dashboard's
	// active count (or request map) growing forever. CPA requests can be long
	// lived, so use a generous lease and prune at a lower cadence.
	providerRuntimeRequestLease  = 30 * time.Minute
	providerRuntimePruneInterval = time.Minute
)

// ProviderRuntimeSnapshot is intentionally redacted. It contains no API key,
// token, cookie, header, or provider credential material.
type ProviderRuntimeSnapshot struct {
	Provider                string               `json:"provider"`
	AuthIndex               string               `json:"auth_index,omitempty"`
	Identity                string               `json:"identity"`
	Supported               bool                 `json:"supported"`
	Reason                  string               `json:"reason,omitempty"`
	ConcurrencyConfigurable bool                 `json:"concurrency_configurable"`
	Active                  int                  `json:"active"`
	Limit                   int                  `json:"limit"`
	Limit15s                int                  `json:"limit_15s"`
	Used60s                 int                  `json:"used_60s"`
	Used15s                 int                  `json:"used_15s"`
	InputTokens             int64                `json:"input_tokens"`
	OutputTokens            int64                `json:"output_tokens"`
	ReasoningTokens         int64                `json:"reasoning_tokens"`
	CachedTokens            int64                `json:"cached_tokens"`
	TotalTokens             int64                `json:"total_tokens"`
	AmountUSD               float64              `json:"amount_usd"`
	RatedRequests           int64                `json:"rated_requests"`
	UnratedRequests         int64                `json:"unrated_requests"`
	Quota                   ProviderRuntimeQuota `json:"quota"`
	Models                  []ProviderModelUsage `json:"models,omitempty"`
	UpdatedAt               time.Time            `json:"updated_at"`
}

type ProviderRuntimeQuota struct {
	FiveHourUsedTokens int64   `json:"five_hour_used_tokens"`
	SevenDayUsedTokens int64   `json:"seven_day_used_tokens"`
	FiveHourPercent    float64 `json:"five_hour_percent,omitempty"`
	SevenDayPercent    float64 `json:"seven_day_percent,omitempty"`
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
	At     time.Time
	Tokens int64
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
	RequestEvents   []time.Time `json:"-"`
	UpdatedAt       time.Time
}

type persistedProviderRuntimeState struct {
	Version    int                                 `json:"version"`
	Aggregates map[string]providerRuntimeAggregate `json:"aggregates"`
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
	loaded        bool
	dirty         bool
	persistTimer  *time.Timer
}

func NewProviderRuntimeTracker(calculator UsageCreditCalculator) *ProviderRuntimeTracker {
	return &ProviderRuntimeTracker{
		requests:   make(map[string]providerRuntimeRequest),
		aggregates: make(map[string]*providerRuntimeAggregate),
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
	if t.loaded && t.store == path {
		return
	}
	if t.persistTimer != nil {
		t.persistTimer.Stop()
		t.persistTimer = nil
	}
	if t.loaded && t.dirty && t.store != "" {
		_ = t.persistLocked()
	}
	aggregates, errLoad := loadProviderRuntimeState(path)
	if errLoad != nil {
		// Runtime metrics are best-effort. A corrupt or older state file should
		// not prevent the plugin from loading; the next usage event will replace
		// it with a valid current-version file.
		aggregates = make(map[string]*providerRuntimeAggregate)
	}
	t.mu.Lock()
	t.requests = make(map[string]providerRuntimeRequest)
	t.aggregates = aggregates
	t.nextPrune = time.Time{}
	t.mu.Unlock()
	t.store = path
	t.loaded = true
	t.dirty = false
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
	t.mu.RUnlock()
	if errSave := saveProviderRuntimeState(t.store, aggregates); errSave != nil {
		return errSave
	}
	t.mu.Lock()
	t.dirty = false
	t.mu.Unlock()
	return nil
}

func providerRuntimeStorePath(dataDir string) string {
	return filepath.Join(dataDir, providerRuntimeStoreFileName)
}

func loadProviderRuntimeState(path string) (map[string]*providerRuntimeAggregate, error) {
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		return nil, errRead
	}
	var persisted persistedProviderRuntimeState
	if errDecode := json.Unmarshal(raw, &persisted); errDecode != nil {
		return nil, fmt.Errorf("decode provider runtime state: %w", errDecode)
	}
	if persisted.Version != providerRuntimeStoreVersion {
		return nil, fmt.Errorf("unsupported provider runtime store version %d", persisted.Version)
	}
	aggregates := make(map[string]*providerRuntimeAggregate, len(persisted.Aggregates))
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
		aggregate := value
		aggregates[key] = &aggregate
	}
	return aggregates, nil
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

func saveProviderRuntimeState(path string, aggregates map[string]*providerRuntimeAggregate) error {
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
	return savePrivateJSON(path, persistedProviderRuntimeState{Version: providerRuntimeStoreVersion, Aggregates: values})
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
	defer t.mu.Unlock()
	t.pruneExpiredLocked(now)
	aggregateKey := runtimeAggregateKey(provider, identity)
	if current, exists := t.requests[request.RequestID]; exists {
		if current.AggregateKey == aggregateKey {
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
	aggregate := t.ensureAggregateLocked(aggregateKey, identity, provider, authIndex)
	if aggregate == nil {
		return cpaapi.RequestInterceptResponse{}, false
	}
	policy, configurable := ProviderQuotaPolicy{}, false
	if t.quotaPolicies != nil {
		policy, configurable = t.quotaPolicies.ResolveProviderPolicy(provider, authIndex, identity)
	}
	aggregate.RequestEvents = pruneProviderRequestEvents(aggregate.RequestEvents, now)
	used60 := len(aggregate.RequestEvents)
	used15 := countProviderRequestEvents(aggregate.RequestEvents, now.Add(-15*time.Second), now)
	if configurable && policy.Concurrency15s != nil && *policy.Concurrency15s > 0 && used15 >= *policy.Concurrency15s {
		return providerConcurrencyRejectedResponse(*policy.Concurrency15s, 15, used15), true
	}
	if configurable && policy.Concurrency != nil && *policy.Concurrency > 0 && used60 >= *policy.Concurrency {
		return providerConcurrencyRejectedResponse(*policy.Concurrency, 60, used60), true
	}
	t.requests[request.RequestID] = providerRuntimeRequest{AggregateKey: aggregateKey, AdmittedAt: now}
	aggregate.Active++
	aggregate.RequestEvents = append(aggregate.RequestEvents, now)
	aggregate.UpdatedAt = now
	return cpaapi.RequestInterceptResponse{}, false
}

func (t *ProviderRuntimeTracker) ObserveRequest(request cpaapi.RequestInterceptRequest) {
	_, _ = t.InterceptRequest(request)
}

func providerConcurrencyRejectedResponse(limit, window, used int) cpaapi.RequestInterceptResponse {
	body, _ := json.Marshal(map[string]any{"error": map[string]any{
		"type":           "provider_concurrency_limit_reached",
		"message":        "the selected AI provider has reached its configured request limit",
		"limit":          limit,
		"used":           used,
		"window_seconds": window,
	}})
	return cpaapi.RequestInterceptResponse{
		Terminate:       true,
		StatusCode:      http.StatusTooManyRequests,
		ResponseHeaders: http.Header{"Content-Type": {"application/json"}, "Retry-After": {"1"}},
		ResponseBody:    body,
	}
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
	aggregateKey := runtimeAggregateKey(provider, identity)
	aggregate := t.ensureAggregateLocked(aggregateKey, identity, provider, authIndex)
	if aggregate == nil {
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
	if total > 0 {
		aggregate.Events = append(aggregate.Events, providerRuntimeEvent{At: now, Tokens: total})
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
		fiveHourTokens, sevenDayTokens := runtimeWindowTokens(aggregate.Events, now)
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
		used60 := len(aggregate.RequestEvents)
		used15 := countProviderRequestEvents(aggregate.RequestEvents, now.Add(-15*time.Second), now)
		limit, limit15s := 0, 0
		if configurable && policy.Concurrency != nil {
			limit = *policy.Concurrency
		}
		if configurable && policy.Concurrency15s != nil {
			limit15s = *policy.Concurrency15s
		}
		out = append(out, ProviderRuntimeSnapshot{
			Provider:                aggregate.Provider,
			AuthIndex:               aggregate.AuthIndex,
			Identity:                aggregate.Identity,
			Supported:               supported,
			Reason:                  reason,
			ConcurrencyConfigurable: configurable,
			Active:                  aggregate.Active,
			Limit:                   limit,
			Limit15s:                limit15s,
			Used60s:                 used60,
			Used15s:                 used15,
			InputTokens:             aggregate.InputTokens,
			OutputTokens:            aggregate.OutputTokens,
			ReasoningTokens:         aggregate.ReasoningTokens,
			CachedTokens:            aggregate.CachedTokens,
			TotalTokens:             aggregate.TotalTokens,
			AmountUSD:               float64(aggregate.AmountNanos) / creditNanosPerUSD,
			RatedRequests:           aggregate.RatedRequests,
			UnratedRequests:         aggregate.UnratedRequests,
			Quota: ProviderRuntimeQuota{
				FiveHourUsedTokens: fiveHourTokens,
				SevenDayUsedTokens: sevenDayTokens,
			},
			Models:    models,
			UpdatedAt: aggregate.UpdatedAt,
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

func runtimeWindowTokens(events []providerRuntimeEvent, now time.Time) (int64, int64) {
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
			sevenDay = saturatingAdd(sevenDay, event.Tokens)
		}
		if event.At.After(fiveHourCutoff) {
			fiveHour = saturatingAdd(fiveHour, event.Tokens)
		}
	}
	return fiveHour, sevenDay
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
	cutoff := now.Add(-time.Minute)
	kept := events[:0]
	for _, event := range events {
		if event.After(cutoff) && !event.After(now) {
			kept = append(kept, event)
		}
	}
	return kept
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
	provider, key := normalizeRuntimeProvider(record.Provider), strings.TrimSpace(record.APIKey)
	if provider == "" || key == "" {
		return "", ""
	}
	digest := sha256.Sum256([]byte(provider + "\x00" + key))
	return "credential:" + hex.EncodeToString(digest[:]), ""
}

func (a *App) handleAIProviderRuntime() cpaapi.ManagementResponse {
	if a == nil || a.providerRuntime == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "provider runtime metrics are unavailable"})
	}
	return jsonResponse(http.StatusOK, map[string]any{"snapshots": a.providerRuntime.Snapshot(), "updated_at": time.Now().UTC()})
}
