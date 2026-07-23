package manager

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
)

type ExperimentalSettings struct {
	WeeklyOverdraftEnabled bool `json:"weekly_overdraft_enabled" yaml:"weekly_overdraft_enabled"`
	AgentIdentityEnabled   bool `json:"agent_identity_enabled" yaml:"agent_identity_enabled"`
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

type ExperimentalSettingsSnapshot struct {
	Settings     ExperimentalSettings `json:"settings"`
	StorageError string               `json:"storage_error,omitempty"`
}

type ExperimentalSettingsService struct {
	mu         sync.RWMutex
	storeMu    sync.Mutex
	store      string
	settings   ExperimentalSettings
	storageErr string
	configured bool
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
	if sameStore {
		if config.ExperimentalSettings != nil {
			if _, errSet := s.Set(*config.ExperimentalSettings); errSet != nil {
				s.mu.Lock()
				s.storageErr = "experimental settings could not be persisted"
				s.mu.Unlock()
			}
		}
		return
	}
	settings := ExperimentalSettings{}
	storageErr := ""
	loaded, errLoad := loadExperimentalSettings(storePath)
	if errLoad == nil {
		settings = loaded
	} else if !errors.Is(errLoad, os.ErrNotExist) {
		storageErr = "experimental settings could not be loaded"
	}
	if config.ExperimentalSettings != nil {
		settings = *config.ExperimentalSettings
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
	s.configured = true
	s.mu.Unlock()
}

func (s *ExperimentalSettingsService) Snapshot() ExperimentalSettingsSnapshot {
	if s == nil {
		return ExperimentalSettingsSnapshot{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return ExperimentalSettingsSnapshot{Settings: s.settings, StorageError: s.storageErr}
}

func (s *ExperimentalSettingsService) WeeklyOverdraftEnabled() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	enabled := s.settings.WeeklyOverdraftEnabled
	s.mu.RUnlock()
	return enabled
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
	s.storeMu.Lock()
	errSave := saveExperimentalSettings(storePath, settings)
	s.storeMu.Unlock()
	if errSave != nil {
		return ExperimentalSettingsSnapshot{}, fmt.Errorf("save experimental settings: %w", errSave)
	}
	s.mu.Lock()
	s.settings = settings
	s.storageErr = ""
	s.mu.Unlock()
	return s.Snapshot(), nil
}
