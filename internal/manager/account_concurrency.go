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
	accountConcurrencyStoreVersion      = 1
	MaxAccountConcurrencyLimit          = 1000
	accountConcurrencyLeaseTTL          = 24 * time.Hour
	accountConcurrencyPruneInterval     = time.Minute
	accountConcurrencyMaxWait           = 60 * time.Second
	accountConcurrencyMaxWaitersAccount = 100
	accountConcurrencyMaxWaitersTotal   = 1000
	accountConcurrencyTombstoneTTL      = 2 * time.Minute
	selectedAuthMetadataKey             = "selected_auth_id"
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
	FifteenSecLimit int  `json:"limit_15s"`
	Used60s         int  `json:"used_60s"`
	Used15s         int  `json:"used_15s"`
	Supported       bool `json:"supported"`
	Limit           int  `json:"limit"`
	Active          int  `json:"active"`
	Waiting         int  `json:"waiting"`
}

type accountConcurrencyRecord struct {
	AuthID    string `json:"auth_id"`
	AccountID string `json:"account_id,omitempty"`
	Limit     int    `json:"limit"`
	Limit15s  int    `json:"limit_15s,omitempty"`
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
	used60, used15 := s.windowUsageLocked(authID, now)
	return AccountConcurrencySummary{Supported: s.hostSchema >= cpaapi.SchemaVersion, Limit: record.Limit, FifteenSecLimit: record.Limit15s, Active: s.active[authID], Waiting: s.waiting[authID], Used60s: used60, Used15s: used15}
}

func (s *AccountConcurrencyService) SetLimit(account Account, limit int) error {
	return s.SetLimits(account, &limit, nil)
}

// SetLimits updates either or both rolling request-window limits. A nil value
// preserves that window's current setting; zero clears it.
func (s *AccountConcurrencyService) SetLimits(account Account, minuteLimit, fifteenSecondLimit *int) error {
	if s == nil {
		return ErrAccountConcurrencyUnsupported
	}
	if minuteLimit != nil && (*minuteLimit < 0 || *minuteLimit > MaxAccountConcurrencyLimit) {
		return fmt.Errorf("account concurrency must be between 0 and %d", MaxAccountConcurrencyLimit)
	}
	if fifteenSecondLimit != nil && (*fifteenSecondLimit < 0 || *fifteenSecondLimit > MaxAccountConcurrencyLimit) {
		return fmt.Errorf("15-second account concurrency must be between 0 and %d", MaxAccountConcurrencyLimit)
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
	if minuteLimit != nil {
		current.Limit = *minuteLimit
	}
	if fifteenSecondLimit != nil {
		current.Limit15s = *fifteenSecondLimit
	}
	if current.Limit == 0 && current.Limit15s == 0 {
		delete(next, authID)
	} else {
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

		record := s.limits[authID]
		used60, used15 := s.windowUsageLocked(authID, now)
		waitFor, saturated := s.nextAdmissionWaitLocked(authID, record, used60, used15, now)
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

func (s *AccountConcurrencyService) nextAdmissionWaitLocked(authID string, record accountConcurrencyRecord, used60, used15 int, now time.Time) (time.Duration, bool) {
	var availableAt time.Time
	if record.Limit15s > 0 && used15 >= record.Limit15s {
		if candidate := windowAvailableAt(s.events[authID], now, 15*time.Second, record.Limit15s, used15); candidate.After(availableAt) {
			availableAt = candidate
		}
	}
	if record.Limit > 0 && used60 >= record.Limit {
		if candidate := windowAvailableAt(s.events[authID], now, time.Minute, record.Limit, used60); candidate.After(availableAt) {
			availableAt = candidate
		}
	}
	if availableAt.IsZero() {
		return 0, false
	}
	return availableAt.Sub(now), true
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
			if event.After(now.Add(-time.Minute)) && !event.After(now) {
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

func (s *AccountConcurrencyService) windowUsageLocked(authID string, now time.Time) (int, int) {
	cutoff60, cutoff15 := now.Add(-time.Minute), now.Add(-15*time.Second)
	used60, used15 := 0, 0
	for _, event := range s.events[authID] {
		if event.After(cutoff60) && !event.After(now) {
			used60++
		}
		if event.After(cutoff15) && !event.After(now) {
			used15++
		}
	}
	return used60, used15
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
		if record.AuthID == "" || len(record.AuthID) > 4096 || (record.Limit < 1 && record.Limit15s < 1) || record.Limit > MaxAccountConcurrencyLimit || record.Limit15s > MaxAccountConcurrencyLimit {
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
