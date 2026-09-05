package manager

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

const (
	accountConcurrencyStoreVersion         = 1
	MaxAccountConcurrencyLimit             = 1000
	accountConcurrencyLeaseTTL             = 24 * time.Hour
	accountConcurrencyPruneInterval        = time.Minute
	accountConcurrencyMaxWait              = 60 * time.Second
	accountConcurrencyMaxWaitersAccount    = 100
	accountConcurrencyMaxWaitersTotal      = 1000
	accountConcurrencyTombstoneTTL         = 2 * time.Minute
	accountConcurrencySchedulerReserveTTL  = 10 * time.Second
	DefaultAccountConcurrencyWindowSeconds = 15
	MinAccountConcurrencyWindowSeconds     = 1
	MaxAccountConcurrencyWindowSeconds     = 3600
	selectedAuthMetadataKey                = "selected_auth_id"
)

var (
	ErrAccountConcurrencyUnsupported = errors.New("account concurrency requires CPA request lifecycle schema v2")
	ErrAccountConcurrencyIdentity    = errors.New("account has no stable CPA auth identity")
)

type AccountConcurrencyAvailability struct {
	Supported             bool   `json:"supported"`
	HostSchemaVersion     uint32 `json:"host_schema_version"`
	RequiredSchemaVersion uint32 `json:"required_schema_version"`
	Reason                string `json:"reason,omitempty"`
	StorageError          string `json:"storage_error,omitempty"`
}

type AccountConcurrencySummary struct {
	// Limit is the maximum number of requests that may be in flight at once.
	Limit int `json:"limit"`
	// RequestLimit is the maximum number of requests admitted in the configured window.
	RequestLimit         int  `json:"request_limit"`
	RequestWindowSeconds int  `json:"request_window_seconds"`
	UsedRequests         int  `json:"used_requests"`
	Active               int  `json:"active"`
	Waiting              int  `json:"waiting"`
	Supported            bool `json:"supported"`

	// Deprecated aliases are retained for older clients and persisted policy code.
	FifteenSecLimit int `json:"limit_15s,omitempty"`
	Used60s         int `json:"used_60s,omitempty"`
	Used15s         int `json:"used_15s,omitempty"`
}

type accountConcurrencyRecord struct {
	AuthID        string `json:"auth_id"`
	AccountID     string `json:"account_id,omitempty"`
	Limit         int    `json:"limit"`
	Limit15s      int    `json:"limit_15s,omitempty"`
	WindowSeconds int    `json:"window_seconds,omitempty"`
}

type persistedAccountConcurrency struct {
	Version int                        `json:"version"`
	Limits  []accountConcurrencyRecord `json:"limits,omitempty"`
}

type accountConcurrencyAdmission struct {
	AuthID     string
	AdmittedAt time.Time
}

type AccountConcurrencyService struct {
	mu              sync.Mutex
	store           string
	loaded          bool
	loadFailed      bool
	storageErr      string
	hostSchema      uint32
	limits          map[string]accountConcurrencyRecord
	active          map[string]int
	requests        map[string]accountConcurrencyAdmission
	events          map[string][]time.Time
	waiting         map[string]int
	waitingRequests map[string]string
	canceled        map[string]time.Time
	reservations    map[string][]time.Time
	schedulerCursor uint64
	wake            chan struct{}
	epoch           uint64
	shuttingDown    bool
	now             func() time.Time
	maxWait         time.Duration
	maxWaiters      int
	maxWaitersTotal int
	tombstoneTTL    time.Duration
	nextPrune       time.Time
	activeGate      atomic.Bool
}

func NewAccountConcurrencyService() *AccountConcurrencyService {
	return &AccountConcurrencyService{
		hostSchema:      cpaapi.SchemaVersion,
		limits:          make(map[string]accountConcurrencyRecord),
		active:          make(map[string]int),
		requests:        make(map[string]accountConcurrencyAdmission),
		events:          make(map[string][]time.Time),
		waiting:         make(map[string]int),
		waitingRequests: make(map[string]string),
		canceled:        make(map[string]time.Time),
		reservations:    make(map[string][]time.Time),
		wake:            make(chan struct{}),
		now:             time.Now,
		maxWait:         accountConcurrencyMaxWait,
		maxWaiters:      accountConcurrencyMaxWaitersAccount,
		maxWaitersTotal: accountConcurrencyMaxWaitersTotal,
		tombstoneTTL:    accountConcurrencyTombstoneTTL,
	}
}

func accountConcurrencyStorePath(dataDir string) string {
	return filepath.Join(dataDir, "account-concurrency.json")
}

func (s *AccountConcurrencyService) Configure(config Config, hostSchema uint32) {
	if s == nil {
		return
	}
	config = normalizeConfig(config)
	hostSchema = normalizeHostSchemaVersion(hostSchema)
	storePath := accountConcurrencyStorePath(config.DataDir)
	s.mu.Lock()
	previousSchema := s.hostSchema
	s.hostSchema = hostSchema
	sameStore := s.loaded && s.store == storePath
	// A service instance can survive a CPA/plugin reconfiguration.  Admission
	// leases belong to the request-lifecycle instance that created them; when
	// the backing store or host schema changes, carrying those leases forward
	// makes the UI report phantom active requests and can reject new traffic.
	if (s.loaded && !sameStore) || previousSchema != hostSchema {
		s.active = make(map[string]int)
		s.requests = make(map[string]accountConcurrencyAdmission)
		s.events = make(map[string][]time.Time)
		s.reservations = make(map[string][]time.Time)
		s.cancelWaitersLocked()
		s.nextPrune = time.Time{}
	}
	s.shuttingDown = false
	if sameStore && !s.loadFailed {
		s.updateActiveGateLocked()
		s.mu.Unlock()
		return
	}
	s.store = storePath
	s.loaded = true
	loaded, errLoad := loadAccountConcurrency(storePath)
	if errLoad != nil {
		if !sameStore {
			s.limits = make(map[string]accountConcurrencyRecord)
		}
		s.loadFailed = !errors.Is(errLoad, os.ErrNotExist)
		if s.loadFailed {
			s.storageErr = "account concurrency state could not be loaded"
		} else {
			s.storageErr = ""
		}
		s.updateActiveGateLocked()
		s.mu.Unlock()
		return
	}
	s.limits = loaded
	s.loadFailed = false
	s.storageErr = ""
	s.updateActiveGateLocked()
	s.mu.Unlock()
}

func normalizeHostSchemaVersion(version uint32) uint32 {
	if version == 0 {
		return cpaapi.LegacySchemaVersion
	}
	return version
}

func (s *AccountConcurrencyService) Availability() AccountConcurrencyAvailability {
	availability := AccountConcurrencyAvailability{RequiredSchemaVersion: cpaapi.SchemaVersion}
	if s == nil {
		availability.HostSchemaVersion = cpaapi.LegacySchemaVersion
		availability.Reason = "host_schema_v2_required"
		return availability
	}
	s.mu.Lock()
	availability.HostSchemaVersion = s.hostSchema
	availability.Supported = s.hostSchema >= cpaapi.SchemaVersion
	availability.StorageError = s.storageErr
	s.mu.Unlock()
	if !availability.Supported {
		availability.Reason = "host_schema_v2_required"
	}
	return availability
}

func (s *AccountConcurrencyService) Summary(authID string) AccountConcurrencySummary {
	if s == nil {
		return AccountConcurrencySummary{}
	}
	authID = strings.TrimSpace(authID)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneExpiredLocked(s.now().UTC())
	record := s.limits[authID]
	now := s.now().UTC()
	windowSeconds := normalizeAccountConcurrencyWindowSeconds(record.WindowSeconds)
	usedRequests := s.windowUsageLocked(authID, now, time.Duration(windowSeconds)*time.Second)
	return AccountConcurrencySummary{Supported: s.hostSchema >= cpaapi.SchemaVersion, Limit: record.Limit, RequestLimit: record.Limit15s, RequestWindowSeconds: windowSeconds, UsedRequests: usedRequests, Active: s.active[authID], Waiting: s.waiting[authID], FifteenSecLimit: record.Limit15s, Used60s: usedRequests, Used15s: usedRequests}
}

func (s *AccountConcurrencyService) SetLimit(account Account, limit int) error {
	return s.SetLimits(account, &limit, nil)
}

// SetLimits updates the simultaneous in-flight limit and the request-window limit.
// A nil value preserves that setting; zero clears it.
func (s *AccountConcurrencyService) SetLimits(account Account, concurrencyLimit, requestLimit *int) error {
	return s.setConfiguration(account, concurrencyLimit, requestLimit, nil)
}

// SetRequestWindowSeconds changes the rolling request window without changing its limit.
func (s *AccountConcurrencyService) SetRequestWindowSeconds(account Account, seconds int) error {
	return s.setConfiguration(account, nil, nil, &seconds)
}

func (s *AccountConcurrencyService) setConfiguration(account Account, concurrencyLimit, requestLimit, windowSeconds *int) error {
	if s == nil {
		return ErrAccountConcurrencyUnsupported
	}
	if concurrencyLimit != nil && (*concurrencyLimit < 0 || *concurrencyLimit > MaxAccountConcurrencyLimit) {
		return fmt.Errorf("account concurrency must be between 0 and %d", MaxAccountConcurrencyLimit)
	}
	if requestLimit != nil && (*requestLimit < 0 || *requestLimit > MaxAccountConcurrencyLimit) {
		return fmt.Errorf("account request limit must be between 0 and %d", MaxAccountConcurrencyLimit)
	}
	if windowSeconds != nil && !validAccountConcurrencyWindowSeconds(*windowSeconds) {
		return fmt.Errorf("account request window must be between %d and %d seconds", MinAccountConcurrencyWindowSeconds, MaxAccountConcurrencyWindowSeconds)
	}
	authID := strings.TrimSpace(account.AuthID)
	if authID == "" || len(authID) > 4096 {
		return ErrAccountConcurrencyIdentity
	}
	accountID := strings.TrimSpace(account.ID)
	if len(accountID) > maxAccountConfigIDLength {
		accountID = ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hostSchema < cpaapi.SchemaVersion {
		return ErrAccountConcurrencyUnsupported
	}
	next := cloneAccountConcurrencyRecords(s.limits)
	current := next[authID]
	current.AuthID, current.AccountID = authID, accountID
	if concurrencyLimit != nil {
		current.Limit = *concurrencyLimit
	}
	if requestLimit != nil {
		current.Limit15s = *requestLimit
	}
	if windowSeconds != nil {
		current.WindowSeconds = *windowSeconds
	}
	if current.Limit == 0 && current.Limit15s == 0 {
		delete(next, authID)
	} else {
		current.WindowSeconds = normalizeAccountConcurrencyWindowSeconds(current.WindowSeconds)
		next[authID] = current
	}
	if errSave := saveAccountConcurrency(s.store, next); errSave != nil {
		return fmt.Errorf("persist account concurrency: %w", errSave)
	}
	s.limits = next
	s.loadFailed = false
	s.storageErr = ""
	s.updateActiveGateLocked()
	s.broadcastLocked()
	return nil
}

func (s *AccountConcurrencyService) RequestInterceptionActive() bool {
	if s == nil {
		return false
	}
	return s.activeGate.Load()
}

func (s *AccountConcurrencyService) RequestInterceptionAcceptsFormat(string) bool {
	return s.RequestInterceptionActive()
}

type accountSchedulerCandidateLoad struct {
	authID   string
	pressure int64
	load     int
}

// PickAuth balances every multi-account pool whose candidates all have plugin-managed
// limits. It reserves the selected account immediately so a burst of scheduler calls
// cannot all observe an idle pool and fall through to the same sticky credential before
// request.intercept_after has recorded the first admission.
func (s *AccountConcurrencyService) PickAuth(request cpaapi.SchedulerPickRequest) cpaapi.SchedulerPickResponse {
	if s == nil || len(request.Candidates) < 2 {
		return cpaapi.SchedulerPickResponse{}
	}
	now := s.now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hostSchema < cpaapi.SchemaVersion || s.shuttingDown {
		return cpaapi.SchedulerPickResponse{}
	}
	s.pruneExpiredLocked(now)
	s.pruneSchedulerReservationsLocked(now)

	loads := make([]accountSchedulerCandidateLoad, 0, len(request.Candidates))
	seen := make(map[string]struct{}, len(request.Candidates))
	for _, candidate := range request.Candidates {
		authID := strings.TrimSpace(candidate.ID)
		if authID == "" {
			continue
		}
		if _, duplicate := seen[authID]; duplicate {
			continue
		}
		seen[authID] = struct{}{}
		record, managed := s.limits[authID]
		if !managed || record.Limit <= 0 && record.Limit15s <= 0 {
			// Mixed managed/unmanaged pools retain the host selector. The plugin has
			// no declared capacity contract for an unmanaged credential and must not
			// silently turn it into the preferred overflow destination.
			return cpaapi.SchedulerPickResponse{}
		}
		reserved := len(s.reservations[authID])
		activeLoad := s.active[authID] + s.waiting[authID] + reserved
		windowSeconds := normalizeAccountConcurrencyWindowSeconds(record.WindowSeconds)
		requestLoad := s.windowUsageLocked(authID, now, time.Duration(windowSeconds)*time.Second) + reserved
		load := activeLoad + requestLoad
		var pressure int64
		if record.Limit > 0 {
			pressure = int64(activeLoad) * 1_000_000 / int64(record.Limit)
		}
		if record.Limit15s > 0 {
			requestPressure := int64(requestLoad) * 1_000_000 / int64(record.Limit15s)
			if requestPressure > pressure {
				pressure = requestPressure
			}
		}
		loads = append(loads, accountSchedulerCandidateLoad{authID: authID, pressure: pressure, load: load})
	}
	if len(loads) < 2 {
		return cpaapi.SchedulerPickResponse{}
	}

	bestPressure := loads[0].pressure
	bestLoad := loads[0].load
	best := make([]string, 0, len(loads))
	for _, candidate := range loads {
		if candidate.pressure < bestPressure || candidate.pressure == bestPressure && candidate.load < bestLoad {
			bestPressure = candidate.pressure
			bestLoad = candidate.load
			best = best[:0]
		}
		if candidate.pressure == bestPressure && candidate.load == bestLoad {
			best = append(best, candidate.authID)
		}
	}
	if len(best) == 0 {
		return cpaapi.SchedulerPickResponse{}
	}
	selected := best[int(s.schedulerCursor%uint64(len(best)))]
	s.schedulerCursor++
	s.reservations[selected] = append(s.reservations[selected], now)
	return cpaapi.SchedulerPickResponse{AuthID: selected, Handled: true}
}

func (s *AccountConcurrencyService) InterceptRequest(request cpaapi.RequestInterceptRequest) (cpaapi.RequestInterceptResponse, bool) {
	if s == nil {
		return cpaapi.RequestInterceptResponse{}, false
	}
	requestID := strings.TrimSpace(request.RequestID)
	if requestID == "" {
		return cpaapi.RequestInterceptResponse{}, false
	}
	authID, _ := request.Metadata[selectedAuthMetadataKey].(string)
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return cpaapi.RequestInterceptResponse{}, false
	}

	deadline := s.now().UTC().Add(s.maxWait)
	registered := false
	reservationConsumed := false
	var waitEpoch uint64
	for {
		now := s.now().UTC()
		s.mu.Lock()
		s.pruneExpiredLocked(now)

		if _, canceled := s.canceled[requestID]; canceled {
			delete(s.canceled, requestID)
			if registered {
				s.removeWaiterLocked(requestID, authID)
			}
			s.mu.Unlock()
			return accountConcurrencyUnavailableResponse("account_concurrency_wait_canceled", "the request completed before account admission", time.Second), true
		}
		if s.shuttingDown || (registered && waitEpoch != s.epoch) {
			if registered {
				s.removeWaiterLocked(requestID, authID)
			}
			s.mu.Unlock()
			return accountConcurrencyUnavailableResponse("account_concurrency_wait_interrupted", "account admission was interrupted by plugin reconfiguration", time.Second), true
		}
		if s.hostSchema < cpaapi.SchemaVersion {
			if registered {
				s.removeWaiterLocked(requestID, authID)
			}
			s.mu.Unlock()
			return cpaapi.RequestInterceptResponse{}, false
		}
		if current, exists := s.requests[requestID]; exists {
			if current.AuthID == authID {
				if registered {
					s.removeWaiterLocked(requestID, authID)
				}
				s.mu.Unlock()
				return cpaapi.RequestInterceptResponse{}, false
			}
			delete(s.requests, requestID)
			s.decrementActiveLocked(current.AuthID)
		}
		if !reservationConsumed {
			s.consumeSchedulerReservationLocked(authID)
			reservationConsumed = true
		}

		record := s.limits[authID]
		windowSeconds := normalizeAccountConcurrencyWindowSeconds(record.WindowSeconds)
		usedRequests := s.windowUsageLocked(authID, now, time.Duration(windowSeconds)*time.Second)
		waitFor, saturated := s.nextAdmissionWaitLocked(authID, record, usedRequests, now)
		if !saturated {
			if registered {
				s.removeWaiterLocked(requestID, authID)
			}
			s.active[authID]++
			s.events[authID] = append(s.events[authID], now)
			s.requests[requestID] = accountConcurrencyAdmission{AuthID: authID, AdmittedAt: now}
			s.mu.Unlock()
			return cpaapi.RequestInterceptResponse{}, false
		}

		if !registered {
			if _, duplicate := s.waitingRequests[requestID]; duplicate {
				s.mu.Unlock()
				return accountConcurrencyUnavailableResponse("account_concurrency_duplicate_wait", "the request is already waiting for account admission", time.Second), true
			}
			if s.waiting[authID] >= s.maxWaiters || len(s.waitingRequests) >= s.maxWaitersTotal {
				s.mu.Unlock()
				return accountConcurrencyUnavailableResponse("account_concurrency_queue_full", "the selected account wait queue is full", waitFor), true
			}
			s.waiting[authID]++
			s.waitingRequests[requestID] = authID
			registered = true
			waitEpoch = s.epoch
		}

		remaining := deadline.Sub(s.now().UTC())
		if s.maxWait <= 0 || remaining <= 0 {
			s.removeWaiterLocked(requestID, authID)
			s.mu.Unlock()
			return accountConcurrencyUnavailableResponse("account_concurrency_wait_timeout", "timed out waiting for account admission", waitFor), true
		}
		if waitFor <= 0 {
			waitFor = time.Millisecond
		}
		if waitFor > remaining {
			waitFor = remaining
		}
		wake := s.wake
		s.mu.Unlock()

		timer := time.NewTimer(waitFor)
		select {
		case <-wake:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
		}
	}
}

func (s *AccountConcurrencyService) nextAdmissionWaitLocked(authID string, record accountConcurrencyRecord, usedRequests int, now time.Time) (time.Duration, bool) {
	if record.Limit > 0 && s.active[authID] >= record.Limit {
		// Completion broadcasts wake waiters immediately; the fallback avoids a busy loop
		// when a host does not deliver completion callbacks.
		return time.Second, true
	}
	windowSeconds := normalizeAccountConcurrencyWindowSeconds(record.WindowSeconds)
	if record.Limit15s > 0 && usedRequests >= record.Limit15s {
		window := time.Duration(windowSeconds) * time.Second
		if candidate := windowAvailableAt(s.events[authID], now, window, record.Limit15s, usedRequests); candidate.After(now) {
			return candidate.Sub(now), true
		}
	}
	return 0, false
}

func windowAvailableAt(events []time.Time, now time.Time, window time.Duration, limit, used int) time.Time {
	needed := used - limit + 1
	cutoff := now.Add(-window)
	for _, event := range events {
		if !event.After(cutoff) || event.After(now) {
			continue
		}
		needed--
		if needed == 0 {
			return event.Add(window).Add(time.Nanosecond)
		}
	}
	return now.Add(time.Millisecond)
}

func accountConcurrencyUnavailableResponse(kind, message string, retryAfter time.Duration) cpaapi.RequestInterceptResponse {
	seconds := int(retryAfter.Round(time.Second) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	body, _ := json.Marshal(map[string]any{"error": map[string]any{
		"type":    kind,
		"message": message,
	}})
	return cpaapi.RequestInterceptResponse{
		Terminate:       true,
		StatusCode:      http.StatusServiceUnavailable,
		ResponseHeaders: http.Header{"Content-Type": {"application/json"}, "Retry-After": {fmt.Sprintf("%d", seconds)}},
		ResponseBody:    body,
	}
}

func (s *AccountConcurrencyService) Complete(completion cpaapi.RequestCompletion) {
	if s == nil {
		return
	}
	requestID := strings.TrimSpace(completion.RequestID)
	if requestID == "" {
		return
	}
	s.mu.Lock()
	if authID, waiting := s.waitingRequests[requestID]; waiting {
		s.removeWaiterLocked(requestID, authID)
		s.canceled[requestID] = s.now().UTC()
		s.broadcastLocked()
		s.mu.Unlock()
		return
	}
	admission, exists := s.requests[requestID]
	if exists {
		delete(s.requests, requestID)
		s.decrementActiveLocked(admission.AuthID)
		s.broadcastLocked()
	}
	s.mu.Unlock()
}

func (s *AccountConcurrencyService) Shutdown() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.shuttingDown = true
	s.active = make(map[string]int)
	s.requests = make(map[string]accountConcurrencyAdmission)
	s.events = make(map[string][]time.Time)
	s.reservations = make(map[string][]time.Time)
	s.cancelWaitersLocked()
	s.activeGate.Store(false)
	s.mu.Unlock()
}

func (s *AccountConcurrencyService) decrementActiveLocked(authID string) {
	if s.active[authID] <= 1 {
		delete(s.active, authID)
	} else {
		s.active[authID]--
	}
}

func (s *AccountConcurrencyService) removeWaiterLocked(requestID, authID string) {
	if current, exists := s.waitingRequests[requestID]; !exists || current != authID {
		return
	}
	delete(s.waitingRequests, requestID)
	if s.waiting[authID] <= 1 {
		delete(s.waiting, authID)
	} else {
		s.waiting[authID]--
	}
}

func (s *AccountConcurrencyService) cancelWaitersLocked() {
	s.waiting = make(map[string]int)
	s.waitingRequests = make(map[string]string)
	s.epoch++
	s.broadcastLocked()
}

func (s *AccountConcurrencyService) broadcastLocked() {
	if s.wake == nil {
		s.wake = make(chan struct{})
		return
	}
	close(s.wake)
	s.wake = make(chan struct{})
}

func (s *AccountConcurrencyService) updateActiveGateLocked() {
	s.activeGate.Store(s.hostSchema >= cpaapi.SchemaVersion)
}

func (s *AccountConcurrencyService) consumeSchedulerReservationLocked(authID string) {
	reservations := s.reservations[authID]
	if len(reservations) <= 1 {
		delete(s.reservations, authID)
		return
	}
	s.reservations[authID] = reservations[1:]
}

func (s *AccountConcurrencyService) pruneSchedulerReservationsLocked(now time.Time) {
	cutoff := now.Add(-accountConcurrencySchedulerReserveTTL)
	for authID, reservations := range s.reservations {
		kept := reservations[:0]
		for _, reservedAt := range reservations {
			if reservedAt.After(cutoff) && !reservedAt.After(now) {
				kept = append(kept, reservedAt)
			}
		}
		if len(kept) == 0 {
			delete(s.reservations, authID)
		} else {
			s.reservations[authID] = kept
		}
	}
}

func (s *AccountConcurrencyService) pruneExpiredLocked(now time.Time) {
	if !s.nextPrune.IsZero() && now.Before(s.nextPrune) {
		return
	}
	s.nextPrune = now.Add(accountConcurrencyPruneInterval)
	cutoff := now.Add(-accountConcurrencyLeaseTTL)
	for requestID, admission := range s.requests {
		if admission.AdmittedAt.After(cutoff) {
			continue
		}
		delete(s.requests, requestID)
		if s.active[admission.AuthID] <= 1 {
			delete(s.active, admission.AuthID)
		} else {
			s.active[admission.AuthID]--
		}
	}
	for authID, events := range s.events {
		kept := events[:0]
		for _, event := range events {
			if event.After(now.Add(-time.Duration(MaxAccountConcurrencyWindowSeconds)*time.Second)) && !event.After(now) {
				kept = append(kept, event)
			}
		}
		if len(kept) == 0 {
			delete(s.events, authID)
		} else {
			s.events[authID] = kept
		}
	}
}

func (s *AccountConcurrencyService) windowUsageLocked(authID string, now time.Time, window time.Duration) int {
	cutoff := now.Add(-window)
	used := 0
	for _, event := range s.events[authID] {
		if event.After(cutoff) && !event.After(now) {
			used++
		}
	}
	return used
}

func loadAccountConcurrency(path string) (map[string]accountConcurrencyRecord, error) {
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		return nil, errRead
	}
	var persisted persistedAccountConcurrency
	if errDecode := json.Unmarshal(raw, &persisted); errDecode != nil {
		return nil, fmt.Errorf("decode account concurrency: %w", errDecode)
	}
	if persisted.Version != accountConcurrencyStoreVersion {
		return nil, fmt.Errorf("unsupported account concurrency store version %d", persisted.Version)
	}
	limits := make(map[string]accountConcurrencyRecord, len(persisted.Limits))
	for _, record := range persisted.Limits {
		record.AuthID = strings.TrimSpace(record.AuthID)
		record.AccountID = strings.TrimSpace(record.AccountID)
		if record.WindowSeconds == 0 {
			record.WindowSeconds = DefaultAccountConcurrencyWindowSeconds
		}
		if record.AuthID == "" || len(record.AuthID) > 4096 || (record.Limit < 1 && record.Limit15s < 1) || record.Limit > MaxAccountConcurrencyLimit || record.Limit15s > MaxAccountConcurrencyLimit || !validAccountConcurrencyWindowSeconds(record.WindowSeconds) {
			continue
		}
		if len(record.AccountID) > maxAccountConfigIDLength {
			record.AccountID = ""
		}
		limits[record.AuthID] = record
	}
	return limits, nil
}

func saveAccountConcurrency(path string, limits map[string]accountConcurrencyRecord) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("account concurrency store path is empty")
	}
	records := make([]accountConcurrencyRecord, 0, len(limits))
	for _, record := range limits {
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].AuthID < records[j].AuthID })
	return savePrivateJSON(path, persistedAccountConcurrency{Version: accountConcurrencyStoreVersion, Limits: records})
}

func cloneAccountConcurrencyRecords(input map[string]accountConcurrencyRecord) map[string]accountConcurrencyRecord {
	cloned := make(map[string]accountConcurrencyRecord, len(input))
	for authID, record := range input {
		cloned[authID] = record
	}
	return cloned
}

func normalizeAccountConcurrencyWindowSeconds(seconds int) int {
	if !validAccountConcurrencyWindowSeconds(seconds) {
		return DefaultAccountConcurrencyWindowSeconds
	}
	return seconds
}

func validAccountConcurrencyWindowSeconds(seconds int) bool {
	return seconds >= MinAccountConcurrencyWindowSeconds && seconds <= MaxAccountConcurrencyWindowSeconds
}
