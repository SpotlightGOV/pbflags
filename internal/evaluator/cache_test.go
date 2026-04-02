package evaluator

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
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

func TestCacheStore_Override_SetAndGet(t *testing.T) {
	cs := newTestCache(t)

	override := &CachedOverride{
		FlagID:   "feature/1",
		EntityID: "user-42",
		State:    pbflagsv1.State_STATE_ENABLED,
		Value:    &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_Int64Value{Int64Value: 99}},
	}
	cs.SetOverride(override)
	waitCaches(cs)

	got := cs.GetOverride("feature/1", "user-42")
	require.NotNil(t, got, "expected cached override")
	require.Equal(t, int64(99), got.Value.GetInt64Value(), "override value")
}

func TestCacheStore_Override_TTLExpiry(t *testing.T) {
	cs := newTestCache(t)

	override := &CachedOverride{
		FlagID:   "feature/1",
		EntityID: "user-1",
		State:    pbflagsv1.State_STATE_ENABLED,
		Value:    &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_BoolValue{BoolValue: true}},
	}
	cs.SetOverride(override)
	waitCaches(cs)
	require.NotNil(t, cs.GetOverride("feature/1", "user-1"), "expected override before TTL")

	time.Sleep(100 * time.Millisecond)
	require.Nil(t, cs.GetOverride("feature/1", "user-1"), "expected nil after TTL")
}

func TestCacheStore_StaleOverride_SurvivesTTL(t *testing.T) {
	cs := newTestCache(t)

	override := &CachedOverride{
		FlagID:   "feature/1",
		EntityID: "user-1",
		State:    pbflagsv1.State_STATE_ENABLED,
		Value:    &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_DoubleValue{DoubleValue: 3.14}},
	}
	cs.SetOverride(override)
	time.Sleep(100 * time.Millisecond)

	stale := cs.GetStaleOverride("feature/1", "user-1")
	require.NotNil(t, stale, "expected stale override to survive TTL")
	require.Equal(t, 3.14, stale.Value.GetDoubleValue(), "stale override value")
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
		KilledOverrides: map[KillKey]struct{}{
			{FlagID: "flag-3", EntityID: "user-1"}: {},
		},
	})

	ks = cs.GetKillSet()
	require.True(t, ks.IsKilled("flag-1"), "expected flag-1 killed")
	require.True(t, ks.IsKilled("flag-2"), "expected flag-2 killed")
	require.False(t, ks.IsKilled("flag-3"), "expected flag-3 NOT globally killed")
	require.True(t, ks.IsEntityKilled("flag-3", "user-1"), "expected flag-3 killed for user-1")
	require.False(t, ks.IsEntityKilled("flag-3", "user-2"), "expected flag-3 NOT killed for user-2")
}

func TestCacheStore_KillSet_AtomicReplace(t *testing.T) {
	cs := newTestCache(t)

	cs.SetKillSet(&KillSet{
		FlagIDs:         map[string]struct{}{"flag-1": {}},
		KilledOverrides: make(map[KillKey]struct{}),
	})
	cs.SetKillSet(&KillSet{
		FlagIDs:         map[string]struct{}{"flag-2": {}},
		KilledOverrides: make(map[KillKey]struct{}),
	})

	ks := cs.GetKillSet()
	require.False(t, ks.IsKilled("flag-1"), "flag-1 should no longer be killed after replacement")
	require.True(t, ks.IsKilled("flag-2"), "flag-2 should be killed after replacement")
}

func TestKillSet_NilSafe(t *testing.T) {
	var ks *KillSet
	require.False(t, ks.IsKilled("anything"), "nil KillSet.IsKilled should return false")
	require.False(t, ks.IsEntityKilled("anything", "anyone"), "nil KillSet.IsEntityKilled should return false")
}

func TestCacheStore_JitteredTTL_NoJitter(t *testing.T) {
	cs, err := NewCacheStore(CacheStoreConfig{
		FlagTTL:         time.Second,
		OverrideTTL:     time.Second,
		OverrideMaxSize: 10,
		JitterPercent:   0,
	})
	require.NoError(t, err)
	defer cs.Close()

	ttl := cs.jitteredTTL(time.Second)
	require.Equal(t, time.Second, ttl, "jitteredTTL with 0%% jitter")
}

func TestCacheStore_JitteredTTL_WithJitter(t *testing.T) {
	cs, err := NewCacheStore(CacheStoreConfig{
		FlagTTL:         time.Second,
		OverrideTTL:     time.Second,
		OverrideMaxSize: 10,
		JitterPercent:   20,
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
