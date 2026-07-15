package manager

import (
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	defaultWorkers = 6
	maxWorkers     = 16
)

type Config struct {
	Workers           int    `yaml:"workers"`
	DataDir           string `yaml:"data_dir"`
	ManagementBaseURL string `yaml:"management_base_url"`
}

func ParseConfig(raw []byte) Config {
	cfg := Config{}
	if len(raw) > 0 {
		_ = yaml.Unmarshal(raw, &cfg)
	}
	return normalizeConfig(cfg)
}

func normalizeConfig(cfg Config) Config {
	if cfg.Workers <= 0 {
		cfg.Workers = defaultWorkers
	}
	if cfg.Workers > maxWorkers {
		cfg.Workers = maxWorkers
	}
	cfg.DataDir = strings.TrimSpace(cfg.DataDir)
	if cfg.DataDir == "" {
		cfg.DataDir = strings.TrimSpace(os.Getenv("CPA_ACCOUNT_CONFIG_MANAGER_DATA_DIR"))
	}
	if cfg.DataDir == "" {
		cfg.DataDir = "data/cpa-account-config-manager"
	}
	cfg.ManagementBaseURL = strings.TrimRight(strings.TrimSpace(cfg.ManagementBaseURL), "/")
	return cfg
}
