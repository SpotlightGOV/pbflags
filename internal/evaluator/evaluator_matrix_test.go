package evaluator

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Helpers ---

func boolVal(v bool) *pbflagsv1.FlagValue {
	return &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_BoolValue{BoolValue: v}}
}

// --- State Matrix: Global flags ---

func TestEval_GlobalDefault_FlagExists(t *testing.T) {
	cache := newTestCache(t)
	fetcher := &stubFetcher{
		flagState: &CachedFlagState{FlagID: "f/1", State: pbflagsv1.State_STATE_DEFAULT},
	}
	eval := NewEvaluator(cache, fetcher, slog.Default(), NewNoopMetrics(), noopTracer())

	val, src := eval.Evaluate(context.Background(), "f/1", "")
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT, src, "source")
	require.Nil(t, val, "value = nil (conditions and compiled defaults handle values)")
}

func TestEval_GlobalDefault_NilValue(t *testing.T) {
	cache := newTestCache(t)
	fetcher := &stubFetcher{
		flagState: &CachedFlagState{FlagID: "f/1", State: pbflagsv1.State_STATE_DEFAULT, Value: nil},
	}
	eval := NewEvaluator(cache, fetcher, slog.Default(), NewNoopMetrics(), noopTracer())

	val, src := eval.Evaluate(context.Background(), "f/1", "")
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT, src, "source")
	require.Nil(t, val, "value = nil (client has compiled defaults)")
}

func TestEval_GlobalDefault(t *testing.T) {
	cache := newTestCache(t)
	fetcher := &stubFetcher{
		flagState: &CachedFlagState{FlagID: "f/1", State: pbflagsv1.State_STATE_DEFAULT, Value: nil},
	}
	eval := NewEvaluator(cache, fetcher, slog.Default(), NewNoopMetrics(), noopTracer())

	val, src := eval.Evaluate(context.Background(), "f/1", "")
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT, src, "source")
	require.Nil(t, val, "value = nil (client has compiled defaults)")
}

func TestEval_GlobalKilled(t *testing.T) {
	cache := newTestCache(t)
	fetcher := &stubFetcher{
		flagState: &CachedFlagState{FlagID: "f/1", State: pbflagsv1.State_STATE_KILLED, Value: boolVal(true)},
	}
	eval := NewEvaluator(cache, fetcher, slog.Default(), NewNoopMetrics(), noopTracer())

	val, src := eval.Evaluate(context.Background(), "f/1", "")
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT, src, "source")
	require.Nil(t, val, "value = nil (client has compiled defaults)")
}

func TestEval_UnknownFlag_FetchReturnsNil(t *testing.T) {
	cache := newTestCache(t)
	fetcher := &stubFetcher{flagState: nil}
	eval := NewEvaluator(cache, fetcher, slog.Default(), NewNoopMetrics(), noopTracer())

	val, src := eval.Evaluate(context.Background(), "unknown/1", "")
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT, src, "source")
	require.Nil(t, val, "value should be nil (no default registered)")
}

// --- Kill Set Tests ---

func TestEval_KillSet_GlobalKill(t *testing.T) {
	cache := newTestCache(t)
	cache.SetKillSet(&KillSet{
		FlagIDs: map[string]struct{}{"f/1": {}},
	})
	fetcher := &stubFetcher{
		flagState: &CachedFlagState{FlagID: "f/1", State: pbflagsv1.State_STATE_DEFAULT},
	}
	eval := NewEvaluator(cache, fetcher, slog.Default(), NewNoopMetrics(), noopTracer())

	val, src := eval.Evaluate(context.Background(), "f/1", "")
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_KILLED, src, "source")
	require.Nil(t, val, "value = nil (client has compiled defaults)")
}

// --- Archived Flag Fallback ---

func TestEval_ArchivedFlag(t *testing.T) {
	cache := newTestCache(t)
	fetcher := &stubFetcher{
		flagState: &CachedFlagState{FlagID: "f/1", State: pbflagsv1.State_STATE_DEFAULT, Archived: true},
	}
	eval := NewEvaluator(cache, fetcher, slog.Default(), NewNoopMetrics(), noopTracer())

	val, src := eval.Evaluate(context.Background(), "f/1", "")
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT, src, "source")
	require.Nil(t, val, "value = nil (archived flags return default)")
}

func TestEval_ArchivedFlag_NilValue(t *testing.T) {
	cache := newTestCache(t)
	fetcher := &stubFetcher{
		flagState: &CachedFlagState{FlagID: "f/1", State: pbflagsv1.State_STATE_DEFAULT, Value: nil, Archived: true},
	}
	eval := NewEvaluator(cache, fetcher, slog.Default(), NewNoopMetrics(), noopTracer())

	val, src := eval.Evaluate(context.Background(), "f/1", "")
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT, src, "source")
	require.Nil(t, val, "value = nil (client has compiled defaults)")
}

// --- Stale Cache Fallback ---

func TestEval_AllFetchesFail_ReturnsNil(t *testing.T) {
	cache := newTestCache(t)
	fetcher := &stubFetcher{
		flagErr: errors.New("unreachable"),
	}
	eval := NewEvaluator(cache, fetcher, slog.Default(), NewNoopMetrics(), noopTracer())

	val, src := eval.Evaluate(context.Background(), "f/1", "")
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT, src, "source")
	require.Nil(t, val, "value = nil (client has compiled defaults)")
}

// --- On-Demand Fetch Caching ---

func TestEval_OnDemandFetch_CachesFlagState(t *testing.T) {
	cache := newTestCache(t)

	callCount := 0
	fetcher := &callCountFetcher{
		flagState: &CachedFlagState{FlagID: "f/1", State: pbflagsv1.State_STATE_DEFAULT},
		counter:   &callCount,
	}
	eval := NewEvaluator(cache, fetcher, slog.Default(), NewNoopMetrics(), noopTracer())

	eval.Evaluate(context.Background(), "f/1", "")
	waitCaches(cache)
	eval.Evaluate(context.Background(), "f/1", "")

	require.Equal(t, 1, callCount, "expected 1 fetch call (cached)")
}

type callCountFetcher struct {
	flagState *CachedFlagState
	flagErr   error
	counter   *int
}

func (f *callCountFetcher) FetchFlagState(_ context.Context, _ string) (*CachedFlagState, error) {
	*f.counter++
	return f.flagState, f.flagErr
}

// --- Never Throw ---

func TestEval_NeverReturnsError(t *testing.T) {
	scenarios := []struct {
		name    string
		flagID  string
		fetcher *stubFetcher
		killSet *KillSet
	}{
		{
			name:    "unknown flag, fetch fails",
			flagID:  "unknown",
			fetcher: &stubFetcher{flagErr: errors.New("fail")},
		},
		{
			name:    "known flag, nil fetch result",
			flagID:  "f/1",
			fetcher: &stubFetcher{},
		},
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			cache := newTestCache(t)
			if sc.killSet != nil {
				cache.SetKillSet(sc.killSet)
			}
			eval := NewEvaluator(cache, sc.fetcher, slog.Default(), NewNoopMetrics(), noopTracer())
			val, src := eval.Evaluate(context.Background(), sc.flagID, "")
			_ = val
			_ = src
		})
	}
}

// --- All Four Value Types ---

func TestEval_AllFlagTypes_ReturnDefault(t *testing.T) {
	// Global state no longer carries values — conditions handle that.
	// All flag types should return DEFAULT source with nil value.
	types := []string{"bool", "string", "int64", "double"}
	for _, name := range types {
		t.Run(name, func(t *testing.T) {
			cache := newTestCache(t)
			fetcher := &stubFetcher{
				flagState: &CachedFlagState{FlagID: "f/1", State: pbflagsv1.State_STATE_DEFAULT},
			}
			eval := NewEvaluator(cache, fetcher, slog.Default(), NewNoopMetrics(), noopTracer())

			val, src := eval.Evaluate(context.Background(), "f/1", "")
			require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT, src, "source")
			require.Nil(t, val, "value = nil (conditions and compiled defaults handle values)")
		})
	}
}

// --- Concurrent Evaluator safety ---

func TestEval_ConcurrentEvaluate(t *testing.T) {
	cache := newTestCache(t)
	fetcher := &stubFetcher{
		flagState: &CachedFlagState{FlagID: "f/1", State: pbflagsv1.State_STATE_DEFAULT},
	}
	eval := NewEvaluator(cache, fetcher, slog.Default(), NewNoopMetrics(), noopTracer())

	errc := make(chan error, 100)
	for i := 0; i < 100; i++ {
		go func() {
			defer func() {
				if r := recover(); r != nil {
					errc <- errors.New("panic in Evaluate")
				} else {
					errc <- nil
				}
			}()
			eval.Evaluate(context.Background(), "f/1", "")
		}()
	}

	for i := 0; i < 100; i++ {
		err := <-errc
		assert.NoError(t, err, "concurrent Evaluate")
	}
}
