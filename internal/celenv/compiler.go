package celenv

import (
	"fmt"

	"github.com/google/cel-go/cel"
	celast "github.com/google/cel-go/common/ast"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Compiler compiles and type-checks CEL expressions against the
// EvaluationContext. Compiled programs can be reused across evaluations.
type Compiler struct {
	env *cel.Env
}

// NewCompiler creates a compiler for the given EvaluationContext message descriptor.
func NewCompiler(md protoreflect.MessageDescriptor) (*Compiler, error) {
	env, err := New(md)
	if err != nil {
		return nil, err
	}
	return &Compiler{env: env}, nil
}

// Env returns the underlying CEL environment.
func (c *Compiler) Env() *cel.Env { return c.env }

// CompiledExpr holds a compiled CEL expression ready for evaluation.
type CompiledExpr struct {
	Source  string      // original CEL source text
	AST     *cel.Ast    // type-checked AST (for dimension extraction)
	Program cel.Program // compiled program (for evaluation)
}

// Compile compiles a single CEL expression. The expression must evaluate
// to bool and use only the v1 restricted subset of CEL operations.
func (c *Compiler) Compile(expr string) (*CompiledExpr, error) {
	ast, iss := c.env.Compile(expr)
	if iss.Err() != nil {
		return nil, iss.Err()
	}
	if ast.OutputType() != cel.BoolType {
		return nil, fmt.Errorf("expression must return bool, got %v", ast.OutputType())
	}
	if err := validateV1Subset(ast); err != nil {
		return nil, err
	}
	prg, err := c.env.Program(ast)
	if err != nil {
		return nil, fmt.Errorf("program creation: %w", err)
	}
	return &CompiledExpr{Source: expr, AST: ast, Program: prg}, nil
}

// allowedV1Operators is the set of CEL operator/function names permitted
// in v1 condition expressions.
var allowedV1Operators = map[string]bool{
	"_==_": true, // equality
	"_!=_": true, // inequality
	"_&&_": true, // logical AND
	"_||_": true, // logical OR
	"!_":   true, // logical NOT
	"_<_":  true, // less than
	"_<=_": true, // less than or equal
	"_>_":  true, // greater than
	"_>=_": true, // greater than or equal
	"@in":  true, // containment (in operator)
}

// validateV1Subset walks the compiled AST and rejects expressions that use
// operations outside the v1 restricted subset.
func validateV1Subset(ast *cel.Ast) error {
	nav := celast.NavigateAST(ast.NativeRep())
	return walkValidate(nav)
}

func walkValidate(nav celast.NavigableExpr) error {
	switch nav.Kind() {
	case celast.ComprehensionKind:
		return fmt.Errorf("comprehension expressions (exists, all, filter, map) are not supported in v1 conditions")
	case celast.MapKind:
		return fmt.Errorf("map literal expressions are not supported in v1 conditions")
	case celast.StructKind:
		return fmt.Errorf("struct literal expressions are not supported in v1 conditions")
	case celast.CallKind:
		fn := nav.AsCall().FunctionName()
		if !allowedV1Operators[fn] {
			return fmt.Errorf("operator/function %q is not supported in v1 conditions", fn)
		}
	case celast.IdentKind, celast.SelectKind, celast.LiteralKind, celast.ListKind:
		// These are always allowed.
	}

	for _, child := range nav.Children() {
		if err := walkValidate(child); err != nil {
			return err
		}
	}
	return nil
}
