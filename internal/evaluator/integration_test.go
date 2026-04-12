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
	fetcher   *DBFetcher
	tracker   *HealthTracker
}

func setupIntegration(t *testing.T) *testEnv {
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

	eval := NewEvaluator(cache, fetcher, logger, noopM, noopT)

	t.Cleanup(func() {
		cache.Close()
	})

	return &testEnv{
		pool:      pool,
		evaluator: eval,
		cache:     cache,
		fetcher:   fetcher,
		tracker:   tracker,
	}
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

// notifSpecs returns the standard 4-flag spec used by most evaluator tests.
func notifSpecs() []testdb.FlagSpec {
	return []testdb.FlagSpec{
		{FlagType: "BOOL", Layer: "USER"},
		{FlagType: "STRING", Layer: "GLOBAL"},
		{FlagType: "INT64", Layer: "GLOBAL"},
		{FlagType: "DOUBLE", Layer: "GLOBAL"},
	}
}

// TestEvaluationLifecycle tests the full flag evaluation lifecycle:
// DEFAULT → ENABLED (with value) → KILLED → back to DEFAULT.
func TestEvaluationLifecycle(t *testing.T) {
	t.Parallel()
	env := setupIntegration(t)
	ctx := context.Background()
	tf := testdb.CreateTestFeature(t, env.pool, notifSpecs())

	// Phase 1: DEFAULT state — evaluator returns nil (client has compiled defaults).
	val, src := env.evaluator.Evaluate(ctx, tf.FlagID(1), "user-1")
	require.Nil(t, val)
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT, src)

	val, src = env.evaluator.Evaluate(ctx, tf.FlagID(2), "")
	require.Nil(t, val)
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT, src)

	// Phase 2: ENABLED with server-side value.
	env.cache.FlushAll()
	env.cache.WaitAll()
	setFlagState(t, env.pool, tf.FlagID(1), "ENABLED", boolVal(false))

	val, src = env.evaluator.Evaluate(ctx, tf.FlagID(1), "user-1")
	require.False(t, val.GetBoolValue())
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_GLOBAL, src)

	// Phase 3: KILLED — should return nil (client has compiled defaults).
	env.cache.FlushAll()
	env.cache.WaitAll()
	setFlagState(t, env.pool, tf.FlagID(1), "KILLED", nil)

	// Also update kill set.
	ks, err := env.fetcher.GetKilledFlags(ctx)
	require.NoError(t, err)
	env.cache.SetKillSet(ks)

	val, src = env.evaluator.Evaluate(ctx, tf.FlagID(1), "user-1")
	require.Nil(t, val) // nil — client uses compiled default
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_KILLED, src)

	// Phase 4: Unkill — back to DEFAULT.
	setFlagState(t, env.pool, tf.FlagID(1), "DEFAULT", nil)
	ks, err = env.fetcher.GetKilledFlags(ctx)
	require.NoError(t, err)
	env.cache.SetKillSet(ks)
	env.cache.FlushAll()
	env.cache.WaitAll()

	val, src = env.evaluator.Evaluate(ctx, tf.FlagID(1), "user-1")
	require.Nil(t, val) // nil — client uses compiled default
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT, src)
}

// TestOverrideLifecycle tests per-entity overrides.
func TestOverrideLifecycle(t *testing.T) {
	t.Parallel()
	env := setupIntegration(t)
	ctx := context.Background()
	tf := testdb.CreateTestFeature(t, env.pool, []testdb.FlagSpec{
		{FlagType: "BOOL", Layer: "USER"},
	})

	// No override — returns nil with DEFAULT source.
	val, src := env.evaluator.Evaluate(ctx, tf.FlagID(1), "user-1")
	require.Nil(t, val)
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT, src)

	// Set an override.
	overrideVal := boolVal(false)
	overrideBytes, err := proto.Marshal(overrideVal)
	require.NoError(t, err)
	_, err = env.pool.Exec(ctx, `
		INSERT INTO feature_flags.flag_overrides (flag_id, entity_id, state, value)
		VALUES ($1, 'user-1', 'ENABLED', $2)`, tf.FlagID(1), overrideBytes)
	require.NoError(t, err)

	env.cache.FlushAll()
	env.cache.WaitAll()

	val, src = env.evaluator.Evaluate(ctx, tf.FlagID(1), "user-1")
	require.False(t, val.GetBoolValue())
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_OVERRIDE, src)

	// Different entity has no override — returns nil with DEFAULT.
	val, src = env.evaluator.Evaluate(ctx, tf.FlagID(1), "user-2")
	require.Nil(t, val)
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT, src)

	// Remove override.
	_, err = env.pool.Exec(ctx, `DELETE FROM feature_flags.flag_overrides WHERE flag_id = $1 AND entity_id = 'user-1'`, tf.FlagID(1))
	require.NoError(t, err)
	env.cache.FlushAll()
	env.cache.WaitAll()

	val, src = env.evaluator.Evaluate(ctx, tf.FlagID(1), "user-1")
	require.Nil(t, val) // back to nil — client uses compiled default
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
	t.Parallel()
	env := setupIntegration(t)
	ctx := context.Background()
	tf := testdb.CreateTestFeature(t, env.pool, []testdb.FlagSpec{
		{FlagType: "BOOL", Layer: "USER"},
	})

	setFlagState(t, env.pool, tf.FlagID(1), "ENABLED", boolVal(false))

	// Wrap the fetcher to simulate failures.
	ff := &failableFetcher{real: env.fetcher}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	eval := NewEvaluator(env.cache, ff, logger, NewNoopMetrics(), noopTracer())

	// Healthy fetch to populate stale map.
	val, src := eval.Evaluate(ctx, tf.FlagID(1), "user-1")
	require.False(t, val.GetBoolValue())
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_GLOBAL, src)
	require.Equal(t, pbflagsv1.EvaluatorStatus_EVALUATOR_STATUS_SERVING, env.tracker.Status())

	// Simulate outage: clear hot cache but preserve stale map.
	ff.failing.Store(true)
	env.cache.FlushHot()
	env.cache.WaitAll()

	// Record enough failures to trigger DEGRADED.
	for i := 0; i < 3; i++ {
		env.tracker.RecordFailure()
	}
	require.Equal(t, pbflagsv1.EvaluatorStatus_EVALUATOR_STATUS_DEGRADED, env.tracker.Status())

	// Evaluator should return stale value (background refresh will fail).
	val, src = eval.Evaluate(ctx, tf.FlagID(1), "user-1")
	require.False(t, val.GetBoolValue())
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_STALE, src)

	// Restore connectivity and flush everything for a fresh fetch.
	ff.failing.Store(false)
	env.cache.FlushAll()
	env.cache.WaitAll()

	val, src = eval.Evaluate(ctx, tf.FlagID(1), "user-1")
	require.False(t, val.GetBoolValue())
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_GLOBAL, src)
	require.Equal(t, pbflagsv1.EvaluatorStatus_EVALUATOR_STATUS_SERVING, env.tracker.Status())
}

// TestStaleCacheDuringOutage verifies stale cache persists through outages.
func TestStaleCacheDuringOutage(t *testing.T) {
	t.Parallel()
	env := setupIntegration(t)
	ctx := context.Background()
	tf := testdb.CreateTestFeature(t, env.pool, []testdb.FlagSpec{
		{FlagType: "STRING", Layer: "GLOBAL"},
	})

	setFlagState(t, env.pool, tf.FlagID(1), "ENABLED", stringVal("weekly"))

	ff := &failableFetcher{real: env.fetcher}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	eval := NewEvaluator(env.cache, ff, logger, NewNoopMetrics(), noopTracer())

	// Populate stale map with a successful fetch.
	val, src := eval.Evaluate(ctx, tf.FlagID(1), "")
	require.Equal(t, "weekly", val.GetStringValue())
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_GLOBAL, src)

	// Go offline. Clear hot cache but preserve stale map.
	ff.failing.Store(true)
	env.cache.FlushHot()
	env.cache.WaitAll()

	// Multiple evaluations should consistently return stale value.
	for i := 0; i < 5; i++ {
		val, src = eval.Evaluate(ctx, tf.FlagID(1), "")
		require.Equal(t, "weekly", val.GetStringValue())
		require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_STALE, src)
	}

	// Meanwhile, the DB value changes (simulating a deploy while we're degraded).
	ff.failing.Store(false) // temporarily restore to update
	setFlagState(t, env.pool, tf.FlagID(1), "ENABLED", stringVal("monthly"))
	ff.failing.Store(true) // go offline again

	// Still returns stale "weekly" because we can't reach DB.
	env.cache.FlushHot()
	env.cache.WaitAll()
	val, src = eval.Evaluate(ctx, tf.FlagID(1), "")
	require.Equal(t, "weekly", val.GetStringValue())
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_STALE, src)

	// Restore and flush everything for fresh fetch.
	ff.failing.Store(false)
	env.cache.FlushAll()
	env.cache.WaitAll()
	val, src = eval.Evaluate(ctx, tf.FlagID(1), "")
	require.Equal(t, "monthly", val.GetStringValue())
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_GLOBAL, src)
}

// TestArchivedFlagRetrieval verifies archived flags return their last value.
func TestArchivedFlagRetrieval(t *testing.T) {
	t.Parallel()
	env := setupIntegration(t)
	ctx := context.Background()
	tf := testdb.CreateTestFeature(t, env.pool, []testdb.FlagSpec{
		{FlagType: "INT64", Layer: "GLOBAL"},
	})

	setFlagState(t, env.pool, tf.FlagID(1), "ENABLED", int64Val(5))

	// Verify non-archived returns GLOBAL.
	val, src := env.evaluator.Evaluate(ctx, tf.FlagID(1), "")
	require.Equal(t, int64(5), val.GetInt64Value())
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_GLOBAL, src)

	// Archive the flag.
	_, err := env.pool.Exec(ctx, `UPDATE feature_flags.flags SET archived_at = now() WHERE flag_id = $1`, tf.FlagID(1))
	require.NoError(t, err)
	env.cache.FlushAll()
	env.cache.WaitAll()

	// Archived flag returns its value with ARCHIVED source.
	val, src = env.evaluator.Evaluate(ctx, tf.FlagID(1), "")
	require.Equal(t, int64(5), val.GetInt64Value())
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_ARCHIVED, src)
}

// TestAllFlagTypes verifies all four flag types evaluate correctly.
func TestAllFlagTypes(t *testing.T) {
	t.Parallel()
	env := setupIntegration(t)
	ctx := context.Background()
	tf := testdb.CreateTestFeature(t, env.pool, notifSpecs())

	// Set server values.
	setFlagState(t, env.pool, tf.FlagID(1), "ENABLED", boolVal(false))
	setFlagState(t, env.pool, tf.FlagID(2), "ENABLED", stringVal("weekly"))
	setFlagState(t, env.pool, tf.FlagID(3), "ENABLED", int64Val(10))
	setFlagState(t, env.pool, tf.FlagID(4), "ENABLED", doubleVal(0.95))

	val, _ := env.evaluator.Evaluate(ctx, tf.FlagID(1), "user-1")
	require.False(t, val.GetBoolValue())

	val, _ = env.evaluator.Evaluate(ctx, tf.FlagID(2), "")
	require.Equal(t, "weekly", val.GetStringValue())

	val, _ = env.evaluator.Evaluate(ctx, tf.FlagID(3), "")
	require.Equal(t, int64(10), val.GetInt64Value())

	val, _ = env.evaluator.Evaluate(ctx, tf.FlagID(4), "")
	require.InDelta(t, 0.95, val.GetDoubleValue(), 0.001)
}

// TestGlobalKillOverridesEntityOverride verifies global kill takes precedence over overrides.
func TestGlobalKillOverridesEntityOverride(t *testing.T) {
	t.Parallel()
	env := setupIntegration(t)
	ctx := context.Background()
	tf := testdb.CreateTestFeature(t, env.pool, notifSpecs())

	// Set an entity override.
	setOverride(t, env.pool, tf.FlagID(1), "user-99", "ENABLED", boolVal(false))
	env.cache.FlushAll()
	env.cache.WaitAll()

	val, src := env.evaluator.Evaluate(ctx, tf.FlagID(1), "user-99")
	require.False(t, val.GetBoolValue())
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_OVERRIDE, src)

	// Now globally kill the flag.
	setFlagState(t, env.pool, tf.FlagID(1), "KILLED", nil)
	ks, err := env.fetcher.GetKilledFlags(ctx)
	require.NoError(t, err)
	env.cache.SetKillSet(ks)
	env.cache.FlushAll()
	env.cache.WaitAll()

	// Global kill should override the entity override.
	val, src = env.evaluator.Evaluate(ctx, tf.FlagID(1), "user-99")
	require.Nil(t, val) // nil — client uses compiled default
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_KILLED, src)
}

// TestUnknownFlagEval verifies unknown flags return nil value with DEFAULT source.
func TestUnknownFlagEval(t *testing.T) {
	t.Parallel()
	env := setupIntegration(t)

	val, src := env.evaluator.Evaluate(context.Background(), "nonexistent/1", "")
	require.Nil(t, val)
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT, src)
}

// TestConcurrentEval verifies concurrent evaluations are safe (no panics, no data races).
func TestConcurrentEval(t *testing.T) {
	t.Parallel()
	env := setupIntegration(t)
	ctx := context.Background()
	tf := testdb.CreateTestFeature(t, env.pool, []testdb.FlagSpec{
		{FlagType: "BOOL", Layer: "USER"},
	})

	setFlagState(t, env.pool, tf.FlagID(1), "ENABLED", boolVal(false))

	const goroutines = 20
	errc := make(chan error, goroutines)
	for i := range goroutines {
		go func() {
			val, _ := env.evaluator.Evaluate(ctx, tf.FlagID(1), fmt.Sprintf("user-%d", i))
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
	t.Parallel()
	env := setupIntegration(t)
	ctx := context.Background()
	tf := testdb.CreateTestFeature(t, env.pool, notifSpecs())

	ff := &failableFetcher{real: env.fetcher}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	eval := NewEvaluator(env.cache, ff, logger, NewNoopMetrics(), noopTracer())

	// Set override and prime the cache.
	setOverride(t, env.pool, tf.FlagID(1), "user-stale", "ENABLED", boolVal(false))
	env.cache.FlushAll()
	env.cache.WaitAll()

	val, src := eval.Evaluate(ctx, tf.FlagID(1), "user-stale")
	require.False(t, val.GetBoolValue())
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_OVERRIDE, src)

	// Make fetcher fail, expire hot cache — override should be served from stale map.
	ff.failing.Store(true)
	env.cache.FlushHot()
	env.cache.WaitAll()

	val, src = eval.Evaluate(ctx, tf.FlagID(1), "user-stale")
	require.False(t, val.GetBoolValue())
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_STALE, src)
}

// TestNilDefaultValue verifies a flag not in the DB returns nil value safely.
func TestNilDefaultValue(t *testing.T) {
	t.Parallel()
	env := setupIntegration(t)
	ctx := context.Background()
	tf := testdb.CreateTestFeature(t, env.pool, []testdb.FlagSpec{
		{FlagType: "BOOL", Layer: "GLOBAL"},
	})

	val, src := env.evaluator.Evaluate(ctx, tf.FlagID(1), "")
	require.Nil(t, val)
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT, src)
}
