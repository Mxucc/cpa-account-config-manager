package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"cpa-account-config-manager/internal/magpiecompat"
	"cpa-account-config-manager/internal/provider"
)

// ProviderSettings are the provider-domain switches. They live beside the
// imported-provider store rather than in the experimental settings, because they
// only affect the provider surface.
//
// MagpieHookExecution defaults to false: reading a plugin's models and allowance
// runs third-party code, so it is an explicit opt-in and never a side effect of
// importing a manifest.
type ProviderSettings struct {
	MagpieHookExecution bool `json:"magpie_hook_execution"`
}

type providerSettingsFile struct {
	Version  int              `json:"version"`
	Settings ProviderSettings `json:"settings"`
}

// ProviderSettingsService persists the switches.
type ProviderSettingsService struct {
	mu      sync.Mutex
	path    string
	loaded  bool
	value   ProviderSettings
	lastErr string
}

func NewProviderSettingsService(path string) *ProviderSettingsService {
	return &ProviderSettingsService{path: path}
}

func (s *ProviderSettingsService) loadLocked() {
	if s.loaded || s.path == "" {
		return
	}
	s.loaded = true
	raw, errRead := os.ReadFile(s.path)
	if errRead != nil {
		if !os.IsNotExist(errRead) {
			s.lastErr = errRead.Error()
		}
		return
	}
	var stored providerSettingsFile
	if errUnmarshal := json.Unmarshal(raw, &stored); errUnmarshal != nil {
		s.lastErr = "provider settings are unreadable"
		return
	}
	s.value = stored.Settings
}

// Get returns the current switches.
func (s *ProviderSettingsService) Get() ProviderSettings {
	if s == nil {
		return ProviderSettings{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	return s.value
}

// MagpieHookExecution reports whether hooks may run.
func (s *ProviderSettingsService) MagpieHookExecution() bool {
	return s.Get().MagpieHookExecution
}

// Set persists the switches.
func (s *ProviderSettingsService) Set(settings ProviderSettings) error {
	if s == nil {
		return fmt.Errorf("provider settings are unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	previous := s.value
	s.value = settings
	if s.path == "" {
		// Without a data directory the switch still applies to this process; it is
		// simply not persisted, which the caller can see in the response.
		return nil
	}
	if errWrite := s.persistLocked(); errWrite != nil {
		s.value = previous
		return errWrite
	}
	return nil
}

func (s *ProviderSettingsService) persistLocked() error {
	if errMkdir := os.MkdirAll(filepath.Dir(s.path), 0o755); errMkdir != nil {
		return errMkdir
	}
	raw, errMarshal := json.MarshalIndent(providerSettingsFile{Version: 1, Settings: s.value}, "", "  ")
	if errMarshal != nil {
		return errMarshal
	}
	temporary := s.path + ".tmp"
	if errWrite := os.WriteFile(temporary, raw, 0o600); errWrite != nil {
		return errWrite
	}
	return os.Rename(temporary, s.path)
}

// providerSettingsPath mirrors the import store's location.
func providerSettingsPath(dataDir string) string {
	if strings.TrimSpace(dataDir) == "" {
		return ""
	}
	return filepath.Join(dataDir, "provider-settings.json")
}

// pluginHookRunner is the seam the provider source uses to execute hooks. Tests
// replace it so a probe never depends on Bun being installed.
type pluginHookRunner interface {
	Probe(directory string) (provider.MagpiePluginInput, error)
}

// defaultPluginHookRunner adapts magpiecompat to the provider source.
type defaultPluginHookRunner struct{}

func (defaultPluginHookRunner) Probe(directory string) (provider.MagpiePluginInput, error) {
	result, errProbe := magpiecompat.ProbeWith(context.Background(), directory)
	if errProbe != nil {
		return provider.MagpiePluginInput{}, errProbe
	}
	windows := make([]provider.MagpieWindow, 0, len(result.Windows))
	for _, window := range result.Windows {
		windows = append(windows, provider.MagpieWindow{
			Name: window.Name, Used: window.Used, Limit: window.Limit, Balance: window.Balance,
			Unit: window.Unit, Reset: window.Reset, Aside: window.Aside,
		})
	}
	return provider.MagpiePluginInput{
		ProviderID: result.ID,
		Models:     result.Models,
		Methods:    result.Methods,
		Windows:    windows,
		Warnings:   result.Warnings,
		UpdatedAt:  time.Now().UTC(),
	}, nil
}
