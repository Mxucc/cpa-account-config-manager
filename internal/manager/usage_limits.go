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
	usageLimitStoreVersion = 2
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

type UsageLimitsScope struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type UsageLimitsSnapshot struct {
	Scope              UsageLimitsScope   `json:"scope"`
	Config             UsageLimitsConfig  `json:"config"`
	CreditUsedUSD      float64            `json:"credit_used_usd"`
	CreditModelUsedUSD map[string]float64 `json:"credit_model_used_usd,omitempty"`
	UpdatedAt          time.Time          `json:"updated_at"`
	StorageError       string             `json:"storage_error,omitempty"`
}

type persistedUsageLimits struct {
	Version                int                          `json:"version"`
	Configs                map[string]UsageLimitsConfig `json:"configs,omitempty"`
	CreditScopesNanos      map[string]int64             `json:"credit_scopes_nanos,omitempty"`
	CreditScopeModelsNanos map[string]int64             `json:"credit_scope_models_nanos,omitempty"`
	UpdatedAt              time.Time                    `json:"updated_at"`
}

type UsageLimitService struct {
	mu                     sync.RWMutex
	store                  string
	loaded                 bool
	storageErr             string
	configs                map[string]UsageLimitsConfig
	creditScopesNanos      map[string]int64
	creditScopeModelsNanos map[string]int64
	usage                  UsageSnapshotReader
	calculator             UsageCreditCalculator
	updatedAt              time.Time
}

func NewUsageLimitService(usage UsageSnapshotReader, calculator UsageCreditCalculator) *UsageLimitService {
	return &UsageLimitService{
		usage: usage, calculator: calculator,
		configs:                make(map[string]UsageLimitsConfig),
		creditScopesNanos:      make(map[string]int64),
		creditScopeModelsNanos: make(map[string]int64),
	}
}

func usageLimitsStorePath(dataDir string) string { return filepath.Join(dataDir, "usage-limits.json") }

func AccountUsageLimitsScope(accountID string) UsageLimitsScope {
	return UsageLimitsScope{Kind: "account", ID: strings.TrimSpace(accountID)}
}

func ProviderUsageLimitsScope(provider string) UsageLimitsScope {
	return UsageLimitsScope{Kind: "provider", ID: normalizeRuntimeProvider(provider)}
}

func normalizeUsageLimitsScope(scope UsageLimitsScope) (UsageLimitsScope, error) {
	scope.Kind = strings.ToLower(strings.TrimSpace(scope.Kind))
	scope.ID = strings.TrimSpace(scope.ID)
	if scope.Kind != "account" && scope.Kind != "provider" {
		return UsageLimitsScope{}, errors.New("usage limit scope must be account or provider")
	}
	if scope.ID == "" || len(scope.ID) > 4096 {
		return UsageLimitsScope{}, errors.New("usage limit scope id is required")
	}
	if scope.Kind == "provider" {
		scope.ID = normalizeRuntimeProvider(scope.ID)
		if scope.ID == "" {
			return UsageLimitsScope{}, errors.New("usage limit provider is required")
		}
	}
	return scope, nil
}

func usageLimitScopeKey(scope UsageLimitsScope) (string, error) {
	scope, err := normalizeUsageLimitsScope(scope)
	if err != nil {
		return "", err
	}
	if scope.Kind == "account" {
		// Account IDs can be auth indexes or auth IDs. Hashing keeps those
		// identifiers out of the persisted usage-limit store.
		digest := sha256.Sum256([]byte(scope.ID))
		return "account:" + hex.EncodeToString(digest[:]), nil
	}
	return "provider:" + scope.ID, nil
}

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
		if math.IsNaN(rule.Percent) || math.IsInf(rule.Percent, 0) || rule.Percent < 0 {
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

func validateUsageLimitsConfigForScope(scope UsageLimitsScope, config UsageLimitsConfig) error {
	config = normalizeUsageLimitsConfig(config)
	if scope.Kind == "provider" {
		if config.Total != nil && config.Total.Basis == UsageLimitBasisAccount {
			return errors.New("provider total usage limit only supports credit amount")
		}
		for _, model := range config.Models {
			if model.Rule.Basis == UsageLimitBasisAccount {
				return fmt.Errorf("provider model %q usage limit only supports credit amount", model.Model)
			}
		}
	}
	return validateUsageLimitsConfig(config)
}

func normalizeUsageLimitsConfigForScope(scope UsageLimitsScope, config UsageLimitsConfig) UsageLimitsConfig {
	config = normalizeUsageLimitsConfig(config)
	if scope.Kind != "provider" {
		return config
	}
	if config.Total != nil && config.Total.Basis == UsageLimitBasisAccount {
		config.Total = &UsageLimitRule{Basis: UsageLimitBasisCredit}
	}
	for index := range config.Models {
		if config.Models[index].Rule.Basis == UsageLimitBasisAccount {
			config.Models[index].Rule = UsageLimitRule{Basis: UsageLimitBasisCredit}
		}
	}
	return config
}

func normalizeStoredUsageLimitsConfig(key string, config UsageLimitsConfig) UsageLimitsConfig {
	if strings.HasPrefix(key, "provider:") {
		return normalizeUsageLimitsConfigForScope(UsageLimitsScope{Kind: "provider"}, config)
	}
	return normalizeUsageLimitsConfig(config)
}

func (s *UsageLimitService) Configure(config Config) {
	if s == nil {
		return
	}
	path := usageLimitsStorePath(normalizeConfig(config).DataDir)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loaded && s.store == path && s.storageErr == "" {
		return
	}
	loaded, err := loadUsageLimits(path)
	if err != nil {
		if s.store != path || !s.loaded {
			s.resetLocked()
		}
		s.store, s.loaded = path, true
		if errors.Is(err, os.ErrNotExist) {
			s.storageErr = ""
		} else {
			s.storageErr = "usage limits could not be loaded"
		}
		return
	}
	s.store, s.loaded, s.storageErr = path, true, ""
	s.configs = cloneUsageLimitConfigs(loaded.Configs)
	s.creditScopesNanos = cloneInt64Map(loaded.CreditScopesNanos)
	s.creditScopeModelsNanos = cloneInt64Map(loaded.CreditScopeModelsNanos)
	s.updatedAt = loaded.UpdatedAt
}

func (s *UsageLimitService) resetLocked() {
	s.configs = make(map[string]UsageLimitsConfig)
	s.creditScopesNanos = make(map[string]int64)
	s.creditScopeModelsNanos = make(map[string]int64)
	s.updatedAt = time.Time{}
}

func (s *UsageLimitService) Get(scope UsageLimitsScope) UsageLimitsSnapshot {
	if s == nil {
		return UsageLimitsSnapshot{Scope: scope}
	}
	key, err := usageLimitScopeKey(scope)
	if err != nil {
		return UsageLimitsSnapshot{Scope: scope}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshotLocked(scope, key)
}

func (s *UsageLimitService) Set(scope UsageLimitsScope, config UsageLimitsConfig) (UsageLimitsSnapshot, error) {
	if s == nil {
		return UsageLimitsSnapshot{}, errors.New("usage limits are unavailable")
	}
	normalizedScope, err := normalizeUsageLimitsScope(scope)
	if err != nil {
		return UsageLimitsSnapshot{}, err
	}
	config = normalizeUsageLimitsConfig(config)
	if err := validateUsageLimitsConfigForScope(normalizedScope, config); err != nil {
		return UsageLimitsSnapshot{}, err
	}
	key, _ := usageLimitScopeKey(normalizedScope)
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.loaded || strings.TrimSpace(s.store) == "" {
		return UsageLimitsSnapshot{}, errors.New("usage limits storage is unavailable")
	}
	nextConfigs := cloneUsageLimitConfigs(s.configs)
	if config.Enabled || config.Total != nil || len(config.Models) > 0 {
		nextConfigs[key] = config
	} else {
		delete(nextConfigs, key)
	}
	state := persistedUsageLimits{Version: usageLimitStoreVersion, Configs: nextConfigs, CreditScopesNanos: s.creditScopesNanos, CreditScopeModelsNanos: s.creditScopeModelsNanos, UpdatedAt: time.Now().UTC()}
	if err := saveUsageLimits(s.store, state); err != nil {
		return UsageLimitsSnapshot{}, fmt.Errorf("persist usage limits: %w", err)
	}
	s.configs, s.updatedAt, s.storageErr = nextConfigs, state.UpdatedAt, ""
	return s.snapshotLocked(normalizedScope, key), nil
}

func (s *UsageLimitService) HasCreditLimits() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, config := range s.configs {
		if hasCreditLimit(config) {
			return true
		}
	}
	return false
}

func hasCreditLimit(config UsageLimitsConfig) bool {
	if config.Total != nil && config.Total.Enabled && config.Total.Basis == UsageLimitBasisCredit {
		return true
	}
	for _, item := range config.Models {
		if item.Rule.Enabled && item.Rule.Basis == UsageLimitBasisCredit {
			return true
		}
	}
	return false
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
	accountScope := AccountUsageLimitsScope(usageLimitAccountID(identity))
	providerScope := ProviderUsageLimitsScope(record.Provider)
	accountKey, _ := usageLimitScopeKey(accountScope)
	providerKey, providerErr := usageLimitScopeKey(providerScope)
	model := normalizeUsageLimitModel(record.Model)
	s.mu.Lock()
	defer s.mu.Unlock()
	if !hasCreditLimit(s.configs[accountKey]) && (providerErr != nil || !hasCreditLimit(s.configs[providerKey])) {
		return
	}
	for _, key := range []string{accountKey, providerKey} {
		if key == "" || !hasCreditLimit(s.configs[key]) {
			continue
		}
		s.creditScopesNanos[key] = usageLimitSaturatingAdd(s.creditScopesNanos[key], charge.AmountNanos)
		if model != "" {
			s.creditScopeModelsNanos[key+"\x00"+model] = usageLimitSaturatingAdd(s.creditScopeModelsNanos[key+"\x00"+model], charge.AmountNanos)
		}
	}
	s.updatedAt = time.Now().UTC()
	s.persistLocked()
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
	identity, _ := runtimeIdentityFromMetadata(request.Metadata)
	provider := runtimeProviderFromMetadata(request.Metadata)
	if provider == "" {
		provider = normalizeRuntimeProvider(request.ToFormat)
	}
	scopes := []struct {
		scope    UsageLimitsScope
		identity string
	}{
		{AccountUsageLimitsScope(usageLimitAccountID(identity)), identity},
		{ProviderUsageLimitsScope(provider), identity},
	}
	s.mu.RLock()
	configs := make(map[string]UsageLimitsConfig, 2)
	spent := make(map[string]int64, 2)
	for _, item := range scopes {
		key, err := usageLimitScopeKey(item.scope)
		if err != nil {
			continue
		}
		config, ok := s.configs[key]
		if !ok || !config.Enabled {
			continue
		}
		configs[key] = normalizeUsageLimitsConfig(config)
		spent[key] = s.creditScopesNanos[key]
	}
	s.mu.RUnlock()
	for key, config := range configs {
		if rule, ok := usageLimitModelRule(config, model); ok && rule.Rule.Enabled {
			if rule.Rule.Basis == UsageLimitBasisCredit && exceedsCredit(s.scopeModelSpent(key, model), rule.Rule.AmountUSD) {
				return usageLimitRejection("model_credit", model, key), true
			}
			if rule.Rule.Basis == UsageLimitBasisAccount && identity != "" {
				if percent, ok := s.accountPercent(identity, rule.Rule.Window); ok && percent >= rule.Rule.Percent {
					return usageLimitRejection("model_account", model, key), true
				}
			}
		}
		if config.Total != nil && config.Total.Enabled && (model == "" || modelRuleWithinTotal(config, model)) {
			rule := config.Total
			if rule.Basis == UsageLimitBasisCredit && exceedsCredit(spent[key], rule.AmountUSD) {
				return usageLimitRejection("total_credit", model, key), true
			}
			if rule.Basis == UsageLimitBasisAccount && identity != "" {
				if percent, ok := s.accountPercent(identity, rule.Window); ok && percent >= rule.Percent {
					return usageLimitRejection("total_account", model, key), true
				}
			}
		}
	}
	return cpaapi.RequestInterceptResponse{}, false
}

func (s *UsageLimitService) scopeModelSpent(key, model string) int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.creditScopeModelsNanos[key+"\x00"+model]
}

func (s *UsageLimitService) accountPercent(identity, window string) (float64, bool) {
	if s.usage == nil || strings.TrimSpace(identity) == "" {
		return 0, false
	}
	lookup := strings.TrimPrefix(identity, "auth-index:")
	if lookup == identity {
		lookup = strings.TrimPrefix(identity, "auth-id:")
	}
	snapshot := s.usage.Snapshot(lookup)
	if snapshot == nil || snapshot.Codex == nil {
		return 0, false
	}
	usage := snapshot.Codex.FiveHour
	if window == UsageLimitWindowSevenDay {
		usage = snapshot.Codex.SevenDay
	}
	if usage == nil || math.IsNaN(usage.UsedPercent) || math.IsInf(usage.UsedPercent, 0) {
		return 0, false
	}
	return usage.UsedPercent, true
}

func (s *UsageLimitService) snapshotLocked(scope UsageLimitsScope, key string) UsageLimitsSnapshot {
	models := make(map[string]float64)
	prefix := key + "\x00"
	for modelKey, value := range s.creditScopeModelsNanos {
		if strings.HasPrefix(modelKey, prefix) {
			models[strings.TrimPrefix(modelKey, prefix)] = float64(value) / float64(creditNanosPerUSD)
		}
	}
	return UsageLimitsSnapshot{Scope: scope, Config: normalizeUsageLimitsConfig(s.configs[key]), CreditUsedUSD: float64(s.creditScopesNanos[key]) / float64(creditNanosPerUSD), CreditModelUsedUSD: models, UpdatedAt: s.updatedAt, StorageError: s.storageErr}
}

func (s *UsageLimitService) persistLocked() {
	if s.loaded && s.store != "" {
		_ = saveUsageLimits(s.store, persistedUsageLimits{Version: usageLimitStoreVersion, Configs: s.configs, CreditScopesNanos: s.creditScopesNanos, CreditScopeModelsNanos: s.creditScopeModelsNanos, UpdatedAt: s.updatedAt})
	}
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
func usageLimitAccountID(identity string) string {
	identity = strings.TrimSpace(identity)
	for _, prefix := range []string{"auth-index:", "auth-id:"} {
		if strings.HasPrefix(identity, prefix) {
			return strings.TrimPrefix(identity, prefix)
		}
	}
	return identity
}
func normalizeUsageLimitModel(model string) string { return strings.ToLower(strings.TrimSpace(model)) }
func usageLimitRejection(basis, model, scope string) cpaapi.RequestInterceptResponse {
	body, _ := json.Marshal(map[string]string{"error": "usage limit reached", "code": "usage_limit_reached", "basis": basis, "model": model, "scope": scope})
	return cpaapi.RequestInterceptResponse{Terminate: true, StatusCode: http.StatusTooManyRequests, ResponseHeaders: http.Header{"Content-Type": []string{"application/json"}}, ResponseBody: body}
}

func cloneUsageLimitConfigs(input map[string]UsageLimitsConfig) map[string]UsageLimitsConfig {
	out := make(map[string]UsageLimitsConfig, len(input))
	for key, value := range input {
		out[key] = normalizeStoredUsageLimitsConfig(key, value)
	}
	return out
}
func cloneInt64Map(input map[string]int64) map[string]int64 {
	out := make(map[string]int64, len(input))
	for key, value := range input {
		if value > 0 {
			out[key] = value
		}
	}
	return out
}
func usageLimitSaturatingAdd(left, right int64) int64 {
	if right <= 0 || left >= math.MaxInt64-right {
		return math.MaxInt64
	}
	return left + right
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
	if state.Version != usageLimitStoreVersion {
		return persistedUsageLimits{}, fmt.Errorf("unsupported usage limits store version %d", state.Version)
	}
	if state.Configs == nil {
		state.Configs = make(map[string]UsageLimitsConfig)
	}
	return state, nil
}
func saveUsageLimits(path string, state persistedUsageLimits) error {
	return savePrivateJSON(path, state)
}

type ProviderUsageLimitsRequest struct {
	Provider string             `json:"provider"`
	Config   *UsageLimitsConfig `json:"config,omitempty"`
}

func (a *App) handleProviderUsageLimits(req cpaapi.ManagementRequest, save bool) cpaapi.ManagementResponse {
	if a == nil || a.usageLimits == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "usage limits service is unavailable"})
	}
	var request ProviderUsageLimitsRequest
	if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errDecode.Error()})
	}
	provider := strings.TrimSpace(request.Provider)
	if provider == "" {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "provider is required"})
	}
	scope := ProviderUsageLimitsScope(provider)
	if _, errScope := normalizeUsageLimitsScope(scope); errScope != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errScope.Error()})
	}
	if !save {
		return jsonResponse(http.StatusOK, a.usageLimits.Get(scope))
	}
	if request.Config == nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "config is required"})
	}
	snapshot, errSet := a.usageLimits.Set(scope, *request.Config)
	if errSet != nil {
		status := http.StatusBadRequest
		if strings.Contains(errSet.Error(), "persist") || strings.Contains(errSet.Error(), "storage") {
			status = http.StatusServiceUnavailable
		}
		return jsonResponse(status, map[string]any{"error": errSet.Error()})
	}
	return jsonResponse(http.StatusOK, snapshot)
}
