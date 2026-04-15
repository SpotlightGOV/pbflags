package evaluator

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
)

func noopTracer() trace.Tracer {
	return tracenoop.NewTracerProvider().Tracer("test")
}

// stubFetcher implements Fetcher for testing.
type stubFetcher struct {
	flagState *CachedFlagState
	flagErr   error
}

func (f *stubFetcher) FetchFlagState(_ context.Context, _ string) (*CachedFlagState, error) {
	return f.flagState, f.flagErr
}

func newTestCache(t *testing.T) *CacheStore {
	t.Helper()
	cs, err := NewCacheStore(CacheStoreConfig{
		FlagTTL:       50 * time.Millisecond,
		JitterPercent: 0,
	})
	require.NoError(t, err)
	t.Cleanup(cs.Close)
	return cs
}

func waitCaches(cs *CacheStore) {
	cs.flagCache.Wait()
}

func TestResolveGlobal_StaleCache(t *testing.T) {
	cache := newTestCache(t)

	fetcher := &stubFetcher{
		flagState: &CachedFlagState{
			FlagID: "test-flag",
			State:  pbflagsv1.State_STATE_DEFAULT,
		},
	}
	eval := NewEvaluator(cache, fetcher, slog.Default(), NewNoopMetrics())

	val, src := eval.Evaluate(context.Background(), "test-flag", "")
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT, src, "expected DEFAULT source")
	require.Nil(t, val)

	time.Sleep(100 * time.Millisecond)
	require.Nil(t, cache.GetFlagState("test-flag"), "expected Ristretto cache to have expired")

	fetcher.flagState = nil
	fetcher.flagErr = errors.New("server unreachable")

	val, src = eval.Evaluate(context.Background(), "test-flag", "")
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT, src, "expected DEFAULT source (stale returns default too)")
	require.Nil(t, val)
}

func TestResolveGlobal_StaleCache_NextCallReturnsFresh(t *testing.T) {
	cache := newTestCache(t)

	fetcher := &stubFetcher{
		flagState: &CachedFlagState{
			FlagID: "test-flag",
			State:  pbflagsv1.State_STATE_DEFAULT,
		},
	}
	eval := NewEvaluator(cache, fetcher, slog.Default(), NewNoopMetrics())

	// Cold start: blocks on fetch, returns DEFAULT.
	val, src := eval.Evaluate(context.Background(), "test-flag", "")
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT, src)
	require.Nil(t, val)

	// Simulate TTL expiry.
	cache.FlushHot()
	cache.WaitAll()

	// First eval after expiry: returns stale/default, triggers background refresh.
	val, src = eval.Evaluate(context.Background(), "test-flag", "")
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT, src, "first miss should return default")
	require.Nil(t, val)

	// Wait for background refresh to populate hot cache.
	require.Eventually(t, func() bool {
		cache.WaitAll()
		return cache.GetFlagState("test-flag") != nil
	}, time.Second, time.Millisecond)

	// Next eval: background refresh populated the cache.
	val, src = eval.Evaluate(context.Background(), "test-flag", "")
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT, src, "after refresh should return DEFAULT")
	require.Nil(t, val)
}

func TestResolveGlobal_NoStaleCache_FallsToDefault(t *testing.T) {
	cache := newTestCache(t)

	fetcher := &stubFetcher{
		flagErr: errors.New("server unreachable"),
	}
	eval := NewEvaluator(cache, fetcher, slog.Default(), NewNoopMetrics())

	val, src := eval.Evaluate(context.Background(), "test-flag", "")
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT, src, "expected DEFAULT source with no stale cache")
	require.Nil(t, val, "expected nil value (no default registered)")
}

func TestInlineKillCheck_ReturnsKilled(t *testing.T) {
	cache := newTestCache(t)

	fetcher := &stubFetcher{
		flagState: &CachedFlagState{
			FlagID: "test-flag",
			State:  pbflagsv1.State_STATE_KILLED,
		},
	}

	eval := NewEvaluator(cache, fetcher, slog.Default(), NewNoopMetrics(),
		WithInlineKillCheck())

	_, src := eval.Evaluate(context.Background(), "test-flag", "")
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_KILLED, src,
		"inline kill check should return KILLED")
}
