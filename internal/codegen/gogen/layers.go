package gogen

import (
	"fmt"
	"strings"
	"unicode"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/reflect/protoreflect"
)

const layersExtNum = 51002 // (pbflags.layers) on EnumOptions

// layerValue represents a single value from the (pbflags.layers) enum.
type layerValue struct {
	name       string // original enum value name, e.g., "LAYER_ENTITY"
	number     int32  // enum ordinal
	strippedName string // prefix-stripped name, e.g., "ENTITY"
	layerName  string // lowercase layer name for matching, e.g., "entity"
	isGlobal   bool   // true for ordinal 0, 1, or name containing "GLOBAL"
}

// layerDef holds the discovered layer enum and its parsed values.
type layerDef struct {
	enumName string       // fully qualified enum name
	values   []layerValue // all enum values
	byName   map[string]layerValue // keyed by lowercase layer name (prefix-stripped)
}

// discoverLayers scans all files in the plugin request for an enum annotated
// with option (pbflags.layers) = true. Returns the layer definition, or an
// error if none is found or multiple are found.
func discoverLayers(plugin *protogen.Plugin) (*layerDef, error) {
	var found []*protogen.Enum
	for _, f := range plugin.Files {
		for _, e := range f.Enums {
			if hasLayersOption(e) {
				found = append(found, e)
			}
		}
		// Also check enums nested in messages.
		for _, msg := range f.Messages {
			for _, e := range msg.Enums {
				if hasLayersOption(e) {
					found = append(found, e)
				}
			}
		}
	}

	if len(found) == 0 {
		return nil, fmt.Errorf("no enum annotated with option (pbflags.layers) = true found in input files")
	}
	if len(found) > 1 {
		names := make([]string, len(found))
		for i, e := range found {
			names[i] = string(e.Desc.FullName())
		}
		return nil, fmt.Errorf("multiple enums annotated with (pbflags.layers): %s", strings.Join(names, ", "))
	}

	return parseLayerEnum(found[0])
}

// hasLayersOption checks if the enum has option (pbflags.layers) = true.
func hasLayersOption(e *protogen.Enum) bool {
	opts := e.Desc.Options()
	if opts == nil {
		return false
	}

	// Try resolved extensions first.
	protoMsg := opts.(interface{ ProtoReflect() protoreflect.Message })
	rm := protoMsg.ProtoReflect()

	var found bool
	rm.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if fd.Number() == layersExtNum && fd.IsExtension() {
			found = v.Bool()
			return false
		}
		return true
	})
	if found {
		return true
	}

	// Fall back to unknown fields (when the extension isn't linked).
	unk := rm.GetUnknown()
	return parseLayersFromUnknown(unk)
}

// parseLayersFromUnknown parses the (pbflags.layers) bool from unknown fields.
func parseLayersFromUnknown(b []byte) bool {
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return false
		}
		b = b[n:]
		if num == layersExtNum && typ == protowire.VarintType {
			v, n := protowire.ConsumeVarint(b)
			if n < 0 {
				return false
			}
			return v != 0
		}
		n = skipField(b, typ)
		if n < 0 {
			return false
		}
		b = b[n:]
	}
	return false
}

// parseLayerEnum extracts layer values from the annotated enum.
func parseLayerEnum(e *protogen.Enum) (*layerDef, error) {
	values := make([]layerValue, 0, len(e.Values))
	for _, v := range e.Values {
		values = append(values, layerValue{
			name:   string(v.Desc.Name()),
			number: int32(v.Desc.Number()),
		})
	}

	if len(values) == 0 {
		return nil, fmt.Errorf("layers enum %s has no values", e.Desc.FullName())
	}
	if values[0].number != 0 {
		return nil, fmt.Errorf("layers enum %s must have ordinal 0 as its first value", e.Desc.FullName())
	}

	// Compute the common prefix.
	prefix := commonEnumPrefix(values)

	// Strip prefix and classify.
	byName := make(map[string]layerValue, len(values))
	for i := range values {
		stripped := strings.TrimPrefix(values[i].name, prefix)
		values[i].strippedName = stripped
		values[i].layerName = strings.ToLower(stripped)
		values[i].isGlobal = values[i].number == 0 ||
			strings.EqualFold(stripped, "GLOBAL") ||
			strings.EqualFold(stripped, "UNSPECIFIED")
		byName[values[i].layerName] = values[i]
	}

	return &layerDef{
		enumName: string(e.Desc.FullName()),
		values:   values,
		byName:   byName,
	}, nil
}

// resolveLayer looks up a layer name string (e.g., "entity") against the
// discovered layer values. Returns the matching layerValue and true, or
// an error if the name doesn't match any value.
func (ld *layerDef) resolveLayer(name string) (layerValue, bool) {
	if name == "" {
		return layerValue{isGlobal: true}, true
	}
	lv, ok := ld.byName[strings.ToLower(name)]
	return lv, ok
}

// generateLayersPackage generates a shared layers package with typed ID
// wrappers for each non-global layer value.
func generateLayersPackage(plugin *protogen.Plugin, layers *layerDef, packagePrefix string) error {
	importPath := protogen.GoImportPath(packagePrefix + "/layers")
	g := plugin.NewGeneratedFile("layers/layers.go", importPath)
	p := g.P

	p("// Code generated by protoc-gen-pbflags. DO NOT EDIT.")
	p()
	p("package layers")
	p()

	// Collect non-global layers.
	var nonGlobal []layerValue
	for _, lv := range layers.values {
		if !lv.isGlobal {
			nonGlobal = append(nonGlobal, lv)
		}
	}

	if len(nonGlobal) == 0 {
		p("// No non-global layers defined.")
		return nil
	}

	for _, lv := range nonGlobal {
		typeName := toPascalCase(strings.ToLower(lv.strippedName)) + "ID"
		ctorName := toPascalCase(strings.ToLower(lv.strippedName))
		paramName := lowerFirst(toPascalCase(strings.ToLower(lv.strippedName)))

		p("// ", typeName, " identifies ", addArticle(strings.ToLower(lv.strippedName)), " for layer-scoped flag evaluation.")
		p("// The zero value evaluates global state.")
		p("type ", typeName, " struct{ v string }")
		p()
		p("// ", ctorName, " creates a ", typeName, " for the given identifier.")
		p("func ", ctorName, "(", paramName, " string) ", typeName, " { return ", typeName, "{v: ", paramName, "} }")
		p()
		p("// String returns the underlying identifier.")
		p("func (id ", typeName, ") String() string { return id.v }")
		p()
	}

	return nil
}

// addArticle adds "a" or "an" before a word.
func addArticle(word string) string {
	if len(word) == 0 {
		return word
	}
	first := unicode.ToLower(rune(word[0]))
	if first == 'a' || first == 'e' || first == 'i' || first == 'o' || first == 'u' {
		return "an " + word
	}
	return "a " + word
}

// layerTypeName returns the Go type name for a layer (e.g., "entity" → "EntityID").
func layerTypeName(layerName string) string {
	return toPascalCase(layerName) + "ID"
}

// layerParamName returns the Go parameter name for a layer (e.g., "entity" → "entity").
func layerParamName(layerName string) string {
	return lowerFirst(toPascalCase(layerName))
}

// commonEnumPrefix computes the longest common underscore-delimited prefix
// shared by all enum value names (e.g., "LAYER_" for LAYER_GLOBAL, LAYER_USER).
func commonEnumPrefix(values []layerValue) string {
	if len(values) == 0 {
		return ""
	}
	prefix := values[0].name
	for _, v := range values[1:] {
		for !strings.HasPrefix(v.name, prefix) {
			idx := strings.LastIndex(prefix[:len(prefix)-1], "_")
			if idx < 0 {
				return ""
			}
			prefix = prefix[:idx+1]
		}
	}
	// Ensure prefix ends with underscore.
	if prefix != "" && !strings.HasSuffix(prefix, "_") {
		idx := strings.LastIndex(prefix, "_")
		if idx < 0 {
			return ""
		}
		prefix = prefix[:idx+1]
	}
	return prefix
}
