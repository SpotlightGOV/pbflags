package evaluator

import (
	"testing"

	"github.com/stretchr/testify/require"

	example "github.com/SpotlightGOV/pbflags/gen/example"
	"github.com/SpotlightGOV/pbflags/internal/celenv"
)

func TestBuildCacheKey_NoMeta(t *testing.T) {
	key := BuildCacheKey("notifications/1", 0, nil, nil)
	require.Equal(t, "notifications/1@0", key)
}

func TestBuildCacheKey_Bounded(t *testing.T) {
	meta := CachedDimMeta{
		"plan":        {Classification: celenv.Bounded},
		"is_internal": {Classification: celenv.Bounded},
	}
	ctx := &example.EvaluationContext{
		Plan:       example.PlanLevel_PLAN_LEVEL_ENTERPRISE,
		IsInternal: true,
	}
	key := BuildCacheKey("notifications/2", 0, meta, ctx)
	require.Contains(t, key, "notifications/2@0")
	require.Contains(t, key, "is_internal")
	require.Contains(t, key, "plan")
}

func TestBuildCacheKey_FiniteFilterUniform(t *testing.T) {
	meta := CachedDimMeta{
		"user_id": {Classification: celenv.FiniteFilterUniform, LiteralSet: []string{"user-1", "user-2"}},
	}

	ctx := &example.EvaluationContext{UserId: "user-1"}
	key := BuildCacheKey("f/1", 0, meta, ctx)
	require.Contains(t, key, "1") // match=true → "1"

	ctx2 := &example.EvaluationContext{UserId: "user-99"}
	key2 := BuildCacheKey("f/1", 0, meta, ctx2)
	require.Contains(t, key2, "0") // match=false → "0"

	require.NotEqual(t, key, key2)
}

func TestBuildCacheKey_Unbounded(t *testing.T) {
	meta := CachedDimMeta{
		"user_id": {Classification: celenv.Unbounded},
	}

	ctx := &example.EvaluationContext{UserId: "user-42"}
	key := BuildCacheKey("f/1", 0, meta, ctx)
	require.Contains(t, key, "user-42")
}

func TestBuildCacheKey_VersionInvalidation(t *testing.T) {
	meta := CachedDimMeta{
		"plan": {Classification: celenv.Bounded},
	}
	ctx := &example.EvaluationContext{Plan: example.PlanLevel_PLAN_LEVEL_PRO}

	key0 := BuildCacheKey("f/1", 0, meta, ctx)
	key1 := BuildCacheKey("f/1", 1, meta, ctx)
	require.NotEqual(t, key0, key1, "different versions must produce different keys")
}

func TestBuildCacheKey_DelimiterCollision(t *testing.T) {
	meta := CachedDimMeta{
		"user_id": {Classification: celenv.Unbounded},
	}

	// Values with special characters should not collide.
	ctxA := &example.EvaluationContext{UserId: "alice|plan=3"}
	ctxB := &example.EvaluationContext{UserId: "alice"}
	keyA := BuildCacheKey("f/1", 0, meta, ctxA)
	keyB := BuildCacheKey("f/1", 0, meta, ctxB)
	require.NotEqual(t, keyA, keyB, "length-prefixed encoding should prevent collision")
}

func TestConditionCache_SetAndGet(t *testing.T) {
	cc, err := NewConditionCache(100)
	require.NoError(t, err)
	defer cc.Close()

	val := boolVal(true)
	cc.Set("test-key", val)
	cc.Wait()

	got, noMatch, ok := cc.Get("test-key")
	require.True(t, ok)
	require.False(t, noMatch)
	require.Equal(t, true, got.GetBoolValue())
}

func TestConditionCache_NoMatch(t *testing.T) {
	cc, err := NewConditionCache(100)
	require.NoError(t, err)
	defer cc.Close()

	cc.SetNoMatch("no-match-key")
	cc.Wait()

	got, noMatch, ok := cc.Get("no-match-key")
	require.True(t, ok)
	require.True(t, noMatch)
	require.Nil(t, got)
}

func TestConditionCache_Miss(t *testing.T) {
	cc, err := NewConditionCache(100)
	require.NoError(t, err)
	defer cc.Close()

	_, _, ok := cc.Get("missing")
	require.False(t, ok)
}

func TestConditionCache_InvalidateFlag(t *testing.T) {
	cc, err := NewConditionCache(100)
	require.NoError(t, err)
	defer cc.Close()

	require.Equal(t, uint64(0), cc.FlagVersion("f/1"))
	cc.InvalidateFlag("f/1")
	require.Equal(t, uint64(1), cc.FlagVersion("f/1"))
	cc.InvalidateFlag("f/1")
	require.Equal(t, uint64(2), cc.FlagVersion("f/1"))
}

func TestParseDimMeta(t *testing.T) {
	data := []byte(`{"plan":{"classification":"bounded"},"user_id":{"classification":"finite_filter_uniform","literal_set":["u1","u2"]}}`)
	meta := ParseDimMeta(data)
	require.Len(t, meta, 2)
	require.Equal(t, celenv.Bounded, meta["plan"].Classification)
	require.Equal(t, celenv.FiniteFilterUniform, meta["user_id"].Classification)
	require.Equal(t, []string{"u1", "u2"}, meta["user_id"].LiteralSet)
}

func TestParseDimMeta_Nil(t *testing.T) {
	require.Nil(t, ParseDimMeta(nil))
	require.Nil(t, ParseDimMeta([]byte{}))
}
