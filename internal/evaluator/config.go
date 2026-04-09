package evaluator

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

func normalizeAddr(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return addr
	}
	if _, port, ok := strings.Cut(addr, ":"); ok {
		return ":" + port
	}
	return ":" + addr
}

// Config is the evaluator configuration.
type Config struct {
	Descriptors string      `yaml:"descriptors"`
	Server      string      `yaml:"server"`
	Listen      string      `yaml:"listen"`
	Admin       string      `yaml:"admin"`
	Database    string      `yaml:"database"`
	Cache       CacheConfig `yaml:"cache"`
	EnvName     string      `yaml:"env_name"`
	EnvColor    string      `yaml:"env_color"`
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
			FlagTTL:         5 * time.Minute,
			OverrideTTL:     5 * time.Minute,
			OverrideMaxSize: 10_000,
			JitterPercent:   20,
			FetchTimeout:    500 * time.Millisecond,
		},
	}
}

// LoadConfig reads configuration from a YAML file, applying defaults for unset fields.
// Environment variables override file values:
//
//	PBFLAGS_DESCRIPTORS → Descriptors
//	PBFLAGS_SERVER      → Server
//	PBFLAGS_LISTEN      → Listen
//	PBFLAGS_ADMIN       → Admin
//	PBFLAGS_DATABASE    → Database
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
	if v := os.Getenv("PBFLAGS_SERVER"); v != "" {
		cfg.Server = v
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

	if cfg.Descriptors == "" {
		return Config{}, fmt.Errorf("config: descriptors path is required")
	}

	if cfg.Admin != "" && cfg.Database == "" {
		return Config{}, fmt.Errorf("config: database is required when admin is enabled")
	}

	if cfg.Database == "" && cfg.Server == "" {
		return Config{}, fmt.Errorf("config: either database (root mode) or server (proxy mode) is required")
	}

	return cfg, nil
}
