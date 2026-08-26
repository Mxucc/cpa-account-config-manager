package manager

import (
	"encoding/json"
	"errors"
	"fmt"
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
	usagePersistRetryDelay  = 30 * time.Second
	overdraftPrearmPercent  = 95.0
)

// Overdraft cycle states mirror the quota-overdraft evidence states used by the
// sub2api-overdraft fork: pending (cycle opened but no evidence yet), passed
// (an injected request or probe proved overdraft works), failed (an explicit
// quota 429 or exhausted probe run proved it does not for this cycle),
// inconclusive (transient evidence that neither proves nor disproves), and
// recovered (the quota window returned to 0% or was reset).
const (
	overdraftStatusPending      = "pending"
	overdraftStatusPassed       = "passed"
	overdraftStatusFailed       = "failed"
	overdraftStatusInconclusive = "inconclusive"
	overdraftStatusRecovered    = "recovered"
)

// weeklyOverdraftInjectionTTL bounds how long a successful request-interceptor
// injection is treated as evidence that a later usage record for the same
// account carried the overdraft payload. The CPA usage ABI reports AuthIndex
// without the originating RequestID, so the plugin approximates the
// sub2api-overdraft per-request injectedAccounts set with a bounded per-account
// timestamp. Two hours comfortably covers a long Codex task while keeping the
// correlation window tight enough to avoid crediting unrelated requests.
const (
	weeklyOverdraftInjectionTTL   = 2 * time.Hour
	maxTrackedOverdraftInjections = 256
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
	OverdraftStatus    string     `json:"overdraft_status,omitempty"`
	OverdraftTokens    int64      `json:"overdraft_tokens,omitempty"`
	OverdraftRequests  int64      `json:"overdraft_requests,omitempty"`
	OverdraftAmountUSD float64    `json:"overdraft_amount_usd,omitempty"`
	OverdraftRated     int64      `json:"overdraft_rated_requests,omitempty"`
	OverdraftUnrated   int64      `json:"overdraft_unrated_requests,omitempty"`
	OverdraftStartedAt *time.Time `json:"overdraft_started_at,omitempty"`
	OverdraftRecoverAt *time.Time `json:"overdraft_recover_at,omitempty"`
}

// overdraftGateState is the read-only view the request interceptor uses to
// decide whether business traffic should carry the overdraft payload.
type overdraftGateState struct {
	FiveHourUsedPercent float64
	SevenDayUsedPercent float64
	FiveHourCycleStatus string
	SevenDayCycleStatus string
	FiveHourResetAt     time.Time
	SevenDayResetAt     time.Time
	FiveHourRecoverAt   time.Time
	SevenDayRecoverAt   time.Time
	Has                 bool
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
	Active                    bool       `json:"active"`
	Status                    string     `json:"status,omitempty"`
	CycleKey                  string     `json:"cycle_key,omitempty"`
	Attempts                  int        `json:"attempts,omitempty"`
	ReasonCode                string     `json:"reason_code,omitempty"`
	VerifiedAt                *time.Time `json:"verified_at,omitempty"`
	BaselineTokens            int64      `json:"baseline_tokens,omitempty"`
	BaselineRequests          int64      `json:"baseline_requests,omitempty"`
	BaselineCreditAmountNanos int64      `json:"baseline_credit_amount_nanos,omitempty"`
	BaselineCreditRated       int64      `json:"baseline_credit_rated_requests,omitempty"`
	BaselineCreditUnrated     int64      `json:"baseline_credit_unrated_requests,omitempty"`
	StartedAt                 time.Time  `json:"started_at,omitempty"`
	RecoverAt                 time.Time  `json:"recover_at,omitempty"`
	WindowMinutes             int        `json:"window_minutes,omitempty"`
	ChangedAt                 time.Time  `json:"changed_at"`
}

type UsageTracker struct {
	mu                sync.RWMutex
	storeMu           sync.Mutex
	accounts          map[string]usageAggregate
	bindings          map[string]usageBinding
	bindingsReady     bool
	now               func() time.Time
	store             string
	durableStore      string
	allowDurable      bool
	loaded            bool
	dirty             bool
	generation        uint64
	persistDelay      time.Duration
	persistRetryDelay time.Duration
	retryScheduled    bool
	retryTimer        *time.Timer
	storageErr        string
	wake              chan struct{}
	stop              chan struct{}
	done              chan struct{}
	closeOnce         sync.Once
	creditCalculator  UsageCreditCalculator
	overdraftInjected map[string]time.Time
}

func NewUsageTracker() *UsageTracker {
	tracker := &UsageTracker{
		accounts:          make(map[string]usageAggregate),
		bindings:          make(map[string]usageBinding),
		now:               time.Now,
		persistDelay:      usagePersistDelay,
		persistRetryDelay: usagePersistRetryDelay,
		wake:              make(chan struct{}, 1),
		stop:              make(chan struct{}),
		done:              make(chan struct{}),
		overdraftInjected: make(map[string]time.Time),
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
	previousAllowDurable := t.allowDurable
	previousDurableStore := t.durableStore
	t.allowDurable = config.implicitDataDir
	if !t.allowDurable {
		t.durableStore = ""
	} else if t.durableStore != "" {
		storePath = t.durableStore
	}
	if t.loaded && t.store == storePath && t.storageErr == "" {
		t.mu.Unlock()
		return
	}
	if t.loaded && t.dirty && t.store != "" {
		if persisted, errSave := persistUsageState(t.store, t.accounts); errSave == nil {
			t.accounts = mergeUsageAggregates(t.accounts, persisted)
			t.dirty = false
			t.storageErr = ""
		} else {
			// Do not switch stores while the current in-memory state is dirty.
			// A failed write must never silently discard usage collected since
			// the last successful persistence.
			t.storageErr = "usage state could not be persisted"
			// Keep the previous durable-store selection in sync with the
			// still-active store when configuration cannot be committed.
			t.allowDurable = previousAllowDurable
			t.durableStore = previousDurableStore
			t.mu.Unlock()
			return
		}
	}
	accounts, recovered, errLoad := loadUsageStateWithBackup(storePath)
	if errLoad != nil {
		accounts = make(map[string]usageAggregate)
		if !errors.Is(errLoad, os.ErrNotExist) {
			t.storageErr = "usage state could not be loaded"
		} else {
			t.storageErr = ""
		}
	} else {
		t.storageErr = ""
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
	// UsageRecord may carry either the auth index (the primary usage binding
	// key) or the credential ID; resolve both to the same usage state and
	// overdraft injection evidence so the interceptor's selected_auth_id path
	// and the host's usage reports stay aligned.
	authIndex := strings.TrimSpace(firstNonEmpty(record.AuthIndex, record.AuthID))
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
	authIndex = t.resolveUsageAuthIndexLocked(authIndex)
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
	// Overdraft evidence from real business traffic: a successful request that
	// actually carried the overdraft payload (recent interceptor injection for
	// the same account) while its quota window is still exhausted proves the
	// overdraft works (passed), mirroring the sub2api-overdraft coordinator
	// which only calls observeBusinessSuccess for requests marked as injected.
	// An explicit quota 429 on a request that carried the overdraft payload
	// confirms the account is genuinely limited even with the overdraft
	// (failed, terminal for the same cycle), mirroring finishBusinessQuotaFailure
	// in the sub2api-overdraft fork; a 429 on a request that never carried the
	// payload only proves the plain account is limited and stays pending for
	// the probe path to decide. Transient rate-limit 429s stay inconclusive
	// and keep flowing through the normal rate-limit policy.
	if !record.Failed && t.overdraftInjectedRecently(authIndex, now) {
		if aggregate.FiveHourOverdraft != nil && aggregate.FiveHourOverdraft.Active && usageWindowStillExhausted(aggregate.Codex.FiveHour) {
			applyOverdraftCycleEvidence(&aggregate.FiveHourOverdraft, aggregate.Codex.FiveHour, 5*60, overdraftStatusPassed, "business_request_ok", now, now)
		}
		if aggregate.SevenDayOverdraft != nil && aggregate.SevenDayOverdraft.Active && usageWindowStillExhausted(aggregate.Codex.SevenDay) {
			applyOverdraftCycleEvidence(&aggregate.SevenDayOverdraft, aggregate.Codex.SevenDay, 7*24*60, overdraftStatusPassed, "business_request_ok", now, now)
		}
	} else if usageFailureIsQuotaLimited(record, now) && t.overdraftInjectedRecently(authIndex, now) {
		applyOverdraftCycleEvidence(&aggregate.FiveHourOverdraft, aggregate.Codex.FiveHour, 5*60, overdraftStatusFailed, "quota_limit_reached", now, now)
		applyOverdraftCycleEvidence(&aggregate.SevenDayOverdraft, aggregate.Codex.SevenDay, 7*24*60, overdraftStatusFailed, "quota_limit_reached", now, now)
	}
	t.accounts[storageKey] = aggregate
	t.dirty = true
	t.generation++
	t.mu.Unlock()
	t.requestPersist()
}

// usageFailureIsQuotaLimited classifies an explicit quota 429 the same way the
// sub2api-overdraft fork does: definite subscription-quota markers win, then
// transient rate-limit evidence is excluded, then usage headers and JSON quota
// evidence are considered. Ordinary transient 429s are deliberately not
// treated as quota-exhausted evidence.
func usageFailureIsQuotaLimited(record cpaapi.UsageRecord, now time.Time) bool {
	if !record.Failed || record.Failure.StatusCode != http.StatusTooManyRequests {
		return false
	}
	text := normalizedFailureText(record.Failure.Body)
	for _, marker := range []string{
		"usage_limit_reached",
		"usage limit has been reached",
		"you have reached your usage limit",
		"quota exhausted",
		"insufficient quota",
		"insufficient_quota",
		"weekly limit reached",
		"weekly_limit_reached",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	if usageFailureTextHasTransientRateLimit(text) {
		return false
	}
	var payload any
	parsedPayload := len(record.Failure.Body) > 0 && json.Unmarshal([]byte(record.Failure.Body), &payload) == nil
	if parsedPayload {
		if usageFailureJSONHasTransientRateLimitEvidence(payload, 0) {
			return false
		}
		if usageFailureJSONHasQuotaEvidence(payload, 0) {
			return true
		}
	}
	if snapshot := parseCodexUsageHeaders(record.ResponseHeaders, now); snapshot != nil {
		if snapshot.FiveHour != nil && snapshot.FiveHour.UsedPercent >= 100 ||
			snapshot.SevenDay != nil && snapshot.SevenDay.UsedPercent >= 100 {
			return true
		}
	}
	return false
}

// usageFailureJSONQuotaCode matches the explicit quota error codes the
// sub2api-overdraft fork recognizes inside a 429 JSON body, including the two
// codes (monthly limit, billing hard limit) that are only matched structurally.
func usageFailureJSONQuotaCode(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.NewReplacer("-", "_", " ", "_").Replace(value)
	switch value {
	case "usage_limit_reached", "weekly_limit_reached", "monthly_limit_reached",
		"quota_exhausted", "insufficient_quota", "billing_hard_limit_reached":
		return true
	default:
		return false
	}
}

func usageFailureJSONHasQuotaEvidence(value any, depth int) bool {
	if depth > 6 {
		return false
	}
	switch typed := value.(type) {
	case map[string]any:
		for key, raw := range typed {
			normalizedKey := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(key), "-", "_"))
			switch normalizedKey {
			case "type", "code", "reason", "error_code":
				if marker, ok := raw.(string); ok && usageFailureJSONQuotaCode(marker) {
					return true
				}
			case "limit_reached", "limitreached":
				if reached, ok := raw.(bool); ok && reached {
					return true
				}
			case "used_percent", "usedpercent":
				if used, ok := raw.(float64); ok && used >= 100 {
					return true
				}
			}
			if usageFailureJSONHasQuotaEvidence(raw, depth+1) {
				return true
			}
		}
	case []any:
		for _, item := range typed {
			if usageFailureJSONHasQuotaEvidence(item, depth+1) {
				return true
			}
		}
	case string:
		if usageFailureJSONQuotaCode(typed) {
			return true
		}
		text := strings.ToLower(strings.Join(strings.Fields(typed), " "))
		for _, marker := range []string{
			"usage limit has been reached",
			"you have reached your usage limit",
			"quota exhausted",
			"insufficient quota",
			"weekly limit reached",
		} {
			if strings.Contains(text, marker) {
				return true
			}
		}
	}
	return false
}

func usageFailureJSONHasTransientRateLimitEvidence(value any, depth int) bool {
	if depth > 6 {
		return false
	}
	switch typed := value.(type) {
	case map[string]any:
		for key, raw := range typed {
			normalizedKey := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(key), "-", "_"))
			if normalizedKey == "type" || normalizedKey == "code" || normalizedKey == "reason" || normalizedKey == "error_code" {
				if marker, ok := raw.(string); ok {
					marker = strings.NewReplacer("-", "_", " ", "_").Replace(strings.ToLower(strings.TrimSpace(marker)))
					switch marker {
					case "rate_limit_error", "rate_limit_exceeded", "too_many_requests", "request_rate_limited", "token_rate_limited":
						return true
					}
				}
			}
			if usageFailureJSONHasTransientRateLimitEvidence(raw, depth+1) {
				return true
			}
		}
	case []any:
		for _, item := range typed {
			if usageFailureJSONHasTransientRateLimitEvidence(item, depth+1) {
				return true
			}
		}
	}
	return false
}

func usageFailureTextHasTransientRateLimit(text string) bool {
	normalized := strings.NewReplacer("-", "_", " ", "_").Replace(strings.ToLower(text))
	for _, marker := range []string{
		"rate_limit_error",
		"rate_limit_exceeded",
		"too_many_requests",
		"request_rate_limited",
		"token_rate_limited",
	} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
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
			Active: true, Status: overdraftStatusPending, CycleKey: overdraftCycleKeyFor(window, fallbackMinutes, exhaustedAt),
			BaselineTokens: aggregate.SuccessfulTokens, BaselineRequests: aggregate.SuccessfulRequests,
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

// MarkOverdraftCycle records overdraft evidence for one quota window cycle.
// passed marks a successful business request or probe that carried the
// overdraft payload; failed marks an explicit quota 429 (terminal for the
// same cycle); inconclusive marks transient evidence that neither proves nor
// disproves the overdraft. The state machine mirrors the sub2api-overdraft
// probe coordinator so the interceptor and the auto-disable gate agree on the
// same cycle evidence.
func (t *UsageTracker) MarkOverdraftCycle(authIndex, quotaWindow, status, reason string, testedAt time.Time) {
	if t == nil {
		return
	}
	authIndex = safeOperationIdentifier(authIndex, 256)
	if authIndex == "" {
		return
	}
	status = normalizeOverdraftStatus(status)
	if status == "" {
		return
	}
	now := t.currentTime()
	testedAt = testedAt.UTC()
	if testedAt.IsZero() || testedAt.After(now.Add(time.Minute)) {
		testedAt = now
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
	mark := func(window *UsageWindowSnapshot, cycle **overdraftCycleState, fallbackMinutes int) {
		window = currentUsageWindow(window, aggregate.Codex.ObservedAt, testedAt)
		if window == nil {
			return
		}
		if applyOverdraftCycleEvidence(cycle, window, fallbackMinutes, status, reason, testedAt, now) {
			changed = true
		}
	}
	switch normalizeInspectionQuotaWindow(quotaWindow) {
	case InspectionQuotaWindowFiveHour, InspectionQuotaWindowFiveHourFallback:
		mark(aggregate.Codex.FiveHour, &aggregate.FiveHourOverdraft, 5*60)
	case InspectionQuotaWindowSevenDay:
		mark(aggregate.Codex.SevenDay, &aggregate.SevenDayOverdraft, 7*24*60)
	default:
		mark(aggregate.Codex.FiveHour, &aggregate.FiveHourOverdraft, 5*60)
		mark(aggregate.Codex.SevenDay, &aggregate.SevenDayOverdraft, 7*24*60)
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

func stoppedOverdraftCycle(cycle *overdraftCycleState, now time.Time) *overdraftCycleState {
	if cycle == nil || !cycle.Active {
		return cycle
	}
	// A cycle that reaches 0% is a recovered window, not a deleted one. Keeping
	// the recovered marker lets the interceptor and the UI distinguish "quota
	// recovered" from "never overdrafted" and prevents a stale active cycle
	// from being resurrected by a late usage response.
	return &overdraftCycleState{
		Active: false, Status: overdraftStatusRecovered, CycleKey: cycle.CycleKey,
		StartedAt: cycle.StartedAt, RecoverAt: cycle.RecoverAt, WindowMinutes: cycle.WindowMinutes,
		ChangedAt: now.UTC(),
	}
}

// normalizeOverdraftStatus accepts the overdraft cycle states and maps legacy
// empty statuses onto their derived values.
func normalizeOverdraftStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case overdraftStatusPending, overdraftStatusPassed, overdraftStatusFailed, overdraftStatusInconclusive, overdraftStatusRecovered:
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

// overdraftCycleKeyFor builds a stable per-window cycle key from the observed
// reset time (falling back to the estimated recovery time). The key is the
// identity used to decide whether late evidence belongs to the same quota
// cycle, mirroring the sub2api-overdraft signal keys.
func overdraftCycleKeyFor(window *UsageWindowSnapshot, fallbackMinutes int, exhaustedAt time.Time) string {
	prefix := "window"
	if fallbackMinutes >= 7*24*60 {
		prefix = "7d"
	} else if fallbackMinutes > 0 {
		prefix = "5h"
	}
	if window != nil && window.ResetAt != nil {
		return fmt.Sprintf("%s:%d", prefix, window.ResetAt.Unix())
	}
	minutes := fallbackMinutes
	if window != nil && window.WindowMinutes > 0 {
		minutes = window.WindowMinutes
	}
	return fmt.Sprintf("%s:%d", prefix, exhaustedAt.Add(time.Duration(minutes)*time.Minute).UTC().Unix())
}

// applyOverdraftCycleEvidence updates one overdraft cycle with a state
// transition. failed is terminal for the same cycle: later passed or
// inconclusive evidence cannot reopen a confirmed-failed cycle, matching the
// sub2api-overdraft probe state machine. Returns true when the cycle changed.
func applyOverdraftCycleEvidence(cycle **overdraftCycleState, window *UsageWindowSnapshot, fallbackMinutes int, status, reason string, testedAt, now time.Time) bool {
	if cycle == nil {
		return false
	}
	status = normalizeOverdraftStatus(status)
	if status == "" {
		return false
	}
	reason = strings.TrimSpace(reason)
	current := *cycle
	if current == nil {
		minutes := fallbackMinutes
		if window != nil && window.WindowMinutes > 0 {
			minutes = window.WindowMinutes
		}
		recoverAt := testedAt.Add(time.Duration(minutes) * time.Minute).UTC()
		*cycle = &overdraftCycleState{
			Active:        status != overdraftStatusFailed && status != overdraftStatusRecovered,
			Status:        status,
			CycleKey:      overdraftCycleKeyFor(window, fallbackMinutes, testedAt),
			Attempts:      1,
			ReasonCode:    reason,
			VerifiedAt:    timePointer(testedAt),
			StartedAt:     testedAt,
			RecoverAt:     recoverAt,
			WindowMinutes: minutes,
			ChangedAt:     now,
		}
		return true
	}
	if current.Status == overdraftStatusFailed && status != overdraftStatusFailed {
		return false
	}
	if current.Status == status && current.ReasonCode == reason {
		return false
	}
	current.Active = status != overdraftStatusFailed && status != overdraftStatusRecovered
	current.Status = status
	current.Attempts++
	if current.CycleKey == "" {
		current.CycleKey = overdraftCycleKeyFor(window, fallbackMinutes, testedAt)
	}
	current.ReasonCode = reason
	current.VerifiedAt = timePointer(testedAt)
	current.ChangedAt = now
	return true
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

// resolveUsageAuthIndexLocked normalizes a request-lifecycle identifier to the
// canonical usage binding key. CPA reports usage with either the auth index
// (the primary bindings key) or the credential ID (AuthID / selected_auth_id
// request metadata); the credential ID is reverse-resolved through the auth
// file bindings so both key families reach the same usage state and overdraft
// injection evidence. The caller must hold at least a read lock.
func (t *UsageTracker) resolveUsageAuthIndexLocked(identifier string) string {
	identifier = strings.TrimSpace(identifier)
	if identifier == "" {
		return ""
	}
	if _, bound := t.bindings[identifier]; bound {
		return identifier
	}
	for index, binding := range t.bindings {
		if binding.AuthID == identifier {
			return index
		}
	}
	return identifier
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
	t.closeOnce.Do(func() {
		t.mu.Lock()
		if t.retryTimer != nil {
			t.retryTimer.Stop()
			t.retryTimer = nil
		}
		t.retryScheduled = false
		t.mu.Unlock()
		close(t.stop)
	})
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
		t.mu.Lock()
		if t.store == storePath {
			t.storageErr = "usage state could not be persisted"
			// Keep dirty state and arrange a bounded retry. External events may
			// still wake the worker, but a failed write must not leave it idle
			// forever when no new usage arrives.
			if !t.retryScheduled {
				t.retryScheduled = true
				delay := t.persistRetryDelay
				if delay <= 0 {
					delay = usagePersistRetryDelay
				}
				t.retryTimer = time.AfterFunc(delay, func() {
					if t == nil {
						return
					}
					t.mu.Lock()
					t.retryScheduled = false
					t.retryTimer = nil
					t.mu.Unlock()
					t.requestPersist()
				})
			}
		}
		t.mu.Unlock()
		return
	}
	t.mu.Lock()
	if t.store == storePath {
		t.accounts = mergeUsageAggregates(t.accounts, persisted)
		t.storageErr = ""
		if t.retryTimer != nil {
			t.retryTimer.Stop()
			t.retryTimer = nil
		}
		t.retryScheduled = false
	}
	if t.generation == generation && t.store == storePath {
		t.dirty = false
	}
	t.mu.Unlock()
}

// StorageError reports a sanitized persistence failure, if any. The raw
// filesystem error is intentionally not exposed because it may contain local
// paths or other implementation details.
func (t *UsageTracker) StorageError() string {
	if t == nil {
		return ""
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.storageErr
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
	snapshot.OverdraftStatus = overdraftStatusOf(cycle)
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

func overdraftStatusOf(cycle *overdraftCycleState) string {
	if cycle == nil {
		return ""
	}
	status := strings.TrimSpace(cycle.Status)
	if status == "" {
		if cycle.Active {
			return overdraftStatusPending
		}
		return ""
	}
	return status
}

// OverdraftGateState reports the per-window usage percentages and overdraft
// cycle statuses used by the request interceptor pre-arm decision. The
// identifier may be either the CPA auth index (the primary usage binding key)
// or the credential ID carried by the selected_auth_id request metadata; the
// credential ID is reverse-resolved through the auth file bindings so both
// request-lifecycle key families reach the same usage state.
func (t *UsageTracker) OverdraftGateState(identifier string) overdraftGateState {
	var state overdraftGateState
	if t == nil {
		return state
	}
	identifier = strings.TrimSpace(identifier)
	if identifier == "" {
		return state
	}
	t.mu.RLock()
	authIndex := t.resolveUsageAuthIndexLocked(identifier)
	storageKey, identity := t.usageStorageKeyLocked(authIndex)
	if t.bindingsReady && identity == (usageIdentityFingerprint{}) {
		t.mu.RUnlock()
		return state
	}
	aggregate, exists := t.accounts[storageKey]
	t.mu.RUnlock()
	if !exists || usageIdentitiesConflict(aggregate.Identity, identity) {
		return state
	}
	state.Has = true
	if aggregate.Codex != nil {
		if window := aggregate.Codex.FiveHour; window != nil {
			state.FiveHourUsedPercent = window.UsedPercent
			if window.ResetAt != nil {
				state.FiveHourResetAt = window.ResetAt.UTC()
			}
		}
		if window := aggregate.Codex.SevenDay; window != nil {
			state.SevenDayUsedPercent = window.UsedPercent
			if window.ResetAt != nil {
				state.SevenDayResetAt = window.ResetAt.UTC()
			}
		}
	}
	state.FiveHourCycleStatus = overdraftStatusOf(aggregate.FiveHourOverdraft)
	state.SevenDayCycleStatus = overdraftStatusOf(aggregate.SevenDayOverdraft)
	if cycle := aggregate.FiveHourOverdraft; cycle != nil {
		state.FiveHourRecoverAt = cycle.RecoverAt
	}
	if cycle := aggregate.SevenDayOverdraft; cycle != nil {
		state.SevenDayRecoverAt = cycle.RecoverAt
	}
	return state
}

// NoteOverdraftInjection records that the request interceptor just appended the
// no-op exec overdraft pair to a request for the given account. The CPA usage
// ABI does not correlate UsageRecord back to its RequestID, so this bounded
// timestamp map is the evidence used to decide whether a later successful
// usage record may mark an active overdraft cycle passed. The identifier may
// be the CPA auth index or the credential ID; both resolve to the same usage
// storage key as Observe uses.
func (t *UsageTracker) NoteOverdraftInjection(authIndex string) {
	if t == nil {
		return
	}
	authIndex = strings.TrimSpace(authIndex)
	if authIndex == "" {
		return
	}
	now := t.currentTime()
	t.mu.Lock()
	defer t.mu.Unlock()
	authIndex = t.resolveUsageAuthIndexLocked(authIndex)
	storageKey, _ := t.usageStorageKeyLocked(authIndex)
	if storageKey == "" {
		return
	}
	if t.overdraftInjected == nil {
		t.overdraftInjected = make(map[string]time.Time)
	}
	if len(t.overdraftInjected) >= maxTrackedOverdraftInjections {
		oldestKey := ""
		var oldestAt time.Time
		for key, injectedAt := range t.overdraftInjected {
			if oldestKey == "" || injectedAt.Before(oldestAt) || injectedAt.Equal(oldestAt) && key < oldestKey {
				oldestKey, oldestAt = key, injectedAt
			}
		}
		if oldestKey != "" {
			delete(t.overdraftInjected, oldestKey)
		}
	}
	t.overdraftInjected[storageKey] = now
}

// overdraftInjectedRecently reports whether the account carried an overdraft
// injection recently enough for a succeeding usage record to count as passed
// evidence. The caller must hold at least a read lock: Observe evaluates this
// while already holding the write lock.
func (t *UsageTracker) overdraftInjectedRecently(authIndex string, now time.Time) bool {
	if t == nil {
		return false
	}
	authIndex = strings.TrimSpace(authIndex)
	if authIndex == "" {
		return false
	}
	storageKey, _ := t.usageStorageKeyLocked(authIndex)
	injectedAt, exists := t.overdraftInjected[storageKey]
	return exists && !injectedAt.IsZero() && now.Sub(injectedAt) <= weeklyOverdraftInjectionTTL
}

// usageWindowStillExhausted mirrors the sub2api-overdraft signal check
// (codexQuotaOverdraftSignalFromAccount) that requires the observed window to
// remain at or above 100% before business success can confirm an overdraft
// cycle. A window that dropped below 100% but did not reach 0% keeps the
// cycle open (not recovered) yet no longer counts as passed evidence.
func usageWindowStillExhausted(window *UsageWindowSnapshot) bool {
	return window != nil && window.UsedPercent >= 100
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
	cloned.VerifiedAt = cloneTimePointer(cycle.VerifiedAt)
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
