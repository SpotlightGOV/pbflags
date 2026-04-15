package evaluator

import (
	"os"
	"time"
)

// parseDurationEnv reads an environment variable as a time.Duration.
// Returns zero and false if the variable is unset or empty.
func parseDurationEnv(key string) (time.Duration, bool) {
	v := os.Getenv(key)
	if v == "" {
		return 0, false
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, false
	}
	return d, true
}

// Config is the shared evaluator/admin configuration.
type Config struct {
	Descriptors string
	Bundle      string
	Upstream    string
	Listen      string
	Admin       string
	Database    string
	Cache       CacheConfig
	EnvName     string
	EnvColor    string
}

// CacheConfig controls cache TTLs and sizes.
type CacheConfig struct {
	KillTTL       time.Duration
	FlagTTL       time.Duration
	JitterPercent int
	FetchTimeout  time.Duration
}

// DefaultConfig returns a Config with all default values applied.
func DefaultConfig() Config {
	return Config{
		Listen: "localhost:9201",
		Cache: CacheConfig{
			KillTTL:       30 * time.Second,
			FlagTTL:       10 * time.Minute,
			JitterPercent: 20,
			FetchTimeout:  500 * time.Millisecond,
		},
	}
}

// LoadConfig reads configuration from environment variable overrides on top
// of defaults. It does not validate the result — each binary is responsible
// for checking that the fields it requires are populated.
func LoadConfig() Config {
	cfg := DefaultConfig()

	if v := os.Getenv("PBFLAGS_DESCRIPTORS"); v != "" {
		cfg.Descriptors = v
	}
	if v := os.Getenv("PBFLAGS_BUNDLE"); v != "" {
		cfg.Bundle = v
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
	if d, ok := parseDurationEnv("PBFLAGS_CACHE_KILL_TTL"); ok {
		cfg.Cache.KillTTL = d
	}
	if d, ok := parseDurationEnv("PBFLAGS_CACHE_FLAG_TTL"); ok {
		cfg.Cache.FlagTTL = d
	}
	return cfg
}
