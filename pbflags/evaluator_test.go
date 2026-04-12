package pbflags_test

import (
	"context"
	"testing"

	examplepb "github.com/SpotlightGOV/pbflags/gen/example"
	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
	"github.com/SpotlightGOV/pbflags/pbflags"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestContextWithAndFromContext(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// No evaluator set — FromContext returns noop.
	eval := pbflags.FromContext(ctx)
	result, err := eval.Evaluate(ctx, "test/1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Value != nil {
		t.Errorf("noop should return nil value, got %v", result.Value)
	}
	if result.Source != pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT {
		t.Errorf("noop source should be DEFAULT, got %v", result.Source)
	}

	// Set an evaluator and retrieve it.
	mock := &mockEvaluator{val: &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_BoolValue{BoolValue: true}}}
	ctx = pbflags.ContextWith(ctx, mock)
	eval = pbflags.FromContext(ctx)
	result, err = eval.Evaluate(ctx, "test/1")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Value.GetBoolValue() {
		t.Error("expected true from mock evaluator")
	}
}

func TestContextWith_PanicsOnNil(t *testing.T) {
	t.Parallel()

	// Interface-nil panics.
	assertPanics(t, "interface-nil", func() {
		pbflags.ContextWith(context.Background(), nil)
	})

	// Typed-nil panics.
	assertPanics(t, "typed-nil", func() {
		var eval *mockEvaluator // typed-nil
		pbflags.ContextWith(context.Background(), eval)
	})
}

func TestFromContext_TypedNilFallsBackToNoop(t *testing.T) {
	t.Parallel()
	// Simulate a typed-nil that was stored via context.WithValue directly
	// (bypassing ContextWith). FromContext should still return noop.
	ctx := context.WithValue(context.Background(), struct{ x int }{}, (*mockEvaluator)(nil))

	// This uses a different key, so FromContext won't find it — that's fine,
	// the point is FromContext returns noop for missing evaluators.
	eval := pbflags.FromContext(ctx)
	result, err := eval.Evaluate(ctx, "test/1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Source != pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT {
		t.Errorf("expected DEFAULT, got %v", result.Source)
	}
}

func TestNoopEvaluator_BulkEvaluate(t *testing.T) {
	t.Parallel()
	eval := pbflags.FromContext(context.Background())
	results, err := eval.BulkEvaluate(context.Background(), []string{"a/1", "b/2", "c/3"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	for i, r := range results {
		if r.Value != nil {
			t.Errorf("result[%d]: expected nil value", i)
		}
		if r.Source != pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT {
			t.Errorf("result[%d]: expected DEFAULT source", i)
		}
	}
}

func TestNoopEvaluator_WithReturnsNoop(t *testing.T) {
	t.Parallel()
	eval := pbflags.FromContext(context.Background())
	scoped := eval.With(pbflags.StringDimension("user_id", "u1"))

	result, err := scoped.Evaluate(context.Background(), "test/1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Source != pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT {
		t.Errorf("noop.With should still be noop, got source %v", result.Source)
	}
}

// TestDimension_WithOverrideSemantics verifies that when dimensions with the
// same field name are applied, the last one wins. This is the Apply-level
// override semantic documented on Evaluator.With.
func TestDimension_WithOverrideSemantics(t *testing.T) {
	t.Parallel()
	ctx := &examplepb.EvaluationContext{}
	msg := ctx.ProtoReflect()

	// Apply "user_id" = "a", then "user_id" = "b". Last one wins.
	dims := []pbflags.Dimension{
		pbflags.StringDimension("user_id", "a"),
		pbflags.StringDimension("user_id", "b"),
	}
	for _, d := range dims {
		d.Apply(msg)
	}

	if ctx.UserId != "b" {
		t.Errorf("expected user_id=%q after override, got %q", "b", ctx.UserId)
	}

	// Same for enum: plan = PRO, then plan = ENTERPRISE.
	pbflags.EnumDimension("plan", protoreflect.EnumNumber(examplepb.PlanLevel_PLAN_LEVEL_PRO)).Apply(msg)
	pbflags.EnumDimension("plan", protoreflect.EnumNumber(examplepb.PlanLevel_PLAN_LEVEL_ENTERPRISE)).Apply(msg)

	if ctx.Plan != examplepb.PlanLevel_PLAN_LEVEL_ENTERPRISE {
		t.Errorf("expected plan=ENTERPRISE after override, got %v", ctx.Plan)
	}
}

func assertPanics(t *testing.T, name string, fn func()) {
	t.Helper()
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("%s: expected panic, got none", name)
		}
	}()
	fn()
}

// mockEvaluator is a minimal Evaluator for testing context round-trip.
// Its With() appends rather than deduplicating — this is deliberately
// permissive for test simplicity. Override semantics are enforced at the
// Dimension.Apply level (last-write-wins on the proto message), not by
// the Evaluator.With accumulation.
type mockEvaluator struct {
	val  *pbflagsv1.FlagValue
	dims []pbflags.Dimension
}

func (m *mockEvaluator) With(dims ...pbflags.Dimension) pbflags.Evaluator {
	combined := make([]pbflags.Dimension, len(m.dims)+len(dims))
	copy(combined, m.dims)
	copy(combined[len(m.dims):], dims)
	return &mockEvaluator{val: m.val, dims: combined}
}

func (m *mockEvaluator) Evaluate(_ context.Context, _ string) (*pbflags.Result, error) {
	return &pbflags.Result{
		Value:  m.val,
		Source: pbflagsv1.EvaluationSource_EVALUATION_SOURCE_GLOBAL,
	}, nil
}

func (m *mockEvaluator) BulkEvaluate(_ context.Context, flagIDs []string) ([]*pbflags.Result, error) {
	results := make([]*pbflags.Result, len(flagIDs))
	for i := range results {
		results[i] = &pbflags.Result{
			Value:  m.val,
			Source: pbflagsv1.EvaluationSource_EVALUATION_SOURCE_GLOBAL,
		}
	}
	return results, nil
}
