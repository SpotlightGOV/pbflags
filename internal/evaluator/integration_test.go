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
		FlagTTL:       100 * time.Millisecond,
		JitterPercent: 0,
	})
	require.NoError(t, err)

	noopM := NewNoopMetrics()
	noopT := noopTracer()
	tracker := NewHealthTracker(noopM)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	fetcher := NewDBFetcher(pool, tracker, logger, noopM, noopT)

	eval := NewEvaluator(cache, fetcher, logger, noopM)

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

func setFlagKilled(t *testing.T, pool *pgxpool.Pool, flagID string, killed bool) {
	t.Helper()
	ctx := context.Background()
	if killed {
		_, err := pool.Exec(ctx,
			`UPDATE feature_flags.flags SET killed_at = now(), updated_at = now() WHERE flag_id = $1`,
			flagID)
		require.NoError(t, err)
	} else {
		_, err := pool.Exec(ctx,
			`UPDATE feature_flags.flags SET killed_at = NULL, updated_at = now() WHERE flag_id = $1`,
			flagID)
		require.NoError(t, err)
	}
}

// notifSpecs returns the standard 4-flag spec used by most evaluator tests.
func notifSpecs() []testdb.FlagSpec {
	return []testdb.FlagSpec{
		{FlagType: "BOOL"},
		{FlagType: "STRING"},
		{FlagType: "INT64"},
		{FlagType: "DOUBLE"},
	}
}

// TestEvaluationLifecycle tests the full flag evaluation lifecycle:
// DEFAULT → KILLED → back to DEFAULT.
func TestEvaluationLifecycle(t *testing.T) {
	t.Parallel()
	env := setupIntegration(t)
	ctx := context.Background()
	tf := testdb.CreateTestFeature(t, env.pool, notifSpecs())

	// Phase 1: DEFAULT state — evaluator returns nil (client has compiled defaults).
	val, src := env.evaluator.Evaluate(ctx, tf.FlagID(1), "")
	require.Nil(t, val)
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT, src)

	val, src = env.evaluator.Evaluate(ctx, tf.FlagID(2), "")
	require.Nil(t, val)
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT, src)

	// Phase 2: KILLED — should return nil (client has compiled defaults).
	env.cache.FlushAll()
	env.cache.WaitAll()
	setFlagKilled(t, env.pool, tf.FlagID(1), true)

	// Also update kill set.
	ks, err := env.fetcher.GetKilledFlags(ctx)
	require.NoError(t, err)
	env.cache.SetKillSet(ks)

	val, src = env.evaluator.Evaluate(ctx, tf.FlagID(1), "")
	require.Nil(t, val) // nil — client uses compiled default
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_KILLED, src)

	// Phase 3: Unkill — back to DEFAULT.
	setFlagKilled(t, env.pool, tf.FlagID(1), false)
	ks, err = env.fetcher.GetKilledFlags(ctx)
	require.NoError(t, err)
	env.cache.SetKillSet(ks)
	env.cache.FlushAll()
	env.cache.WaitAll()

	val, src = env.evaluator.Evaluate(ctx, tf.FlagID(1), "")
	require.Nil(t, val) // nil — client uses compiled default
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

// TestDegradationLifecycle tests SERVING → DEGRADED → SERVING transitions.
func TestDegradationLifecycle(t *testing.T) {
	t.Parallel()
	env := setupIntegration(t)
	ctx := context.Background()
	tf := testdb.CreateTestFeature(t, env.pool, []testdb.FlagSpec{
		{FlagType: "BOOL"},
	})

	// Wrap the fetcher to simulate failures.
	ff := &failableFetcher{real: env.fetcher}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	eval := NewEvaluator(env.cache, ff, logger, NewNoopMetrics())

	// Healthy fetch to populate stale map.
	val, src := eval.Evaluate(ctx, tf.FlagID(1), "")
	require.Nil(t, val)
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT, src)
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
	val, src = eval.Evaluate(ctx, tf.FlagID(1), "")
	require.Nil(t, val)
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT, src)

	// Restore connectivity and flush everything for a fresh fetch.
	ff.failing.Store(false)
	env.cache.FlushAll()
	env.cache.WaitAll()

	val, src = eval.Evaluate(ctx, tf.FlagID(1), "")
	require.Nil(t, val)
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT, src)
	require.Equal(t, pbflagsv1.EvaluatorStatus_EVALUATOR_STATUS_SERVING, env.tracker.Status())
}

// TestStaleCacheDuringOutage verifies stale cache persists through outages.
func TestStaleCacheDuringOutage(t *testing.T) {
	t.Parallel()
	env := setupIntegration(t)
	ctx := context.Background()
	tf := testdb.CreateTestFeature(t, env.pool, []testdb.FlagSpec{
		{FlagType: "STRING"},
	})

	ff := &failableFetcher{real: env.fetcher}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	eval := NewEvaluator(env.cache, ff, logger, NewNoopMetrics())

	// Populate stale map with a successful fetch.
	val, src := eval.Evaluate(ctx, tf.FlagID(1), "")
	require.Nil(t, val)
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT, src)

	// Go offline. Clear hot cache but preserve stale map.
	ff.failing.Store(true)
	env.cache.FlushHot()
	env.cache.WaitAll()

	// Multiple evaluations should consistently return stale/default.
	for i := 0; i < 5; i++ {
		val, src = eval.Evaluate(ctx, tf.FlagID(1), "")
		require.Nil(t, val)
		require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT, src)
	}

	// Restore and flush everything for fresh fetch.
	ff.failing.Store(false)
	env.cache.FlushAll()
	env.cache.WaitAll()
	val, src = eval.Evaluate(ctx, tf.FlagID(1), "")
	require.Nil(t, val)
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT, src)
}

// TestArchivedFlagRetrieval verifies archived flags return DEFAULT.
func TestArchivedFlagRetrieval(t *testing.T) {
	t.Parallel()
	env := setupIntegration(t)
	ctx := context.Background()
	tf := testdb.CreateTestFeature(t, env.pool, []testdb.FlagSpec{
		{FlagType: "INT64"},
	})

	// Verify non-archived returns DEFAULT.
	val, src := env.evaluator.Evaluate(ctx, tf.FlagID(1), "")
	require.Nil(t, val)
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT, src)

	// Archive the flag.
	_, err := env.pool.Exec(ctx, `UPDATE feature_flags.flags SET archived_at = now() WHERE flag_id = $1`, tf.FlagID(1))
	require.NoError(t, err)
	env.cache.FlushAll()
	env.cache.WaitAll()

	// Archived flag returns DEFAULT.
	val, src = env.evaluator.Evaluate(ctx, tf.FlagID(1), "")
	require.Nil(t, val)
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT, src)
}

// TestAllFlagTypes verifies all four flag types return DEFAULT.
func TestAllFlagTypes(t *testing.T) {
	t.Parallel()
	env := setupIntegration(t)
	ctx := context.Background()
	tf := testdb.CreateTestFeature(t, env.pool, notifSpecs())

	// All flags return nil/DEFAULT — values are handled by conditions.
	for i := 1; i <= 4; i++ {
		val, src := env.evaluator.Evaluate(ctx, tf.FlagID(i), "")
		require.Nil(t, val)
		require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT, src)
	}
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
		{FlagType: "BOOL"},
	})

	const goroutines = 20
	errc := make(chan error, goroutines)
	for range goroutines {
		go func() {
			val, _ := env.evaluator.Evaluate(ctx, tf.FlagID(1), "")
			if val != nil {
				errc <- fmt.Errorf("concurrent eval: got %v, want nil", val)
				return
			}
			errc <- nil
		}()
	}
	for range goroutines {
		assert.NoError(t, <-errc)
	}
}

// TestNilDefaultValue verifies a flag not in the DB returns nil value safely.
func TestNilDefaultValue(t *testing.T) {
	t.Parallel()
	env := setupIntegration(t)
	ctx := context.Background()
	tf := testdb.CreateTestFeature(t, env.pool, []testdb.FlagSpec{
		{FlagType: "BOOL"},
	})

	val, src := env.evaluator.Evaluate(ctx, tf.FlagID(1), "")
	require.Nil(t, val)
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT, src)
}
