package evaluator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
	"github.com/SpotlightGOV/pbflags/internal/testdb"
)

// testEnv holds all components for an integration test.
type testEnv struct {
	pool      *pgxpool.Pool
	evaluator *Evaluator
	cache     *CacheStore
	registry  *Registry
	fetcher   *DBFetcher
	tracker   *HealthTracker
}

func setupIntegration(t *testing.T, defs []FlagDef) *testEnv {
	t.Helper()

	_, pool := testdb.Require(t)

	cache, err := NewCacheStore(CacheStoreConfig{
		FlagTTL:         100 * time.Millisecond,
		OverrideTTL:     100 * time.Millisecond,
		OverrideMaxSize: 100,
		JitterPercent:   0,
	})
	require.NoError(t, err)

	noopM := NewNoopMetrics()
	noopT := noopTracer()
	tracker := NewHealthTracker(noopM)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	fetcher := NewDBFetcher(pool, tracker, logger, noopM, noopT)

	defaults := NewDefaults(defs)
	registry := NewRegistry(defaults)
	eval := NewEvaluator(registry, cache, fetcher, logger, noopM, noopT)

	t.Cleanup(func() {
		cache.Close()
	})

	return &testEnv{
		pool:      pool,
		evaluator: eval,
		cache:     cache,
		registry:  registry,
		fetcher:   fetcher,
		tracker:   tracker,
	}
}

func seedFlag(t *testing.T, pool *pgxpool.Pool, featureID, flagID, flagType, layer string, fieldNum int, value *pbflagsv1.FlagValue) {
	t.Helper()
	ctx := context.Background()
	_, err := pool.Exec(ctx, `
		INSERT INTO feature_flags.features (feature_id) VALUES ($1) ON CONFLICT DO NOTHING`, featureID)
	require.NoError(t, err)

	var valBytes []byte
	if value != nil {
		valBytes, err = proto.Marshal(value)
		require.NoError(t, err)
	}

	_, err = pool.Exec(ctx, `
		INSERT INTO feature_flags.flags (flag_id, feature_id, field_number, flag_type, layer, value)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT DO NOTHING`, flagID, featureID, fieldNum, flagType, layer, valBytes)
	require.NoError(t, err)
}

func setFlagState(t *testing.T, pool *pgxpool.Pool, flagID, state string, value *pbflagsv1.FlagValue) {
	t.Helper()
	var valBytes []byte
	if value != nil {
		var err error
		valBytes, err = proto.Marshal(value)
		require.NoError(t, err)
	}
	_, err := pool.Exec(context.Background(),
		`UPDATE feature_flags.flags SET state = $2, value = $3 WHERE flag_id = $1`,
		flagID, state, valBytes)
	require.NoError(t, err)
}

func stringVal(v string) *pbflagsv1.FlagValue {
	return &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringValue{StringValue: v}}
}

// notificationsDefs returns flag definitions matching the example notifications proto.
func notificationsDefs() []FlagDef {
	return []FlagDef{
		{
			FlagID:    "eval_notif/1",
			FeatureID: "eval_notif",
			FieldNum:  1,
			Name:      "email_enabled",
			FlagType:  pbflagsv1.FlagType_FLAG_TYPE_BOOL,
			Layer:     "user",
			Default:   boolVal(true),
		},
		{
			FlagID:    "eval_notif/2",
			FeatureID: "eval_notif",
			FieldNum:  2,
			Name:      "digest_frequency",
			FlagType:  pbflagsv1.FlagType_FLAG_TYPE_STRING,
			Layer:     "",
			Default:   stringVal("daily"),
		},
		{
			FlagID:    "eval_notif/3",
			FeatureID: "eval_notif",
			FieldNum:  3,
			Name:      "max_retries",
			FlagType:  pbflagsv1.FlagType_FLAG_TYPE_INT64,
			Layer:     "",
			Default:   int64Val(3),
		},
		{
			FlagID:    "eval_notif/4",
			FeatureID: "eval_notif",
			FieldNum:  4,
			Name:      "score_threshold",
			FlagType:  pbflagsv1.FlagType_FLAG_TYPE_DOUBLE,
			Layer:     "",
			Default:   doubleVal(0.75),
		},
	}
}

// TestEvaluationLifecycle tests the full flag evaluation lifecycle:
// DEFAULT → ENABLED (with value) → KILLED → back to DEFAULT.
func TestEvaluationLifecycle(t *testing.T) {
	defs := notificationsDefs()
	env := setupIntegration(t, defs)
	ctx := context.Background()

	// Seed the flags.
	seedFlag(t, env.pool, "eval_notif", "eval_notif/1", "BOOL", "USER", 1, nil)
	seedFlag(t, env.pool, "eval_notif", "eval_notif/2", "STRING", "GLOBAL", 2, nil)
	seedFlag(t, env.pool, "eval_notif", "eval_notif/3", "INT64", "GLOBAL", 3, nil)
	seedFlag(t, env.pool, "eval_notif", "eval_notif/4", "DOUBLE", "GLOBAL", 4, nil)

	// Phase 1: DEFAULT state — compiled defaults should be returned.
	val, src := env.evaluator.Evaluate(ctx, "eval_notif/1", "user-1")
	require.True(t, val.GetBoolValue())
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT, src)

	val, src = env.evaluator.Evaluate(ctx, "eval_notif/2", "")
	require.Equal(t, "daily", val.GetStringValue())
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT, src)

	// Phase 2: ENABLED with server-side value.
	env.cache.FlushAll()
	env.cache.WaitAll()
	setFlagState(t, env.pool, "eval_notif/1", "ENABLED", boolVal(false))

	val, src = env.evaluator.Evaluate(ctx, "eval_notif/1", "user-1")
	require.False(t, val.GetBoolValue())
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_GLOBAL, src)

	// Phase 3: KILLED — should return compiled default.
	env.cache.FlushAll()
	env.cache.WaitAll()
	setFlagState(t, env.pool, "eval_notif/1", "KILLED", nil)

	// Also update kill set.
	ks, err := env.fetcher.GetKilledFlags(ctx)
	require.NoError(t, err)
	env.cache.SetKillSet(ks)

	val, src = env.evaluator.Evaluate(ctx, "eval_notif/1", "user-1")
	require.True(t, val.GetBoolValue()) // compiled default
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_KILLED, src)

	// Phase 4: Unkill — back to DEFAULT.
	setFlagState(t, env.pool, "eval_notif/1", "DEFAULT", nil)
	ks, err = env.fetcher.GetKilledFlags(ctx)
	require.NoError(t, err)
	env.cache.SetKillSet(ks)
	env.cache.FlushAll()
	env.cache.WaitAll()

	val, src = env.evaluator.Evaluate(ctx, "eval_notif/1", "user-1")
	require.True(t, val.GetBoolValue()) // compiled default
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT, src)
}

// TestOverrideLifecycle tests per-entity overrides.
func TestOverrideLifecycle(t *testing.T) {
	defs := notificationsDefs()
	env := setupIntegration(t, defs)
	ctx := context.Background()

	seedFlag(t, env.pool, "eval_notif", "eval_notif/1", "BOOL", "USER", 1, nil)

	// No override — returns global/default.
	val, src := env.evaluator.Evaluate(ctx, "eval_notif/1", "user-1")
	require.True(t, val.GetBoolValue())
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT, src)

	// Set an override.
	overrideVal := boolVal(false)
	overrideBytes, err := proto.Marshal(overrideVal)
	require.NoError(t, err)
	_, err = env.pool.Exec(ctx, `
		INSERT INTO feature_flags.flag_overrides (flag_id, entity_id, state, value)
		VALUES ('eval_notif/1', 'user-1', 'ENABLED', $1)`, overrideBytes)
	require.NoError(t, err)

	env.cache.FlushAll()
	env.cache.WaitAll()

	val, src = env.evaluator.Evaluate(ctx, "eval_notif/1", "user-1")
	require.False(t, val.GetBoolValue())
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_OVERRIDE, src)

	// Different entity has no override.
	val, src = env.evaluator.Evaluate(ctx, "eval_notif/1", "user-2")
	require.True(t, val.GetBoolValue())
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT, src)

	// Remove override.
	_, err = env.pool.Exec(ctx, `DELETE FROM feature_flags.flag_overrides WHERE flag_id = 'eval_notif/1' AND entity_id = 'user-1'`)
	require.NoError(t, err)
	env.cache.FlushAll()
	env.cache.WaitAll()

	val, src = env.evaluator.Evaluate(ctx, "eval_notif/1", "user-1")
	require.True(t, val.GetBoolValue()) // back to default
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT, src)
}

// failableFetcher wraps a real fetcher and can be told to fail on demand.
type failableFetcher struct {
	real    Fetcher
	failing atomic.Bool
}

func (f *failableFetcher) FetchFlagState(ctx context.Context, flagID string) (*CachedFlagState, error) {
	if f.failing.Load() {
		return nil, errors.New("simulated outage")
	}
	return f.real.FetchFlagState(ctx, flagID)
}

func (f *failableFetcher) FetchOverrides(ctx context.Context, entityID string, flagIDs []string) ([]*CachedOverride, error) {
	if f.failing.Load() {
		return nil, errors.New("simulated outage")
	}
	return f.real.FetchOverrides(ctx, entityID, flagIDs)
}

// TestDegradationLifecycle tests SERVING → DEGRADED → SERVING transitions.
func TestDegradationLifecycle(t *testing.T) {
	defs := notificationsDefs()
	env := setupIntegration(t, defs)
	ctx := context.Background()

	seedFlag(t, env.pool, "eval_notif", "eval_notif/1", "BOOL", "USER", 1, nil)
	setFlagState(t, env.pool, "eval_notif/1", "ENABLED", boolVal(false))

	// Wrap the fetcher to simulate failures.
	ff := &failableFetcher{real: env.fetcher}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	eval := NewEvaluator(env.registry, env.cache, ff, logger, NewNoopMetrics(), noopTracer())

	// Healthy fetch to populate stale map.
	val, src := eval.Evaluate(ctx, "eval_notif/1", "user-1")
	require.False(t, val.GetBoolValue())
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_GLOBAL, src)
	require.Equal(t, pbflagsv1.EvaluatorStatus_EVALUATOR_STATUS_SERVING, env.tracker.Status())

	// Simulate outage.
	ff.failing.Store(true)
	env.cache.FlushAll()
	env.cache.WaitAll()

	// Record enough failures to trigger DEGRADED.
	for i := 0; i < 3; i++ {
		env.tracker.RecordFailure()
	}
	require.Equal(t, pbflagsv1.EvaluatorStatus_EVALUATOR_STATUS_DEGRADED, env.tracker.Status())

	// Evaluator should still return stale cached value.
	val, src = eval.Evaluate(ctx, "eval_notif/1", "user-1")
	require.False(t, val.GetBoolValue())
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_CACHED, src)

	// Restore connectivity.
	ff.failing.Store(false)
	env.cache.FlushAll()
	env.cache.WaitAll()

	val, src = eval.Evaluate(ctx, "eval_notif/1", "user-1")
	require.False(t, val.GetBoolValue())
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_GLOBAL, src)
	require.Equal(t, pbflagsv1.EvaluatorStatus_EVALUATOR_STATUS_SERVING, env.tracker.Status())
}

// TestStaleCacheDuringOutage verifies stale cache persists through outages.
func TestStaleCacheDuringOutage(t *testing.T) {
	defs := notificationsDefs()
	env := setupIntegration(t, defs)
	ctx := context.Background()

	seedFlag(t, env.pool, "eval_notif", "eval_notif/2", "STRING", "GLOBAL", 2, nil)
	setFlagState(t, env.pool, "eval_notif/2", "ENABLED", stringVal("weekly"))

	ff := &failableFetcher{real: env.fetcher}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	eval := NewEvaluator(env.registry, env.cache, ff, logger, NewNoopMetrics(), noopTracer())

	// Populate stale map with a successful fetch.
	val, src := eval.Evaluate(ctx, "eval_notif/2", "")
	require.Equal(t, "weekly", val.GetStringValue())
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_GLOBAL, src)

	// Go offline. Hot cache expires, stale map persists.
	ff.failing.Store(true)
	env.cache.FlushAll()
	env.cache.WaitAll()

	// Multiple evaluations should consistently return stale cached value.
	for i := 0; i < 5; i++ {
		val, src = eval.Evaluate(ctx, "eval_notif/2", "")
		require.Equal(t, "weekly", val.GetStringValue())
		require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_CACHED, src)
	}

	// Meanwhile, the DB value changes (simulating a deploy while we're degraded).
	ff.failing.Store(false) // temporarily restore to update
	setFlagState(t, env.pool, "eval_notif/2", "ENABLED", stringVal("monthly"))
	ff.failing.Store(true) // go offline again

	// Still returns stale "weekly" because we can't reach DB.
	env.cache.FlushAll()
	env.cache.WaitAll()
	val, src = eval.Evaluate(ctx, "eval_notif/2", "")
	require.Equal(t, "weekly", val.GetStringValue())
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_CACHED, src)

	// Restore and see new value.
	ff.failing.Store(false)
	env.cache.FlushAll()
	env.cache.WaitAll()
	val, src = eval.Evaluate(ctx, "eval_notif/2", "")
	require.Equal(t, "monthly", val.GetStringValue())
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_GLOBAL, src)
}

// TestArchivedFlagRetrieval verifies archived flags return their last value.
func TestArchivedFlagRetrieval(t *testing.T) {
	defs := notificationsDefs()
	env := setupIntegration(t, defs)
	ctx := context.Background()

	seedFlag(t, env.pool, "eval_notif", "eval_notif/3", "INT64", "GLOBAL", 3, nil)
	setFlagState(t, env.pool, "eval_notif/3", "ENABLED", int64Val(5))

	// Verify non-archived returns GLOBAL.
	val, src := env.evaluator.Evaluate(ctx, "eval_notif/3", "")
	require.Equal(t, int64(5), val.GetInt64Value())
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_GLOBAL, src)

	// Archive the flag.
	_, err := env.pool.Exec(ctx, `UPDATE feature_flags.flags SET archived_at = now() WHERE flag_id = 'eval_notif/3'`)
	require.NoError(t, err)
	env.cache.FlushAll()
	env.cache.WaitAll()

	// Archived flag returns its value with ARCHIVED source.
	val, src = env.evaluator.Evaluate(ctx, "eval_notif/3", "")
	require.Equal(t, int64(5), val.GetInt64Value())
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_ARCHIVED, src)
}

// TestAllFlagTypes verifies all four flag types evaluate correctly.
func TestAllFlagTypes(t *testing.T) {
	defs := notificationsDefs()
	env := setupIntegration(t, defs)
	ctx := context.Background()

	seedFlag(t, env.pool, "eval_notif", "eval_notif/1", "BOOL", "USER", 1, nil)
	seedFlag(t, env.pool, "eval_notif", "eval_notif/2", "STRING", "GLOBAL", 2, nil)
	seedFlag(t, env.pool, "eval_notif", "eval_notif/3", "INT64", "GLOBAL", 3, nil)
	seedFlag(t, env.pool, "eval_notif", "eval_notif/4", "DOUBLE", "GLOBAL", 4, nil)

	// Set server values.
	setFlagState(t, env.pool, "eval_notif/1", "ENABLED", boolVal(false))
	setFlagState(t, env.pool, "eval_notif/2", "ENABLED", stringVal("weekly"))
	setFlagState(t, env.pool, "eval_notif/3", "ENABLED", int64Val(10))
	setFlagState(t, env.pool, "eval_notif/4", "ENABLED", doubleVal(0.95))

	val, _ := env.evaluator.Evaluate(ctx, "eval_notif/1", "user-1")
	require.False(t, val.GetBoolValue())

	val, _ = env.evaluator.Evaluate(ctx, "eval_notif/2", "")
	require.Equal(t, "weekly", val.GetStringValue())

	val, _ = env.evaluator.Evaluate(ctx, "eval_notif/3", "")
	require.Equal(t, int64(10), val.GetInt64Value())

	val, _ = env.evaluator.Evaluate(ctx, "eval_notif/4", "")
	require.InDelta(t, 0.95, val.GetDoubleValue(), 0.001)
}

// --- Additional tests ported from spotlightgov ---

func seedAllFlags(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	seedFlag(t, pool, "eval_notif", "eval_notif/1", "BOOL", "USER", 1, nil)
	seedFlag(t, pool, "eval_notif", "eval_notif/2", "STRING", "GLOBAL", 2, nil)
	seedFlag(t, pool, "eval_notif", "eval_notif/3", "INT64", "GLOBAL", 3, nil)
	seedFlag(t, pool, "eval_notif", "eval_notif/4", "DOUBLE", "GLOBAL", 4, nil)
}

func setOverride(t *testing.T, pool *pgxpool.Pool, flagID, entityID, state string, value *pbflagsv1.FlagValue) {
	t.Helper()
	var valBytes []byte
	if value != nil {
		var err error
		valBytes, err = proto.Marshal(value)
		require.NoError(t, err)
	}
	_, err := pool.Exec(context.Background(), `
		INSERT INTO feature_flags.flag_overrides (flag_id, entity_id, state, value)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (flag_id, entity_id) DO UPDATE SET state = EXCLUDED.state, value = EXCLUDED.value`,
		flagID, entityID, state, valBytes)
	require.NoError(t, err)
}

// TestGlobalKillOverridesEntityOverride verifies global kill takes precedence over overrides.
func TestGlobalKillOverridesEntityOverride(t *testing.T) {
	defs := notificationsDefs()
	env := setupIntegration(t, defs)
	ctx := context.Background()

	seedAllFlags(t, env.pool)

	// Set an entity override.
	setOverride(t, env.pool, "eval_notif/1", "user-99", "ENABLED", boolVal(false))
	env.cache.FlushAll()
	env.cache.WaitAll()

	val, src := env.evaluator.Evaluate(ctx, "eval_notif/1", "user-99")
	require.False(t, val.GetBoolValue())
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_OVERRIDE, src)

	// Now globally kill the flag.
	setFlagState(t, env.pool, "eval_notif/1", "KILLED", nil)
	ks, err := env.fetcher.GetKilledFlags(ctx)
	require.NoError(t, err)
	env.cache.SetKillSet(ks)
	env.cache.FlushAll()
	env.cache.WaitAll()

	// Global kill should override the entity override.
	val, src = env.evaluator.Evaluate(ctx, "eval_notif/1", "user-99")
	require.True(t, val.GetBoolValue()) // compiled default
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_KILLED, src)
}

// TestUnknownFlagEval verifies unknown flags return nil value with DEFAULT source.
func TestUnknownFlagEval(t *testing.T) {
	defs := notificationsDefs()
	env := setupIntegration(t, defs)
	seedAllFlags(t, env.pool)

	val, src := env.evaluator.Evaluate(context.Background(), "nonexistent/1", "")
	require.Nil(t, val)
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT, src)
}

// TestConcurrentEval verifies concurrent evaluations are safe (no panics, no data races).
func TestConcurrentEval(t *testing.T) {
	defs := notificationsDefs()
	env := setupIntegration(t, defs)
	ctx := context.Background()

	seedAllFlags(t, env.pool)
	setFlagState(t, env.pool, "eval_notif/1", "ENABLED", boolVal(false))

	const goroutines = 20
	errc := make(chan error, goroutines)
	for i := range goroutines {
		go func() {
			val, _ := env.evaluator.Evaluate(ctx, "eval_notif/1", fmt.Sprintf("user-%d", i))
			if val.GetBoolValue() != false {
				errc <- fmt.Errorf("concurrent eval: got %v, want false", val)
				return
			}
			errc <- nil
		}()
	}
	for range goroutines {
		assert.NoError(t, <-errc)
	}
}

// TestOverrideStaleCacheDuringOutage verifies stale override is served when fetcher fails.
func TestOverrideStaleCacheDuringOutage(t *testing.T) {
	defs := notificationsDefs()
	env := setupIntegration(t, defs)
	ctx := context.Background()

	seedAllFlags(t, env.pool)

	ff := &failableFetcher{real: env.fetcher}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	eval := NewEvaluator(env.registry, env.cache, ff, logger, NewNoopMetrics(), noopTracer())

	// Set override and prime the cache.
	setOverride(t, env.pool, "eval_notif/1", "user-stale", "ENABLED", boolVal(false))
	env.cache.FlushAll()
	env.cache.WaitAll()

	val, src := eval.Evaluate(ctx, "eval_notif/1", "user-stale")
	require.False(t, val.GetBoolValue())
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_OVERRIDE, src)

	// Make fetcher fail, expire cache — override should be served from stale cache.
	ff.failing.Store(true)
	env.cache.FlushAll()
	env.cache.WaitAll()

	val, src = eval.Evaluate(ctx, "eval_notif/1", "user-stale")
	require.False(t, val.GetBoolValue())
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_CACHED, src)
}

// TestNilDefaultValue verifies a flag not in the registry returns nil value safely.
func TestNilDefaultValue(t *testing.T) {
	defs := notificationsDefs()
	env := setupIntegration(t, defs)
	ctx := context.Background()

	// Insert a flag with no corresponding registry entry.
	_, err := env.pool.Exec(ctx, `
		INSERT INTO feature_flags.features (feature_id) VALUES ('niltest') ON CONFLICT DO NOTHING`)
	require.NoError(t, err)
	_, err = env.pool.Exec(ctx, `
		INSERT INTO feature_flags.flags (flag_id, feature_id, field_number, flag_type, layer, state)
		VALUES ('niltest/1', 'niltest', 1, 'BOOL', 'GLOBAL', 'DEFAULT')
		ON CONFLICT DO NOTHING`)
	require.NoError(t, err)

	val, src := env.evaluator.Evaluate(ctx, "niltest/1", "")
	require.Nil(t, val)
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT, src)
}
