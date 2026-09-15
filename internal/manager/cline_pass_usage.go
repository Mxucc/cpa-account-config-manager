package manager

import (
	"math"
	"strings"
	"sync"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

// Cline Pass traffic reaches the plugin through the same CPA usage callback the
// provider runtime dashboard already consumes, so the reference-priced quota
// windows are fed from that existing plumbing instead of a new observer. The
// events are kept in memory only: they hold token counts and reference-priced USD
// amounts, never a credential, and nothing here is persisted or logged.
const (
	// clinePassUsageMaxEventsPerKey bounds the retained reference-priced events of
	// one ledger key. The newest events are kept, and events no documented window
	// can reach are dropped as they age out.
	clinePassUsageMaxEventsPerKey = 2048

	// clinePassUsageMaxKeys bounds the tracked ledger keys, so traffic that cannot
	// be attributed to a stored account can never grow the ledger without bound.
	clinePassUsageMaxKeys = 128

	// clinePassUsageMaxFutureSkew bounds how far a usage record's own timestamp may
	// run ahead of the local clock before the record is stamped with the local time
	// instead, mirroring how the account usage tracker treats a skewed timestamp.
	clinePassUsageMaxFutureSkew = 24 * time.Hour

	// clinePassUsageUSDPrecision is the resolution the published USD amounts are
	// rounded to (one microdollar), so a window never shows float noise like
	// 1.4000000000000001.
	clinePassUsageUSDPrecision = 1e-6
)

// clinePassChannelKind is the CPA channel kind this plugin writes for a Cline Pass
// credential. It is what decides the runtime provider name CPA reports for the
// channel's usage callbacks.
const clinePassChannelKind = "openai-compatibility"

// clinePassChannelCredentialIdentity is the credential identity CPA's usage
// callback reports for the Cline Pass channel of one credential. Only the one-way
// digest is ever compared or stored: a credential value never enters the ledger.
func clinePassChannelCredentialIdentity(accessToken string) string {
	return aiProviderRuntimeCredentialIdentity(aiProviderRuntimeProviderName(clinePassChannelKind), strings.TrimSpace(accessToken))
}

// ClinePassQuotaWindowUsage is the reference-priced usage of one Cline Pass account
// over one documented window. InputTokens counts the uncached input; the cache
// fields carry the cached halves, so input + cache read + cache write is the prompt
// total the gateway measures. USD is reference-priced list value, not a charge.
type ClinePassQuotaWindowUsage struct {
	USD              float64 `json:"usd"`
	InputTokens      int64   `json:"input_tokens"`
	OutputTokens     int64   `json:"output_tokens"`
	CacheReadTokens  int64   `json:"cache_read_tokens"`
	CacheWriteTokens int64   `json:"cache_write_tokens"`
	Requests         int64   `json:"requests"`
	// UnpricedRequests counts the requests of a model the documentation does not
	// price. Their tokens are still reported, but they add no reference value.
	UnpricedRequests int64 `json:"unpriced_requests"`
}

// ClinePassQuotaUsage is the additive, credential-free quota block of one account
// view. Every amount is reference-priced list value rather than a charge, which is
// what Reference reports; MonthlySubscriptionUSD carries the documented flat
// subscription price so the UI can compare the two.
type ClinePassQuotaUsage struct {
	FiveHour               ClinePassQuotaWindowUsage `json:"five_hour"`
	Weekly                 ClinePassQuotaWindowUsage `json:"weekly"`
	Monthly                ClinePassQuotaWindowUsage `json:"monthly"`
	MonthlySubscriptionUSD float64                   `json:"monthly_subscription_usd"`
	Reference              bool                      `json:"reference"`
}

// clinePassUsageEvent is one retained reference-priced request.
type clinePassUsageEvent struct {
	At               time.Time
	USD              float64
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	Priced           bool
}

// clinePassUsageLedger keeps the reference-priced events of the documented windows
// in memory, keyed by the account id or, for traffic whose account row is not
// stored yet, by the bound channel's credential identity. A nil ledger is valid
// and reports empty windows, so an incompletely constructed service stays safe.
type clinePassUsageLedger struct {
	mu     sync.Mutex
	events map[string][]clinePassUsageEvent
}

func newClinePassUsageLedger() *clinePassUsageLedger {
	return &clinePassUsageLedger{events: map[string][]clinePassUsageEvent{}}
}

// observe appends one priced event and drops what no documented window can reach
// any more.
func (l *clinePassUsageLedger) observe(now time.Time, key string, event clinePassUsageEvent) {
	if l == nil || strings.TrimSpace(key) == "" {
		return
	}
	if event.At.IsZero() {
		event.At = now
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.events == nil {
		l.events = map[string][]clinePassUsageEvent{}
	}
	if _, exists := l.events[key]; !exists && len(l.events) >= clinePassUsageMaxKeys {
		l.evictOldestKeyLocked()
	}
	cutoff := clinePassWindowCutoff(now)
	kept := make([]clinePassUsageEvent, 0, len(l.events[key])+1)
	for _, candidate := range l.events[key] {
		if !candidate.At.Before(cutoff) {
			kept = append(kept, candidate)
		}
	}
	if len(kept) >= clinePassUsageMaxEventsPerKey {
		kept = kept[len(kept)-clinePassUsageMaxEventsPerKey+1:]
	}
	l.events[key] = append(kept, event)
}

// usage aggregates the retained events of the supplied ledger keys over the three
// documented windows. The keys are read at one instant so the windows agree.
func (l *clinePassUsageLedger) usage(now time.Time, keys ...string) ClinePassQuotaUsage {
	usage := ClinePassQuotaUsage{MonthlySubscriptionUSD: clinePassMonthlySubscriptionUSD, Reference: true}
	if l == nil || len(keys) == 0 {
		return usage
	}
	rollingCutoff := now.Add(-clinePassRollingWindow)
	weekStart := clinePassWeekStart(now)
	monthStart := clinePassMonthStart(now)
	l.mu.Lock()
	defer l.mu.Unlock()
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		for _, event := range l.events[key] {
			if !event.At.Before(rollingCutoff) {
				addClinePassUsageEvent(&usage.FiveHour, event)
			}
			if !event.At.Before(weekStart) {
				addClinePassUsageEvent(&usage.Weekly, event)
			}
			if !event.At.Before(monthStart) {
				addClinePassUsageEvent(&usage.Monthly, event)
			}
		}
	}
	return usage
}

// evictOldestKeyLocked drops the ledger key whose newest event is the oldest, so
// unattributable traffic stays bounded without discarding the active account.
func (l *clinePassUsageLedger) evictOldestKeyLocked() {
	oldestKey := ""
	var oldestAt time.Time
	for key, events := range l.events {
		newest := time.Time{}
		for _, event := range events {
			if event.At.After(newest) {
				newest = event.At
			}
		}
		if oldestKey == "" || newest.Before(oldestAt) {
			oldestKey, oldestAt = key, newest
		}
	}
	if oldestKey != "" {
		delete(l.events, oldestKey)
	}
}

// clinePassWindowCutoff is the oldest instant any documented window can still
// reach, so an event before it can never be reported again. The three windows are
// independent boundaries, and the earliest of them wins: a five-hour-old event at
// the start of a month belongs to the rolling window even though the calendar month
// has just rolled over.
func clinePassWindowCutoff(now time.Time) time.Time {
	cutoff := now.Add(-clinePassRollingWindow)
	for _, boundary := range []time.Time{clinePassWeekStart(now), clinePassMonthStart(now)} {
		if boundary.Before(cutoff) {
			cutoff = boundary
		}
	}
	return cutoff
}

// addClinePassUsageEvent folds one event into a window bucket. Tokens saturate
// instead of overflowing, and the USD amount is rounded to the published
// resolution so a window never shows float noise.
func addClinePassUsageEvent(target *ClinePassQuotaWindowUsage, event clinePassUsageEvent) {
	if target == nil {
		return
	}
	target.InputTokens = saturatingAdd(target.InputTokens, event.InputTokens)
	target.OutputTokens = saturatingAdd(target.OutputTokens, event.OutputTokens)
	target.CacheReadTokens = saturatingAdd(target.CacheReadTokens, event.CacheReadTokens)
	target.CacheWriteTokens = saturatingAdd(target.CacheWriteTokens, event.CacheWriteTokens)
	target.Requests++
	if event.Priced {
		target.USD = roundClinePassUSD(target.USD + event.USD)
		return
	}
	target.UnpricedRequests++
}

func roundClinePassUSD(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	if value <= 0 {
		return 0
	}
	return math.Round(value/clinePassUsageUSDPrecision) * clinePassUsageUSDPrecision
}

// ObserveUsage prices one usage record at the documented Cline Pass reference rates
// and adds it to the account's documented windows. The record arrives on the CPA
// usage callback the plugin already consumes, so no additional observer or host
// callback is registered. A record that is not Cline Pass traffic is ignored, and
// only token counts and reference-priced amounts are retained: no credential value
// is stored, logged or returned.
func (s *ClinePassService) ObserveUsage(record cpaapi.UsageRecord) {
	if s == nil {
		return
	}
	key, ok := s.clinePassUsageKey(record)
	if !ok {
		return
	}
	now := s.now().UTC()
	detail := record.Detail
	input := nonNegative(detail.InputTokens)
	cacheRead := nonNegative(detail.CacheReadTokens)
	if cacheRead == 0 {
		cacheRead = nonNegative(detail.CachedTokens)
	}
	cacheWrite := nonNegative(detail.CacheCreationTokens)
	uncachedInput := input - cacheRead - cacheWrite
	if uncachedInput < 0 {
		uncachedInput = 0
	}
	output := nonNegative(detail.OutputTokens)
	at := record.RequestedAt.UTC()
	if at.IsZero() || at.After(now.Add(clinePassUsageMaxFutureSkew)) {
		at = now
	}
	usd, priced := clinePassReferenceCostAt(firstNonEmpty(record.Model, record.Alias), uncachedInput, output, cacheRead, cacheWrite, at)
	s.usage.observe(now, key, clinePassUsageEvent{
		At:               at,
		USD:              usd,
		InputTokens:      uncachedInput,
		OutputTokens:     output,
		CacheReadTokens:  cacheRead,
		CacheWriteTokens: cacheWrite,
		Priced:           priced,
	})
}

// clinePassUsageKey resolves the ledger key of one usage record. A record whose
// credential is the token of a stored account is keyed by that account id, which is
// the primary identity. A record that names a published Cline Pass model but whose
// account row is not stored is keyed by the bound channel's credential identity
// instead, so the traffic is recorded under the identity CPA reports for the
// channel and attaches to the account as soon as it is stored. Anything else is not
// Cline Pass traffic and is ignored.
func (s *ClinePassService) clinePassUsageKey(record cpaapi.UsageRecord) (string, bool) {
	identity := runtimeCredentialIdentity(record)
	token := strings.TrimSpace(record.APIKey)
	s.mu.RLock()
	for _, account := range s.accounts {
		if identity != "" && identity == clinePassChannelCredentialIdentity(account.AccessToken) {
			s.mu.RUnlock()
			return account.ID, true
		}
		// A host that reports a different provider string for the same channel still
		// presents the channel credential, so the raw token is the fallback match.
		if token != "" && token == strings.TrimSpace(account.AccessToken) {
			s.mu.RUnlock()
			return account.ID, true
		}
	}
	s.mu.RUnlock()
	if identity != "" && clinePassIsPublishedModel(firstNonEmpty(record.Model, record.Alias)) {
		return identity, true
	}
	return "", false
}

// clinePassQuotaUsageLocked builds the additive quota block of one account view.
// The account id and the account's channel credential identity are both read, so a
// record observed before the account was stored still shows up. The caller must
// hold at least a read lock, so the stored token cannot change while its identity
// is derived.
func (s *ClinePassService) clinePassQuotaUsageLocked(account ClinePassAccount) ClinePassQuotaUsage {
	keys := []string{account.ID}
	if identity := clinePassChannelCredentialIdentity(account.AccessToken); identity != "" && identity != account.ID {
		keys = append(keys, identity)
	}
	return s.usage.usage(s.now().UTC(), keys...)
}
