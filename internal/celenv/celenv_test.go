package celenv

import (
	"testing"

	example "github.com/SpotlightGOV/pbflags/gen/example"
	"github.com/google/cel-go/cel"
)

func env(t *testing.T) *cel.Env {
	t.Helper()
	md := (&example.EvaluationContext{}).ProtoReflect().Descriptor()
	e, err := New(md)
	if err != nil {
		t.Fatalf("celenv.New: %v", err)
	}
	return e
}

func TestCompile(t *testing.T) {
	tests := []struct {
		name string
		expr string
	}{
		{"enum equality", `ctx.plan == PlanLevel.ENTERPRISE`},
		{"enum inequality", `ctx.plan != PlanLevel.FREE`},
		{"string equality", `ctx.user_id == "user-99"`},
		{"string presence", `ctx.user_id != ""`},
		{"bool dimension", `ctx.is_internal`},
		{"boolean logic", `ctx.plan == PlanLevel.PRO && ctx.is_internal`},
		{"negation", `!(ctx.is_internal)`},
		{"device type enum", `ctx.device_type == DeviceType.MOBILE`},
		{"enum in list", `ctx.device_type in [DeviceType.MOBILE, DeviceType.TABLET]`},
		{"compound", `ctx.plan == PlanLevel.ENTERPRISE && ctx.device_type != DeviceType.DESKTOP`},
	}

	e := env(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ast, iss := e.Compile(tt.expr)
			if iss.Err() != nil {
				t.Fatalf("compile %q: %v", tt.expr, iss.Err())
			}
			if ast.OutputType() != cel.BoolType {
				t.Errorf("expected bool output, got %v", ast.OutputType())
			}
		})
	}
}

func TestCompileErrors(t *testing.T) {
	tests := []struct {
		name string
		expr string
	}{
		{"nonexistent field", `ctx.nonexistent == "x"`},
		{"nonexistent enum value", `ctx.plan == PlanLevel.NONEXISTENT`},
		{"wrong type comparison", `ctx.plan == "string"`},
	}

	e := env(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, iss := e.Compile(tt.expr)
			if iss.Err() == nil {
				t.Errorf("expected compile error for %q", tt.expr)
			}
		})
	}
}

func TestEvaluate(t *testing.T) {
	e := env(t)

	ast, iss := e.Compile(`ctx.plan == PlanLevel.ENTERPRISE`)
	if iss.Err() != nil {
		t.Fatalf("compile: %v", iss.Err())
	}
	prg, err := e.Program(ast)
	if err != nil {
		t.Fatalf("program: %v", err)
	}

	// Match
	ctx := &example.EvaluationContext{Plan: example.PlanLevel_PLAN_LEVEL_ENTERPRISE}
	out, _, err := prg.Eval(map[string]any{"ctx": ctx})
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	if out.Value() != true {
		t.Errorf("expected true, got %v", out.Value())
	}

	// No match
	ctx2 := &example.EvaluationContext{Plan: example.PlanLevel_PLAN_LEVEL_FREE}
	out2, _, err := prg.Eval(map[string]any{"ctx": ctx2})
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	if out2.Value() != false {
		t.Errorf("expected false, got %v", out2.Value())
	}
}

func TestEvaluateCompound(t *testing.T) {
	e := env(t)

	ast, iss := e.Compile(`ctx.plan == PlanLevel.PRO && ctx.is_internal`)
	if iss.Err() != nil {
		t.Fatalf("compile: %v", iss.Err())
	}
	prg, err := e.Program(ast)
	if err != nil {
		t.Fatalf("program: %v", err)
	}

	tests := []struct {
		name   string
		ctx    *example.EvaluationContext
		expect bool
	}{
		{
			"both true",
			&example.EvaluationContext{Plan: example.PlanLevel_PLAN_LEVEL_PRO, IsInternal: true},
			true,
		},
		{
			"wrong plan",
			&example.EvaluationContext{Plan: example.PlanLevel_PLAN_LEVEL_FREE, IsInternal: true},
			false,
		},
		{
			"not internal",
			&example.EvaluationContext{Plan: example.PlanLevel_PLAN_LEVEL_PRO, IsInternal: false},
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, _, err := prg.Eval(map[string]any{"ctx": tt.ctx})
			if err != nil {
				t.Fatalf("eval: %v", err)
			}
			if out.Value() != tt.expect {
				t.Errorf("expected %v, got %v", tt.expect, out.Value())
			}
		})
	}
}

func TestPascalToUpperSnake(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"PlanLevel", "PLAN_LEVEL"},
		{"DeviceType", "DEVICE_TYPE"},
		{"HTTPMethod", "HTTP_METHOD"},
		{"Simple", "SIMPLE"},
		{"ABCDef", "ABC_DEF"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got := pascalToUpperSnake(tt.in)
			if got != tt.want {
				t.Errorf("PascalToUpperSnake(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
