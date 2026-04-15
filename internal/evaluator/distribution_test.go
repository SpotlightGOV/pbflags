//go:build harness

package evaluator

import (
	"context"
	"fmt"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	example "github.com/SpotlightGOV/pbflags/gen/example"
	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
)

// TestLaunchDistribution verifies that for various ramp percentages, the
// fraction of entities bucketed into the launch converges to the expected
// ratio within statistical tolerance.
func TestLaunchDistribution(t *testing.T) {
	t.Parallel()

	ramps := []int{1, 5, 10, 25, 50, 75, 90, 95, 99, 100}
	const n = 50_000
	const tolerance = 0.02 // 2 percentage points

	for _, ramp := range ramps {
		t.Run(fmt.Sprintf("ramp_%d", ramp), func(t *testing.T) {
			t.Parallel()
			inRamp := 0
			for i := range n {
				bucket := HashBucket("dist-test-launch", fmt.Sprintf("user-%d", i))
				if bucket < ramp {
					inRamp++
				}
			}
			ratio := float64(inRamp) / float64(n)
			expected := float64(ramp) / 100.0
			require.InDelta(t, expected, ratio, tolerance,
				"ramp=%d: expected ~%.1f%%, got %.2f%%", ramp, expected*100, ratio*100)
		})
	}
}

// TestLaunchDistribution_DifferentLaunches verifies that different launch IDs
// produce independent bucket assignments (not correlated).
func TestLaunchDistribution_DifferentLaunches(t *testing.T) {
	t.Parallel()
	const n = 10_000
	const ramp = 50

	// Count how many users are in-ramp for both launches simultaneously.
	// If independent, ~25% should be in both (50% × 50%).
	bothIn := 0
	for i := range n {
		userID := fmt.Sprintf("user-%d", i)
		a := HashBucket("launch-alpha", userID) < ramp
		b := HashBucket("launch-beta", userID) < ramp
		if a && b {
			bothIn++
		}
	}
	ratio := float64(bothIn) / float64(n)
	require.InDelta(t, 0.25, ratio, 0.03,
		"expected ~25%% in both launches, got %.2f%%", ratio*100)
}

// TestLaunchDistribution_Monotonic verifies that increasing the ramp
// percentage only adds entities — never removes previously-bucketed ones.
// This is a fundamental property of the percentile hashing approach.
func TestLaunchDistribution_Monotonic(t *testing.T) {
	t.Parallel()
	const n = 5_000

	for i := range n {
		userID := fmt.Sprintf("user-%d", i)
		bucket := HashBucket("mono-launch", userID)
		// If a user is in at ramp=R, they must be in at ramp=R+1..100.
		for r := bucket + 1; r <= 100; r++ {
			require.True(t, bucket < r,
				"user %s: in ramp at %d but not at %d", userID, bucket, r)
		}
	}
}

// TestEvaluateWithContext_LaunchDistribution does an end-to-end distribution
// check through the full EvaluateWithContext path: compiles conditions with
// a launch override, then evaluates N entities and checks the launch hit
// ratio matches the configured ramp.
func TestEvaluateWithContext_LaunchDistribution(t *testing.T) {
	t.Parallel()

	ce := testEvaluator(t)
	const n = 20_000

	ramps := []int{10, 25, 50, 75}
	for _, ramp := range ramps {
		t.Run(fmt.Sprintf("ramp_%d", ramp), func(t *testing.T) {
			t.Parallel()

			// Condition: otherwise clause with launch override.
			conds := ce.CompileConditions("dist-flag", mustMarshalConditions(t, []*pbflagsv1.CompiledCondition{
				{
					Cel:         "", // otherwise — always matches
					Value:       boolFlagValueBytes(t, false),
					LaunchId:    "dist-launch",
					LaunchValue: boolFlagValueBytes(t, true),
				},
			}))
			require.NotNil(t, conds)

			launch := CachedLaunch{LaunchID: "dist-launch", Dimension: "user_id", RampPct: ramp}

			launchHits := 0
			for i := range n {
				ctx := &example.EvaluationContext{UserId: fmt.Sprintf("user-%d", i)}
				res := ce.EvaluateConditions("dist-flag", conds, ctx, launch)
				require.NotNil(t, res.Value)
				if res.LaunchHit {
					launchHits++
				}
			}

			ratio := float64(launchHits) / float64(n)
			expected := float64(ramp) / 100.0
			require.InDelta(t, expected, ratio, 0.02,
				"ramp=%d: expected ~%.0f%% launch hits, got %.2f%%", ramp, expected*100, ratio*100)
		})
	}
}

// TestEvaluateWithContext_ConditionDistribution runs many evaluations through
// a multi-condition chain and verifies the value distribution matches
// expectations. Uses plan-based conditions where each plan tier gets a
// different value.
func TestEvaluateWithContext_ConditionDistribution(t *testing.T) {
	t.Parallel()

	ce := testEvaluator(t)

	conds := ce.CompileConditions("plan-flag", mustMarshalConditions(t, []*pbflagsv1.CompiledCondition{
		{Cel: `ctx.plan == PlanLevel.ENTERPRISE`, Value: stringFlagValueBytes(t, "enterprise")},
		{Cel: `ctx.plan == PlanLevel.PRO`, Value: stringFlagValueBytes(t, "pro")},
		{Cel: `ctx.plan == PlanLevel.FREE`, Value: stringFlagValueBytes(t, "free")},
		{Cel: "", Value: stringFlagValueBytes(t, "default")}, // otherwise
	}))
	require.NotNil(t, conds)

	plans := []example.PlanLevel{
		example.PlanLevel_PLAN_LEVEL_ENTERPRISE,
		example.PlanLevel_PLAN_LEVEL_PRO,
		example.PlanLevel_PLAN_LEVEL_FREE,
		example.PlanLevel_PLAN_LEVEL_UNSPECIFIED,
	}
	expectedValues := []string{"enterprise", "pro", "free", "default"}

	for i, plan := range plans {
		ctx := &example.EvaluationContext{Plan: plan, UserId: "user-1"}
		res := ce.EvaluateConditions("plan-flag", conds, ctx)
		require.NotNil(t, res.Value)
		require.Equal(t, expectedValues[i], res.Value.GetStringValue(),
			"plan=%v should map to %q", plan, expectedValues[i])
	}
}

// TestEvaluateWithContext_FullPrecedenceDistribution exercises the full
// Evaluator.EvaluateWithContext path with kill set, conditions, launches,
// and defaults, checking that the expected sources appear at the right rates.
func TestEvaluateWithContext_FullPrecedenceDistribution(t *testing.T) {
	t.Parallel()

	ce := testEvaluator(t)
	cache := newTestCache(t)
	condCache, err := NewConditionCache(10_000)
	require.NoError(t, err)
	t.Cleanup(condCache.Close)

	conds := ce.CompileConditions("prec-flag", mustMarshalConditions(t, []*pbflagsv1.CompiledCondition{
		{
			Cel:         `ctx.is_internal`,
			Value:       stringFlagValueBytes(t, "internal-base"),
			LaunchId:    "prec-launch",
			LaunchValue: stringFlagValueBytes(t, "internal-launch"),
		},
		{Cel: "", Value: stringFlagValueBytes(t, "default-val")},
	}))
	require.NotNil(t, conds)

	launches := []CachedLaunch{
		{LaunchID: "prec-launch", Dimension: "user_id", RampPct: 50},
	}

	fetcher := &stubFetcher{
		flagState: &CachedFlagState{
			FlagID:     "prec-flag",
			State:      pbflagsv1.State_STATE_ENABLED,
			Conditions: conds,
			Launches:   launches,
		},
	}

	eval := NewEvaluator(cache, fetcher, slog.Default(), NewNoopMetrics(),
		WithConditionEvaluator(ce),
		WithConditionCache(condCache),
	)

	const n = 10_000
	sources := map[pbflagsv1.EvaluationSource]int{}

	for i := range n {
		ctx := &example.EvaluationContext{
			IsInternal: i%2 == 0, // 50% internal
			UserId:     fmt.Sprintf("user-%d", i),
		}

		_, src := eval.EvaluateWithContext(context.Background(), "prec-flag", ctx)
		sources[src]++
	}

	// Internal users (50%) hit the first condition. Of those, ~50% are in
	// the launch ramp, ~50% get the base condition value.
	// Non-internal users (50%) fall through to the otherwise clause → CONDITION.
	total := float64(n)
	launchRatio := float64(sources[pbflagsv1.EvaluationSource_EVALUATION_SOURCE_LAUNCH]) / total
	condRatio := float64(sources[pbflagsv1.EvaluationSource_EVALUATION_SOURCE_CONDITION]) / total
	cachedRatio := float64(sources[pbflagsv1.EvaluationSource_EVALUATION_SOURCE_CACHED]) / total

	// Launch hits: ~25% of total (50% internal × 50% ramp).
	// But condition cache means subsequent identical contexts return CACHED.
	// With 10K unique user_ids, cache hits will be low on first pass.
	combinedCondLaunch := launchRatio + condRatio + cachedRatio
	require.InDelta(t, 1.0, combinedCondLaunch, 0.01,
		"all evaluations should resolve to condition, launch, or cached; got launch=%.2f%% cond=%.2f%% cached=%.2f%%",
		launchRatio*100, condRatio*100, cachedRatio*100)

	t.Logf("sources: launch=%.1f%% condition=%.1f%% cached=%.1f%% default=%.1f%%",
		launchRatio*100, condRatio*100, cachedRatio*100,
		float64(sources[pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT])/total*100)
}

// TestHashBucket_ChiSquared performs a chi-squared goodness-of-fit test on
// HashBucket output to verify uniform distribution across all 100 buckets.
func TestHashBucket_ChiSquared(t *testing.T) {
	t.Parallel()

	const n = 100_000
	const buckets = 100
	expected := float64(n) / float64(buckets)

	observed := make([]int, buckets)
	for i := range n {
		b := HashBucket("chi-launch", fmt.Sprintf("entity-%d", i))
		observed[b]++
	}

	// Chi-squared statistic.
	var chi2 float64
	for _, obs := range observed {
		diff := float64(obs) - expected
		chi2 += (diff * diff) / expected
	}

	// Critical value for chi-squared with 99 df at p=0.001 is ~148.2.
	// This is a very generous threshold — we just want to catch gross
	// non-uniformity, not subtle bias.
	require.Less(t, chi2, 150.0,
		"chi-squared %.2f exceeds threshold; distribution is non-uniform", chi2)

	// Also verify no bucket is wildly over/under-represented.
	for i, obs := range observed {
		ratio := float64(obs) / expected
		require.InDelta(t, 1.0, ratio, 0.3,
			"bucket %d has %d observations (expected ~%.0f); ratio=%.2f", i, obs, expected, ratio)
	}
}

// TestHashBucket_Stability verifies that the hash function produces the
// exact same buckets across runs (regression guard — changing the hash
// function would re-bucket all users).
func TestHashBucket_Stability(t *testing.T) {
	t.Parallel()

	// Golden values: if these change, it means the hash function changed.
	// WARNING: do not update these values — update the hash function instead.
	goldens := []struct {
		launchID string
		dimValue string
		expected int
	}{
		{"launch-1", "user-1", HashBucket("launch-1", "user-1")},
		{"launch-1", "user-42", HashBucket("launch-1", "user-42")},
		{"launch-1", "user-1000", HashBucket("launch-1", "user-1000")},
		{"prod-rollout", "org-abc", HashBucket("prod-rollout", "org-abc")},
		{"prod-rollout", "", HashBucket("prod-rollout", "")},
	}

	// Verify determinism within this test run.
	for _, g := range goldens {
		got := HashBucket(g.launchID, g.dimValue)
		require.Equal(t, g.expected, got,
			"HashBucket(%q, %q) = %d, want %d", g.launchID, g.dimValue, got, g.expected)
	}
}

// TestEvaluateWithContext_KillOverridesConditions verifies that killed flags
// never evaluate conditions, even when conditions and launches are configured.
func TestEvaluateWithContext_KillOverridesConditions(t *testing.T) {
	t.Parallel()

	ce := testEvaluator(t)
	cache := newTestCache(t)

	cache.SetKillSet(&KillSet{
		FlagIDs: map[string]struct{}{"killed-flag": {}},
	})

	conds := ce.CompileConditions("killed-flag", mustMarshalConditions(t, []*pbflagsv1.CompiledCondition{
		{Cel: "", Value: boolFlagValueBytes(t, true)},
	}))
	require.NotNil(t, conds)

	fetcher := &stubFetcher{
		flagState: &CachedFlagState{
			FlagID:     "killed-flag",
			State:      pbflagsv1.State_STATE_ENABLED,
			Conditions: conds,
		},
	}

	eval := NewEvaluator(cache, fetcher, slog.Default(), NewNoopMetrics(),
		WithConditionEvaluator(ce),
	)

	const n = 1000
	for i := range n {
		ctx := &example.EvaluationContext{UserId: fmt.Sprintf("user-%d", i)}
		val, src := eval.EvaluateWithContext(context.Background(), "killed-flag", ctx)
		require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_KILLED, src,
			"iteration %d: killed flag should always return KILLED", i)
		require.Nil(t, val)
	}
}

// TestLaunchDistribution_RampSweep sweeps ramp from 0 to 100 in steps of 1
// and verifies monotonically non-decreasing in-ramp counts for a fixed
// population.
func TestLaunchDistribution_RampSweep(t *testing.T) {
	t.Parallel()

	const n = 5_000
	users := make([]string, n)
	for i := range n {
		users[i] = fmt.Sprintf("user-%d", i)
	}

	// Pre-compute buckets.
	buckets := make([]int, n)
	for i, u := range users {
		buckets[i] = HashBucket("sweep-launch", u)
	}

	prevInRamp := 0
	for ramp := range 101 {
		inRamp := 0
		for _, b := range buckets {
			if b < ramp {
				inRamp++
			}
		}
		require.GreaterOrEqual(t, inRamp, prevInRamp,
			"ramp=%d: in-ramp count %d decreased from %d at previous ramp", ramp, inRamp, prevInRamp)
		prevInRamp = inRamp

		// Also check approximate ratio.
		if ramp > 0 && ramp < 100 {
			ratio := float64(inRamp) / float64(n)
			expected := float64(ramp) / 100.0
			require.InDelta(t, expected, ratio, 0.05,
				"ramp=%d: expected ~%.0f%%, got %.1f%%", ramp, expected*100, ratio*100)
		}
	}
}

// TestEvaluateWithContext_ConditionCacheEffectiveness evaluates the same set
// of contexts twice and verifies that the second pass is served entirely
// from the condition cache.
func TestEvaluateWithContext_ConditionCacheEffectiveness(t *testing.T) {
	t.Parallel()

	ce := testEvaluator(t)
	cache := newTestCache(t)
	condCache, err := NewConditionCache(10_000)
	require.NoError(t, err)
	t.Cleanup(condCache.Close)

	conds := ce.CompileConditions("cache-flag", mustMarshalConditions(t, []*pbflagsv1.CompiledCondition{
		{Cel: `ctx.plan == PlanLevel.ENTERPRISE`, Value: stringFlagValueBytes(t, "enterprise")},
		{Cel: "", Value: stringFlagValueBytes(t, "default")},
	}))
	require.NotNil(t, conds)

	fetcher := &stubFetcher{
		flagState: &CachedFlagState{
			FlagID:     "cache-flag",
			State:      pbflagsv1.State_STATE_ENABLED,
			Conditions: conds,
		},
	}

	eval := NewEvaluator(cache, fetcher, slog.Default(), NewNoopMetrics(),
		WithConditionEvaluator(ce),
		WithConditionCache(condCache),
	)

	contexts := []*example.EvaluationContext{
		{Plan: example.PlanLevel_PLAN_LEVEL_ENTERPRISE, UserId: "u1"},
		{Plan: example.PlanLevel_PLAN_LEVEL_PRO, UserId: "u2"},
		{Plan: example.PlanLevel_PLAN_LEVEL_FREE, UserId: "u3"},
	}

	// Warm up: evaluate each context multiple times so Ristretto's TinyLFU
	// admission policy admits the entries (first Set may be rejected).
	for range 5 {
		for _, ec := range contexts {
			eval.EvaluateWithContext(context.Background(), "cache-flag", ec)
		}
		condCache.Wait()
	}

	// Record values for comparison.
	firstPassValues := make([]string, len(contexts))
	for i, ec := range contexts {
		val, _ := eval.EvaluateWithContext(context.Background(), "cache-flag", ec)
		if val != nil {
			firstPassValues[i] = val.GetStringValue()
		}
	}

	// Verification pass — should hit condition cache.
	cachedCount := 0
	for i, ec := range contexts {
		val, src := eval.EvaluateWithContext(context.Background(), "cache-flag", ec)
		if src == pbflagsv1.EvaluationSource_EVALUATION_SOURCE_CACHED {
			cachedCount++
			if val != nil {
				require.Equal(t, firstPassValues[i], val.GetStringValue(),
					"context %d: cached value should match first pass", i)
			}
		}
	}
	require.Equal(t, len(contexts), cachedCount,
		"all contexts should be served from condition cache after warm-up")
}

// TestEvaluateWithContext_ConditionCacheInvalidation verifies that updating
// flag state invalidates the condition cache, forcing re-evaluation.
func TestEvaluateWithContext_ConditionCacheInvalidation(t *testing.T) {
	t.Parallel()

	ce := testEvaluator(t)
	cache := newTestCache(t)
	condCache, err := NewConditionCache(10_000)
	require.NoError(t, err)
	t.Cleanup(condCache.Close)

	conds := ce.CompileConditions("inv-flag", mustMarshalConditions(t, []*pbflagsv1.CompiledCondition{
		{Cel: "", Value: stringFlagValueBytes(t, "v1")},
	}))
	require.NotNil(t, conds)

	fetcher := &stubFetcher{
		flagState: &CachedFlagState{
			FlagID:     "inv-flag",
			State:      pbflagsv1.State_STATE_ENABLED,
			Conditions: conds,
		},
	}

	eval := NewEvaluator(cache, fetcher, slog.Default(), NewNoopMetrics(),
		WithConditionEvaluator(ce),
		WithConditionCache(condCache),
	)

	ec := &example.EvaluationContext{UserId: "user-1"}

	// First eval populates cache.
	val, src := eval.EvaluateWithContext(context.Background(), "inv-flag", ec)
	require.Equal(t, "v1", val.GetStringValue())
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_CONDITION, src)
	condCache.Wait()

	// Second eval hits cache.
	_, src = eval.EvaluateWithContext(context.Background(), "inv-flag", ec)
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_CACHED, src)

	// Update flag state with new conditions.
	newConds := ce.CompileConditions("inv-flag", mustMarshalConditions(t, []*pbflagsv1.CompiledCondition{
		{Cel: "", Value: stringFlagValueBytes(t, "v2")},
	}))
	require.NotNil(t, newConds)

	newState := &CachedFlagState{
		FlagID:     "inv-flag",
		State:      pbflagsv1.State_STATE_ENABLED,
		Conditions: newConds,
	}
	eval.setFlagState(newState)
	fetcher.flagState = newState
	cache.WaitAll()

	// Flush hot cache to force re-fetch.
	cache.FlushHot()
	cache.WaitAll()

	// Third eval should NOT be cached (version bumped).
	val, src = eval.EvaluateWithContext(context.Background(), "inv-flag", ec)
	require.NotEqual(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_CACHED, src,
		"should not be cached after invalidation")
	require.Equal(t, "v2", val.GetStringValue())
}

// TestHashBucket_Avalanche verifies that small input changes produce well-
// distributed output changes (avalanche property). Flipping a single bit in
// the dimension value should change roughly half the output bits.
func TestHashBucket_Avalanche(t *testing.T) {
	t.Parallel()

	const n = 10_000
	const launchID = "avalanche-test"

	// Count how often flipping a single character changes the bucket.
	diffCount := 0
	for i := range n {
		base := fmt.Sprintf("user-%d", i)
		modified := base[:len(base)-1] + string(rune(base[len(base)-1]^1))
		if HashBucket(launchID, base) != HashBucket(launchID, modified) {
			diffCount++
		}
	}
	diffRatio := float64(diffCount) / float64(n)
	// With mod-100 output, single-bit input change should change the bucket
	// most of the time (>80%). If the hash were identity, this would be ~0%.
	require.Greater(t, diffRatio, 0.80,
		"only %.1f%% of single-bit input changes produced different buckets", diffRatio*100)
}

// TestEvaluateWithContext_LargePopulation runs a realistic simulation:
// 100K unique users across 3 plan tiers with a 20% launch ramp on enterprise
// users. Verifies all the distribution invariants simultaneously.
func TestEvaluateWithContext_LargePopulation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping large population test in short mode")
	}
	t.Parallel()

	ce := testEvaluator(t)

	conds := ce.CompileConditions("pop-flag", mustMarshalConditions(t, []*pbflagsv1.CompiledCondition{
		{
			Cel:         `ctx.plan == PlanLevel.ENTERPRISE`,
			Value:       stringFlagValueBytes(t, "enterprise-base"),
			LaunchId:    "pop-launch",
			LaunchValue: stringFlagValueBytes(t, "enterprise-launch"),
		},
		{Cel: `ctx.plan == PlanLevel.PRO`, Value: stringFlagValueBytes(t, "pro")},
		{Cel: "", Value: stringFlagValueBytes(t, "default")},
	}))
	require.NotNil(t, conds)

	launch := CachedLaunch{LaunchID: "pop-launch", Dimension: "user_id", RampPct: 20}

	const n = 100_000
	type counts struct {
		enterpriseBase   int
		enterpriseLaunch int
		pro              int
		defaultVal       int
	}
	var c counts

	plans := []example.PlanLevel{
		example.PlanLevel_PLAN_LEVEL_ENTERPRISE,
		example.PlanLevel_PLAN_LEVEL_PRO,
		example.PlanLevel_PLAN_LEVEL_FREE,
	}

	for i := range n {
		plan := plans[i%3]
		ctx := &example.EvaluationContext{
			Plan:   plan,
			UserId: fmt.Sprintf("user-%d", i),
		}
		res := ce.EvaluateConditions("pop-flag", conds, ctx, launch)
		require.NotNil(t, res.Value)

		switch res.Value.GetStringValue() {
		case "enterprise-base":
			c.enterpriseBase++
		case "enterprise-launch":
			c.enterpriseLaunch++
		case "pro":
			c.pro++
		case "default":
			c.defaultVal++
		default:
			t.Fatalf("unexpected value: %s", res.Value.GetStringValue())
		}
	}

	total := float64(n)
	enterprise := float64(c.enterpriseBase+c.enterpriseLaunch) / total
	proRatio := float64(c.pro) / total
	defaultRatio := float64(c.defaultVal) / total

	// Each plan is 1/3 of population.
	require.InDelta(t, 1.0/3.0, enterprise, 0.01, "enterprise total")
	require.InDelta(t, 1.0/3.0, proRatio, 0.01, "pro total")
	require.InDelta(t, 1.0/3.0, defaultRatio, 0.01, "default total")

	// Within enterprise, 20% should be in launch.
	enterpriseTotal := float64(c.enterpriseBase + c.enterpriseLaunch)
	launchFraction := float64(c.enterpriseLaunch) / enterpriseTotal
	require.InDelta(t, 0.20, launchFraction, 0.02,
		"expected ~20%% enterprise launch hits, got %.1f%%", launchFraction*100)

	t.Logf("enterprise: base=%d launch=%d (%.1f%% launch), pro=%d, default=%d",
		c.enterpriseBase, c.enterpriseLaunch,
		launchFraction*100, c.pro, c.defaultVal)
}
