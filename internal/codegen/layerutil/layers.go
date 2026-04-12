// Package layerutil provides shared layer discovery and parsing logic for
// codegen backends (Go, Java, etc.).
package layerutil

import (
	"fmt"
	"strings"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/reflect/protoreflect"
)

const layersExtNum = 51002 // (pbflags.layers) on EnumOptions

// LayerValue represents a single value from the (pbflags.layers) enum.
type LayerValue struct {
	Name         string // original enum value name, e.g., "LAYER_ENTITY"
	Number       int32  // enum ordinal
	StrippedName string // prefix-stripped name, e.g., "ENTITY"
	LayerName    string // lowercase layer name for matching, e.g., "entity"
	IsGlobal     bool   // true for ordinal 0, 1, or name containing "GLOBAL"
}

// LayerDef holds the discovered layer enum and its parsed values.
type LayerDef struct {
	EnumName string                // fully qualified enum name
	Values   []LayerValue          // all enum values
	ByName   map[string]LayerValue // keyed by lowercase layer name (prefix-stripped)
}

// ResolveLayer looks up a layer name string (e.g., "entity") against the
// discovered layer values. Returns the matching LayerValue and true, or
// false if the name doesn't match any value.
func (ld *LayerDef) ResolveLayer(name string) (LayerValue, bool) {
	if name == "" || strings.EqualFold(name, "global") {
		return LayerValue{IsGlobal: true}, true
	}
	lv, ok := ld.ByName[strings.ToLower(name)]
	return lv, ok
}

// DiscoverLayers scans all files in the plugin request for an enum annotated
// with option (pbflags.layers) = true. Returns the layer definition, or an
// error if none is found or multiple are found.
func DiscoverLayers(plugin *protogen.Plugin) (*LayerDef, error) {
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
		return nil, nil // No layers enum; context dimensions replace layers.
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
func parseLayerEnum(e *protogen.Enum) (*LayerDef, error) {
	values := make([]LayerValue, 0, len(e.Values))
	for _, v := range e.Values {
		values = append(values, LayerValue{
			Name:   string(v.Desc.Name()),
			Number: int32(v.Desc.Number()),
		})
	}

	if len(values) == 0 {
		return nil, fmt.Errorf("layers enum %s has no values", e.Desc.FullName())
	}
	if values[0].Number != 0 {
		return nil, fmt.Errorf("layers enum %s must have ordinal 0 as its first value", e.Desc.FullName())
	}

	// Compute the common prefix.
	prefix := commonEnumPrefix(values)

	// Strip prefix and classify.
	byName := make(map[string]LayerValue, len(values))
	for i := range values {
		stripped := strings.TrimPrefix(values[i].Name, prefix)
		values[i].StrippedName = stripped
		values[i].LayerName = strings.ToLower(stripped)
		values[i].IsGlobal = values[i].Number == 0 ||
			strings.EqualFold(stripped, "GLOBAL") ||
			strings.EqualFold(stripped, "UNSPECIFIED")
		byName[values[i].LayerName] = values[i]
	}

	return &LayerDef{
		EnumName: string(e.Desc.FullName()),
		Values:   values,
		ByName:   byName,
	}, nil
}

// commonEnumPrefix computes the longest common underscore-delimited prefix
// shared by all enum value names (e.g., "LAYER_" for LAYER_GLOBAL, LAYER_USER).
func commonEnumPrefix(values []LayerValue) string {
	if len(values) == 0 {
		return ""
	}
	prefix := values[0].Name
	for _, v := range values[1:] {
		for !strings.HasPrefix(v.Name, prefix) {
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

// skipField skips over a protobuf field value of the given wire type.
func skipField(b []byte, typ protowire.Type) int {
	switch typ {
	case protowire.VarintType:
		_, n := protowire.ConsumeVarint(b)
		return n
	case protowire.Fixed32Type:
		_, n := protowire.ConsumeFixed32(b)
		return n
	case protowire.Fixed64Type:
		_, n := protowire.ConsumeFixed64(b)
		return n
	case protowire.BytesType:
		_, n := protowire.ConsumeBytes(b)
		return n
	default:
		return -1
	}
}
