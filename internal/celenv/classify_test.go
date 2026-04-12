package celenv

import (
	"testing"

	example "github.com/SpotlightGOV/pbflags/gen/example"
	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
	"github.com/google/cel-go/cel"
)

func compileAST(t *testing.T, c *Compiler, expr string) *cel.Ast {
	t.Helper()
	compiled, err := c.Compile(expr)
	if err != nil {
		t.Fatalf("Compile(%q): %v", expr, err)
	}
	return compiled.AST
}

func strVal(s string) *pbflagsv1.FlagValue {
	return &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringValue{StringValue: s}}
}

func intVal(n int64) *pbflagsv1.FlagValue {
	return &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_Int64Value{Int64Value: n}}
}

func TestClassifyBounded(t *testing.T) {
	c := compiler(t)
	md := (&example.EvaluationContext{}).ProtoReflect().Descriptor()
	bounded := BoundedDimsFromDescriptor(md)

	// Enum dimension is always bounded.
	ast := compileAST(t, c, `ctx.plan == PlanLevel.ENTERPRISE`)
	result := ClassifyDimensions([]*cel.Ast{ast}, []*pbflagsv1.FlagValue{strVal("daily")}, bounded)

	if m, ok := result["plan"]; !ok {
		t.Fatal("plan not found")
	} else if m.Classification != Bounded {
		t.Errorf("plan classification = %v, want bounded", m.Classification)
	}
}

func TestClassifyBoolBounded(t *testing.T) {
	c := compiler(t)
	md := (&example.EvaluationContext{}).ProtoReflect().Descriptor()
	bounded := BoundedDimsFromDescriptor(md)

	ast := compileAST(t, c, `ctx.is_internal`)
	result := ClassifyDimensions([]*cel.Ast{ast}, []*pbflagsv1.FlagValue{strVal("yes")}, bounded)

	if m, ok := result["is_internal"]; !ok {
		t.Fatal("is_internal not found")
	} else if m.Classification != Bounded {
		t.Errorf("is_internal classification = %v, want bounded", m.Classification)
	}
}

func TestClassifyFiniteFilterUniform(t *testing.T) {
	c := compiler(t)
	md := (&example.EvaluationContext{}).ProtoReflect().Descriptor()
	bounded := BoundedDimsFromDescriptor(md)

	// Two conditions matching different user_ids but producing the same value.
	ast0 := compileAST(t, c, `ctx.user_id == "user-1"`)
	ast1 := compileAST(t, c, `ctx.user_id == "user-2"`)
	sameVal := strVal("beta")

	result := ClassifyDimensions(
		[]*cel.Ast{ast0, ast1, nil},
		[]*pbflagsv1.FlagValue{sameVal, sameVal, strVal("default")},
		bounded,
	)

	m := result["user_id"]
	if m == nil {
		t.Fatal("user_id not found")
	}
	if m.Classification != FiniteFilterUniform {
		t.Errorf("classification = %v, want finite_filter_uniform", m.Classification)
	}
	if len(m.LiteralSet) != 2 {
		t.Errorf("literal set = %v, want 2 elements", m.LiteralSet)
	}
}

func TestClassifyFiniteFilterDistinct(t *testing.T) {
	c := compiler(t)
	md := (&example.EvaluationContext{}).ProtoReflect().Descriptor()
	bounded := BoundedDimsFromDescriptor(md)

	// Two conditions matching different user_ids with different values.
	ast0 := compileAST(t, c, `ctx.user_id == "user-99"`)
	ast1 := compileAST(t, c, `ctx.user_id in ["user-1", "user-2"]`)

	result := ClassifyDimensions(
		[]*cel.Ast{ast0, ast1, nil},
		[]*pbflagsv1.FlagValue{strVal("special"), strVal("beta"), strVal("default")},
		bounded,
	)

	m := result["user_id"]
	if m == nil {
		t.Fatal("user_id not found")
	}
	if m.Classification != FiniteFilterDistinct {
		t.Errorf("classification = %v, want finite_filter_distinct", m.Classification)
	}
	if len(m.LiteralSet) != 3 {
		t.Errorf("literal set = %v, want 3 elements", m.LiteralSet)
	}
}

func TestClassifyInequalityLiteral(t *testing.T) {
	c := compiler(t)
	md := (&example.EvaluationContext{}).ProtoReflect().Descriptor()
	bounded := BoundedDimsFromDescriptor(md)

	// != against a literal is a 2-bucket partition, not unbounded.
	ast := compileAST(t, c, `ctx.user_id != "admin"`)
	result := ClassifyDimensions(
		[]*cel.Ast{ast},
		[]*pbflagsv1.FlagValue{strVal("restricted")},
		bounded,
	)

	m := result["user_id"]
	if m == nil {
		t.Fatal("user_id not found")
	}
	if m.Classification == Unbounded {
		t.Errorf("classification = unbounded, want finite filter (literal comparison against admin)")
	}
	if len(m.LiteralSet) != 1 || m.LiteralSet[0] != "admin" {
		t.Errorf("literal set = %v, want [admin]", m.LiteralSet)
	}
}

func TestClassifyComparisonOperators(t *testing.T) {
	c := compiler(t)
	md := (&example.EvaluationContext{}).ProtoReflect().Descriptor()
	bounded := BoundedDimsFromDescriptor(md)

	// All comparison operators against literals should be finite filter.
	ops := []struct {
		name string
		expr string
	}{
		{"!=", `ctx.user_id != ""`},
		{"<", `ctx.session_id < "M"`},
		{"<=", `ctx.session_id <= "Z"`},
		{">", `ctx.session_id > "A"`},
		{">=", `ctx.session_id >= "B"`},
	}

	for _, op := range ops {
		t.Run(op.name, func(t *testing.T) {
			ast := compileAST(t, c, op.expr)
			result := ClassifyDimensions(
				[]*cel.Ast{ast},
				[]*pbflagsv1.FlagValue{strVal("val")},
				bounded,
			)
			for dim, m := range result {
				if m.Classification == Unbounded {
					t.Errorf("%s: dim %q classified as unbounded, want finite filter", op.expr, dim)
				}
			}
		})
	}
}

func TestClassifyUnbounded(t *testing.T) {
	c := compiler(t)
	md := (&example.EvaluationContext{}).ProtoReflect().Descriptor()
	bounded := BoundedDimsFromDescriptor(md)

	// Dim compared to another dim — no literal → unbounded.
	ast := compileAST(t, c, `ctx.user_id == ctx.session_id`)
	result := ClassifyDimensions(
		[]*cel.Ast{ast},
		[]*pbflagsv1.FlagValue{strVal("match")},
		bounded,
	)

	for _, dim := range []string{"user_id", "session_id"} {
		m := result[dim]
		if m == nil {
			t.Errorf("%s not found", dim)
			continue
		}
		if m.Classification != Unbounded {
			t.Errorf("%s classification = %v, want unbounded", dim, m.Classification)
		}
	}
}

func TestClassifyMultipleDimensions(t *testing.T) {
	c := compiler(t)
	md := (&example.EvaluationContext{}).ProtoReflect().Descriptor()
	bounded := BoundedDimsFromDescriptor(md)

	// Compound expression with enum and bool.
	ast := compileAST(t, c, `ctx.plan == PlanLevel.ENTERPRISE && ctx.is_internal`)
	result := ClassifyDimensions(
		[]*cel.Ast{ast},
		[]*pbflagsv1.FlagValue{intVal(10)},
		bounded,
	)

	if len(result) != 2 {
		t.Errorf("got %d dimensions, want 2", len(result))
	}
	if result["plan"].Classification != Bounded {
		t.Errorf("plan = %v, want bounded", result["plan"].Classification)
	}
	if result["is_internal"].Classification != Bounded {
		t.Errorf("is_internal = %v, want bounded", result["is_internal"].Classification)
	}
}

func TestClassifyNilConditionSkipped(t *testing.T) {
	md := (&example.EvaluationContext{}).ProtoReflect().Descriptor()
	bounded := BoundedDimsFromDescriptor(md)

	// Only otherwise clause → no dimensions referenced.
	result := ClassifyDimensions(
		[]*cel.Ast{nil},
		[]*pbflagsv1.FlagValue{strVal("default")},
		bounded,
	)
	if len(result) != 0 {
		t.Errorf("expected no dimensions, got %v", result)
	}
}

func TestBoundedDimsFromDescriptor(t *testing.T) {
	md := (&example.EvaluationContext{}).ProtoReflect().Descriptor()
	bounded := BoundedDimsFromDescriptor(md)

	// Enum dims: plan, device_type. Bool dims: is_internal.
	expected := map[string]bool{
		"plan":        true,
		"device_type": true,
		"is_internal": true,
	}
	for name, want := range expected {
		if bounded[name] != want {
			t.Errorf("bounded[%q] = %v, want %v", name, bounded[name], want)
		}
	}
	// String dims should not be bounded.
	if bounded["user_id"] {
		t.Error("user_id should not be bounded")
	}
	if bounded["session_id"] {
		t.Error("session_id should not be bounded")
	}
}
