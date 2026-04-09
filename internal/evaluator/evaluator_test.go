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
	overrides []*CachedOverride
	overErr   error
}

func (f *stubFetcher) FetchFlagState(_ context.Context, _ string) (*CachedFlagState, error) {
	return f.flagState, f.flagErr
}

func (f *stubFetcher) FetchOverrides(_ context.Context, _ string, _ []string) ([]*CachedOverride, error) {
	return f.overrides, f.overErr
}

func newTestCache(t *testing.T) *CacheStore {
	t.Helper()
	cs, err := NewCacheStore(CacheStoreConfig{
		FlagTTL:         50 * time.Millisecond,
		OverrideTTL:     50 * time.Millisecond,
		OverrideMaxSize: 100,
		JitterPercent:   0,
	})
	require.NoError(t, err)
	t.Cleanup(cs.Close)
	return cs
}

func waitCaches(cs *CacheStore) {
	cs.flagCache.Wait()
	cs.overrideCache.Wait()
}

func newTestRegistry() *Registry {
	return NewRegistry(&Defaults{flags: make(map[string]FlagDef)})
}

func TestResolveGlobal_StaleCache(t *testing.T) {
	cache := newTestCache(t)
	reg := newTestRegistry()

	serverValue := &pbflagsv1.FlagValue{
		Value: &pbflagsv1.FlagValue_BoolValue{BoolValue: true},
	}

	fetcher := &stubFetcher{
		flagState: &CachedFlagState{
			FlagID: "test-flag",
			State:  pbflagsv1.State_STATE_ENABLED,
			Value:  serverValue,
		},
	}
	eval := NewEvaluator(reg, cache, fetcher, slog.Default(), NewNoopMetrics(), noopTracer())

	val, src := eval.Evaluate(context.Background(), "test-flag", "")
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_GLOBAL, src, "expected GLOBAL source")
	require.Equal(t, true, val.GetBoolValue(), "expected true")

	time.Sleep(100 * time.Millisecond)
	require.Nil(t, cache.GetFlagState("test-flag"), "expected Ristretto cache to have expired")

	fetcher.flagState = nil
	fetcher.flagErr = errors.New("server unreachable")

	val, src = eval.Evaluate(context.Background(), "test-flag", "")
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_CACHED, src, "expected CACHED source during outage")
	require.Equal(t, true, val.GetBoolValue(), "expected stale value true")
}

func TestResolveGlobal_NoStaleCache_FallsToDefault(t *testing.T) {
	cache := newTestCache(t)
	reg := newTestRegistry()

	fetcher := &stubFetcher{
		flagErr: errors.New("server unreachable"),
	}
	eval := NewEvaluator(reg, cache, fetcher, slog.Default(), NewNoopMetrics(), noopTracer())

	val, src := eval.Evaluate(context.Background(), "test-flag", "")
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT, src, "expected DEFAULT source with no stale cache")
	require.Nil(t, val, "expected nil value (no default registered)")
}

func TestResolveOverride_StaleCache(t *testing.T) {
	cache := newTestCache(t)
	reg := newTestRegistry()

	overrideValue := &pbflagsv1.FlagValue{
		Value: &pbflagsv1.FlagValue_StringValue{StringValue: "custom"},
	}

	fetcher := &stubFetcher{
		overrides: []*CachedOverride{
			{
				FlagID:   "test-flag",
				EntityID: "entity-1",
				State:    pbflagsv1.State_STATE_ENABLED,
				Value:    overrideValue,
			},
		},
	}

	reg.Swap(&Defaults{
		flags: map[string]FlagDef{
			"test-flag": {FlagID: "test-flag", Layer: "user"},
		},
	})

	eval := NewEvaluator(reg, cache, fetcher, slog.Default(), NewNoopMetrics(), noopTracer())

	val, src := eval.Evaluate(context.Background(), "test-flag", "entity-1")
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_OVERRIDE, src, "expected OVERRIDE source")
	require.Equal(t, "custom", val.GetStringValue(), "expected 'custom'")

	time.Sleep(100 * time.Millisecond)

	fetcher.overrides = nil
	fetcher.overErr = errors.New("server unreachable")
	fetcher.flagErr = errors.New("server unreachable")

	val, src = eval.Evaluate(context.Background(), "test-flag", "entity-1")
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_CACHED, src, "expected CACHED source during outage")
	require.Equal(t, "custom", val.GetStringValue(), "expected stale override 'custom'")
}
