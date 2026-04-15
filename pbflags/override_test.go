package pbflags_test

import (
	"context"
	"testing"

	"github.com/SpotlightGOV/pbflags/gen/pbflags/flagmeta"
	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
	"github.com/SpotlightGOV/pbflags/pbflags"
)

var testDescriptors = []flagmeta.FlagDescriptor{
	{ID: "feature/1", Type: flagmeta.FlagTypeBool},
	{ID: "feature/2", Type: flagmeta.FlagTypeString},
	{ID: "feature/3", Type: flagmeta.FlagTypeInt64},
	{ID: "feature/4", Type: flagmeta.FlagTypeDouble},
	{ID: "feature/5", Type: flagmeta.FlagTypeString, IsList: true},
}

func TestWithOverrides_Evaluate(t *testing.T) {
	t.Parallel()
	inner := &mockEvaluator{val: &pbflagsv1.FlagValue{
		Value: &pbflagsv1.FlagValue_StringValue{StringValue: "from-server"},
	}}
	eval := pbflags.WithOverrides(inner, nil,
		pbflags.BoolOverride("feature/1", true),
	)

	// Overridden flag returns the override.
	r, err := eval.Evaluate(context.Background(), "feature/1")
	if err != nil {
		t.Fatal(err)
	}
	if !r.Value.GetBoolValue() {
		t.Error("expected overridden bool true")
	}
	if r.Source != pbflagsv1.EvaluationSource_EVALUATION_SOURCE_OVERRIDE {
		t.Errorf("expected OVERRIDE source, got %v", r.Source)
	}

	// Non-overridden flag delegates to inner.
	r, err = eval.Evaluate(context.Background(), "feature/2")
	if err != nil {
		t.Fatal(err)
	}
	if r.Value.GetStringValue() != "from-server" {
		t.Errorf("expected delegated value, got %v", r.Value)
	}
	if r.Source != pbflagsv1.EvaluationSource_EVALUATION_SOURCE_GLOBAL {
		t.Errorf("expected GLOBAL source from inner, got %v", r.Source)
	}
}

func TestWithOverrides_BulkEvaluate(t *testing.T) {
	t.Parallel()
	inner := &mockEvaluator{val: &pbflagsv1.FlagValue{
		Value: &pbflagsv1.FlagValue_Int64Value{Int64Value: 42},
	}}
	eval := pbflags.WithOverrides(inner, nil,
		pbflags.BoolOverride("a/1", true),
		pbflags.StringOverride("c/3", "override-c"),
	)

	results, err := eval.BulkEvaluate(context.Background(), []string{"a/1", "b/2", "c/3"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	// a/1 — overridden
	if !results[0].Value.GetBoolValue() {
		t.Error("a/1: expected overridden bool true")
	}
	if results[0].Source != pbflagsv1.EvaluationSource_EVALUATION_SOURCE_OVERRIDE {
		t.Errorf("a/1: expected OVERRIDE, got %v", results[0].Source)
	}

	// b/2 — delegated
	if results[1].Value.GetInt64Value() != 42 {
		t.Errorf("b/2: expected delegated int64 42, got %v", results[1].Value)
	}

	// c/3 — overridden
	if results[2].Value.GetStringValue() != "override-c" {
		t.Errorf("c/3: expected overridden string, got %v", results[2].Value)
	}
}

func TestWithOverrides_BulkEvaluate_AllOverridden(t *testing.T) {
	t.Parallel()
	inner := &mockEvaluator{val: &pbflagsv1.FlagValue{
		Value: &pbflagsv1.FlagValue_BoolValue{BoolValue: false},
	}}
	eval := pbflags.WithOverrides(inner, nil,
		pbflags.BoolOverride("x/1", true),
		pbflags.BoolOverride("x/2", true),
	)

	results, err := eval.BulkEvaluate(context.Background(), []string{"x/1", "x/2"})
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range results {
		if r.Source != pbflagsv1.EvaluationSource_EVALUATION_SOURCE_OVERRIDE {
			t.Errorf("result[%d]: expected OVERRIDE, got %v", i, r.Source)
		}
	}
}

func TestWithOverrides_WithPreservesOverrides(t *testing.T) {
	t.Parallel()
	inner := &mockEvaluator{val: &pbflagsv1.FlagValue{
		Value: &pbflagsv1.FlagValue_BoolValue{BoolValue: false},
	}}
	eval := pbflags.WithOverrides(inner, nil,
		pbflags.BoolOverride("feature/1", true),
	)

	scoped := eval.With(pbflags.StringDimension("user_id", "u1"))
	r, err := scoped.Evaluate(context.Background(), "feature/1")
	if err != nil {
		t.Fatal(err)
	}
	if !r.Value.GetBoolValue() {
		t.Error("override should survive With()")
	}
	if r.Source != pbflagsv1.EvaluationSource_EVALUATION_SOURCE_OVERRIDE {
		t.Errorf("expected OVERRIDE source after With(), got %v", r.Source)
	}
}

func TestWithOverrides_EmptyReturnsInner(t *testing.T) {
	t.Parallel()
	inner := &mockEvaluator{val: &pbflagsv1.FlagValue{
		Value: &pbflagsv1.FlagValue_BoolValue{BoolValue: true},
	}}
	eval := pbflags.WithOverrides(inner, nil)
	if _, ok := eval.(*mockEvaluator); !ok {
		t.Error("expected inner evaluator returned for no overrides")
	}
}

func TestWithOverrides_PanicsOnNil(t *testing.T) {
	t.Parallel()
	assertPanics(t, "nil-eval", func() {
		pbflags.WithOverrides(nil, nil,
			pbflags.BoolOverride("x/1", true),
		)
	})
}

// --- Validation tests ---

func TestValidateOverrides_TypeMatch(t *testing.T) {
	t.Parallel()
	overrides := []pbflags.Override{
		pbflags.BoolOverride("feature/1", true),
		pbflags.StringOverride("feature/2", "hello"),
		pbflags.Int64Override("feature/3", 42),
		pbflags.DoubleOverride("feature/4", 3.14),
		pbflags.StringListOverride("feature/5", "a", "b"),
	}
	if err := pbflags.ValidateOverrides(overrides, testDescriptors); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestValidateOverrides_TypeMismatch(t *testing.T) {
	t.Parallel()
	overrides := []pbflags.Override{
		pbflags.StringOverride("feature/1", "wrong"), // bool flag, string value
	}
	err := pbflags.ValidateOverrides(overrides, testDescriptors)
	if err == nil {
		t.Fatal("expected error for type mismatch")
	}
	if got := err.Error(); !contains(got, "feature/1") || !contains(got, "bool") || !contains(got, "string") {
		t.Errorf("error should mention flag ID and types, got: %s", got)
	}
}

func TestValidateOverrides_UnknownFlag(t *testing.T) {
	t.Parallel()
	overrides := []pbflags.Override{
		pbflags.BoolOverride("unknown/99", true),
	}
	err := pbflags.ValidateOverrides(overrides, testDescriptors)
	if err == nil {
		t.Fatal("expected error for unknown flag")
	}
	if got := err.Error(); !contains(got, "unknown/99") {
		t.Errorf("error should mention the unknown flag ID, got: %s", got)
	}
}

func TestValidateOverrides_ListScalarMismatch(t *testing.T) {
	t.Parallel()
	overrides := []pbflags.Override{
		pbflags.StringOverride("feature/5", "scalar"), // expects []string
	}
	err := pbflags.ValidateOverrides(overrides, testDescriptors)
	if err == nil {
		t.Fatal("expected error for list/scalar mismatch")
	}
}

func TestWithOverrides_PanicsOnValidationFailure(t *testing.T) {
	t.Parallel()
	inner := &mockEvaluator{}
	assertPanics(t, "type-mismatch", func() {
		pbflags.WithOverrides(inner, testDescriptors,
			pbflags.StringOverride("feature/1", "wrong"), // bool flag
		)
	})
}

func TestWithOverrides_NilDescriptorsSkipsValidation(t *testing.T) {
	t.Parallel()
	inner := &mockEvaluator{val: &pbflagsv1.FlagValue{
		Value: &pbflagsv1.FlagValue_BoolValue{BoolValue: false},
	}}
	// Would fail validation (string override for bool descriptor) but
	// descriptors is nil, so no panic.
	eval := pbflags.WithOverrides(inner, nil,
		pbflags.StringOverride("feature/1", "no-validation"),
	)
	r, err := eval.Evaluate(context.Background(), "feature/1")
	if err != nil {
		t.Fatal(err)
	}
	if r.Value.GetStringValue() != "no-validation" {
		t.Error("expected override to be returned without validation")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
