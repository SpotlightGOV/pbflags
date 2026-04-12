package celenv

import (
	"fmt"
	"sort"

	"github.com/google/cel-go/cel"
	celast "github.com/google/cel-go/common/ast"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
)

// DimClassification describes how a dimension is used for cache key construction.
type DimClassification string

const (
	Bounded              DimClassification = "bounded"
	FiniteFilterUniform  DimClassification = "finite_filter_uniform"
	FiniteFilterDistinct DimClassification = "finite_filter_distinct"
	Unbounded            DimClassification = "unbounded"
)

// DimensionMeta holds classification metadata for a single dimension
// as used by a flag's condition chain.
type DimensionMeta struct {
	Classification DimClassification `json:"classification"`
	LiteralSet     []string          `json:"literal_set,omitempty"`
}

// condUsage tracks how a dimension is used in a single condition.
type condUsage struct {
	literals []string             // nil = non-literal usage; non-nil = literal comparison
	value    *pbflagsv1.FlagValue // the flag value for this condition
}

// ClassifyDimensions analyzes a flag's compiled condition chain and classifies
// each referenced dimension for cache key construction.
//
// conditions and values must have the same length. conditions[i] is the
// compiled AST (nil for "otherwise") and values[i] is the flag value.
// boundedDims is the set of inherently bounded dimensions (enum/bool).
func ClassifyDimensions(
	conditions []*cel.Ast,
	values []*pbflagsv1.FlagValue,
	boundedDims map[string]bool,
) map[string]*DimensionMeta {
	dimUsages := map[string][]condUsage{}

	for i, ast := range conditions {
		if ast == nil {
			continue
		}
		nav := celast.NavigateAST(ast.NativeRep())
		for _, ref := range extractDimRefs(nav) {
			dimUsages[ref.name] = append(dimUsages[ref.name], condUsage{
				literals: ref.literals,
				value:    values[i],
			})
		}
	}

	result := make(map[string]*DimensionMeta, len(dimUsages))
	for name, usages := range dimUsages {
		result[name] = classifyDim(name, usages, boundedDims[name])
	}
	return result
}

func classifyDim(_ string, usages []condUsage, isBounded bool) *DimensionMeta {
	if isBounded {
		return &DimensionMeta{Classification: Bounded}
	}

	for _, u := range usages {
		if u.literals == nil {
			return &DimensionMeta{Classification: Unbounded}
		}
	}

	// All uses are literal comparisons.
	lits := collectLiterals(usages)
	if allSameValue(usages) {
		return &DimensionMeta{Classification: FiniteFilterUniform, LiteralSet: lits}
	}
	return &DimensionMeta{Classification: FiniteFilterDistinct, LiteralSet: lits}
}

func collectLiterals(usages []condUsage) []string {
	seen := map[string]bool{}
	var lits []string
	for _, u := range usages {
		for _, l := range u.literals {
			if !seen[l] {
				seen[l] = true
				lits = append(lits, l)
			}
		}
	}
	sort.Strings(lits)
	return lits
}

func allSameValue(usages []condUsage) bool {
	if len(usages) <= 1 {
		return true
	}
	first := usages[0].value
	for _, u := range usages[1:] {
		if !proto.Equal(first, u.value) {
			return false
		}
	}
	return true
}

// condDimRef is a dimension reference extracted from a condition expression.
type condDimRef struct {
	name     string
	literals []string // non-nil = literal comparison; nil = non-literal
}

func extractDimRefs(nav celast.NavigableExpr) []condDimRef {
	var refs []condDimRef
	walkDimRefs(nav, &refs)
	return refs
}

func walkDimRefs(nav celast.NavigableExpr, refs *[]condDimRef) {
	switch nav.Kind() {
	case celast.CallKind:
		call := nav.AsCall()
		children := nav.Children()

		switch call.FunctionName() {
		case "_&&_", "_||_", "!_":
			for _, child := range children {
				walkDimRefs(child, refs)
			}
		case "_==_":
			if len(children) == 2 {
				if ref := matchEqLiteral(children[0], children[1]); ref != nil {
					*refs = append(*refs, *ref)
					return
				}
				if ref := matchEqLiteral(children[1], children[0]); ref != nil {
					*refs = append(*refs, *ref)
					return
				}
			}
			recordNonLiteralDims(children, refs)
		case "@in":
			if len(children) == 2 {
				if ref := matchInLiterals(children[0], children[1]); ref != nil {
					*refs = append(*refs, *ref)
					return
				}
			}
			recordNonLiteralDims(children, refs)
		default:
			recordNonLiteralDims(children, refs)
		}

	case celast.SelectKind:
		if dim := ctxDimName(nav); dim != "" {
			*refs = append(*refs, condDimRef{name: dim})
		}
	}
}

func ctxDimName(nav celast.NavigableExpr) string {
	if nav.Kind() != celast.SelectKind {
		return ""
	}
	children := nav.Children()
	if len(children) != 1 {
		return ""
	}
	if children[0].Kind() == celast.IdentKind && children[0].AsIdent() == "ctx" {
		return nav.AsSelect().FieldName()
	}
	return ""
}

func matchEqLiteral(left, right celast.NavigableExpr) *condDimRef {
	dim := ctxDimName(left)
	if dim == "" {
		return nil
	}
	lit, ok := extractLiteral(right)
	if !ok {
		return nil
	}
	return &condDimRef{name: dim, literals: []string{lit}}
}

func matchInLiterals(left, right celast.NavigableExpr) *condDimRef {
	dim := ctxDimName(left)
	if dim == "" {
		return nil
	}
	if right.Kind() != celast.ListKind {
		return nil
	}
	var lits []string
	for _, child := range right.Children() {
		lit, ok := extractLiteral(child)
		if !ok {
			return nil
		}
		lits = append(lits, lit)
	}
	return &condDimRef{name: dim, literals: lits}
}

func extractLiteral(nav celast.NavigableExpr) (string, bool) {
	if nav.Kind() == celast.LiteralKind {
		return fmt.Sprintf("%v", nav.AsLiteral().Value()), true
	}
	if nav.Kind() == celast.IdentKind {
		return nav.AsIdent(), true
	}
	return "", false
}

func recordNonLiteralDims(children []celast.NavigableExpr, refs *[]condDimRef) {
	for _, child := range children {
		if dim := ctxDimName(child); dim != "" {
			*refs = append(*refs, condDimRef{name: dim})
		}
	}
}

// BoundedDimsFromDescriptor returns the set of inherently bounded dimension
// names (enum or bool fields) from an EvaluationContext message descriptor.
func BoundedDimsFromDescriptor(md protoreflect.MessageDescriptor) map[string]bool {
	bounded := map[string]bool{}
	fields := md.Fields()
	for i := 0; i < fields.Len(); i++ {
		f := fields.Get(i)
		switch f.Kind() {
		case protoreflect.EnumKind, protoreflect.BoolKind:
			bounded[string(f.Name())] = true
		}
	}
	return bounded
}
