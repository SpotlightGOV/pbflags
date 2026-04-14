package evaluator

import (
	"testing"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
	"github.com/stretchr/testify/require"
)

func TestHashBucket(t *testing.T) {
	t.Parallel()

	// Determinism: same inputs always produce the same bucket.
	b1 := HashBucket("launch-1", "user-42")
	b2 := HashBucket("launch-1", "user-42")
	require.Equal(t, b1, b2, "HashBucket should be deterministic")
	require.GreaterOrEqual(t, b1, 0)
	require.Less(t, b1, 100)

	// Different launch IDs produce different buckets (with high probability).
	b3 := HashBucket("launch-2", "user-42")
	// Not guaranteed different but statistically very likely.
	_ = b3

	// Different dimension values produce different buckets (with high probability).
	b4 := HashBucket("launch-1", "user-99")
	_ = b4
}

func TestHashBucketDistribution(t *testing.T) {
	t.Parallel()

	// With 10000 distinct users and ramp at 25%, roughly 25% should be in-ramp.
	const n = 10000
	const ramp = 25
	inRamp := 0
	for i := range n {
		bucket := HashBucket("test-launch", string(rune('A'+i%26))+string(rune('0'+i)))
		if bucket < ramp {
			inRamp++
		}
	}
	ratio := float64(inRamp) / float64(n)
	require.InDelta(t, 0.25, ratio, 0.05, "expected ~25%% in ramp, got %.2f%%", ratio*100)
}

func TestEvaluateLaunches_NoContext(t *testing.T) {
	t.Parallel()
	launches := []CachedLaunch{{
		LaunchID:  "launch-1",
		Dimension: "user_id",
		RampPct:   100,
		Value:     &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_BoolValue{BoolValue: true}},
	}}

	val, id := EvaluateLaunches(launches, nil)
	require.Nil(t, val)
	require.Empty(t, id)
}

func TestEvaluateLaunches_NoLaunches(t *testing.T) {
	t.Parallel()
	// With empty launches, should return nil.
	val, id := EvaluateLaunches(nil, nil)
	require.Nil(t, val)
	require.Empty(t, id)
}
