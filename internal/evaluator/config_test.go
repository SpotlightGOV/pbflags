package evaluator

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseDurationEnv(t *testing.T) {
	tests := []struct {
		name    string
		envVal  string
		wantDur time.Duration
		wantOK  bool
	}{
		{
			name:    "valid duration",
			envVal:  "5s",
			wantDur: 5 * time.Second,
			wantOK:  true,
		},
		{
			name:    "valid complex duration",
			envVal:  "2m30s",
			wantDur: 2*time.Minute + 30*time.Second,
			wantOK:  true,
		},
		{
			name:    "invalid duration",
			envVal:  "not-a-duration",
			wantDur: 0,
			wantOK:  false,
		},
		{
			name:    "empty string",
			envVal:  "",
			wantDur: 0,
			wantOK:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// t.Setenv — cannot be parallel
			const key = "TEST_PARSE_DURATION_ENV"
			if tt.envVal != "" {
				t.Setenv(key, tt.envVal)
			}

			got, ok := parseDurationEnv(key)
			require.Equal(t, tt.wantOK, ok)
			require.Equal(t, tt.wantDur, got)
		})
	}
}

func TestParseDurationEnv_Unset(t *testing.T) {
	// Verify behavior when the variable is truly unset (no Setenv call at all).
	// Cannot be parallel because other subtests in the suite may set the same key.
	const key = "TEST_PARSE_DURATION_ENV_UNSET"
	got, ok := parseDurationEnv(key)
	require.False(t, ok)
	require.Equal(t, time.Duration(0), got)
}

func TestConfig_Default(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig()

	require.Equal(t, "", cfg.Descriptors)
	require.Equal(t, "", cfg.Upstream)
	require.Equal(t, "localhost:9201", cfg.Listen)
	require.Equal(t, "", cfg.Admin)
	require.Equal(t, "", cfg.Database)
	require.Equal(t, "", cfg.EnvName)
	require.Equal(t, "", cfg.EnvColor)
	require.Equal(t, 30*time.Second, cfg.Cache.KillTTL)
	require.Equal(t, 10*time.Minute, cfg.Cache.FlagTTL)
	require.Equal(t, 20, cfg.Cache.JitterPercent)
	require.Equal(t, 500*time.Millisecond, cfg.Cache.FetchTimeout)
}

func TestConfig_Load(t *testing.T) {
	tests := []struct {
		name   string
		envKey string
		envVal string
		check  func(t *testing.T, cfg Config)
	}{
		{
			name:   "PBFLAGS_DESCRIPTORS override",
			envKey: "PBFLAGS_DESCRIPTORS",
			envVal: "/tmp/descriptors.binpb",
			check: func(t *testing.T, cfg Config) {
				require.Equal(t, "/tmp/descriptors.binpb", cfg.Descriptors)
			},
		},
		{
			name:   "PBFLAGS_UPSTREAM override",
			envKey: "PBFLAGS_UPSTREAM",
			envVal: "http://upstream:8080",
			check: func(t *testing.T, cfg Config) {
				require.Equal(t, "http://upstream:8080", cfg.Upstream)
			},
		},
		{
			name:   "PBFLAGS_LISTEN override",
			envKey: "PBFLAGS_LISTEN",
			envVal: "0.0.0.0:9999",
			check: func(t *testing.T, cfg Config) {
				require.Equal(t, "0.0.0.0:9999", cfg.Listen)
			},
		},
		{
			name:   "PBFLAGS_ADMIN override",
			envKey: "PBFLAGS_ADMIN",
			envVal: "localhost:9202",
			check: func(t *testing.T, cfg Config) {
				require.Equal(t, "localhost:9202", cfg.Admin)
			},
		},
		{
			name:   "PBFLAGS_DATABASE override",
			envKey: "PBFLAGS_DATABASE",
			envVal: "postgres://user:pass@localhost/db",
			check: func(t *testing.T, cfg Config) {
				require.Equal(t, "postgres://user:pass@localhost/db", cfg.Database)
			},
		},
		{
			name:   "PBFLAGS_ENV_NAME override",
			envKey: "PBFLAGS_ENV_NAME",
			envVal: "staging",
			check: func(t *testing.T, cfg Config) {
				require.Equal(t, "staging", cfg.EnvName)
			},
		},
		{
			name:   "PBFLAGS_ENV_COLOR override",
			envKey: "PBFLAGS_ENV_COLOR",
			envVal: "#ff0000",
			check: func(t *testing.T, cfg Config) {
				require.Equal(t, "#ff0000", cfg.EnvColor)
			},
		},
		{
			name:   "PBFLAGS_CACHE_KILL_TTL override",
			envKey: "PBFLAGS_CACHE_KILL_TTL",
			envVal: "1m",
			check: func(t *testing.T, cfg Config) {
				require.Equal(t, 1*time.Minute, cfg.Cache.KillTTL)
			},
		},
		{
			name:   "PBFLAGS_CACHE_FLAG_TTL override",
			envKey: "PBFLAGS_CACHE_FLAG_TTL",
			envVal: "30s",
			check: func(t *testing.T, cfg Config) {
				require.Equal(t, 30*time.Second, cfg.Cache.FlagTTL)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// t.Setenv — cannot be parallel
			t.Setenv(tt.envKey, tt.envVal)

			cfg := LoadConfig()
			tt.check(t, cfg)
		})
	}
}

func TestConfig_LoadDefaults(t *testing.T) {
	// Without any env overrides, LoadConfig should return DefaultConfig values.
	// Not parallel because LoadConfig reads the real environment.
	cfg := LoadConfig()
	def := DefaultConfig()

	require.Equal(t, def.Listen, cfg.Listen)
	require.Equal(t, def.Cache.KillTTL, cfg.Cache.KillTTL)
	require.Equal(t, def.Cache.FlagTTL, cfg.Cache.FlagTTL)
	require.Equal(t, def.Cache.JitterPercent, cfg.Cache.JitterPercent)
	require.Equal(t, def.Cache.FetchTimeout, cfg.Cache.FetchTimeout)
}
