package manager

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
)

type ExperimentalSettings struct {
	WeeklyOverdraftEnabled    bool                              `json:"weekly_overdraft_enabled" yaml:"weekly_overdraft_enabled"`
	AgentIdentityEnabled      bool                              `json:"agent_identity_enabled" yaml:"agent_identity_enabled"`
	AutoModelWhitelistEnabled bool                              `json:"auto_model_whitelist_enabled" yaml:"auto_model_whitelist_enabled"`
	Sub2APICreditUsageEnabled bool                              `json:"sub2api_credit_usage_enabled" yaml:"sub2api_credit_usage_enabled"`
	CodexIdentity             ExperimentalCodexIdentitySettings `json:"codex_identity" yaml:"codex_identity"`
}

// ExperimentalCodexIdentitySettings mirrors the reference fork's Codex
// identity convergence and codex_cli_only access gate. Advanced values are
// stored as JSON strings so the Management API remains allow-listed and can
// reject malformed policy before it reaches the request path.
type ExperimentalCodexIdentitySettings struct {
	OutboundConvergenceEnabled bool   `json:"outbound_convergence_enabled" yaml:"outbound_convergence_enabled"`
	IngressGateEnabled         bool   `json:"ingress_gate_enabled" yaml:"ingress_gate_enabled"`
	AllowAppServerClients      bool   `json:"allow_app_server_clients" yaml:"allow_app_server_clients"`
	ConvergenceMode            string `json:"convergence_mode,omitempty" yaml:"convergence_mode,omitempty"`
	MinVersion                 string `json:"min_version,omitempty" yaml:"min_version,omitempty"`
	MaxVersion                 string `json:"max_version,omitempty" yaml:"max_version,omitempty"`
	Whitelist                  string `json:"whitelist,omitempty" yaml:"whitelist,omitempty"`
	Blacklist                  string `json:"blacklist,omitempty" yaml:"blacklist,omitempty"`
	FingerprintSignals         string `json:"fingerprint_signals,omitempty" yaml:"fingerprint_signals,omitempty"`
}

func (s *ExperimentalSettingsService) AgentIdentityEnabled() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	enabled := s.settings.AgentIdentityEnabled
	s.mu.RUnlock()
	return enabled
}

func (s *ExperimentalSettingsService) AutoModelWhitelistEnabled() bool {
	return true
}

func normalizeExperimentalSettings(settings ExperimentalSettings) ExperimentalSettings {
	settings.AutoModelWhitelistEnabled = true
	settings.CodexIdentity = NormalizeExperimentalCodexIdentitySettings(settings.CodexIdentity)
	return settings
}

// ValidateExperimentalCodexIdentitySettings rejects malformed advanced values
// at the Management boundary so bad policy is never persisted.
func ValidateExperimentalCodexIdentitySettings(settings ExperimentalCodexIdentitySettings) error {
	if !validCodexFingerprintMode(settings.ConvergenceMode) {
		return fmt.Errorf("convergence_mode must be empty, off, device, session, or full")
	}
	for name, version := range map[string]string{"min_version": settings.MinVersion, "max_version": settings.MaxVersion} {
		if version == "" {
			continue
		}
		if _, ok := parseCodexEngineVersion("codex_cli_rs/" + version); !ok || compareVersions(version, "0.0.0") < 0 {
			return fmt.Errorf("%s: invalid semantic version %q", name, version)
		}
	}
	if settings.MinVersion != "" && settings.MaxVersion != "" && compareVersions(settings.MaxVersion, settings.MinVersion) < 0 {
		return fmt.Errorf("max_version is lower than min_version")
	}
	if err := validateCodexAllowedClientsJSON(settings.Whitelist, true); err != nil {
		return fmt.Errorf("whitelist: %w", err)
	}
	if err := validateCodexAllowedClientsJSON(settings.Blacklist, false); err != nil {
		return fmt.Errorf("blacklist: %w", err)
	}
	if err := validateEngineFingerprintSignalsJSON(settings.FingerprintSignals); err != nil {
		return fmt.Errorf("fingerprint_signals: %w", err)
	}
	return nil
}

// NormalizeExperimentalCodexIdentitySettings turns malformed legacy or loaded
// advanced values into safe empty policy instead of panicking during startup.
func NormalizeExperimentalCodexIdentitySettings(settings ExperimentalCodexIdentitySettings) ExperimentalCodexIdentitySettings {
	if ValidateExperimentalCodexIdentitySettings(settings) != nil {
		settings.ConvergenceMode = ""
		settings.MinVersion = ""
		settings.MaxVersion = ""
		settings.Whitelist = ""
		settings.Blacklist = ""
		settings.FingerprintSignals = ""
		return settings
	}
	settings.MinVersion = strings.TrimSpace(settings.MinVersion)
	settings.MaxVersion = strings.TrimSpace(settings.MaxVersion)
	settings.ConvergenceMode = string(normalizeCodexFingerprintMode(settings.ConvergenceMode))
	return settings
}

func (s *ExperimentalSettingsService) codexIdentitySnapshot() ExperimentalCodexIdentitySettings {
	if s == nil {
		return ExperimentalCodexIdentitySettings{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return normalizeExperimentalSettings(ExperimentalSettings{CodexIdentity: s.settings.CodexIdentity}).CodexIdentity
}

func (s *ExperimentalSettingsService) CodexIdentity() ExperimentalCodexIdentitySettings {
	if s == nil {
		return ExperimentalCodexIdentitySettings{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.settings.CodexIdentity
}

type ExperimentalSettingsSnapshot struct {
	Settings     ExperimentalSettings `json:"settings"`
	StorageError string               `json:"storage_error,omitempty"`
}

type ExperimentalSettingsService struct {
	mu                        sync.RWMutex
	storeMu                   sync.Mutex
	store                     string
	settings                  ExperimentalSettings
	storageErr                string
	configured                bool
	loadFailed                bool
	weeklyOverdraftEnabled    atomic.Bool
	sub2APICreditUsageEnabled atomic.Bool
	codexIdentityEnabled      atomic.Bool
}

func NewExperimentalSettingsService() *ExperimentalSettingsService {
	config := normalizeConfig(Config{})
	return &ExperimentalSettingsService{store: experimentalSettingsStorePath(config.DataDir)}
}

func (s *ExperimentalSettingsService) Configure(config Config) {
	if s == nil {
		return
	}
	config = normalizeConfig(config)
	storePath := experimentalSettingsStorePath(config.DataDir)
	s.mu.RLock()
	sameStore := s.configured && s.store == storePath
	s.mu.RUnlock()
	if sameStore && !s.loadFailed {
		if config.ExperimentalSettings != nil {
			if _, errSet := s.Set(*config.ExperimentalSettings); errSet != nil {
				s.mu.Lock()
				s.storageErr = "experimental settings could not be persisted"
				s.mu.Unlock()
			}
		}
		return
	}
	settings := normalizeExperimentalSettings(ExperimentalSettings{})
	storageErr := ""
	loaded, errLoad := loadExperimentalSettings(storePath)
	if errLoad == nil {
		settings = normalizeExperimentalSettings(loaded)
	} else if !errors.Is(errLoad, os.ErrNotExist) {
		storageErr = "experimental settings could not be loaded"
	}
	if config.ExperimentalSettings != nil {
		settings = normalizeExperimentalSettings(*config.ExperimentalSettings)
		s.storeMu.Lock()
		if errSave := saveExperimentalSettings(storePath, settings); errSave != nil {
			storageErr = "experimental settings could not be persisted"
		}
		s.storeMu.Unlock()
	}
	s.mu.Lock()
	s.store = storePath
	s.settings = settings
	s.storageErr = storageErr
	s.loadFailed = storageErr == "experimental settings could not be loaded"
	s.configured = true
	s.mu.Unlock()
	s.weeklyOverdraftEnabled.Store(settings.WeeklyOverdraftEnabled)
	s.sub2APICreditUsageEnabled.Store(settings.Sub2APICreditUsageEnabled)
	s.codexIdentityEnabled.Store(settings.CodexIdentity.OutboundConvergenceEnabled || settings.CodexIdentity.IngressGateEnabled)
}

func (s *ExperimentalSettingsService) Snapshot() ExperimentalSettingsSnapshot {
	if s == nil {
		return ExperimentalSettingsSnapshot{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return ExperimentalSettingsSnapshot{Settings: normalizeExperimentalSettings(s.settings), StorageError: s.storageErr}
}

func (s *ExperimentalSettingsService) WeeklyOverdraftEnabled() bool {
	if s == nil {
		return false
	}
	return s.weeklyOverdraftEnabled.Load()
}

func (s *ExperimentalSettingsService) Sub2APICreditUsageEnabled() bool {
	if s == nil {
		return false
	}
	return s.sub2APICreditUsageEnabled.Load()
}

func (s *ExperimentalSettingsService) Set(settings ExperimentalSettings) (ExperimentalSettingsSnapshot, error) {
	if s == nil {
		return ExperimentalSettingsSnapshot{}, fmt.Errorf("experimental settings are unavailable")
	}
	s.mu.RLock()
	storePath := s.store
	configured := s.configured
	s.mu.RUnlock()
	if !configured || strings.TrimSpace(storePath) == "" {
		return ExperimentalSettingsSnapshot{}, fmt.Errorf("experimental settings storage is unavailable")
	}
	settings = normalizeExperimentalSettings(settings)
	if errValidate := ValidateExperimentalCodexIdentitySettings(settings.CodexIdentity); errValidate != nil {
		return ExperimentalSettingsSnapshot{}, errValidate
	}
	s.storeMu.Lock()
	errSave := saveExperimentalSettings(storePath, settings)
	s.storeMu.Unlock()
	if errSave != nil {
		return ExperimentalSettingsSnapshot{}, fmt.Errorf("save experimental settings: %w", errSave)
	}
	s.mu.Lock()
	s.settings = settings
	s.storageErr = ""
	s.loadFailed = false
	s.mu.Unlock()
	s.weeklyOverdraftEnabled.Store(settings.WeeklyOverdraftEnabled)
	s.sub2APICreditUsageEnabled.Store(settings.Sub2APICreditUsageEnabled)
	s.codexIdentityEnabled.Store(settings.CodexIdentity.OutboundConvergenceEnabled || settings.CodexIdentity.IngressGateEnabled)
	return s.Snapshot(), nil
}
