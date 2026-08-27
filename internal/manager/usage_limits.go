package manager

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

const (
	usageLimitStoreVersion = 1
	usageLimitMaxModels    = 256
	usageLimitMaxPercent   = 100
	usageLimitMaxUSD       = 1_000_000_000
)

const (
	UsageLimitBasisAccount   = "account"
	UsageLimitBasisCredit    = "credit"
	UsageLimitWindowFiveHour = "five_hour"
	UsageLimitWindowSevenDay = "seven_day"
)

type UsageLimitRule struct {
	Enabled   bool    `json:"enabled"`
	Basis     string  `json:"basis"`
	Window    string  `json:"window,omitempty"`
	Percent   float64 `json:"percent,omitempty"`
	AmountUSD float64 `json:"amount_usd,omitempty"`
}

type UsageModelLimit struct {
	Model       string         `json:"model"`
	Rule        UsageLimitRule `json:"rule"`
	WithinTotal bool           `json:"within_total"`
}

type UsageLimitsConfig struct {
	Enabled bool              `json:"enabled"`
	Total   *UsageLimitRule   `json:"total,omitempty"`
	Models  []UsageModelLimit `json:"models,omitempty"`
}

type UsageLimitsSnapshot struct {
	Config             UsageLimitsConfig  `json:"config"`
	CreditUsedUSD      float64            `json:"credit_used_usd"`
	CreditModelUsedUSD map[string]float64 `json:"credit_model_used_usd,omitempty"`
	UpdatedAt          time.Time          `json:"updated_at"`
	StorageError       string             `json:"storage_error,omitempty"`
}

type persistedUsageLimits struct {
	Version                  int               `json:"version"`
	Config                   UsageLimitsConfig `json:"config"`
	CreditTotalNanos         int64             `json:"credit_total_nanos,omitempty"`
	CreditModelsNanos        map[string]int64  `json:"credit_models_nanos,omitempty"`
	CreditAccountsNanos      map[string]int64  `json:"credit_accounts_nanos,omitempty"`
	CreditAccountModelsNanos map[string]int64  `json:"credit_account_models_nanos,omitempty"`
	UpdatedAt                time.Time         `json:"updated_at"`
}

type UsageLimitService struct {
	mu                       sync.RWMutex
	store                    string
	loaded                   bool
	storageErr               string
	config                   UsageLimitsConfig
	creditTotalNanos         int64
	creditModelsNanos        map[string]int64
	creditAccountsNanos      map[string]int64
	creditAccountModelsNanos map[string]int64
	usage                    UsageSnapshotReader
	calculator               UsageCreditCalculator
	updatedAt                time.Time
}

func NewUsageLimitService(usage UsageSnapshotReader, calculator UsageCreditCalculator) *UsageLimitService {
	return &UsageLimitService{
		usage: usage, calculator: calculator,
		creditModelsNanos:        make(map[string]int64),
		creditAccountsNanos:      make(map[string]int64),
		creditAccountModelsNanos: make(map[string]int64),
	}
}

func usageLimitsStorePath(dataDir string) string { return filepath.Join(dataDir, "usage-limits.json") }

func normalizeUsageLimitRule(rule UsageLimitRule) UsageLimitRule {
	rule.Basis = strings.ToLower(strings.TrimSpace(rule.Basis))
	rule.Window = strings.ToLower(strings.TrimSpace(rule.Window))
	if rule.Basis == "" {
		rule.Basis = UsageLimitBasisAccount
	}
	if rule.Basis == UsageLimitBasisAccount {
		if rule.Window != UsageLimitWindowFiveHour && rule.Window != UsageLimitWindowSevenDay {
			rule.Window = UsageLimitWindowFiveHour
		}
		if math.IsNaN(rule.Percent) || math.IsInf(rule.Percent, 0) {
			rule.Percent = 0
		}
		if rule.Percent < 0 {
			rule.Percent = 0
		}
		if rule.Percent > usageLimitMaxPercent {
			rule.Percent = usageLimitMaxPercent
		}
		rule.AmountUSD = 0
	} else {
		rule.Basis = UsageLimitBasisCredit
		rule.Window = ""
		if math.IsNaN(rule.AmountUSD) || math.IsInf(rule.AmountUSD, 0) || rule.AmountUSD < 0 {
			rule.AmountUSD = 0
		}
		if rule.AmountUSD > usageLimitMaxUSD {
			rule.AmountUSD = usageLimitMaxUSD
		}
		rule.Percent = 0
	}
	return rule
}

func normalizeUsageLimitsConfig(config UsageLimitsConfig) UsageLimitsConfig {
	if config.Total != nil {
		rule := normalizeUsageLimitRule(*config.Total)
		config.Total = &rule
	}
	models := make([]UsageModelLimit, 0, len(config.Models))
	seen := make(map[string]struct{})
	for _, item := range config.Models {
		item.Model = strings.TrimSpace(item.Model)
		if item.Model == "" {
			continue
		}
		key := strings.ToLower(item.Model)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		item.Rule = normalizeUsageLimitRule(item.Rule)
		models = append(models, item)
		if len(models) >= usageLimitMaxModels {
			break
		}
	}
	config.Models = models
	return config
}

func validateUsageLimitsConfig(config UsageLimitsConfig) error {
	config = normalizeUsageLimitsConfig(config)
	if config.Total != nil && config.Total.Enabled {
		if config.Total.Basis == UsageLimitBasisAccount && config.Total.Percent <= 0 {
			return errors.New("total account limit percent must be greater than zero")
		}
		if config.Total.Basis == UsageLimitBasisCredit && config.Total.AmountUSD <= 0 {
			return errors.New("total credit limit amount must be greater than zero")
		}
	}
	for _, model := range config.Models {
		if !model.Rule.Enabled {
			continue
		}
		if model.Rule.Basis == UsageLimitBasisAccount && model.Rule.Percent <= 0 {
			return fmt.Errorf("model %q account limit percent must be greater than zero", model.Model)
		}
		if model.Rule.Basis == UsageLimitBasisCredit && model.Rule.AmountUSD <= 0 {
			return fmt.Errorf("model %q credit limit amount must be greater than zero", model.Model)
		}
	}
	return nil
}

func (s *UsageLimitService) HasCreditLimits() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.hasCreditLimitLocked()
}

func (s *UsageLimitService) Configure(config Config) {
	if s == nil {
		return
	}
	config = normalizeConfig(config)
	path := usageLimitsStorePath(config.DataDir)
	s.mu.Lock()
	if s.loaded && s.store == path && s.storageErr == "" {
		s.mu.Unlock()
		return
	}
	pathChanged := s.store != path
	loaded, err := loadUsageLimits(path)
	if err != nil {
		// A host reconfiguration can point this service at a different data
		// directory. Never carry the previous directory's counters into the
		// new store, especially when the new store has not been created yet.
		if pathChanged || !s.loaded {
			s.config = UsageLimitsConfig{}
			s.creditTotalNanos = 0
			s.creditModelsNanos = make(map[string]int64)
			s.creditAccountsNanos = make(map[string]int64)
			s.creditAccountModelsNanos = make(map[string]int64)
			s.updatedAt = time.Time{}
		}
		s.store, s.loaded = path, true
		if errors.Is(err, os.ErrNotExist) {
			s.storageErr = ""
		} else {
			s.storageErr = "usage limits could not be loaded"
		}
		s.mu.Unlock()
		return
	}
	s.store, s.loaded = path, true
	s.config = normalizeUsageLimitsConfig(loaded.Config)
	s.creditTotalNanos = maxInt64Zero(loaded.CreditTotalNanos)
	s.creditModelsNanos = cloneInt64Map(loaded.CreditModelsNanos)
	s.creditAccountsNanos = cloneInt64Map(loaded.CreditAccountsNanos)
	s.creditAccountModelsNanos = cloneInt64Map(loaded.CreditAccountModelsNanos)
	s.updatedAt = loaded.UpdatedAt
	s.storageErr = ""
	s.mu.Unlock()
}

func (s *UsageLimitService) Snapshot() UsageLimitsSnapshot {
	if s == nil {
		return UsageLimitsSnapshot{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	models := make(map[string]float64, len(s.creditModelsNanos))
	for k, v := range s.creditModelsNanos {
		models[k] = float64(v) / float64(creditNanosPerUSD)
	}
	return UsageLimitsSnapshot{Config: normalizeUsageLimitsConfig(s.config), CreditUsedUSD: float64(s.creditTotalNanos) / float64(creditNanosPerUSD), CreditModelUsedUSD: models, UpdatedAt: s.updatedAt, StorageError: s.storageErr}
}

func (s *UsageLimitService) Set(config UsageLimitsConfig) (UsageLimitsSnapshot, error) {
	if s == nil {
		return UsageLimitsSnapshot{}, errors.New("usage limits are unavailable")
	}
	config = normalizeUsageLimitsConfig(config)
	if err := validateUsageLimitsConfig(config); err != nil {
		return UsageLimitsSnapshot{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.loaded || strings.TrimSpace(s.store) == "" {
		return UsageLimitsSnapshot{}, errors.New("usage limits storage is unavailable")
	}
	state := persistedUsageLimits{Version: usageLimitStoreVersion, Config: config, CreditTotalNanos: s.creditTotalNanos, CreditModelsNanos: s.creditModelsNanos, CreditAccountsNanos: s.creditAccountsNanos, CreditAccountModelsNanos: s.creditAccountModelsNanos, UpdatedAt: time.Now().UTC()}
	if err := saveUsageLimits(s.store, state); err != nil {
		return UsageLimitsSnapshot{}, fmt.Errorf("persist usage limits: %w", err)
	}
	s.config, s.updatedAt, s.storageErr = config, state.UpdatedAt, ""
	if calculator, ok := s.calculator.(*Sub2APICreditUsage); ok && s.hasCreditLimitLocked() {
		calculator.SetEnabled(true)
	}
	return s.snapshotLocked(), nil
}

func (s *UsageLimitService) snapshotLocked() UsageLimitsSnapshot {
	models := make(map[string]float64, len(s.creditModelsNanos))
	for k, v := range s.creditModelsNanos {
		models[k] = float64(v) / float64(creditNanosPerUSD)
	}
	return UsageLimitsSnapshot{Config: normalizeUsageLimitsConfig(s.config), CreditUsedUSD: float64(s.creditTotalNanos) / float64(creditNanosPerUSD), CreditModelUsedUSD: models, UpdatedAt: s.updatedAt, StorageError: s.storageErr}
}

func (s *UsageLimitService) ObserveUsage(record cpaapi.UsageRecord) {
	if s == nil || record.Failed || s.calculator == nil {
		return
	}
	charge := s.calculator.Calculate(record)
	if !charge.Enabled || !charge.Rated || charge.AmountNanos <= 0 {
		return
	}
	identity, _ := runtimeIdentityFromUsage(record)
	identity = usageLimitIdentityKey(identity)
	model := normalizeUsageLimitModel(record.Model)
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.config.Enabled || !s.hasCreditLimitLocked() {
		return
	}
	s.creditTotalNanos = saturatingAdd(s.creditTotalNanos, charge.AmountNanos)
	if model != "" {
		s.creditModelsNanos[model] = saturatingAdd(s.creditModelsNanos[model], charge.AmountNanos)
	}
	if identity != "" {
		s.creditAccountsNanos[identity] = saturatingAdd(s.creditAccountsNanos[identity], charge.AmountNanos)
		if model != "" {
			key := identity + "\x00" + model
			s.creditAccountModelsNanos[key] = saturatingAdd(s.creditAccountModelsNanos[key], charge.AmountNanos)
		}
	}
	s.updatedAt = time.Now().UTC()
	if s.loaded && s.store != "" {
		_ = saveUsageLimits(s.store, persistedUsageLimits{Version: usageLimitStoreVersion, Config: s.config, CreditTotalNanos: s.creditTotalNanos, CreditModelsNanos: s.creditModelsNanos, CreditAccountsNanos: s.creditAccountsNanos, CreditAccountModelsNanos: s.creditAccountModelsNanos, UpdatedAt: s.updatedAt})
	}
}

func (s *UsageLimitService) hasCreditLimitLocked() bool {
	if s.config.Total != nil && s.config.Total.Enabled && s.config.Total.Basis == UsageLimitBasisCredit {
		return true
	}
	for _, item := range s.config.Models {
		if item.Rule.Enabled && item.Rule.Basis == UsageLimitBasisCredit {
			return true
		}
	}
	return false
}

func (s *UsageLimitService) RequestInterceptionActive() bool              { return s != nil }
func (s *UsageLimitService) RequestInterceptionAcceptsFormat(string) bool { return s != nil }

func (s *UsageLimitService) InterceptRequest(request cpaapi.RequestInterceptRequest) (cpaapi.RequestInterceptResponse, bool) {
	if s == nil {
		return cpaapi.RequestInterceptResponse{}, false
	}
	model := normalizeUsageLimitModel(request.RequestedModel)
	if model == "" {
		model = normalizeUsageLimitModel(request.Model)
	}
	identity := ""
	if request.Metadata != nil {
		for _, key := range []string{"selected_auth_id", "selected_auth_index", "auth_id", "auth_index"} {
			if value, ok := request.Metadata[key].(string); ok && strings.TrimSpace(value) != "" {
				identity = strings.TrimSpace(value)
				break
			}
		}
	}
	provider := runtimeProviderFromMetadata(request.Metadata)
	s.mu.RLock()
	config := normalizeUsageLimitsConfig(s.config)
	total := s.creditTotalNanos
	modelSpent := s.creditModelsNanos[model]
	s.mu.RUnlock()
	if !config.Enabled {
		return cpaapi.RequestInterceptResponse{}, false
	}
	if modelRule, ok := usageLimitModelRule(config, model); ok && modelRule.Rule.Enabled {
		if modelRule.Rule.Basis == UsageLimitBasisCredit && exceedsCredit(modelSpent, modelRule.Rule.AmountUSD) {
			return usageLimitRejection("model_credit", model, provider)
		}
		if modelRule.Rule.Basis == UsageLimitBasisAccount && identity != "" {
			if percent, ok := s.accountPercent(identity, modelRule.Rule.Window); ok && percent >= modelRule.Rule.Percent {
				return usageLimitRejection("model_account", model, provider)
			}
		}
	}
	if config.Total != nil && config.Total.Enabled && (model == "" || modelRuleWithinTotal(config, model)) {
		rule := config.Total
		if rule.Basis == UsageLimitBasisCredit && exceedsCredit(total, rule.AmountUSD) {
			return usageLimitRejection("total_credit", model, provider)
		}
		if rule.Basis == UsageLimitBasisAccount && identity != "" {
			if percent, ok := s.accountPercent(identity, rule.Window); ok && percent >= rule.Percent {
				return usageLimitRejection("total_account", model, provider)
			}
		}
	}
	return cpaapi.RequestInterceptResponse{}, false
}

func (s *UsageLimitService) accountPercent(identity, window string) (float64, bool) {
	if s.usage == nil || strings.TrimSpace(identity) == "" {
		return 0, false
	}
	snapshot := s.usage.Snapshot(identity)
	if snapshot == nil || snapshot.Codex == nil {
		return 0, false
	}
	var usage *UsageWindowSnapshot
	if window == UsageLimitWindowSevenDay {
		usage = snapshot.Codex.SevenDay
	} else {
		usage = snapshot.Codex.FiveHour
	}
	if usage == nil || math.IsNaN(usage.UsedPercent) || math.IsInf(usage.UsedPercent, 0) {
		return 0, false
	}
	return usage.UsedPercent, true
}

func usageLimitModelRule(config UsageLimitsConfig, model string) (UsageModelLimit, bool) {
	for _, item := range config.Models {
		if strings.EqualFold(item.Model, model) {
			return item, true
		}
	}
	return UsageModelLimit{}, false
}
func modelRuleWithinTotal(config UsageLimitsConfig, model string) bool {
	item, ok := usageLimitModelRule(config, model)
	return !ok || item.WithinTotal
}
func exceedsCredit(nanos int64, amount float64) bool {
	return amount > 0 && float64(nanos)/float64(creditNanosPerUSD) >= amount
}
func normalizeUsageLimitModel(model string) string { return strings.ToLower(strings.TrimSpace(model)) }
func usageLimitIdentityKey(identity string) string {
	identity = strings.TrimSpace(identity)
	if identity == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(identity))
	return hex.EncodeToString(digest[:])
}
func usageLimitRejection(basis, model, provider string) (cpaapi.RequestInterceptResponse, bool) {
	body, _ := json.Marshal(map[string]string{"error": "usage limit reached", "code": "usage_limit_reached", "basis": basis, "model": model, "provider": provider})
	return cpaapi.RequestInterceptResponse{Terminate: true, StatusCode: http.StatusTooManyRequests, ResponseHeaders: http.Header{"Content-Type": []string{"application/json"}}, ResponseBody: body}, true
}
func maxInt64Zero(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}
func cloneInt64Map(source map[string]int64) map[string]int64 {
	target := make(map[string]int64, len(source))
	for key, value := range source {
		if value >= 0 {
			target[key] = value
		}
	}
	return target
}

func loadUsageLimits(path string) (persistedUsageLimits, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return persistedUsageLimits{}, err
	}
	var state persistedUsageLimits
	if err := json.Unmarshal(raw, &state); err != nil {
		return persistedUsageLimits{}, err
	}
	if state.Version != 0 && state.Version != usageLimitStoreVersion {
		return persistedUsageLimits{}, fmt.Errorf("unsupported usage limit store version %d", state.Version)
	}
	state.Config = normalizeUsageLimitsConfig(state.Config)
	state.CreditModelsNanos = cloneInt64Map(state.CreditModelsNanos)
	state.CreditAccountsNanos = cloneInt64Map(state.CreditAccountsNanos)
	state.CreditAccountModelsNanos = cloneInt64Map(state.CreditAccountModelsNanos)
	return state, nil
}
func saveUsageLimits(path string, state persistedUsageLimits) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("usage limits path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return writePrivateFileAtomically(path, raw)
}
