package manager

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	defaultWorkers = 6
	maxWorkers     = 16
)

type Config struct {
	Workers              int                      `yaml:"workers"`
	DataDir              string                   `yaml:"data_dir"`
	ManagementBaseURL    string                   `yaml:"management_base_url"`
	DefaultPolicy        *DefaultPolicy           `yaml:"default_policy,omitempty"`
	InspectionPolicy     *InspectionPolicy        `yaml:"inspection_policy,omitempty"`
	UpdatePolicy         *UpdatePolicy            `yaml:"update_policy,omitempty"`
	OperationSettings    *OperationSettingsConfig `yaml:"operation_settings,omitempty"`
	GlobalPolicy         *GlobalPolicy            `yaml:"global_policy,omitempty"`
	ExperimentalSettings *ExperimentalSettings    `yaml:"experimental_settings,omitempty"`
	implicitDataDir      bool
}

type OperationSettingsConfig struct {
	ExtendedHistory bool `json:"extended_history" yaml:"extended_history"`
}

func ParseConfig(raw []byte) Config {
	cfg, _ := ParseConfigStrict(raw)
	return cfg
}

// ParseConfigStrict parses plugin configuration without silently replacing an
// invalid document with defaults. The host lifecycle path uses this function
// so a malformed live reconfiguration cannot disable persisted automation.
func ParseConfigStrict(raw []byte) (Config, error) {
	cfg := Config{}
	if len(raw) > 0 {
		if errUnmarshal := yaml.Unmarshal(raw, &cfg); errUnmarshal != nil {
			return normalizeConfig(Config{}), fmt.Errorf("invalid plugin configuration")
		}
	}
	return normalizeConfig(cfg), nil
}

func normalizeConfig(cfg Config) Config {
	if cfg.Workers <= 0 {
		cfg.Workers = defaultWorkers
	}
	if cfg.Workers > maxWorkers {
		cfg.Workers = maxWorkers
	}
	cfg.DataDir = strings.TrimSpace(cfg.DataDir)
	if !cfg.implicitDataDir {
		if cfg.DataDir == "" {
			cfg.DataDir = strings.TrimSpace(os.Getenv("CPA_ACCOUNT_CONFIG_MANAGER_DATA_DIR"))
		}
		if cfg.DataDir == "" {
			cfg.DataDir = "data/cpa-account-config-manager"
			cfg.implicitDataDir = true
		}
	}
	cfg.ManagementBaseURL = strings.TrimRight(strings.TrimSpace(cfg.ManagementBaseURL), "/")
	if cfg.DefaultPolicy != nil {
		policy := cloneDefaultPolicy(*cfg.DefaultPolicy)
		cfg.DefaultPolicy = &policy
	}
	if cfg.InspectionPolicy != nil {
		policy := cloneInspectionPolicy(*cfg.InspectionPolicy)
		cfg.InspectionPolicy = &policy
	}
	if cfg.UpdatePolicy != nil {
		policy := *cfg.UpdatePolicy
		cfg.UpdatePolicy = &policy
	}
	if cfg.OperationSettings != nil {
		settings := *cfg.OperationSettings
		cfg.OperationSettings = &settings
	}
	if cfg.GlobalPolicy != nil {
		policy := cloneGlobalPolicy(*cfg.GlobalPolicy)
		cfg.GlobalPolicy = &policy
	}
	if cfg.ExperimentalSettings != nil {
		settings := *cfg.ExperimentalSettings
		cfg.ExperimentalSettings = &settings
	}
	return cfg
}
