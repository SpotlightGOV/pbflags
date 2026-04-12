package celenv

import (
	"strings"
	"testing"

	example "github.com/SpotlightGOV/pbflags/gen/example"
)

func compiler(t *testing.T) *Compiler {
	t.Helper()
	md := (&example.EvaluationContext{}).ProtoReflect().Descriptor()
	c, err := NewCompiler(md)
	if err != nil {
		t.Fatalf("NewCompiler: %v", err)
	}
	return c
}

func TestCompilerAllowedExpressions(t *testing.T) {
	tests := []struct {
		name string
		expr string
	}{
		{"enum equality", `ctx.plan == PlanLevel.ENTERPRISE`},
		{"enum inequality", `ctx.plan != PlanLevel.FREE`},
		{"string equality", `ctx.user_id == "user-99"`},
		{"string presence", `ctx.user_id != ""`},
		{"bool dimension", `ctx.is_internal`},
		{"boolean and", `ctx.plan == PlanLevel.PRO && ctx.is_internal`},
		{"boolean or", `ctx.plan == PlanLevel.PRO || ctx.plan == PlanLevel.ENTERPRISE`},
		{"negation", `!(ctx.is_internal)`},
		{"enum in list", `ctx.device_type in [DeviceType.MOBILE, DeviceType.TABLET]`},
		{"int comparison lt", `ctx.plan > PlanLevel.FREE`},
		{"compound", `ctx.plan == PlanLevel.ENTERPRISE && ctx.device_type != DeviceType.DESKTOP`},
	}

	c := compiler(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			compiled, err := c.Compile(tt.expr)
			if err != nil {
				t.Fatalf("Compile(%q): %v", tt.expr, err)
			}
			if compiled.Source != tt.expr {
				t.Errorf("Source = %q, want %q", compiled.Source, tt.expr)
			}
			if compiled.AST == nil {
				t.Error("AST is nil")
			}
			if compiled.Program == nil {
				t.Error("Program is nil")
			}
		})
	}
}

func TestCompilerRejectsDisallowed(t *testing.T) {
	tests := []struct {
		name    string
		expr    string
		wantErr string
	}{
		{
			"string contains",
			`ctx.user_id.contains("admin")`,
			"not supported in v1",
		},
		{
			"string startsWith",
			`ctx.user_id.startsWith("user-")`,
			"not supported in v1",
		},
		{
			"size function",
			`size(ctx.user_id) > 0`,
			"not supported in v1",
		},
		{
			"ternary",
			`ctx.is_internal ? true : false`,
			"not supported in v1",
		},
		{
			"string concat",
			`ctx.user_id + "-suffix" == "user-1-suffix"`,
			"not supported in v1",
		},
		{
			"nonexistent field",
			`ctx.bogus == "x"`,
			"", // type error, not v1 error — just must fail
		},
		{
			"wrong output type",
			`ctx.user_id`,
			"must return bool",
		},
	}

	c := compiler(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := c.Compile(tt.expr)
			if err == nil {
				t.Fatalf("expected error for %q", tt.expr)
			}
			if tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want substring %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestCompilerEvaluate(t *testing.T) {
	c := compiler(t)
	compiled, err := c.Compile(`ctx.plan == PlanLevel.ENTERPRISE && ctx.is_internal`)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	tests := []struct {
		name   string
		ctx    *example.EvaluationContext
		expect bool
	}{
		{
			"match",
			&example.EvaluationContext{
				Plan:       example.PlanLevel_PLAN_LEVEL_ENTERPRISE,
				IsInternal: true,
			},
			true,
		},
		{
			"wrong plan",
			&example.EvaluationContext{
				Plan:       example.PlanLevel_PLAN_LEVEL_FREE,
				IsInternal: true,
			},
			false,
		},
		{
			"not internal",
			&example.EvaluationContext{
				Plan:       example.PlanLevel_PLAN_LEVEL_ENTERPRISE,
				IsInternal: false,
			},
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, _, err := compiled.Program.Eval(map[string]any{"ctx": tt.ctx})
			if err != nil {
				t.Fatalf("Eval: %v", err)
			}
			if out.Value() != tt.expect {
				t.Errorf("got %v, want %v", out.Value(), tt.expect)
			}
		})
	}
}

func TestCompilerProgramReuse(t *testing.T) {
	c := compiler(t)
	compiled, err := c.Compile(`ctx.plan == PlanLevel.PRO`)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	// Evaluate the same compiled program multiple times.
	for _, plan := range []example.PlanLevel{
		example.PlanLevel_PLAN_LEVEL_PRO,
		example.PlanLevel_PLAN_LEVEL_FREE,
		example.PlanLevel_PLAN_LEVEL_ENTERPRISE,
	} {
		ctx := &example.EvaluationContext{Plan: plan}
		out, _, err := compiled.Program.Eval(map[string]any{"ctx": ctx})
		if err != nil {
			t.Fatalf("Eval(%v): %v", plan, err)
		}
		expect := plan == example.PlanLevel_PLAN_LEVEL_PRO
		if out.Value() != expect {
			t.Errorf("plan=%v: got %v, want %v", plan, out.Value(), expect)
		}
	}
}
