package evaluator

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the shared evaluator/admin configuration.
type Config struct {
	Descriptors            string        `yaml:"descriptors"`
	Upstream               string        `yaml:"upstream"`
	Listen                 string        `yaml:"listen"`
	Admin                  string        `yaml:"admin"`
	Database               string        `yaml:"database"`
	Cache                  CacheConfig   `yaml:"cache"`
	EnvName                string        `yaml:"env_name"`
	EnvColor               string        `yaml:"env_color"`
	DefinitionPollInterval time.Duration `yaml:"definition_poll_interval"`
}

// CacheConfig controls cache TTLs and sizes.
type CacheConfig struct {
	KillTTL         time.Duration `yaml:"kill_ttl"`
	FlagTTL         time.Duration `yaml:"flag_ttl"`
	OverrideTTL     time.Duration `yaml:"override_ttl"`
	OverrideMaxSize int64         `yaml:"override_max_entries"`
	JitterPercent   int           `yaml:"jitter_percent"`
	FetchTimeout    time.Duration `yaml:"fetch_timeout"`
}

// DefaultConfig returns a Config with all default values applied.
func DefaultConfig() Config {
	return Config{
		Listen: "localhost:9201",
		Cache: CacheConfig{
			KillTTL:         30 * time.Second,
			FlagTTL:         10 * time.Minute,
			OverrideTTL:     10 * time.Minute,
			OverrideMaxSize: 10_000,
			JitterPercent:   20,
			FetchTimeout:    500 * time.Millisecond,
		},
	}
}

// LoadConfig reads configuration from an optional YAML file and environment
// variable overrides. It does not validate the result — each binary is
// responsible for checking that the fields it requires are populated.
func LoadConfig(path string) (Config, error) {
	cfg := DefaultConfig()

	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return Config{}, fmt.Errorf("read config: %w", err)
		}
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return Config{}, fmt.Errorf("parse config: %w", err)
		}
	}

	if v := os.Getenv("PBFLAGS_DESCRIPTORS"); v != "" {
		cfg.Descriptors = v
	}
	if v := os.Getenv("PBFLAGS_UPSTREAM"); v != "" {
		cfg.Upstream = v
	}
	if v := os.Getenv("PBFLAGS_LISTEN"); v != "" {
		cfg.Listen = v
	}
	if v := os.Getenv("PBFLAGS_ADMIN"); v != "" {
		cfg.Admin = v
	}
	if v := os.Getenv("PBFLAGS_DATABASE"); v != "" {
		cfg.Database = v
	}
	if v := os.Getenv("PBFLAGS_ENV_NAME"); v != "" {
		cfg.EnvName = v
	}
	if v := os.Getenv("PBFLAGS_ENV_COLOR"); v != "" {
		cfg.EnvColor = v
	}

	return cfg, nil
}
