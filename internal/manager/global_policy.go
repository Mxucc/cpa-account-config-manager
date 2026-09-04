package manager

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// GlobalPolicy is the permanent, plugin-wide baseline for account and AI
// provider configuration. Object-level settings and automation policies may
// override these values; nil pointer fields intentionally mean "inherit".
// It contains only configuration metadata and never API keys.
type GlobalPolicy struct {
	Enabled                  bool                              `json:"enabled" yaml:"enabled"`
	Disabled                 *bool                             `json:"disabled,omitempty" yaml:"disabled,omitempty"`
	Priority                 *int                              `json:"priority,omitempty" yaml:"priority,omitempty"`
	ConcurrencyLimit         *int                              `json:"concurrency_limit,omitempty" yaml:"concurrency_limit,omitempty"`
	Concurrency15sLimit      *int                              `json:"concurrency_15s_limit,omitempty" yaml:"concurrency_15s_limit,omitempty"`
	ConcurrencyWindowSeconds *int                              `json:"concurrency_window_seconds,omitempty" yaml:"concurrency_window_seconds,omitempty"`
	QuotaPolicy              *AccountQuotaPolicy               `json:"quota_policy,omitempty" yaml:"quota_policy,omitempty"`
	Note                     *string                           `json:"note,omitempty" yaml:"note,omitempty"`
	Prefix                   *string                           `json:"prefix,omitempty" yaml:"prefix,omitempty"`
	ProxyURL                 *string                           `json:"proxy_url,omitempty" yaml:"proxy_url,omitempty"`
	ProxyProfileID           *string                           `json:"proxy_profile_id,omitempty" yaml:"proxy_profile_id,omitempty"`
	AIProviderProxyProfileID *string                           `json:"ai_provider_proxy_profile_id,omitempty" yaml:"ai_provider_proxy_profile_id,omitempty"`
	Websockets               *bool                             `json:"websockets,omitempty" yaml:"websockets,omitempty"`
	Headers                  *HeaderPatch                      `json:"headers,omitempty" yaml:"headers,omitempty"`
	ModelPolicy              *ModelPolicyPatch                 `json:"model_policy,omitempty" yaml:"model_policy,omitempty"`
	CodexIdentity            ExperimentalCodexIdentitySettings `json:"codex_identity" yaml:"codex_identity"`
}

type GlobalPolicySnapshot struct {
	Policy       GlobalPolicy `json:"policy"`
	StorageError string       `json:"storage_error,omitempty"`
}

const globalPolicyStoreVersion = 1

type persistedGlobalPolicy struct {
	Version int          `json:"version"`
	Policy  GlobalPolicy `json:"policy"`
}

type GlobalPolicyService struct {
	mu         sync.RWMutex
	store      string
	loaded     bool
	storageErr string
	policy     GlobalPolicy
}

func NewGlobalPolicyService() *GlobalPolicyService {
	return &GlobalPolicyService{policy: normalizeGlobalPolicy(GlobalPolicy{})}
}

func globalPolicyStorePath(dataDir string) string {
	return filepath.Join(dataDir, "global-policy.json")
}

func (s *GlobalPolicyService) Configure(config Config, legacy ExperimentalCodexIdentitySettings) {
	if s == nil {
		return
	}
	path := globalPolicyStorePath(normalizeConfig(config).DataDir)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loaded && s.store == path {
		if config.GlobalPolicy != nil {
			s.policy = normalizeGlobalPolicy(*config.GlobalPolicy)
		}
		return
	}
	s.store, s.loaded = path, true
	policy := normalizeGlobalPolicy(GlobalPolicy{})
	loaded, err := loadGlobalPolicy(path)
	if err == nil {
		policy = normalizeGlobalPolicy(loaded)
	} else if !errors.Is(err, os.ErrNotExist) {
		s.storageErr = "global policy state could not be loaded"
	}
	if config.GlobalPolicy != nil {
		policy = normalizeGlobalPolicy(*config.GlobalPolicy)
		if errSave := saveGlobalPolicy(path, policy); errSave != nil {
			s.storageErr = "global policy state could not be persisted"
		}
	} else if err != nil && errors.Is(err, os.ErrNotExist) && !globalIdentityEmpty(legacy) {
		// One-time migration from the former experimental location.
		policy.CodexIdentity = NormalizeExperimentalCodexIdentitySettings(legacy)
		if errSave := saveGlobalPolicy(path, policy); errSave != nil {
			s.storageErr = "global policy state could not be persisted"
		}
	}
	s.policy = policy
}

func (s *GlobalPolicyService) Snapshot() GlobalPolicySnapshot {
	if s == nil {
		return GlobalPolicySnapshot{Policy: normalizeGlobalPolicy(GlobalPolicy{})}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return GlobalPolicySnapshot{Policy: cloneGlobalPolicy(s.policy), StorageError: s.storageErr}
}

func (s *GlobalPolicyService) Set(policy GlobalPolicy) (GlobalPolicySnapshot, error) {
	if s == nil {
		return GlobalPolicySnapshot{}, fmt.Errorf("global policy service is unavailable")
	}
	policy = normalizeGlobalPolicy(policy)
	if err := validateGlobalPolicy(policy); err != nil {
		return GlobalPolicySnapshot{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(s.store) == "" {
		return GlobalPolicySnapshot{}, fmt.Errorf("global policy storage is unavailable")
	}
	if err := saveGlobalPolicy(s.store, policy); err != nil {
		return GlobalPolicySnapshot{}, fmt.Errorf("save global policy: %w", err)
	}
	s.policy, s.storageErr = policy, ""
	return GlobalPolicySnapshot{Policy: cloneGlobalPolicy(policy)}, nil
}

func globalIdentityEmpty(value ExperimentalCodexIdentitySettings) bool {
	return !value.OutboundConvergenceEnabled && !value.IngressGateEnabled && !value.AllowAppServerClients &&
		strings.TrimSpace(value.ConvergenceMode) == "" && strings.TrimSpace(value.MinVersion) == "" && strings.TrimSpace(value.MaxVersion) == "" &&
		strings.TrimSpace(value.Whitelist) == "" && strings.TrimSpace(value.Blacklist) == "" && strings.TrimSpace(value.FingerprintSignals) == ""
}

// codexIdentityOverrideFromGlobal converts the global identity settings into
// the subset that can be persisted as an account-level override. Global
// convergence and ingress settings are applied by the request hook; only
// fields represented by CodexIdentityOverride belong in the automatic policy
// patch.
func codexIdentityOverrideFromGlobal(value ExperimentalCodexIdentitySettings) *CodexIdentityOverride {
	override := CodexIdentityOverride{}
	if mode := strings.TrimSpace(value.ConvergenceMode); mode != "" {
		override.ConvergenceMode = &mode
	}
	if value.IngressGateEnabled {
		enabled := true
		override.IngressGateEnabled = &enabled
	}
	if value.AllowAppServerClients {
		enabled := true
		override.AllowAppServerClients = &enabled
	}
	if override.ConvergenceMode == nil && override.IngressGateEnabled == nil && override.AllowAppServerClients == nil {
		return nil
	}
	return &override
}

func normalizeGlobalPolicy(policy GlobalPolicy) GlobalPolicy {
	clone := cloneGlobalPolicy(policy)
	if clone.ProxyProfileID != nil {
		value := strings.ToLower(strings.TrimSpace(*clone.ProxyProfileID))
		clone.ProxyProfileID = &value
	}
	if clone.QuotaPolicy != nil {
		value := *clone.QuotaPolicy
		clone.QuotaPolicy = &value
	}
	if clone.ModelPolicy != nil {
		value := cloneModelPolicyPatch(*clone.ModelPolicy)
		clone.ModelPolicy = &value
	}
	clone.CodexIdentity = NormalizeExperimentalCodexIdentitySettings(clone.CodexIdentity)
	return clone
}

func validateGlobalPolicy(policy GlobalPolicy) error {
	if policy.ConcurrencyLimit != nil && (*policy.ConcurrencyLimit < 0 || *policy.ConcurrencyLimit > MaxAccountConcurrencyLimit) {
		return fmt.Errorf("account concurrency must be between 0 and %d", MaxAccountConcurrencyLimit)
	}
	if policy.Concurrency15sLimit != nil && (*policy.Concurrency15sLimit < 0 || *policy.Concurrency15sLimit > MaxAccountConcurrencyLimit) {
		return fmt.Errorf("account request limit must be between 0 and %d", MaxAccountConcurrencyLimit)
	}
	if policy.ConcurrencyWindowSeconds != nil && !validAccountConcurrencyWindowSeconds(*policy.ConcurrencyWindowSeconds) {
		return fmt.Errorf("account request window must be between %d and %d seconds", MinAccountConcurrencyWindowSeconds, MaxAccountConcurrencyWindowSeconds)
	}
	if policy.QuotaPolicy != nil {
		if err := validateQuotaPolicy(policy.QuotaPolicy.FiveHour); err != nil {
			return err
		}
		if err := validateQuotaPolicy(policy.QuotaPolicy.SevenDay); err != nil {
			return err
		}
	}
	if policy.CodexIdentity.OutboundConvergenceEnabled || policy.CodexIdentity.IngressGateEnabled || !globalIdentityEmpty(policy.CodexIdentity) {
		if err := ValidateExperimentalCodexIdentitySettings(policy.CodexIdentity); err != nil {
			return err
		}
	}
	return nil
}

func cloneGlobalPolicy(policy GlobalPolicy) GlobalPolicy {
	clone := policy
	clone.Disabled = cloneBoolPointer(policy.Disabled)
	clone.Priority = cloneIntPointer(policy.Priority)
	clone.ConcurrencyLimit = cloneIntPointer(policy.ConcurrencyLimit)
	clone.Concurrency15sLimit = cloneIntPointer(policy.Concurrency15sLimit)
	clone.ConcurrencyWindowSeconds = cloneIntPointer(policy.ConcurrencyWindowSeconds)
	clone.Note = cloneStringPointer(policy.Note)
	clone.Prefix = cloneStringPointer(policy.Prefix)
	clone.ProxyURL = cloneStringPointer(policy.ProxyURL)
	clone.ProxyProfileID = cloneStringPointer(policy.ProxyProfileID)
	clone.AIProviderProxyProfileID = cloneStringPointer(policy.AIProviderProxyProfileID)
	clone.Websockets = cloneBoolPointer(policy.Websockets)
	if policy.Headers != nil {
		value := HeaderPatch{Set: map[string]string{}, Remove: append([]string(nil), policy.Headers.Remove...)}
		for k, v := range policy.Headers.Set {
			value.Set[k] = v
		}
		clone.Headers = &value
	}
	if policy.ModelPolicy != nil {
		value := cloneModelPolicyPatch(*policy.ModelPolicy)
		clone.ModelPolicy = &value
	}
	if policy.QuotaPolicy != nil {
		value := *policy.QuotaPolicy
		clone.QuotaPolicy = &value
	}
	return clone
}

func loadGlobalPolicy(path string) (GlobalPolicy, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return GlobalPolicy{}, err
	}
	var persisted persistedGlobalPolicy
	if err := json.Unmarshal(data, &persisted); err != nil {
		return GlobalPolicy{}, err
	}
	if persisted.Version != 0 && persisted.Version != globalPolicyStoreVersion {
		return GlobalPolicy{}, fmt.Errorf("unsupported global policy store version %d", persisted.Version)
	}
	return persisted.Policy, nil
}

func saveGlobalPolicy(path string, policy GlobalPolicy) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	data, err := json.MarshalIndent(persistedGlobalPolicy{Version: globalPolicyStoreVersion, Policy: policy}, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".global-policy-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o640); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
