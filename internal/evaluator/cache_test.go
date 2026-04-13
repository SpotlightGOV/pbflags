package evaluator

import (
	"testing"
	"time"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
	"github.com/stretchr/testify/require"
)

func TestCacheStore_FlagState_SetAndGet(t *testing.T) {
	cs := newTestCache(t)

	state := &CachedFlagState{
		FlagID: "feature/1",
		State:  pbflagsv1.State_STATE_ENABLED,
		Value:  &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_BoolValue{BoolValue: true}},
	}
	cs.SetFlagState(state)
	waitCaches(cs)

	got := cs.GetFlagState("feature/1")
	require.NotNil(t, got, "expected cached flag state")
	require.Equal(t, pbflagsv1.State_STATE_ENABLED, got.State, "state")
	require.Equal(t, true, got.Value.GetBoolValue(), "value")
}

func TestCacheStore_FlagState_MissReturnsNil(t *testing.T) {
	cs := newTestCache(t)
	got := cs.GetFlagState("nonexistent")
	require.Nil(t, got, "expected nil for missing flag")
}

func TestCacheStore_FlagState_TTLExpiry(t *testing.T) {
	cs := newTestCache(t)

	state := &CachedFlagState{
		FlagID: "feature/1",
		State:  pbflagsv1.State_STATE_ENABLED,
		Value:  &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_BoolValue{BoolValue: true}},
	}
	cs.SetFlagState(state)
	waitCaches(cs)
	require.NotNil(t, cs.GetFlagState("feature/1"), "expected cached state before TTL expiry")

	time.Sleep(100 * time.Millisecond)
	require.Nil(t, cs.GetFlagState("feature/1"), "expected nil after TTL expiry")
}

func TestCacheStore_StaleFlagState_SurvivesTTL(t *testing.T) {
	cs := newTestCache(t)

	state := &CachedFlagState{
		FlagID: "feature/1",
		State:  pbflagsv1.State_STATE_ENABLED,
		Value:  &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringValue{StringValue: "hello"}},
	}
	cs.SetFlagState(state)
	time.Sleep(100 * time.Millisecond)

	require.Nil(t, cs.GetFlagState("feature/1"), "expected Ristretto cache expired")

	stale := cs.GetStaleFlagState("feature/1")
	require.NotNil(t, stale, "expected stale flag state to survive TTL")
	require.Equal(t, "hello", stale.Value.GetStringValue(), "stale value")
}

func TestCacheStore_StaleFlagState_MissReturnsNil(t *testing.T) {
	cs := newTestCache(t)
	require.Nil(t, cs.GetStaleFlagState("nonexistent"), "expected nil stale for missing flag")
}

func TestCacheStore_KillSet_SetAndGet(t *testing.T) {
	cs := newTestCache(t)

	ks := cs.GetKillSet()
	require.NotNil(t, ks, "expected non-nil default kill set")
	require.False(t, ks.IsKilled("flag-1"), "expected flag-1 not killed initially")

	cs.SetKillSet(&KillSet{
		FlagIDs: map[string]struct{}{
			"flag-1": {},
			"flag-2": {},
		},
	})

	ks = cs.GetKillSet()
	require.True(t, ks.IsKilled("flag-1"), "expected flag-1 killed")
	require.True(t, ks.IsKilled("flag-2"), "expected flag-2 killed")
	require.False(t, ks.IsKilled("flag-3"), "expected flag-3 NOT globally killed")
}

func TestCacheStore_KillSet_AtomicReplace(t *testing.T) {
	cs := newTestCache(t)

	cs.SetKillSet(&KillSet{
		FlagIDs: map[string]struct{}{"flag-1": {}},
	})
	cs.SetKillSet(&KillSet{
		FlagIDs: map[string]struct{}{"flag-2": {}},
	})

	ks := cs.GetKillSet()
	require.False(t, ks.IsKilled("flag-1"), "flag-1 should no longer be killed after replacement")
	require.True(t, ks.IsKilled("flag-2"), "flag-2 should be killed after replacement")
}

func TestKillSet_NilSafe(t *testing.T) {
	var ks *KillSet
	require.False(t, ks.IsKilled("anything"), "nil KillSet.IsKilled should return false")
}

func TestCacheStore_JitteredTTL_NoJitter(t *testing.T) {
	cs, err := NewCacheStore(CacheStoreConfig{
		FlagTTL:       time.Second,
		JitterPercent: 0,
	})
	require.NoError(t, err)
	defer cs.Close()

	ttl := cs.jitteredTTL(time.Second)
	require.Equal(t, time.Second, ttl, "jitteredTTL with 0%% jitter")
}

func newWriteThroughCache(t *testing.T) *CacheStore {
	t.Helper()
	cs, err := NewCacheStore(CacheStoreConfig{
		FlagTTL:       0,
		JitterPercent: 0,
	})
	require.NoError(t, err)
	t.Cleanup(cs.Close)
	return cs
}

func TestCacheStore_WriteThrough_FlagGetAlwaysMisses(t *testing.T) {
	cs := newWriteThroughCache(t)

	state := &CachedFlagState{
		FlagID: "feature/1",
		State:  pbflagsv1.State_STATE_ENABLED,
		Value:  &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_BoolValue{BoolValue: true}},
	}
	cs.SetFlagState(state)
	waitCaches(cs)

	require.Nil(t, cs.GetFlagState("feature/1"), "write-through: hot cache should always miss")
}

func TestCacheStore_WriteThrough_FlagStaleMapPopulated(t *testing.T) {
	cs := newWriteThroughCache(t)

	state := &CachedFlagState{
		FlagID: "feature/1",
		State:  pbflagsv1.State_STATE_ENABLED,
		Value:  &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringValue{StringValue: "hello"}},
	}
	cs.SetFlagState(state)

	stale := cs.GetStaleFlagState("feature/1")
	require.NotNil(t, stale, "write-through: stale map should still be populated")
	require.Equal(t, "hello", stale.Value.GetStringValue())
}

func TestCacheStore_JitteredTTL_WithJitter(t *testing.T) {
	cs, err := NewCacheStore(CacheStoreConfig{
		FlagTTL:       time.Second,
		JitterPercent: 20,
	})
	require.NoError(t, err)
	defer cs.Close()

	base := time.Second
	minExpected := base - time.Duration(float64(base)*0.20)
	maxExpected := base + time.Duration(float64(base)*0.20)

	for i := 0; i < 100; i++ {
		ttl := cs.jitteredTTL(base)
		require.GreaterOrEqual(t, ttl, minExpected, "jitteredTTL below minimum")
		require.LessOrEqual(t, ttl, maxExpected, "jitteredTTL above maximum")
	}
}
