package evaluator

import (
	"testing"

	"github.com/stretchr/testify/require"

	example "github.com/SpotlightGOV/pbflags/gen/example"
	"github.com/SpotlightGOV/pbflags/internal/celenv"
)

func TestBuildCacheKey_NoMeta(t *testing.T) {
	key := BuildCacheKey("notifications/1", nil, nil)
	require.Equal(t, "notifications/1", key)
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
	key := BuildCacheKey("notifications/2", meta, ctx)
	// Sorted by dim name: is_internal before plan.
	require.Equal(t, "notifications/2|is_internal=true|plan=3", key)
}

func TestBuildCacheKey_FiniteFilterUniform(t *testing.T) {
	meta := CachedDimMeta{
		"user_id": {Classification: celenv.FiniteFilterUniform, LiteralSet: []string{"user-1", "user-2"}},
	}

	// Matching user.
	ctx := &example.EvaluationContext{UserId: "user-1"}
	key := BuildCacheKey("f/1", meta, ctx)
	require.Equal(t, "f/1|user_id:match=true", key)

	// Non-matching user.
	ctx2 := &example.EvaluationContext{UserId: "user-99"}
	key2 := BuildCacheKey("f/1", meta, ctx2)
	require.Equal(t, "f/1|user_id:match=false", key2)
}

func TestBuildCacheKey_FiniteFilterDistinct(t *testing.T) {
	meta := CachedDimMeta{
		"user_id": {Classification: celenv.FiniteFilterDistinct, LiteralSet: []string{"user-1", "user-99"}},
	}

	ctx := &example.EvaluationContext{UserId: "user-99"}
	key := BuildCacheKey("f/1", meta, ctx)
	require.Equal(t, "f/1|user_id:match=user-99", key)

	ctx2 := &example.EvaluationContext{UserId: "other"}
	key2 := BuildCacheKey("f/1", meta, ctx2)
	require.Equal(t, "f/1|user_id:match=none", key2)
}

func TestBuildCacheKey_Unbounded(t *testing.T) {
	meta := CachedDimMeta{
		"user_id": {Classification: celenv.Unbounded},
	}

	ctx := &example.EvaluationContext{UserId: "user-42"}
	key := BuildCacheKey("f/1", meta, ctx)
	require.Equal(t, "f/1|user_id=user-42", key)
}

func TestBuildCacheKey_Mixed(t *testing.T) {
	meta := CachedDimMeta{
		"plan":    {Classification: celenv.Bounded},
		"user_id": {Classification: celenv.FiniteFilterUniform, LiteralSet: []string{"admin"}},
	}
	ctx := &example.EvaluationContext{
		Plan:   example.PlanLevel_PLAN_LEVEL_PRO,
		UserId: "admin",
	}
	key := BuildCacheKey("f/1", meta, ctx)
	require.Equal(t, "f/1|plan=2|user_id:match=true", key)
}

func TestConditionCache_SetAndGet(t *testing.T) {
	cc, err := NewConditionCache(100)
	require.NoError(t, err)
	defer cc.Close()

	val := boolVal(true)
	cc.Set("test-key", val)
	cc.Wait()

	got, ok := cc.Get("test-key")
	require.True(t, ok)
	require.Equal(t, true, got.GetBoolValue())
}

func TestConditionCache_Miss(t *testing.T) {
	cc, err := NewConditionCache(100)
	require.NoError(t, err)
	defer cc.Close()

	_, ok := cc.Get("missing")
	require.False(t, ok)
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
