package evaluator

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
)

// --- Helpers ---

func boolVal(v bool) *pbflagsv1.FlagValue {
	return &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_BoolValue{BoolValue: v}}
}

func strVal(v string) *pbflagsv1.FlagValue {
	return &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringValue{StringValue: v}}
}

func int64Val(v int64) *pbflagsv1.FlagValue {
	return &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_Int64Value{Int64Value: v}}
}

func doubleVal(v float64) *pbflagsv1.FlagValue {
	return &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_DoubleValue{DoubleValue: v}}
}

func registryWith(defs ...FlagDef) *Registry {
	m := make(map[string]FlagDef, len(defs))
	for _, d := range defs {
		m[d.FlagID] = d
	}
	return NewRegistry(&Defaults{flags: m})
}

func globalFlag(id string, def *pbflagsv1.FlagValue) FlagDef {
	return FlagDef{FlagID: id, Layer: "", Default: def}
}

func userFlag(id string, def *pbflagsv1.FlagValue) FlagDef {
	return FlagDef{FlagID: id, Layer: "user", Default: def}
}

// --- 3x3 State Matrix: Global flags ---

func TestEval_GlobalEnabled_WithValue(t *testing.T) {
	cache := newTestCache(t)
	reg := registryWith(globalFlag("f/1", boolVal(false)))
	fetcher := &stubFetcher{
		flagState: &CachedFlagState{FlagID: "f/1", State: pbflagsv1.State_STATE_ENABLED, Value: boolVal(true)},
	}
	eval := NewEvaluator(reg, cache, fetcher, slog.Default(), NewNoopMetrics(), noopTracer())

	val, src := eval.Evaluate(context.Background(), "f/1", "")
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_GLOBAL, src, "source")
	require.Equal(t, true, val.GetBoolValue(), "value")
}

func TestEval_GlobalEnabled_NilValue(t *testing.T) {
	cache := newTestCache(t)
	reg := registryWith(globalFlag("f/1", boolVal(false)))
	fetcher := &stubFetcher{
		flagState: &CachedFlagState{FlagID: "f/1", State: pbflagsv1.State_STATE_ENABLED, Value: nil},
	}
	eval := NewEvaluator(reg, cache, fetcher, slog.Default(), NewNoopMetrics(), noopTracer())

	val, src := eval.Evaluate(context.Background(), "f/1", "")
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT, src, "source")
	require.Equal(t, false, val.GetBoolValue(), "value = compiled default false")
}

func TestEval_GlobalDefault(t *testing.T) {
	cache := newTestCache(t)
	reg := registryWith(globalFlag("f/1", boolVal(true)))
	fetcher := &stubFetcher{
		flagState: &CachedFlagState{FlagID: "f/1", State: pbflagsv1.State_STATE_DEFAULT, Value: nil},
	}
	eval := NewEvaluator(reg, cache, fetcher, slog.Default(), NewNoopMetrics(), noopTracer())

	val, src := eval.Evaluate(context.Background(), "f/1", "")
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT, src, "source")
	require.Equal(t, true, val.GetBoolValue(), "value = compiled default true")
}

func TestEval_GlobalKilled(t *testing.T) {
	cache := newTestCache(t)
	reg := registryWith(globalFlag("f/1", boolVal(false)))
	fetcher := &stubFetcher{
		flagState: &CachedFlagState{FlagID: "f/1", State: pbflagsv1.State_STATE_KILLED, Value: boolVal(true)},
	}
	eval := NewEvaluator(reg, cache, fetcher, slog.Default(), NewNoopMetrics(), noopTracer())

	val, src := eval.Evaluate(context.Background(), "f/1", "")
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT, src, "source")
	require.Equal(t, false, val.GetBoolValue(), "value = compiled default false")
}

func TestEval_UnknownFlag_FetchReturnsNil(t *testing.T) {
	cache := newTestCache(t)
	reg := newTestRegistry()
	fetcher := &stubFetcher{flagState: nil}
	eval := NewEvaluator(reg, cache, fetcher, slog.Default(), NewNoopMetrics(), noopTracer())

	val, src := eval.Evaluate(context.Background(), "unknown/1", "")
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT, src, "source")
	require.Nil(t, val, "value should be nil (no default registered)")
}

// --- Kill Set Tests ---

func TestEval_KillSet_GlobalKill(t *testing.T) {
	cache := newTestCache(t)
	reg := registryWith(globalFlag("f/1", boolVal(false)))
	cache.SetKillSet(&KillSet{
		FlagIDs: map[string]struct{}{"f/1": {}},
	})
	fetcher := &stubFetcher{
		flagState: &CachedFlagState{FlagID: "f/1", State: pbflagsv1.State_STATE_ENABLED, Value: boolVal(true)},
	}
	eval := NewEvaluator(reg, cache, fetcher, slog.Default(), NewNoopMetrics(), noopTracer())

	val, src := eval.Evaluate(context.Background(), "f/1", "")
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_KILLED, src, "source")
	require.Equal(t, false, val.GetBoolValue(), "value = compiled default false")
}

// --- Override Tests ---

func TestEval_Override_Enabled(t *testing.T) {
	cache := newTestCache(t)
	reg := registryWith(userFlag("f/1", strVal("default")))
	fetcher := &stubFetcher{
		overrides: []*CachedOverride{
			{FlagID: "f/1", EntityID: "user-1", State: pbflagsv1.State_STATE_ENABLED, Value: strVal("per-user")},
		},
	}
	eval := NewEvaluator(reg, cache, fetcher, slog.Default(), NewNoopMetrics(), noopTracer())

	val, src := eval.Evaluate(context.Background(), "f/1", "user-1")
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_OVERRIDE, src, "source")
	require.Equal(t, "per-user", val.GetStringValue(), "value")
}

func TestEval_Override_Default_FallsToCompiledDefault(t *testing.T) {
	cache := newTestCache(t)
	reg := registryWith(userFlag("f/1", strVal("compiled")))
	fetcher := &stubFetcher{
		overrides: []*CachedOverride{
			{FlagID: "f/1", EntityID: "user-1", State: pbflagsv1.State_STATE_DEFAULT, Value: nil},
		},
	}
	eval := NewEvaluator(reg, cache, fetcher, slog.Default(), NewNoopMetrics(), noopTracer())

	val, src := eval.Evaluate(context.Background(), "f/1", "user-1")
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT, src, "source")
	require.Equal(t, "compiled", val.GetStringValue(), "value")
}

func TestEval_Override_Killed_FallsToCompiledDefault(t *testing.T) {
	cache := newTestCache(t)
	reg := registryWith(userFlag("f/1", int64Val(42)))
	fetcher := &stubFetcher{
		overrides: []*CachedOverride{
			{FlagID: "f/1", EntityID: "user-1", State: pbflagsv1.State_STATE_KILLED, Value: nil},
		},
	}
	eval := NewEvaluator(reg, cache, fetcher, slog.Default(), NewNoopMetrics(), noopTracer())

	val, src := eval.Evaluate(context.Background(), "f/1", "user-1")
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT, src, "source")
	require.Equal(t, int64(42), val.GetInt64Value(), "value")
}

func TestEval_Override_GlobalLayer_SkipsOverridePath(t *testing.T) {
	cache := newTestCache(t)
	reg := registryWith(globalFlag("f/1", boolVal(false)))
	fetcher := &stubFetcher{
		flagState: &CachedFlagState{FlagID: "f/1", State: pbflagsv1.State_STATE_ENABLED, Value: boolVal(true)},
	}
	eval := NewEvaluator(reg, cache, fetcher, slog.Default(), NewNoopMetrics(), noopTracer())

	val, src := eval.Evaluate(context.Background(), "f/1", "user-1")
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_GLOBAL, src, "source (override skipped for global layer)")
	require.Equal(t, true, val.GetBoolValue(), "value")
}

func TestEval_Override_NoMatch_FallsToGlobal(t *testing.T) {
	cache := newTestCache(t)
	reg := registryWith(userFlag("f/1", strVal("default")))
	fetcher := &stubFetcher{
		overrides: []*CachedOverride{},
		flagState: &CachedFlagState{FlagID: "f/1", State: pbflagsv1.State_STATE_ENABLED, Value: strVal("global-val")},
	}
	eval := NewEvaluator(reg, cache, fetcher, slog.Default(), NewNoopMetrics(), noopTracer())

	val, src := eval.Evaluate(context.Background(), "f/1", "user-1")
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_GLOBAL, src, "source")
	require.Equal(t, "global-val", val.GetStringValue(), "value")
}

// --- Archived Flag Fallback ---

func TestEval_ArchivedFlag_WithValue(t *testing.T) {
	cache := newTestCache(t)
	reg := registryWith(globalFlag("f/1", boolVal(false)))
	fetcher := &stubFetcher{
		flagState: &CachedFlagState{FlagID: "f/1", State: pbflagsv1.State_STATE_ENABLED, Value: boolVal(true), Archived: true},
	}
	eval := NewEvaluator(reg, cache, fetcher, slog.Default(), NewNoopMetrics(), noopTracer())

	val, src := eval.Evaluate(context.Background(), "f/1", "")
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_ARCHIVED, src, "source")
	require.Equal(t, true, val.GetBoolValue(), "value (archived value)")
}

func TestEval_ArchivedFlag_NilValue(t *testing.T) {
	cache := newTestCache(t)
	reg := registryWith(globalFlag("f/1", strVal("fallback")))
	fetcher := &stubFetcher{
		flagState: &CachedFlagState{FlagID: "f/1", State: pbflagsv1.State_STATE_ENABLED, Value: nil, Archived: true},
	}
	eval := NewEvaluator(reg, cache, fetcher, slog.Default(), NewNoopMetrics(), noopTracer())

	val, src := eval.Evaluate(context.Background(), "f/1", "")
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT, src, "source")
	require.Equal(t, "fallback", val.GetStringValue(), "value")
}

// --- Stale Cache Fallback ---

func TestEval_StaleOverride_FallbackOnFetchFailure(t *testing.T) {
	cache := newTestCache(t)
	reg := registryWith(userFlag("f/1", strVal("default")))

	fetcher := &stubFetcher{
		overrides: []*CachedOverride{
			{FlagID: "f/1", EntityID: "user-1", State: pbflagsv1.State_STATE_ENABLED, Value: strVal("stale-override")},
		},
	}
	eval := NewEvaluator(reg, cache, fetcher, slog.Default(), NewNoopMetrics(), noopTracer())
	eval.Evaluate(context.Background(), "f/1", "user-1")

	time.Sleep(100 * time.Millisecond)

	fetcher.overrides = nil
	fetcher.overErr = errors.New("unreachable")
	fetcher.flagErr = errors.New("unreachable")

	val, src := eval.Evaluate(context.Background(), "f/1", "user-1")
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_CACHED, src, "source")
	require.Equal(t, "stale-override", val.GetStringValue(), "value")
}

func TestEval_AllFetchesFail_CompiledDefault(t *testing.T) {
	cache := newTestCache(t)
	reg := registryWith(userFlag("f/1", doubleVal(9.99)))
	fetcher := &stubFetcher{
		overErr: errors.New("unreachable"),
		flagErr: errors.New("unreachable"),
	}
	eval := NewEvaluator(reg, cache, fetcher, slog.Default(), NewNoopMetrics(), noopTracer())

	val, src := eval.Evaluate(context.Background(), "f/1", "user-1")
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT, src, "source")
	require.Equal(t, 9.99, val.GetDoubleValue(), "value")
}

// --- On-Demand Fetch Caching ---

func TestEval_OnDemandFetch_CachesFlagState(t *testing.T) {
	cache := newTestCache(t)
	reg := registryWith(globalFlag("f/1", boolVal(false)))

	callCount := 0
	fetcher := &callCountFetcher{
		flagState: &CachedFlagState{FlagID: "f/1", State: pbflagsv1.State_STATE_ENABLED, Value: boolVal(true)},
		counter:   &callCount,
	}
	eval := NewEvaluator(reg, cache, fetcher, slog.Default(), NewNoopMetrics(), noopTracer())

	eval.Evaluate(context.Background(), "f/1", "")
	waitCaches(cache)
	eval.Evaluate(context.Background(), "f/1", "")

	require.Equal(t, 1, callCount, "expected 1 fetch call (cached)")
}

type callCountFetcher struct {
	flagState *CachedFlagState
	flagErr   error
	overrides []*CachedOverride
	overErr   error
	counter   *int
}

func (f *callCountFetcher) FetchFlagState(_ context.Context, _ string) (*CachedFlagState, error) {
	*f.counter++
	return f.flagState, f.flagErr
}

func (f *callCountFetcher) FetchOverrides(_ context.Context, _ string, _ []string) ([]*CachedOverride, error) {
	return f.overrides, f.overErr
}

// --- Never Throw ---

func TestEval_NeverReturnsError(t *testing.T) {
	scenarios := []struct {
		name    string
		flagID  string
		entity  string
		fetcher *stubFetcher
		reg     *Registry
		killSet *KillSet
	}{
		{
			name:    "unknown flag, all fetches fail",
			flagID:  "unknown",
			fetcher: &stubFetcher{flagErr: errors.New("fail"), overErr: errors.New("fail")},
			reg:     newTestRegistry(),
		},
		{
			name:    "known flag, nil fetch result",
			flagID:  "f/1",
			fetcher: &stubFetcher{},
			reg:     registryWith(globalFlag("f/1", boolVal(true))),
		},
		{
			name:   "entity override, all fail, no stale",
			flagID: "f/1",
			entity: "user-1",
			fetcher: &stubFetcher{
				overErr: errors.New("fail"),
				flagErr: errors.New("fail"),
			},
			reg: registryWith(userFlag("f/1", nil)),
		},
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			cache := newTestCache(t)
			if sc.killSet != nil {
				cache.SetKillSet(sc.killSet)
			}
			eval := NewEvaluator(sc.reg, cache, sc.fetcher, slog.Default(), NewNoopMetrics(), noopTracer())
			val, src := eval.Evaluate(context.Background(), sc.flagID, sc.entity)
			_ = val
			_ = src
		})
	}
}

// --- All Four Value Types ---

func TestEval_AllValueTypes(t *testing.T) {
	tests := []struct {
		name  string
		value *pbflagsv1.FlagValue
		check func(*pbflagsv1.FlagValue) bool
	}{
		{"bool", boolVal(true), func(v *pbflagsv1.FlagValue) bool { return v.GetBoolValue() == true }},
		{"string", strVal("hello"), func(v *pbflagsv1.FlagValue) bool { return v.GetStringValue() == "hello" }},
		{"int64", int64Val(42), func(v *pbflagsv1.FlagValue) bool { return v.GetInt64Value() == 42 }},
		{"double", doubleVal(3.14), func(v *pbflagsv1.FlagValue) bool { return v.GetDoubleValue() == 3.14 }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cache := newTestCache(t)
			reg := registryWith(globalFlag("f/1", nil))
			fetcher := &stubFetcher{
				flagState: &CachedFlagState{FlagID: "f/1", State: pbflagsv1.State_STATE_ENABLED, Value: tt.value},
			}
			eval := NewEvaluator(reg, cache, fetcher, slog.Default(), NewNoopMetrics(), noopTracer())

			val, src := eval.Evaluate(context.Background(), "f/1", "")
			require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_GLOBAL, src, "source")
			require.True(t, tt.check(val), "value check failed for %v", val)
		})
	}
}

// --- Concurrent Evaluator safety ---

func TestEval_ConcurrentEvaluate(t *testing.T) {
	cache := newTestCache(t)
	reg := registryWith(
		globalFlag("f/1", boolVal(false)),
		userFlag("f/2", strVal("default")),
	)
	fetcher := &stubFetcher{
		flagState: &CachedFlagState{FlagID: "f/1", State: pbflagsv1.State_STATE_ENABLED, Value: boolVal(true)},
		overrides: []*CachedOverride{
			{FlagID: "f/2", EntityID: "u1", State: pbflagsv1.State_STATE_ENABLED, Value: strVal("override")},
		},
	}
	eval := NewEvaluator(reg, cache, fetcher, slog.Default(), NewNoopMetrics(), noopTracer())

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
			eval.Evaluate(context.Background(), "f/2", "u1")
		}()
	}

	for i := 0; i < 100; i++ {
		err := <-errc
		assert.NoError(t, err, "concurrent Evaluate")
	}
}
