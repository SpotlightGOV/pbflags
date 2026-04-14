package evaluator

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

	example "github.com/SpotlightGOV/pbflags/gen/example"
	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
)

// helpers ----------------------------------------------------------------

func testEvaluator(t *testing.T) *ConditionEvaluator {
	t.Helper()
	md := (&example.EvaluationContext{}).ProtoReflect().Descriptor()
	ce, err := NewConditionEvaluator(md, slog.Default())
	require.NoError(t, err)
	require.NotNil(t, ce)
	return ce
}

func boolFlagValueBytes(t *testing.T, v bool) []byte {
	t.Helper()
	b, err := proto.Marshal(&pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_BoolValue{BoolValue: v}})
	require.NoError(t, err)
	return b
}

func stringFlagValueBytes(t *testing.T, v string) []byte {
	t.Helper()
	b, err := proto.Marshal(&pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringValue{StringValue: v}})
	require.NoError(t, err)
	return b
}

func mustMarshalConditions(t *testing.T, conds []*pbflagsv1.CompiledCondition) []byte {
	t.Helper()
	b, err := proto.Marshal(&pbflagsv1.StoredConditions{Conditions: conds})
	require.NoError(t, err)
	return b
}

// -----------------------------------------------------------------------
// NewConditionEvaluator
// -----------------------------------------------------------------------

func TestConditionEvaluator_NewNilDescriptor(t *testing.T) {
	t.Parallel()
	ce, err := NewConditionEvaluator(nil, slog.Default())
	require.NoError(t, err)
	require.Nil(t, ce, "nil descriptor should return nil evaluator")
}

func TestConditionEvaluator_NewValidDescriptor(t *testing.T) {
	t.Parallel()
	md := (&example.EvaluationContext{}).ProtoReflect().Descriptor()
	ce, err := NewConditionEvaluator(md, slog.Default())
	require.NoError(t, err)
	require.NotNil(t, ce)
}

// -----------------------------------------------------------------------
// CompileConditions
// -----------------------------------------------------------------------

func TestConditionCompile(t *testing.T) {
	t.Parallel()
	ce := testEvaluator(t)

	tests := []struct {
		name      string
		input     []byte
		wantNil   bool
		wantLen   int
		checkFunc func(t *testing.T, conds []CachedCondition)
	}{
		{
			name:    "nil JSON returns nil",
			input:   nil,
			wantNil: true,
		},
		{
			name:    "empty JSON returns nil",
			input:   []byte{},
			wantNil: true,
		},
		{
			name: "valid condition with CEL expression",
			input: mustMarshalConditions(t, []*pbflagsv1.CompiledCondition{
				{
					Cel:   `ctx.plan == PlanLevel.ENTERPRISE`,
					Value: boolFlagValueBytes(t, true),
				},
			}),
			wantLen: 1,
			checkFunc: func(t *testing.T, conds []CachedCondition) {
				require.NotNil(t, conds[0].Program, "CEL program should be compiled")
				require.Equal(t, `ctx.plan == PlanLevel.ENTERPRISE`, conds[0].Source)
				require.NotNil(t, conds[0].Value)
			},
		},
		{
			name: "otherwise clause with empty CEL",
			input: mustMarshalConditions(t, []*pbflagsv1.CompiledCondition{
				{
					Cel:   "",
					Value: boolFlagValueBytes(t, false),
				},
			}),
			wantLen: 1,
			checkFunc: func(t *testing.T, conds []CachedCondition) {
				require.Nil(t, conds[0].Program, "otherwise should have nil program")
				require.Empty(t, conds[0].Source, "otherwise should have empty source")
				require.NotNil(t, conds[0].Value)
			},
		},
		{
			name:    "malformed proto returns nil",
			input:   []byte(`{not valid`),
			wantNil: true,
		},
		{
			name: "invalid proto value returns nil",
			input: mustMarshalConditions(t, []*pbflagsv1.CompiledCondition{
				{
					Cel:   `ctx.is_internal`,
					Value: []byte("bogus"),
				},
			}),
			wantNil: true,
		},
		{
			name: "invalid CEL expression returns nil",
			input: mustMarshalConditions(t, []*pbflagsv1.CompiledCondition{
				{
					Cel:   `ctx.nonexistent_field == "x"`,
					Value: boolFlagValueBytes(t, true),
				},
			}),
			wantNil: true,
		},
		{
			name: "condition with valid launch override",
			input: mustMarshalConditions(t, []*pbflagsv1.CompiledCondition{
				{
					Cel:         `ctx.is_internal`,
					Value:       boolFlagValueBytes(t, false),
					LaunchId:    "launch-1",
					LaunchValue: boolFlagValueBytes(t, true),
				},
			}),
			wantLen: 1,
			checkFunc: func(t *testing.T, conds []CachedCondition) {
				require.Equal(t, "launch-1", conds[0].LaunchID)
				require.NotNil(t, conds[0].LaunchValue)
			},
		},
		{
			name: "condition with invalid launch value degrades gracefully",
			input: mustMarshalConditions(t, []*pbflagsv1.CompiledCondition{
				{
					Cel:         `ctx.is_internal`,
					Value:       boolFlagValueBytes(t, false),
					LaunchId:    "launch-1",
					LaunchValue: []byte("bogus"),
				},
			}),
			wantLen: 1,
			checkFunc: func(t *testing.T, conds []CachedCondition) {
				require.Empty(t, conds[0].LaunchID, "invalid launch value should be ignored")
				require.Nil(t, conds[0].LaunchValue, "invalid launch value should be nil")
				require.NotNil(t, conds[0].Value, "base value should still be present")
			},
		},
		{
			name: "multiple conditions compile in order",
			input: mustMarshalConditions(t, []*pbflagsv1.CompiledCondition{
				{
					Cel:   `ctx.plan == PlanLevel.ENTERPRISE`,
					Value: stringFlagValueBytes(t, "enterprise"),
				},
				{
					Cel:   `ctx.plan == PlanLevel.PRO`,
					Value: stringFlagValueBytes(t, "pro"),
				},
				{
					Cel:   "",
					Value: stringFlagValueBytes(t, "default"),
				},
			}),
			wantLen: 3,
			checkFunc: func(t *testing.T, conds []CachedCondition) {
				require.NotNil(t, conds[0].Program)
				require.NotNil(t, conds[1].Program)
				require.Nil(t, conds[2].Program, "otherwise has nil program")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			conds := ce.CompileConditions("test-flag", tt.input)
			if tt.wantNil {
				require.Nil(t, conds)
				return
			}
			require.Len(t, conds, tt.wantLen)
			if tt.checkFunc != nil {
				tt.checkFunc(t, conds)
			}
		})
	}
}

// -----------------------------------------------------------------------
// EvaluateConditions
// -----------------------------------------------------------------------

func TestConditionEvaluate(t *testing.T) {
	t.Parallel()
	ce := testEvaluator(t)

	t.Run("empty conditions returns empty result", func(t *testing.T) {
		t.Parallel()
		ctx := &example.EvaluationContext{UserId: "user-1"}
		res := ce.EvaluateConditions("flag-1", nil, ctx)
		require.NotNil(t, res)
		require.Nil(t, res.Value)
		require.Equal(t, 0, res.ConditionsChecked)
	})

	t.Run("nil evalCtx returns empty result", func(t *testing.T) {
		t.Parallel()
		conds := ce.CompileConditions("flag-1", mustMarshalConditions(t, []*pbflagsv1.CompiledCondition{
			{Cel: "", Value: boolFlagValueBytes(t, true)},
		}))
		require.NotNil(t, conds)
		res := ce.EvaluateConditions("flag-1", conds, nil)
		require.NotNil(t, res)
		require.Nil(t, res.Value)
	})

	t.Run("first matching condition returns its value", func(t *testing.T) {
		t.Parallel()
		conds := ce.CompileConditions("flag-1", mustMarshalConditions(t, []*pbflagsv1.CompiledCondition{
			{
				Cel:   `ctx.plan == PlanLevel.ENTERPRISE`,
				Value: stringFlagValueBytes(t, "enterprise-val"),
			},
			{
				Cel:   `ctx.plan == PlanLevel.PRO`,
				Value: stringFlagValueBytes(t, "pro-val"),
			},
		}))
		require.NotNil(t, conds)

		ctx := &example.EvaluationContext{Plan: example.PlanLevel_PLAN_LEVEL_ENTERPRISE}
		res := ce.EvaluateConditions("flag-1", conds, ctx)
		require.NotNil(t, res.Value)
		require.Equal(t, "enterprise-val", res.Value.GetStringValue())
		require.Equal(t, 1, res.ConditionsChecked)
	})

	t.Run("non-matching conditions skip to next", func(t *testing.T) {
		t.Parallel()
		conds := ce.CompileConditions("flag-1", mustMarshalConditions(t, []*pbflagsv1.CompiledCondition{
			{
				Cel:   `ctx.plan == PlanLevel.ENTERPRISE`,
				Value: stringFlagValueBytes(t, "enterprise-val"),
			},
			{
				Cel:   `ctx.plan == PlanLevel.PRO`,
				Value: stringFlagValueBytes(t, "pro-val"),
			},
		}))
		require.NotNil(t, conds)

		ctx := &example.EvaluationContext{Plan: example.PlanLevel_PLAN_LEVEL_PRO}
		res := ce.EvaluateConditions("flag-1", conds, ctx)
		require.NotNil(t, res.Value)
		require.Equal(t, "pro-val", res.Value.GetStringValue())
		require.Equal(t, 2, res.ConditionsChecked, "should have checked both conditions")
	})

	t.Run("no match returns empty result", func(t *testing.T) {
		t.Parallel()
		conds := ce.CompileConditions("flag-1", mustMarshalConditions(t, []*pbflagsv1.CompiledCondition{
			{
				Cel:   `ctx.plan == PlanLevel.ENTERPRISE`,
				Value: stringFlagValueBytes(t, "enterprise-val"),
			},
		}))
		require.NotNil(t, conds)

		ctx := &example.EvaluationContext{Plan: example.PlanLevel_PLAN_LEVEL_FREE}
		res := ce.EvaluateConditions("flag-1", conds, ctx)
		require.Nil(t, res.Value, "no match should return nil value")
		require.Equal(t, 1, res.ConditionsChecked)
	})

	t.Run("otherwise clause always matches", func(t *testing.T) {
		t.Parallel()
		conds := ce.CompileConditions("flag-1", mustMarshalConditions(t, []*pbflagsv1.CompiledCondition{
			{
				Cel:   `ctx.plan == PlanLevel.ENTERPRISE`,
				Value: stringFlagValueBytes(t, "enterprise-val"),
			},
			{
				Cel:   "",
				Value: stringFlagValueBytes(t, "otherwise-val"),
			},
		}))
		require.NotNil(t, conds)

		ctx := &example.EvaluationContext{Plan: example.PlanLevel_PLAN_LEVEL_FREE}
		res := ce.EvaluateConditions("flag-1", conds, ctx)
		require.NotNil(t, res.Value)
		require.Equal(t, "otherwise-val", res.Value.GetStringValue())
		// "otherwise" does not increment ConditionsChecked because it has no program.
		require.Equal(t, 1, res.ConditionsChecked, "only the CEL condition should be counted")
	})

	t.Run("launch override applied when entity in ramp", func(t *testing.T) {
		t.Parallel()
		conds := ce.CompileConditions("flag-1", mustMarshalConditions(t, []*pbflagsv1.CompiledCondition{
			{
				Cel:         `ctx.is_internal`,
				Value:       stringFlagValueBytes(t, "base-val"),
				LaunchId:    "launch-1",
				LaunchValue: stringFlagValueBytes(t, "launch-val"),
			},
		}))
		require.NotNil(t, conds)

		ctx := &example.EvaluationContext{IsInternal: true, UserId: "user-1"}
		launch := CachedLaunch{LaunchID: "launch-1", Dimension: "user_id", RampPct: 100}
		res := ce.EvaluateConditions("flag-1", conds, ctx, launch)
		require.NotNil(t, res.Value)
		require.Equal(t, "launch-val", res.Value.GetStringValue())
		require.True(t, res.LaunchHit)
		require.Equal(t, "launch-1", res.LaunchID)
	})

	t.Run("launch override not applied when entity not in ramp", func(t *testing.T) {
		t.Parallel()
		conds := ce.CompileConditions("flag-1", mustMarshalConditions(t, []*pbflagsv1.CompiledCondition{
			{
				Cel:         `ctx.is_internal`,
				Value:       stringFlagValueBytes(t, "base-val"),
				LaunchId:    "launch-1",
				LaunchValue: stringFlagValueBytes(t, "launch-val"),
			},
		}))
		require.NotNil(t, conds)

		ctx := &example.EvaluationContext{IsInternal: true, UserId: "user-1"}
		launch := CachedLaunch{LaunchID: "launch-1", Dimension: "user_id", RampPct: 0}
		res := ce.EvaluateConditions("flag-1", conds, ctx, launch)
		require.NotNil(t, res.Value)
		require.Equal(t, "base-val", res.Value.GetStringValue())
		require.False(t, res.LaunchHit)
		require.Empty(t, res.LaunchID)
	})

	t.Run("launch override not applied when launch not in map", func(t *testing.T) {
		t.Parallel()
		conds := ce.CompileConditions("flag-1", mustMarshalConditions(t, []*pbflagsv1.CompiledCondition{
			{
				Cel:         `ctx.is_internal`,
				Value:       stringFlagValueBytes(t, "base-val"),
				LaunchId:    "launch-1",
				LaunchValue: stringFlagValueBytes(t, "launch-val"),
			},
		}))
		require.NotNil(t, conds)

		ctx := &example.EvaluationContext{IsInternal: true, UserId: "user-1"}
		// No launches passed at all.
		res := ce.EvaluateConditions("flag-1", conds, ctx)
		require.NotNil(t, res.Value)
		require.Equal(t, "base-val", res.Value.GetStringValue())
		require.False(t, res.LaunchHit)
	})

	t.Run("launch override on otherwise clause", func(t *testing.T) {
		t.Parallel()
		conds := ce.CompileConditions("flag-1", mustMarshalConditions(t, []*pbflagsv1.CompiledCondition{
			{
				Cel:         "", // otherwise
				Value:       stringFlagValueBytes(t, "base-val"),
				LaunchId:    "launch-2",
				LaunchValue: stringFlagValueBytes(t, "launch-val"),
			},
		}))
		require.NotNil(t, conds)

		ctx := &example.EvaluationContext{UserId: "user-1"}
		launch := CachedLaunch{LaunchID: "launch-2", Dimension: "user_id", RampPct: 100}
		res := ce.EvaluateConditions("flag-1", conds, ctx, launch)
		require.NotNil(t, res.Value)
		require.Equal(t, "launch-val", res.Value.GetStringValue())
		require.True(t, res.LaunchHit)
		require.Equal(t, "launch-2", res.LaunchID)
	})

	t.Run("CEL evaluation error skips condition", func(t *testing.T) {
		t.Parallel()
		// Compile a valid CEL expression against the EvaluationContext schema.
		prog := ce.CompileExpression(`ctx.user_id == "target"`)
		require.NotNil(t, prog)

		fvError := &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringValue{StringValue: "error-val"}}
		fvFallback := &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringValue{StringValue: "fallback"}}

		// Build conditions manually: first has a compiled program, second is
		// an "otherwise" clause. We pass a different proto.Message type as
		// evalCtx so that CEL evaluation errors on field access, causing the
		// evaluator to skip to the next condition.
		conds := []CachedCondition{
			{Program: prog, Value: fvError, Source: `ctx.user_id == "target"`},
			{Program: nil, Value: fvFallback}, // otherwise
		}

		// Use a FlagValue as the evalCtx -- it lacks user_id, so CEL
		// evaluation will return an error for the first condition.
		badCtx := &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_BoolValue{BoolValue: true}}
		res := ce.EvaluateConditions("flag-1", conds, badCtx)
		require.NotNil(t, res.Value)
		require.Equal(t, "fallback", res.Value.GetStringValue())
		// The error condition was checked (counted) but skipped.
		require.Equal(t, 1, res.ConditionsChecked)
	})
}

// -----------------------------------------------------------------------
// UnmarshalContext
// -----------------------------------------------------------------------

func TestConditionUnmarshalContext(t *testing.T) {
	t.Parallel()
	ce := testEvaluator(t)

	t.Run("nil Any returns nil", func(t *testing.T) {
		t.Parallel()
		msg, err := ce.UnmarshalContext(nil)
		require.NoError(t, err)
		require.Nil(t, msg)
	})

	t.Run("empty Any returns nil", func(t *testing.T) {
		t.Parallel()
		msg, err := ce.UnmarshalContext(&anypb.Any{})
		require.NoError(t, err)
		require.Nil(t, msg)
	})

	t.Run("valid Any deserializes", func(t *testing.T) {
		t.Parallel()

		original := &example.EvaluationContext{
			UserId:     "user-42",
			Plan:       example.PlanLevel_PLAN_LEVEL_ENTERPRISE,
			IsInternal: true,
		}
		raw, err := proto.Marshal(original)
		require.NoError(t, err)

		anyCtx := &anypb.Any{
			TypeUrl: "type.googleapis.com/example.EvaluationContext",
			Value:   raw,
		}

		msg, err := ce.UnmarshalContext(anyCtx)
		require.NoError(t, err)
		require.NotNil(t, msg)

		// Verify round-trip by checking field values via proto reflection.
		rm := msg.ProtoReflect()
		userFd := rm.Descriptor().Fields().ByName("user_id")
		require.NotNil(t, userFd)
		require.Equal(t, "user-42", rm.Get(userFd).String())

		planFd := rm.Descriptor().Fields().ByName("plan")
		require.NotNil(t, planFd)
		require.EqualValues(t, example.PlanLevel_PLAN_LEVEL_ENTERPRISE, rm.Get(planFd).Enum())
	})

	t.Run("invalid bytes returns error", func(t *testing.T) {
		t.Parallel()
		anyCtx := &anypb.Any{
			TypeUrl: "type.googleapis.com/example.EvaluationContext",
			Value:   []byte{0xff, 0xff, 0xff, 0xff},
		}
		_, err := ce.UnmarshalContext(anyCtx)
		require.Error(t, err)
	})
}
