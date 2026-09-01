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
	accountConcurrencyStoreVersion  = 1
	MaxAccountConcurrencyLimit      = 1000
	accountConcurrencyLeaseTTL      = 24 * time.Hour
	accountConcurrencyPruneInterval = time.Minute
	selectedAuthMetadataKey         = "selected_auth_id"
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
	mu         sync.Mutex
	store      string
	loaded     bool
	loadFailed bool
	storageErr string
	hostSchema uint32
	limits     map[string]accountConcurrencyRecord
	active     map[string]int
	requests   map[string]accountConcurrencyAdmission
	events     map[string][]time.Time
	now        func() time.Time
	nextPrune  time.Time
	activeGate atomic.Bool
}

func NewAccountConcurrencyService() *AccountConcurrencyService {
	return &AccountConcurrencyService{
		hostSchema: cpaapi.SchemaVersion,
		limits:     make(map[string]accountConcurrencyRecord),
		active:     make(map[string]int),
		requests:   make(map[string]accountConcurrencyAdmission),
		events:     make(map[string][]time.Time),
		now:        time.Now,
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
	defer s.mu.Unlock()
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
		s.nextPrune = time.Time{}
	}
	if sameStore && !s.loadFailed {
		s.updateActiveGateLocked()
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
		return
	}
	s.limits = loaded
	s.loadFailed = false
	s.storageErr = ""
	s.updateActiveGateLocked()
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
	return AccountConcurrencySummary{Supported: s.hostSchema >= cpaapi.SchemaVersion, Limit: record.Limit, FifteenSecLimit: record.Limit15s, Active: s.active[authID], Used60s: used60, Used15s: used15}
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
	if s == nil || strings.TrimSpace(request.RequestID) == "" {
		return cpaapi.RequestInterceptResponse{}, false
	}
	authID, _ := request.Metadata[selectedAuthMetadataKey].(string)
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return cpaapi.RequestInterceptResponse{}, false
	}
	now := s.now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hostSchema < cpaapi.SchemaVersion {
		return cpaapi.RequestInterceptResponse{}, false
	}
	s.pruneExpiredLocked(now)
	if current, exists := s.requests[request.RequestID]; exists {
		if current.AuthID == authID {
			return cpaapi.RequestInterceptResponse{}, false
		}
		delete(s.requests, request.RequestID)
		if s.active[current.AuthID] <= 1 {
			delete(s.active, current.AuthID)
		} else {
			s.active[current.AuthID]--
		}
	}
	record := s.limits[authID]
	used60, used15 := s.windowUsageLocked(authID, now)
	if record.Limit15s > 0 && used15 >= record.Limit15s {
		return accountConcurrencyRejectedResponse(record.Limit15s, 15, used15), true
	}
	if record.Limit > 0 && used60 >= record.Limit {
		return accountConcurrencyRejectedResponse(record.Limit, 60, used60), true
	}
	s.active[authID]++
	s.events[authID] = append(s.events[authID], now)
	s.requests[request.RequestID] = accountConcurrencyAdmission{AuthID: authID, AdmittedAt: now}
	return cpaapi.RequestInterceptResponse{}, false
}

func accountConcurrencyRejectedResponse(limit, window, used int) cpaapi.RequestInterceptResponse {
	body, _ := json.Marshal(map[string]any{"error": map[string]any{
		"type":           "account_concurrency_limit_reached",
		"message":        "the selected account has reached its configured request limit",
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

func (s *AccountConcurrencyService) Complete(completion cpaapi.RequestCompletion) {
	if s == nil || strings.TrimSpace(completion.RequestID) == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	admission, exists := s.requests[completion.RequestID]
	if !exists {
		return
	}
	delete(s.requests, completion.RequestID)
	if s.active[admission.AuthID] <= 1 {
		delete(s.active, admission.AuthID)
	} else {
		s.active[admission.AuthID]--
	}
}

func (s *AccountConcurrencyService) Shutdown() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.active = make(map[string]int)
	s.requests = make(map[string]accountConcurrencyAdmission)
	s.events = make(map[string][]time.Time)
	s.activeGate.Store(false)
	s.mu.Unlock()
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
