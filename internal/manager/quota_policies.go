package manager

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

const quotaPolicyStoreVersion = 1

var ErrQuotaPolicyStorageUnavailable = errors.New("quota policy storage is unavailable")

// QuotaWindowPolicy is a plugin-owned quota guard. Account percentages are
// compared with CPA's reported window percentages; provider percentages are
// calculated from the manually configured total token budget.
type QuotaWindowPolicy struct {
	TotalTokens  *int64 `json:"total_tokens,omitempty"`
	LimitPercent *int   `json:"limit_percent,omitempty"`
}

type AccountQuotaPolicy struct {
	FiveHour QuotaWindowPolicy `json:"five_hour"`
	SevenDay QuotaWindowPolicy `json:"seven_day"`
}

type ProviderQuotaPolicy struct {
	Key         string            `json:"key"`
	Label       string            `json:"label,omitempty"`
	Concurrency *int              `json:"concurrency_limit,omitempty"`
	FiveHour    QuotaWindowPolicy `json:"five_hour"`
	SevenDay    QuotaWindowPolicy `json:"seven_day"`
}

type QuotaPolicySnapshot struct {
	Accounts     map[string]AccountQuotaPolicy `json:"accounts"`
	Providers    []ProviderQuotaPolicy         `json:"providers"`
	StorageError string                        `json:"storage_error,omitempty"`
}

type persistedQuotaPolicies struct {
	Version   int                            `json:"version"`
	Accounts  map[string]AccountQuotaPolicy  `json:"accounts,omitempty"`
	Providers map[string]ProviderQuotaPolicy `json:"providers,omitempty"`
}

type QuotaPolicyService struct {
	mu         sync.RWMutex
	store      string
	loaded     bool
	storageErr string
	accounts   map[string]AccountQuotaPolicy
	providers  map[string]ProviderQuotaPolicy
}

func NewQuotaPolicyService() *QuotaPolicyService {
	return &QuotaPolicyService{accounts: make(map[string]AccountQuotaPolicy), providers: make(map[string]ProviderQuotaPolicy)}
}

func quotaPolicyStorePath(dataDir string) string {
	return filepath.Join(dataDir, "quota-policies.json")
}

func (s *QuotaPolicyService) Configure(config Config) {
	if s == nil {
		return
	}
	path := quotaPolicyStorePath(normalizeConfig(config).DataDir)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loaded && s.store == path {
		return
	}
	s.store, s.loaded = path, true
	persisted, err := loadQuotaPolicies(path)
	if err != nil {
		// A first run has no policy file yet. Treat that as an empty, healthy
		// store so the UI does not report a false storage failure before the
		// first policy is saved. Other read/parse failures remain visible.
		if errors.Is(err, os.ErrNotExist) {
			s.storageErr = ""
			s.accounts, s.providers = make(map[string]AccountQuotaPolicy), make(map[string]ProviderQuotaPolicy)
			return
		}
		s.storageErr = "quota policy state could not be loaded"
		s.accounts, s.providers = make(map[string]AccountQuotaPolicy), make(map[string]ProviderQuotaPolicy)
		return
	}
	s.accounts, s.providers, s.storageErr = persisted.Accounts, persisted.Providers, ""
}

func (s *QuotaPolicyService) Snapshot() QuotaPolicySnapshot {
	if s == nil {
		return QuotaPolicySnapshot{Accounts: map[string]AccountQuotaPolicy{}}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	accounts := make(map[string]AccountQuotaPolicy, len(s.accounts))
	for k, v := range s.accounts {
		accounts[k] = v
	}
	providers := make([]ProviderQuotaPolicy, 0, len(s.providers))
	for _, value := range s.providers {
		providers = append(providers, value)
	}
	sort.Slice(providers, func(i, j int) bool { return providers[i].Key < providers[j].Key })
	return QuotaPolicySnapshot{Accounts: accounts, Providers: providers, StorageError: s.storageErr}
}

func (s *QuotaPolicyService) AccountPolicy(id string) AccountQuotaPolicy {
	if s == nil {
		return AccountQuotaPolicy{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.accounts[strings.TrimSpace(id)]
}

func (s *QuotaPolicyService) ProviderPolicy(key string) ProviderQuotaPolicy {
	if s == nil {
		return ProviderQuotaPolicy{Key: strings.TrimSpace(key)}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.providers[strings.TrimSpace(key)]
}

func (s *QuotaPolicyService) HasAccountPolicies() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.accounts) > 0
}

func (s *QuotaPolicyService) SetAccountPolicy(id string, policy AccountQuotaPolicy) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("account id is required")
	}
	if err := validateQuotaPolicy(policy.FiveHour); err != nil {
		return err
	}
	if err := validateQuotaPolicy(policy.SevenDay); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := cloneQuotaPolicyMap(s.accounts)
	if quotaPolicyEmpty(policy) {
		delete(next, id)
	} else {
		next[id] = policy
	}
	if err := saveQuotaPolicies(s.store, next, s.providers); err != nil {
		return fmt.Errorf("%w: %v", ErrQuotaPolicyStorageUnavailable, err)
	}
	s.accounts, s.storageErr = next, ""
	return nil
}

func (s *QuotaPolicyService) SetProviderPolicy(policy ProviderQuotaPolicy) error {
	policy.Key = strings.TrimSpace(policy.Key)
	if policy.Key == "" {
		return fmt.Errorf("provider policy key is required")
	}
	if err := validateQuotaPolicy(policy.FiveHour); err != nil {
		return err
	}
	if err := validateQuotaPolicy(policy.SevenDay); err != nil {
		return err
	}
	if policy.Concurrency != nil && (*policy.Concurrency < 0 || *policy.Concurrency > 1000) {
		return fmt.Errorf("provider concurrency must be between 0 and 1000")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := cloneProviderPolicyMap(s.providers)
	if providerPolicyEmpty(policy) {
		delete(next, policy.Key)
	} else {
		next[policy.Key] = policy
	}
	if err := saveQuotaPolicies(s.store, s.accounts, next); err != nil {
		return fmt.Errorf("%w: %v", ErrQuotaPolicyStorageUnavailable, err)
	}
	s.providers, s.storageErr = next, ""
	return nil
}

func validateQuotaPolicy(policy QuotaWindowPolicy) error {
	if policy.TotalTokens != nil && (*policy.TotalTokens < 0 || *policy.TotalTokens > 9_000_000_000_000_000) {
		return fmt.Errorf("quota total tokens is out of range")
	}
	if policy.LimitPercent != nil && (*policy.LimitPercent < 0 || *policy.LimitPercent > 100) {
		return fmt.Errorf("quota limit percent must be between 0 and 100")
	}
	return nil
}

func quotaPolicyEmpty(policy AccountQuotaPolicy) bool {
	return quotaWindowEmpty(policy.FiveHour) && quotaWindowEmpty(policy.SevenDay)
}
func providerPolicyEmpty(policy ProviderQuotaPolicy) bool {
	return policy.Label == "" && policy.Concurrency == nil && quotaWindowEmpty(policy.FiveHour) && quotaWindowEmpty(policy.SevenDay)
}
func quotaWindowEmpty(policy QuotaWindowPolicy) bool {
	return policy.TotalTokens == nil && policy.LimitPercent == nil
}

func quotaPolicyPointer(service *QuotaPolicyService, id string) *AccountQuotaPolicy {
	if service == nil {
		return nil
	}
	policy := service.AccountPolicy(id)
	if quotaPolicyEmpty(policy) {
		return nil
	}
	return &policy
}

func cloneQuotaPolicyMap(input map[string]AccountQuotaPolicy) map[string]AccountQuotaPolicy {
	out := make(map[string]AccountQuotaPolicy, len(input))
	for k, v := range input {
		out[k] = v
	}
	return out
}
func cloneProviderPolicyMap(input map[string]ProviderQuotaPolicy) map[string]ProviderQuotaPolicy {
	out := make(map[string]ProviderQuotaPolicy, len(input))
	for k, v := range input {
		out[k] = v
	}
	return out
}

func loadQuotaPolicies(path string) (*persistedQuotaPolicies, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var persisted persistedQuotaPolicies
	if err := json.Unmarshal(raw, &persisted); err != nil {
		return nil, err
	}
	if persisted.Version != quotaPolicyStoreVersion {
		return nil, fmt.Errorf("unsupported quota policy store version %d", persisted.Version)
	}
	if persisted.Accounts == nil {
		persisted.Accounts = make(map[string]AccountQuotaPolicy)
	}
	if persisted.Providers == nil {
		persisted.Providers = make(map[string]ProviderQuotaPolicy)
	}
	return &persisted, nil
}

func saveQuotaPolicies(path string, accounts map[string]AccountQuotaPolicy, providers map[string]ProviderQuotaPolicy) error {
	return savePrivateJSON(path, persistedQuotaPolicies{Version: quotaPolicyStoreVersion, Accounts: accounts, Providers: providers})
}
