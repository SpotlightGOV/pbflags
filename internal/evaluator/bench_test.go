//go:build harness

package evaluator

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	example "github.com/SpotlightGOV/pbflags/gen/example"
	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
	"github.com/SpotlightGOV/pbflags/internal/celenv"
)

// --- Helpers for benchmarks ---

func benchCache(b *testing.B) *CacheStore {
	b.Helper()
	cs, err := NewCacheStore(CacheStoreConfig{
		FlagTTL:       time.Hour, // long TTL to avoid expiry during benchmarks
		JitterPercent: 0,
	})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(cs.Close)
	return cs
}

func benchCondEval(b *testing.B) *ConditionEvaluator {
	b.Helper()
	md := (&example.EvaluationContext{}).ProtoReflect().Descriptor()
	ce, err := NewConditionEvaluator(md, slog.Default())
	if err != nil {
		b.Fatal(err)
	}
	return ce
}

func benchEvaluator(b *testing.B, cache *CacheStore, fetcher Fetcher, opts ...EvaluatorOption) *Evaluator {
	b.Helper()
	return NewEvaluator(cache, fetcher, slog.Default(), NewNoopMetrics(), noopTracer(), opts...)
}

func mustMarshalConditionsB(b *testing.B, conds []*pbflagsv1.CompiledCondition) []byte {
	b.Helper()
	data, err := proto.Marshal(&pbflagsv1.StoredConditions{Conditions: conds})
	if err != nil {
		b.Fatal(err)
	}
	return data
}

func boolFlagValueBytesB(b *testing.B, v bool) []byte {
	b.Helper()
	data, err := proto.Marshal(&pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_BoolValue{BoolValue: v}})
	if err != nil {
		b.Fatal(err)
	}
	return data
}

func stringFlagValueBytesB(b *testing.B, v string) []byte {
	b.Helper()
	data, err := proto.Marshal(&pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringValue{StringValue: v}})
	if err != nil {
		b.Fatal(err)
	}
	return data
}

// --- Benchmarks ---

// BenchmarkEvaluate_KillSet measures the fastest path: flag is in kill set.
func BenchmarkEvaluate_KillSet(b *testing.B) {
	cache := benchCache(b)
	cache.SetKillSet(&KillSet{
		FlagIDs: map[string]struct{}{"bench/killed": {}},
	})
	fetcher := &stubFetcher{}
	eval := benchEvaluator(b, cache, fetcher)

	ctx := context.Background()
	b.ResetTimer()
	for range b.N {
		val, src := eval.Evaluate(ctx, "bench/killed", "")
		_ = val
		_ = src
	}
}

// BenchmarkEvaluate_CacheHit measures evaluation when flag state is cached.
func BenchmarkEvaluate_CacheHit(b *testing.B) {
	cache := benchCache(b)
	cache.SetFlagState(&CachedFlagState{
		FlagID: "bench/cached",
		State:  pbflagsv1.State_STATE_DEFAULT,
	})
	cache.WaitAll()
	fetcher := &stubFetcher{}
	eval := benchEvaluator(b, cache, fetcher)

	ctx := context.Background()
	b.ResetTimer()
	for range b.N {
		val, src := eval.Evaluate(ctx, "bench/cached", "")
		_ = val
		_ = src
	}
}

// BenchmarkEvaluate_CacheMiss_Fetch measures the cold-fetch path.
func BenchmarkEvaluate_CacheMiss_Fetch(b *testing.B) {
	cache := benchCache(b)
	fetcher := &stubFetcher{
		flagState: &CachedFlagState{
			FlagID: "bench/miss",
			State:  pbflagsv1.State_STATE_DEFAULT,
		},
	}
	eval := benchEvaluator(b, cache, fetcher)

	ctx := context.Background()
	b.ResetTimer()
	for i := range b.N {
		// Use unique flag IDs to prevent caching across iterations.
		flagID := fmt.Sprintf("bench/miss/%d", i)
		fetcher.flagState.FlagID = flagID
		val, src := eval.Evaluate(ctx, flagID, "")
		_ = val
		_ = src
	}
}

// BenchmarkEvaluateWithContext_SimpleCondition measures a single CEL condition match.
func BenchmarkEvaluateWithContext_SimpleCondition(b *testing.B) {
	ce := benchCondEval(b)
	cache := benchCache(b)

	conds := ce.CompileConditions("bench/simple", mustMarshalConditionsB(b, []*pbflagsv1.CompiledCondition{
		{Cel: `ctx.is_internal`, Value: boolFlagValueBytesB(b, true)},
		{Cel: "", Value: boolFlagValueBytesB(b, false)},
	}))

	cache.SetFlagState(&CachedFlagState{
		FlagID:     "bench/simple",
		State:      pbflagsv1.State_STATE_ENABLED,
		Conditions: conds,
	})
	cache.WaitAll()

	eval := benchEvaluator(b, cache, &stubFetcher{}, WithConditionEvaluator(ce))
	evalCtx := &example.EvaluationContext{IsInternal: true, UserId: "user-1"}
	ctx := context.Background()

	b.ResetTimer()
	for range b.N {
		val, src := eval.EvaluateWithContext(ctx, "bench/simple", evalCtx)
		_ = val
		_ = src
	}
}

// BenchmarkEvaluateWithContext_SimpleCondition_NoMatch measures a single CEL
// condition that doesn't match, falling through to otherwise.
func BenchmarkEvaluateWithContext_SimpleCondition_NoMatch(b *testing.B) {
	ce := benchCondEval(b)
	cache := benchCache(b)

	conds := ce.CompileConditions("bench/nomatch", mustMarshalConditionsB(b, []*pbflagsv1.CompiledCondition{
		{Cel: `ctx.is_internal`, Value: boolFlagValueBytesB(b, true)},
		{Cel: "", Value: boolFlagValueBytesB(b, false)},
	}))

	cache.SetFlagState(&CachedFlagState{
		FlagID:     "bench/nomatch",
		State:      pbflagsv1.State_STATE_ENABLED,
		Conditions: conds,
	})
	cache.WaitAll()

	eval := benchEvaluator(b, cache, &stubFetcher{}, WithConditionEvaluator(ce))
	evalCtx := &example.EvaluationContext{IsInternal: false, UserId: "user-1"}
	ctx := context.Background()

	b.ResetTimer()
	for range b.N {
		val, src := eval.EvaluateWithContext(ctx, "bench/nomatch", evalCtx)
		_ = val
		_ = src
	}
}

// BenchmarkEvaluateWithContext_EnumCondition measures an enum equality CEL check.
func BenchmarkEvaluateWithContext_EnumCondition(b *testing.B) {
	ce := benchCondEval(b)
	cache := benchCache(b)

	conds := ce.CompileConditions("bench/enum", mustMarshalConditionsB(b, []*pbflagsv1.CompiledCondition{
		{Cel: `ctx.plan == PlanLevel.ENTERPRISE`, Value: stringFlagValueBytesB(b, "enterprise")},
		{Cel: `ctx.plan == PlanLevel.PRO`, Value: stringFlagValueBytesB(b, "pro")},
		{Cel: "", Value: stringFlagValueBytesB(b, "default")},
	}))

	cache.SetFlagState(&CachedFlagState{
		FlagID:     "bench/enum",
		State:      pbflagsv1.State_STATE_ENABLED,
		Conditions: conds,
	})
	cache.WaitAll()

	eval := benchEvaluator(b, cache, &stubFetcher{}, WithConditionEvaluator(ce))
	evalCtx := &example.EvaluationContext{Plan: example.PlanLevel_PLAN_LEVEL_PRO}
	ctx := context.Background()

	b.ResetTimer()
	for range b.N {
		val, src := eval.EvaluateWithContext(ctx, "bench/enum", evalCtx)
		_ = val
		_ = src
	}
}

// BenchmarkEvaluateWithContext_ComplexConditionChain measures a longer condition
// chain where the matching condition is near the end.
func BenchmarkEvaluateWithContext_ComplexConditionChain(b *testing.B) {
	ce := benchCondEval(b)
	cache := benchCache(b)

	conds := ce.CompileConditions("bench/complex", mustMarshalConditionsB(b, []*pbflagsv1.CompiledCondition{
		{Cel: `ctx.plan == PlanLevel.ENTERPRISE && ctx.is_internal`, Value: stringFlagValueBytesB(b, "a")},
		{Cel: `ctx.plan == PlanLevel.ENTERPRISE && !ctx.is_internal`, Value: stringFlagValueBytesB(b, "b")},
		{Cel: `ctx.plan == PlanLevel.PRO && ctx.is_internal`, Value: stringFlagValueBytesB(b, "c")},
		{Cel: `ctx.plan == PlanLevel.PRO && !ctx.is_internal`, Value: stringFlagValueBytesB(b, "d")},
		{Cel: `ctx.device_type == DeviceType.MOBILE`, Value: stringFlagValueBytesB(b, "e")},
		{Cel: "", Value: stringFlagValueBytesB(b, "fallback")},
	}))

	cache.SetFlagState(&CachedFlagState{
		FlagID:     "bench/complex",
		State:      pbflagsv1.State_STATE_ENABLED,
		Conditions: conds,
	})
	cache.WaitAll()

	eval := benchEvaluator(b, cache, &stubFetcher{}, WithConditionEvaluator(ce))
	// This context will match the 5th CEL condition (mobile device, free plan).
	evalCtx := &example.EvaluationContext{
		Plan:       example.PlanLevel_PLAN_LEVEL_FREE,
		DeviceType: example.DeviceType_DEVICE_TYPE_MOBILE,
		UserId:     "user-1",
	}
	ctx := context.Background()

	b.ResetTimer()
	for range b.N {
		val, src := eval.EvaluateWithContext(ctx, "bench/complex", evalCtx)
		_ = val
		_ = src
	}
}

// BenchmarkEvaluateWithContext_LaunchOverride measures condition evaluation
// with a launch override that is in ramp.
func BenchmarkEvaluateWithContext_LaunchOverride(b *testing.B) {
	ce := benchCondEval(b)
	cache := benchCache(b)

	conds := ce.CompileConditions("bench/launch", mustMarshalConditionsB(b, []*pbflagsv1.CompiledCondition{
		{
			Cel:         `ctx.is_internal`,
			Value:       stringFlagValueBytesB(b, "base"),
			LaunchId:    "bench-launch",
			LaunchValue: stringFlagValueBytesB(b, "launched"),
		},
		{Cel: "", Value: stringFlagValueBytesB(b, "default")},
	}))

	launches := []CachedLaunch{
		{LaunchID: "bench-launch", Dimension: "user_id", RampPct: 100},
	}

	cache.SetFlagState(&CachedFlagState{
		FlagID:     "bench/launch",
		State:      pbflagsv1.State_STATE_ENABLED,
		Conditions: conds,
		Launches:   launches,
	})
	cache.WaitAll()

	eval := benchEvaluator(b, cache, &stubFetcher{}, WithConditionEvaluator(ce))
	evalCtx := &example.EvaluationContext{IsInternal: true, UserId: "user-1"}
	ctx := context.Background()

	b.ResetTimer()
	for range b.N {
		val, src := eval.EvaluateWithContext(ctx, "bench/launch", evalCtx)
		_ = val
		_ = src
	}
}

// BenchmarkEvaluateWithContext_ConditionCacheHit measures the condition cache
// hit path (fastest path for conditional flags).
func BenchmarkEvaluateWithContext_ConditionCacheHit(b *testing.B) {
	ce := benchCondEval(b)
	cache := benchCache(b)
	condCache, err := NewConditionCache(10_000)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(condCache.Close)

	conds := ce.CompileConditions("bench/condcache", mustMarshalConditionsB(b, []*pbflagsv1.CompiledCondition{
		{Cel: `ctx.plan == PlanLevel.ENTERPRISE`, Value: stringFlagValueBytesB(b, "enterprise")},
		{Cel: "", Value: stringFlagValueBytesB(b, "default")},
	}))

	cache.SetFlagState(&CachedFlagState{
		FlagID:     "bench/condcache",
		State:      pbflagsv1.State_STATE_ENABLED,
		Conditions: conds,
	})
	cache.WaitAll()

	eval := benchEvaluator(b, cache, &stubFetcher{},
		WithConditionEvaluator(ce),
		WithConditionCache(condCache),
	)
	evalCtx := &example.EvaluationContext{Plan: example.PlanLevel_PLAN_LEVEL_ENTERPRISE}
	ctx := context.Background()

	// Prime the condition cache.
	eval.EvaluateWithContext(ctx, "bench/condcache", evalCtx)
	condCache.Wait()

	b.ResetTimer()
	for range b.N {
		val, src := eval.EvaluateWithContext(ctx, "bench/condcache", evalCtx)
		_ = val
		_ = src
	}
}

// BenchmarkEvaluateWithContext_ConditionCacheMiss_VaryingContexts measures
// condition cache behavior with diverse contexts (bounded by dimension
// classification, simulating real traffic).
func BenchmarkEvaluateWithContext_ConditionCacheMiss_VaryingContexts(b *testing.B) {
	ce := benchCondEval(b)
	cache := benchCache(b)
	condCache, err := NewConditionCache(100_000)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(condCache.Close)

	conds := ce.CompileConditions("bench/vary", mustMarshalConditionsB(b, []*pbflagsv1.CompiledCondition{
		{Cel: `ctx.plan == PlanLevel.ENTERPRISE`, Value: stringFlagValueBytesB(b, "enterprise")},
		{Cel: "", Value: stringFlagValueBytesB(b, "default")},
	}))

	dimMeta := CachedDimMeta{
		"plan": &celenv.DimensionMeta{
			Classification: celenv.FiniteFilterDistinct,
			LiteralSet:     []string{"1", "2", "3"},
		},
	}

	cache.SetFlagState(&CachedFlagState{
		FlagID:     "bench/vary",
		State:      pbflagsv1.State_STATE_ENABLED,
		Conditions: conds,
		DimMeta:    dimMeta,
	})
	cache.WaitAll()

	eval := benchEvaluator(b, cache, &stubFetcher{},
		WithConditionEvaluator(ce),
		WithConditionCache(condCache),
	)

	// Pre-build contexts with distinct plans (bounded cardinality → cache reuse).
	plans := []example.PlanLevel{
		example.PlanLevel_PLAN_LEVEL_ENTERPRISE,
		example.PlanLevel_PLAN_LEVEL_PRO,
		example.PlanLevel_PLAN_LEVEL_FREE,
	}
	contexts := make([]*example.EvaluationContext, 1000)
	for i := range contexts {
		contexts[i] = &example.EvaluationContext{
			Plan:   plans[i%3],
			UserId: fmt.Sprintf("user-%d", i),
		}
	}

	// Prime cache with all distinct plan values.
	ctx := context.Background()
	for _, ec := range contexts[:3] {
		eval.EvaluateWithContext(ctx, "bench/vary", ec)
	}
	condCache.Wait()

	b.ResetTimer()
	for i := range b.N {
		ec := contexts[i%len(contexts)]
		val, src := eval.EvaluateWithContext(ctx, "bench/vary", ec)
		_ = val
		_ = src
	}
}

// BenchmarkHashBucket measures the raw hash function.
func BenchmarkHashBucket(b *testing.B) {
	for range b.N {
		_ = HashBucket("bench-launch", "user-42")
	}
}

// BenchmarkHashBucket_VaryingInputs measures hash with varying user IDs.
func BenchmarkHashBucket_VaryingInputs(b *testing.B) {
	users := make([]string, 1000)
	for i := range users {
		users[i] = fmt.Sprintf("user-%d", i)
	}
	b.ResetTimer()
	for i := range b.N {
		_ = HashBucket("bench-launch", users[i%len(users)])
	}
}

// BenchmarkBuildCacheKey measures cache key construction with dimension metadata.
func BenchmarkBuildCacheKey(b *testing.B) {
	meta := CachedDimMeta{
		"plan": &celenv.DimensionMeta{
			Classification: celenv.FiniteFilterDistinct,
			LiteralSet:     []string{"1", "2", "3"},
		},
		"is_internal": &celenv.DimensionMeta{
			Classification: celenv.FiniteFilterUniform,
			LiteralSet:     []string{"true"},
		},
		"user_id": &celenv.DimensionMeta{
			Classification: celenv.Unbounded,
		},
	}

	evalCtx := &example.EvaluationContext{
		Plan:       example.PlanLevel_PLAN_LEVEL_PRO,
		IsInternal: true,
		UserId:     "user-42",
	}

	b.ResetTimer()
	for range b.N {
		_ = BuildCacheKey("bench/flag", 1, meta, evalCtx)
	}
}

// BenchmarkBuildCacheKey_WithLaunches measures cache key with launch components.
func BenchmarkBuildCacheKey_WithLaunches(b *testing.B) {
	meta := CachedDimMeta{
		"plan": &celenv.DimensionMeta{
			Classification: celenv.FiniteFilterDistinct,
			LiteralSet:     []string{"1", "2", "3"},
		},
	}

	launches := []CachedLaunch{
		{LaunchID: "launch-1", Dimension: "user_id", RampPct: 50},
		{LaunchID: "launch-2", Dimension: "user_id", RampPct: 25},
	}

	evalCtx := &example.EvaluationContext{
		Plan:   example.PlanLevel_PLAN_LEVEL_PRO,
		UserId: "user-42",
	}

	b.ResetTimer()
	for range b.N {
		_ = BuildCacheKey("bench/flag", 1, meta, evalCtx, launches...)
	}
}

// BenchmarkConditionEvaluate_Direct measures raw CEL condition evaluation
// (bypassing the Evaluator/cache layer).
func BenchmarkConditionEvaluate_Direct(b *testing.B) {
	ce := benchCondEval(b)

	conds := ce.CompileConditions("bench/direct", mustMarshalConditionsB(b, []*pbflagsv1.CompiledCondition{
		{Cel: `ctx.plan == PlanLevel.ENTERPRISE`, Value: stringFlagValueBytesB(b, "enterprise")},
		{Cel: `ctx.plan == PlanLevel.PRO`, Value: stringFlagValueBytesB(b, "pro")},
		{Cel: "", Value: stringFlagValueBytesB(b, "default")},
	}))

	evalCtx := &example.EvaluationContext{Plan: example.PlanLevel_PLAN_LEVEL_PRO}

	b.ResetTimer()
	for range b.N {
		res := ce.EvaluateConditions("bench/direct", conds, evalCtx)
		_ = res
	}
}

// BenchmarkConditionEvaluate_WorstCase measures CEL evaluation where no
// condition matches (all programs evaluated, no early exit).
func BenchmarkConditionEvaluate_WorstCase(b *testing.B) {
	ce := benchCondEval(b)

	conds := ce.CompileConditions("bench/worst", mustMarshalConditionsB(b, []*pbflagsv1.CompiledCondition{
		{Cel: `ctx.plan == PlanLevel.ENTERPRISE`, Value: stringFlagValueBytesB(b, "a")},
		{Cel: `ctx.plan == PlanLevel.PRO`, Value: stringFlagValueBytesB(b, "b")},
		{Cel: `ctx.is_internal`, Value: stringFlagValueBytesB(b, "c")},
		{Cel: `ctx.device_type == DeviceType.MOBILE`, Value: stringFlagValueBytesB(b, "d")},
		{Cel: `ctx.device_type == DeviceType.TABLET`, Value: stringFlagValueBytesB(b, "e")},
	}))

	// Context that matches none of the above.
	evalCtx := &example.EvaluationContext{
		Plan:       example.PlanLevel_PLAN_LEVEL_FREE,
		IsInternal: false,
		DeviceType: example.DeviceType_DEVICE_TYPE_DESKTOP,
	}

	b.ResetTimer()
	for range b.N {
		res := ce.EvaluateConditions("bench/worst", conds, evalCtx)
		_ = res
	}
}

// BenchmarkKillSet_Lookup measures kill set lookup with a populated set.
func BenchmarkKillSet_Lookup(b *testing.B) {
	ks := &KillSet{FlagIDs: make(map[string]struct{}, 1000)}
	for i := range 1000 {
		ks.FlagIDs[fmt.Sprintf("flag/%d", i)] = struct{}{}
	}

	b.ResetTimer()
	for range b.N {
		_ = ks.IsKilled("flag/500")
	}
}

// BenchmarkKillSet_Miss measures kill set miss performance.
func BenchmarkKillSet_Miss(b *testing.B) {
	ks := &KillSet{FlagIDs: make(map[string]struct{}, 1000)}
	for i := range 1000 {
		ks.FlagIDs[fmt.Sprintf("flag/%d", i)] = struct{}{}
	}

	b.ResetTimer()
	for range b.N {
		_ = ks.IsKilled("flag/nonexistent")
	}
}

// BenchmarkInRamp measures the InRamp method (proto field extraction + hash).
func BenchmarkInRamp(b *testing.B) {
	launch := &CachedLaunch{LaunchID: "bench-launch", Dimension: "user_id", RampPct: 50}
	evalCtx := &example.EvaluationContext{UserId: "user-42"}

	b.ResetTimer()
	for range b.N {
		_ = launch.InRamp(evalCtx)
	}
}
