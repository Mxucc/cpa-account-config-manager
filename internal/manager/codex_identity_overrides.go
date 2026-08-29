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

const codexIdentityOverrideStoreVersion = 1

var ErrCodexIdentityOverrideStorageUnavailable = errors.New("Codex identity override storage is unavailable")

// CodexIdentityOverride contains only plugin-owned, non-secret policy values.
// Nil fields inherit the global experimental setting. A convergence_mode of
// "off" is an explicit per-target opt-out.
type CodexIdentityOverride struct {
	ConvergenceMode       *string `json:"convergence_mode,omitempty"`
	IngressGateEnabled    *bool   `json:"ingress_gate_enabled,omitempty"`
	AllowAppServerClients *bool   `json:"allow_app_server_clients,omitempty"`
}

type CodexIdentityOverrideSnapshot struct {
	Accounts     map[string]CodexIdentityOverride `json:"accounts"`
	Providers    map[string]CodexIdentityOverride `json:"providers"`
	StorageError string                           `json:"storage_error,omitempty"`
}

type persistedCodexIdentityOverrides struct {
	Version   int                              `json:"version"`
	Accounts  map[string]CodexIdentityOverride `json:"accounts,omitempty"`
	Providers map[string]CodexIdentityOverride `json:"providers,omitempty"`
}

type CodexIdentityOverrideService struct {
	mu         sync.RWMutex
	store      string
	loaded     bool
	storageErr string
	accounts   map[string]CodexIdentityOverride
	providers  map[string]CodexIdentityOverride
}

func NewCodexIdentityOverrideService() *CodexIdentityOverrideService {
	return &CodexIdentityOverrideService{
		accounts:  make(map[string]CodexIdentityOverride),
		providers: make(map[string]CodexIdentityOverride),
	}
}

func codexIdentityOverrideStorePath(dataDir string) string {
	return filepath.Join(dataDir, "codex-identity-overrides.json")
}

func (s *CodexIdentityOverrideService) Configure(config Config) {
	if s == nil {
		return
	}
	path := codexIdentityOverrideStorePath(normalizeConfig(config).DataDir)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loaded && s.store == path {
		return
	}
	s.store, s.loaded = path, true
	persisted, err := loadCodexIdentityOverrides(path)
	if err != nil {
		s.accounts, s.providers = make(map[string]CodexIdentityOverride), make(map[string]CodexIdentityOverride)
		if errors.Is(err, os.ErrNotExist) {
			s.storageErr = ""
		} else {
			s.storageErr = "Codex identity override state could not be loaded"
		}
		return
	}
	s.accounts, s.providers, s.storageErr = persisted.Accounts, persisted.Providers, ""
}

func (s *CodexIdentityOverrideService) Snapshot() CodexIdentityOverrideSnapshot {
	if s == nil {
		return CodexIdentityOverrideSnapshot{Accounts: map[string]CodexIdentityOverride{}, Providers: map[string]CodexIdentityOverride{}}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return CodexIdentityOverrideSnapshot{
		Accounts:     cloneCodexIdentityOverrideMap(s.accounts),
		Providers:    cloneCodexIdentityOverrideMap(s.providers),
		StorageError: s.storageErr,
	}
}

func (s *CodexIdentityOverrideService) HasOverrides() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.accounts) > 0 || len(s.providers) > 0
}

func (s *CodexIdentityOverrideService) Account(id string) (CodexIdentityOverride, bool) {
	return s.lookup(true, id)
}

func (s *CodexIdentityOverrideService) Provider(key string) (CodexIdentityOverride, bool) {
	return s.lookup(false, key)
}

func (s *CodexIdentityOverrideService) lookup(account bool, key string) (CodexIdentityOverride, bool) {
	if s == nil {
		return CodexIdentityOverride{}, false
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return CodexIdentityOverride{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := s.providers
	if account {
		values = s.accounts
	}
	value, ok := values[key]
	return cloneCodexIdentityOverride(value), ok
}

func (s *CodexIdentityOverrideService) SetAccount(id string, value CodexIdentityOverride) error {
	return s.set(true, id, value)
}

func (s *CodexIdentityOverrideService) SetProvider(key string, value CodexIdentityOverride) error {
	return s.set(false, key, value)
}

func (s *CodexIdentityOverrideService) set(account bool, key string, value CodexIdentityOverride) error {
	if s == nil {
		return ErrCodexIdentityOverrideStorageUnavailable
	}
	key = strings.TrimSpace(key)
	if !validCodexIdentityOverrideKey(key) {
		return fmt.Errorf("Codex identity override key is invalid")
	}
	normalized, err := validateCodexIdentityOverride(value)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(s.store) == "" {
		return ErrCodexIdentityOverrideStorageUnavailable
	}
	target := s.providers
	if account {
		target = s.accounts
	}
	previous, existed := target[key]
	if codexIdentityOverrideEmpty(normalized) {
		delete(target, key)
	} else {
		target[key] = normalized
	}
	if errSave := s.persistLocked(); errSave != nil {
		if existed {
			target[key] = previous
		} else {
			delete(target, key)
		}
		s.storageErr = "Codex identity override state could not be persisted"
		return fmt.Errorf("%w: %v", ErrCodexIdentityOverrideStorageUnavailable, errSave)
	}
	s.storageErr = ""
	return nil
}

func (s *CodexIdentityOverrideService) persistLocked() error {
	return savePrivateJSON(s.store, persistedCodexIdentityOverrides{
		Version:   codexIdentityOverrideStoreVersion,
		Accounts:  cloneCodexIdentityOverrideMap(s.accounts),
		Providers: cloneCodexIdentityOverrideMap(s.providers),
	})
}

func loadCodexIdentityOverrides(path string) (persistedCodexIdentityOverrides, error) {
	var persisted persistedCodexIdentityOverrides
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		return persisted, errRead
	}
	if errDecode := json.Unmarshal(raw, &persisted); errDecode != nil {
		return persisted, errDecode
	}
	if persisted.Version != codexIdentityOverrideStoreVersion {
		return persisted, fmt.Errorf("unsupported Codex identity override store version %d", persisted.Version)
	}
	if persisted.Accounts == nil {
		persisted.Accounts = make(map[string]CodexIdentityOverride)
	}
	if persisted.Providers == nil {
		persisted.Providers = make(map[string]CodexIdentityOverride)
	}
	for key, value := range persisted.Accounts {
		normalized, err := validateCodexIdentityOverride(value)
		if !validCodexIdentityOverrideKey(key) || err != nil || codexIdentityOverrideEmpty(normalized) {
			delete(persisted.Accounts, key)
			continue
		}
		persisted.Accounts[key] = normalized
	}
	for key, value := range persisted.Providers {
		normalized, err := validateCodexIdentityOverride(value)
		if !validCodexIdentityOverrideKey(key) || err != nil || codexIdentityOverrideEmpty(normalized) {
			delete(persisted.Providers, key)
			continue
		}
		persisted.Providers[key] = normalized
	}
	return persisted, nil
}

func validCodexIdentityOverrideKey(key string) bool {
	key = strings.TrimSpace(key)
	return key != "" && len(key) <= maxAccountConfigIDLength && !strings.ContainsAny(key, "\r\n")
}

func validateCodexIdentityOverride(value CodexIdentityOverride) (CodexIdentityOverride, error) {
	value = cloneCodexIdentityOverride(value)
	if value.ConvergenceMode != nil {
		mode := strings.ToLower(strings.TrimSpace(*value.ConvergenceMode))
		if mode == "" || !validCodexFingerprintMode(mode) {
			return CodexIdentityOverride{}, fmt.Errorf("convergence_mode must be off, device, session, or full")
		}
		value.ConvergenceMode = stringPointer(mode)
	}
	return value, nil
}

func codexIdentityOverrideEmpty(value CodexIdentityOverride) bool {
	return value.ConvergenceMode == nil && value.IngressGateEnabled == nil && value.AllowAppServerClients == nil
}

func cloneCodexIdentityOverride(value CodexIdentityOverride) CodexIdentityOverride {
	clone := value
	if value.ConvergenceMode != nil {
		clone.ConvergenceMode = stringPointer(*value.ConvergenceMode)
	}
	if value.IngressGateEnabled != nil {
		enabled := *value.IngressGateEnabled
		clone.IngressGateEnabled = &enabled
	}
	if value.AllowAppServerClients != nil {
		enabled := *value.AllowAppServerClients
		clone.AllowAppServerClients = &enabled
	}
	return clone
}

func cloneCodexIdentityOverrideMap(values map[string]CodexIdentityOverride) map[string]CodexIdentityOverride {
	clone := make(map[string]CodexIdentityOverride, len(values))
	for key, value := range values {
		clone[key] = cloneCodexIdentityOverride(value)
	}
	return clone
}
