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

// ServerMode indicates how the server handles flag definitions.
type ServerMode int

const (
	// ModeClassic is the legacy mode: parse descriptors into memory, no DB
	// definition loading. Preserved for backward compatibility when
	// --database is provided without --descriptors (root mode without
	// DB-centric definitions).
	ModeClassic ServerMode = iota
	// ModeMonolithic is a single-instance root evaluator that handles
	// migrations, descriptor sync, and definition loading from DB.
	// Inferred when both --descriptors and --database are present.
	// Always runs migrations automatically.
	ModeMonolithic
	// ModeDistributed is a root evaluator that loads definitions from DB
	// only. An external pbflags-sync handles migrations and sync.
	// Requires explicit --distributed flag.
	ModeDistributed
	// ModeProxy connects to an upstream evaluator; no DB or descriptors.
	ModeProxy
)

// Config is the evaluator configuration.
type Config struct {
	Descriptors            string        `yaml:"descriptors"`
	Upstream               string        `yaml:"upstream"`
	Listen                 string        `yaml:"listen"`
	Admin                  string        `yaml:"admin"`
	Database               string        `yaml:"database"`
	Cache                  CacheConfig   `yaml:"cache"`
	EnvName                string        `yaml:"env_name"`
	EnvColor               string        `yaml:"env_color"`
	Mode                   ServerMode    `yaml:"-"` // Resolved from CLI flags, not YAML.
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
			FlagTTL:         5 * time.Minute,
			OverrideTTL:     5 * time.Minute,
			OverrideMaxSize: 10_000,
			JitterPercent:   20,
			FetchTimeout:    500 * time.Millisecond,
		},
	}
}

// LoadConfigWithMode reads configuration and applies the given mode.
func LoadConfigWithMode(path string, mode ServerMode, defPollInterval time.Duration) (Config, error) {
	cfg, err := loadConfigBase(path)
	if err != nil {
		return Config{}, err
	}
	cfg.Mode = mode
	if defPollInterval > 0 {
		cfg.DefinitionPollInterval = defPollInterval
	}
	if err := validateConfig(&cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// LoadConfig reads config for legacy/classic mode invocations.
func LoadConfig(path string) (Config, error) {
	cfg, err := loadConfigBase(path)
	if err != nil {
		return Config{}, err
	}
	if err := validateConfig(&cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func loadConfigBase(path string) (Config, error) {
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

func validateConfig(cfg *Config) error {
	switch cfg.Mode {
	case ModeMonolithic:
		if cfg.Descriptors == "" {
			return fmt.Errorf("config: --descriptors is required when --database is set")
		}
		if cfg.Database == "" {
			return fmt.Errorf("config: --database is required")
		}
		if cfg.Upstream != "" {
			return fmt.Errorf("config: --upstream cannot be combined with --descriptors and --database")
		}
	case ModeDistributed:
		if cfg.Database == "" {
			return fmt.Errorf("config: --database is required in distributed mode")
		}
		if cfg.Descriptors != "" {
			return fmt.Errorf("config: --descriptors is not valid in distributed mode; use pbflags-sync instead")
		}
		if cfg.Upstream != "" {
			return fmt.Errorf("config: --upstream is not valid in distributed mode")
		}
	case ModeProxy:
		if cfg.Upstream == "" {
			return fmt.Errorf("config: --upstream is required in proxy mode")
		}
	default:
		// Classic / legacy mode: infer from flags.
		if cfg.Descriptors == "" {
			return fmt.Errorf("config: descriptors path is required")
		}
		if cfg.Admin != "" && cfg.Database == "" {
			return fmt.Errorf("config: database is required when admin is enabled")
		}
		if cfg.Database == "" && cfg.Upstream == "" {
			return fmt.Errorf("config: either database (root mode) or upstream (proxy mode) is required")
		}
	}
	return nil
}
