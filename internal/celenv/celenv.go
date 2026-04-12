// Package celenv builds CEL type environments from EvaluationContext proto
// message descriptors. The environment declares a "ctx" variable of the
// message type and registers prefix-stripped enum constants for each enum
// type referenced by the context's fields (e.g., PLAN_LEVEL_ENTERPRISE
// becomes accessible as PlanLevel.ENTERPRISE in CEL expressions).
package celenv

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// New creates a CEL type-checking environment from an EvaluationContext
// message descriptor. It declares a "ctx" variable of the message type
// and registers prefix-stripped enum constants for each enum type
// referenced by the context's fields.
func New(md protoreflect.MessageDescriptor) (*cel.Env, error) {
	files := collectFileDescs(md)
	pkg := string(md.FullName().Parent())

	descs := make([]any, len(files))
	for i, f := range files {
		descs[i] = f
	}
	baseOpts := []cel.EnvOption{cel.TypeDescs(descs...)}
	if pkg != "" {
		baseOpts = append(baseOpts, cel.Container(pkg))
	}
	baseEnv, err := cel.NewEnv(baseOpts...)
	if err != nil {
		return nil, fmt.Errorf("base CEL environment: %w", err)
	}

	extOpts := []cel.EnvOption{
		cel.Variable("ctx", cel.ObjectType(string(md.FullName()))),
	}

	aliases := buildEnumAliases(md, pkg)
	if len(aliases) > 0 {
		wrapped := &enumAliasProvider{
			Provider: baseEnv.CELTypeProvider(),
			aliases:  aliases,
		}
		extOpts = append(extOpts,
			cel.CustomTypeProvider(wrapped),
			cel.CustomTypeAdapter(baseEnv.CELTypeAdapter()),
		)
	}

	return baseEnv.Extend(extOpts...)
}

// collectFileDescs gathers the proto file descriptors needed to type-check
// expressions against the given message (the message's own file plus files
// for any enum types referenced by its fields).
func collectFileDescs(md protoreflect.MessageDescriptor) []protoreflect.FileDescriptor {
	seen := map[string]bool{}
	var files []protoreflect.FileDescriptor

	add := func(fd protoreflect.FileDescriptor) {
		p := fd.Path()
		if !seen[p] {
			seen[p] = true
			files = append(files, fd)
		}
	}

	add(md.ParentFile())
	fields := md.Fields()
	for i := 0; i < fields.Len(); i++ {
		f := fields.Get(i)
		if f.Kind() == protoreflect.EnumKind {
			add(f.Enum().ParentFile())
		}
	}
	return files
}

// buildEnumAliases generates prefix-stripped enum value aliases.
// For enum PlanLevel with value PLAN_LEVEL_ENTERPRISE, it registers:
//   - "PlanLevel.ENTERPRISE" (unqualified)
//   - "example.PlanLevel.ENTERPRISE" (package-qualified, if pkg is non-empty)
func buildEnumAliases(md protoreflect.MessageDescriptor, pkg string) map[string]ref.Val {
	aliases := map[string]ref.Val{}
	fields := md.Fields()

	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		if fd.Kind() != protoreflect.EnumKind {
			continue
		}
		ed := fd.Enum()
		typeName := string(ed.Name())
		prefix := pascalToUpperSnake(typeName) + "_"

		values := ed.Values()
		for j := 0; j < values.Len(); j++ {
			ev := values.Get(j)
			full := string(ev.Name())
			stripped := strings.TrimPrefix(full, prefix)
			if stripped == full {
				continue // prefix didn't match; skip alias
			}
			val := types.Int(ev.Number())
			aliases[typeName+"."+stripped] = val
			if pkg != "" {
				aliases[pkg+"."+typeName+"."+stripped] = val
			}
		}
	}
	return aliases
}

// pascalToUpperSnake converts PascalCase to UPPER_SNAKE_CASE.
// For example, "PlanLevel" → "PLAN_LEVEL", "HTTPMethod" → "HTTP_METHOD".
func pascalToUpperSnake(s string) string {
	runes := []rune(s)
	var b strings.Builder
	for i, r := range runes {
		if i > 0 && unicode.IsUpper(r) {
			if unicode.IsLower(runes[i-1]) {
				b.WriteByte('_')
			} else if i+1 < len(runes) && unicode.IsLower(runes[i+1]) {
				b.WriteByte('_')
			}
		}
		b.WriteRune(unicode.ToUpper(r))
	}
	return b.String()
}

// enumAliasProvider wraps a CEL type provider and adds support for
// prefix-stripped enum value names in FindIdent and EnumValue lookups.
type enumAliasProvider struct {
	types.Provider
	aliases map[string]ref.Val
}

func (p *enumAliasProvider) FindIdent(name string) (ref.Val, bool) {
	if v, ok := p.aliases[name]; ok {
		return v, true
	}
	return p.Provider.FindIdent(name)
}

func (p *enumAliasProvider) EnumValue(name string) ref.Val {
	if v, ok := p.aliases[name]; ok {
		return v
	}
	return p.Provider.EnumValue(name)
}
