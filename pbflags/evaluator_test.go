package pbflags_test

import (
	"context"
	"testing"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
	"github.com/SpotlightGOV/pbflags/pbflags"
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

// mockEvaluator is a minimal Evaluator for testing context round-trip.
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
