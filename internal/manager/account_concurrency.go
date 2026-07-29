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
	accountConcurrencyStoreVersion = 1
	MaxAccountConcurrencyLimit     = 1000
	accountConcurrencyLeaseTTL     = 24 * time.Hour
	selectedAuthMetadataKey        = "selected_auth_id"
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
}

type AccountConcurrencySummary struct {
	Supported bool `json:"supported"`
	Limit     int  `json:"limit"`
	Active    int  `json:"active"`
}

type accountConcurrencyRecord struct {
	AuthID    string `json:"auth_id"`
	AccountID string `json:"account_id,omitempty"`
	Limit     int    `json:"limit"`
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
	hostSchema uint32
	limits     map[string]accountConcurrencyRecord
	active     map[string]int
	requests   map[string]accountConcurrencyAdmission
	now        func() time.Time
	activeGate atomic.Bool
}

func NewAccountConcurrencyService() *AccountConcurrencyService {
	return &AccountConcurrencyService{
		hostSchema: cpaapi.SchemaVersion,
		limits:     make(map[string]accountConcurrencyRecord),
		active:     make(map[string]int),
		requests:   make(map[string]accountConcurrencyAdmission),
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
	s.hostSchema = hostSchema
	if s.loaded && s.store == storePath {
		s.updateActiveGateLocked()
		return
	}
	s.store = storePath
	s.loaded = true
	loaded, errLoad := loadAccountConcurrency(storePath)
	if errLoad != nil {
		s.limits = make(map[string]accountConcurrencyRecord)
		s.updateActiveGateLocked()
		return
	}
	s.limits = loaded
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
	record := s.limits[authID]
	return AccountConcurrencySummary{Supported: s.hostSchema >= cpaapi.SchemaVersion, Limit: record.Limit, Active: s.active[authID]}
}

func (s *AccountConcurrencyService) SetLimit(account Account, limit int) error {
	if s == nil {
		return ErrAccountConcurrencyUnsupported
	}
	if limit < 0 || limit > MaxAccountConcurrencyLimit {
		return fmt.Errorf("account concurrency must be between 0 and %d", MaxAccountConcurrencyLimit)
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
	if limit == 0 {
		delete(next, authID)
	} else {
		next[authID] = accountConcurrencyRecord{AuthID: authID, AccountID: accountID, Limit: limit}
	}
	if errSave := saveAccountConcurrency(s.store, next); errSave != nil {
		return fmt.Errorf("persist account concurrency: %w", errSave)
	}
	s.limits = next
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
	limit := s.limits[authID].Limit
	if limit <= 0 {
		return cpaapi.RequestInterceptResponse{}, false
	}
	if s.active[authID] >= limit {
		return accountConcurrencyRejectedResponse(limit), true
	}
	s.active[authID]++
	s.requests[request.RequestID] = accountConcurrencyAdmission{AuthID: authID, AdmittedAt: now}
	return cpaapi.RequestInterceptResponse{}, false
}

func accountConcurrencyRejectedResponse(limit int) cpaapi.RequestInterceptResponse {
	body, _ := json.Marshal(map[string]any{"error": map[string]any{
		"type":    "account_concurrency_limit_reached",
		"message": "the selected account has reached its configured concurrency limit",
		"limit":   limit,
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
	s.updateActiveGateLocked()
}

func (s *AccountConcurrencyService) Shutdown() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.active = make(map[string]int)
	s.requests = make(map[string]accountConcurrencyAdmission)
	s.activeGate.Store(false)
	s.mu.Unlock()
}

func (s *AccountConcurrencyService) updateActiveGateLocked() {
	s.activeGate.Store(s.hostSchema >= cpaapi.SchemaVersion && (len(s.limits) > 0 || len(s.requests) > 0))
}

func (s *AccountConcurrencyService) pruneExpiredLocked(now time.Time) {
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
	s.updateActiveGateLocked()
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
		if record.AuthID == "" || len(record.AuthID) > 4096 || record.Limit < 1 || record.Limit > MaxAccountConcurrencyLimit {
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
