// Package contextutil discovers the EvaluationContext proto message and
// extracts dimension metadata for codegen backends.
package contextutil

import (
	"fmt"
	"strings"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

const contextExtNum = 51003   // (pbflags.context) on MessageOptions
const dimensionExtNum = 51004 // (pbflags.dimension) on FieldOptions

// DimensionKind enumerates the supported proto types for context dimensions.
type DimensionKind int

const (
	DimensionString DimensionKind = iota
	DimensionEnum
	DimensionBool
	DimensionInt64
)

func (k DimensionKind) String() string {
	switch k {
	case DimensionString:
		return "string"
	case DimensionEnum:
		return "enum"
	case DimensionBool:
		return "bool"
	case DimensionInt64:
		return "int64"
	default:
		return "unknown"
	}
}

// DimensionDef describes a single dimension field in the EvaluationContext.
type DimensionDef struct {
	Name         string        // proto field name (e.g., "user_id")
	Number       int32         // proto field number
	Kind         DimensionKind // dimension type
	Description  string        // from DimensionOptions.description
	Distribution int32         // DimensionDistribution enum value (0=unspecified, 1=uniform, 2=categorical)
	Presence     int32         // DimensionPresence enum value (0=unspecified, 1=required, 2=optional)

	// Enum-specific metadata (only populated when Kind == DimensionEnum).
	EnumName   string          // fully qualified enum name
	EnumValues []EnumValueDef  // enum values
	ProtoField *protogen.Field // original protogen field for codegen access
}

// IsUniform returns true if the dimension has distribution UNIFORM (suitable for launch hashing).
func (d *DimensionDef) IsUniform() bool { return d.Distribution == 1 }

// IsCategorical returns true if the dimension has distribution CATEGORICAL or is inherently
// categorical (enum/bool kind).
func (d *DimensionDef) IsCategorical() bool {
	return d.Distribution == 2 || d.Kind == DimensionEnum || d.Kind == DimensionBool
}

// IsRequired returns true if the dimension has presence REQUIRED.
func (d *DimensionDef) IsRequired() bool { return d.Presence == 1 }

// EnumValueDef describes a single enum value.
type EnumValueDef struct {
	Name   string // proto enum value name (e.g., "PLAN_LEVEL_ENTERPRISE")
	Number int32  // ordinal
}

// ContextDef holds the discovered EvaluationContext message and its dimensions.
type ContextDef struct {
	MessageName string         // fully qualified message name
	Dimensions  []DimensionDef // dimensions in field-number order
	Message     *protogen.Message
}

// DiscoverContext scans all files in the plugin request for a message annotated
// with option (pbflags.context). Returns the context definition, or nil if
// none is found. Returns an error if multiple are found or if validation fails.
func DiscoverContext(plugin *protogen.Plugin) (*ContextDef, error) {
	var found []*protogen.Message
	for _, f := range plugin.Files {
		for _, msg := range f.Messages {
			if hasContextOption(msg) {
				found = append(found, msg)
			}
		}
	}

	if len(found) == 0 {
		return nil, nil
	}
	if len(found) > 1 {
		names := make([]string, len(found))
		for i, m := range found {
			names[i] = string(m.Desc.FullName())
		}
		return nil, fmt.Errorf("multiple messages annotated with (pbflags.context): %s", strings.Join(names, ", "))
	}

	return parseContext(found[0])
}

// DiscoverContextFromFiles scans a file registry for a message annotated with
// option (pbflags.context). Returns the message descriptor, or an error if
// none or multiple are found. This is the runtime equivalent of
// DiscoverContext for use outside of protoc plugins (e.g., pbflags-sync).
func DiscoverContextFromFiles(files *protoregistry.Files) (protoreflect.MessageDescriptor, error) {
	var found []protoreflect.MessageDescriptor
	files.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		for i := 0; i < fd.Messages().Len(); i++ {
			msg := fd.Messages().Get(i)
			if HasContextOption(msg.Options()) {
				found = append(found, msg)
			}
		}
		return true
	})

	if len(found) == 0 {
		return nil, fmt.Errorf("no message with (pbflags.context) option found in descriptors")
	}
	if len(found) > 1 {
		names := make([]string, len(found))
		for i, m := range found {
			names[i] = string(m.FullName())
		}
		return nil, fmt.Errorf("multiple messages annotated with (pbflags.context): %s", strings.Join(names, ", "))
	}
	return found[0], nil
}

// HasContextOption checks if the given message options contain the
// (pbflags.context) extension (field number 51003).
func HasContextOption(opts protoreflect.ProtoMessage) bool {
	if opts == nil {
		return false
	}
	rm := opts.ProtoReflect()

	// Try resolved extensions first.
	var found bool
	rm.Range(func(fd protoreflect.FieldDescriptor, _ protoreflect.Value) bool {
		if fd.Number() == contextExtNum && fd.IsExtension() {
			found = true
			return false
		}
		return true
	})
	if found {
		return true
	}

	// Fall back to unknown fields.
	return hasContextInUnknown(rm.GetUnknown())
}

// hasContextOption checks if the message has option (pbflags.context) set.
func hasContextOption(msg *protogen.Message) bool {
	return HasContextOption(msg.Desc.Options())
}

// hasContextInUnknown parses (pbflags.context) from unknown fields.
func hasContextInUnknown(b []byte) bool {
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return false
		}
		b = b[n:]
		if num == contextExtNum && typ == protowire.BytesType {
			// ContextOptions is an empty message — its presence means true.
			_, n := protowire.ConsumeBytes(b)
			return n >= 0
		}
		n = skipField(b, typ)
		if n < 0 {
			return false
		}
		b = b[n:]
	}
	return false
}

func parseContext(msg *protogen.Message) (*ContextDef, error) {
	var dims []DimensionDef

	for _, field := range msg.Fields {
		dim, err := parseDimension(field)
		if err != nil {
			return nil, fmt.Errorf("context %s: %w", msg.Desc.FullName(), err)
		}
		if dim == nil {
			continue // field without (pbflags.dimension) annotation
		}
		dims = append(dims, *dim)
	}

	if len(dims) == 0 {
		return nil, fmt.Errorf("context message %s has no fields annotated with (pbflags.dimension)", msg.Desc.FullName())
	}

	return &ContextDef{
		MessageName: string(msg.Desc.FullName()),
		Dimensions:  dims,
		Message:     msg,
	}, nil
}

func parseDimension(field *protogen.Field) (*DimensionDef, error) {
	opts := field.Desc.Options()
	if opts == nil {
		return nil, nil
	}

	rm := opts.(interface{ ProtoReflect() protoreflect.Message }).ProtoReflect()

	// Check for (pbflags.dimension) extension.
	var hasDim bool
	var description string
	var distribution, presence int32

	rm.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if fd.Number() == dimensionExtNum && fd.IsExtension() {
			hasDim = true
			dimMsg := v.Message()
			dimMsg.Range(func(dfd protoreflect.FieldDescriptor, dv protoreflect.Value) bool {
				switch dfd.Name() {
				case "description":
					description = dv.String()
				case "distribution":
					distribution = int32(dv.Enum())
				case "presence":
					presence = int32(dv.Enum())
				}
				return true
			})
			return false
		}
		return true
	})

	if !hasDim {
		// Try unknown fields.
		hasDim, description, distribution, presence = parseDimensionFromUnknown(rm.GetUnknown())
	}

	if !hasDim {
		return nil, nil
	}

	kind, err := fieldKindToDimensionKind(field)
	if err != nil {
		return nil, err
	}

	dim := &DimensionDef{
		Name:         string(field.Desc.Name()),
		Number:       int32(field.Desc.Number()),
		Kind:         kind,
		Description:  description,
		Distribution: distribution,
		Presence:     presence,
		ProtoField:   field,
	}

	if kind == DimensionEnum {
		enumDesc := field.Desc.Enum()
		dim.EnumName = string(enumDesc.FullName())
		for i := 0; i < enumDesc.Values().Len(); i++ {
			v := enumDesc.Values().Get(i)
			dim.EnumValues = append(dim.EnumValues, EnumValueDef{
				Name:   string(v.Name()),
				Number: int32(v.Number()),
			})
		}
	}

	return dim, nil
}

func fieldKindToDimensionKind(field *protogen.Field) (DimensionKind, error) {
	if field.Desc.IsList() || field.Desc.IsMap() {
		return 0, fmt.Errorf("field %s: repeated/map fields are not supported as dimensions", field.Desc.Name())
	}

	switch field.Desc.Kind() {
	case protoreflect.StringKind:
		return DimensionString, nil
	case protoreflect.EnumKind:
		return DimensionEnum, nil
	case protoreflect.BoolKind:
		return DimensionBool, nil
	case protoreflect.Int64Kind:
		return DimensionInt64, nil
	default:
		return 0, fmt.Errorf("field %s: unsupported dimension type %s (must be string, enum, bool, or int64)",
			field.Desc.Name(), field.Desc.Kind())
	}
}

// parseDimensionFromUnknown extracts DimensionOptions fields from unknown wire data.
func parseDimensionFromUnknown(b []byte) (found bool, description string, distribution, presence int32) {
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return
		}
		b = b[n:]
		if num == dimensionExtNum && typ == protowire.BytesType {
			data, n := protowire.ConsumeBytes(b)
			if n < 0 {
				return
			}
			b = b[n:]
			found = true
			description, distribution, presence = parseDimensionOptionsBytes(data)
			continue
		}
		n = skipField(b, typ)
		if n < 0 {
			return
		}
		b = b[n:]
	}
	return
}

func parseDimensionOptionsBytes(b []byte) (description string, distribution, presence int32) {
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return
		}
		b = b[n:]
		switch {
		case num == 1 && typ == protowire.BytesType: // description
			data, n := protowire.ConsumeBytes(b)
			if n < 0 {
				return
			}
			description = string(data)
			b = b[n:]
		case num == 2 && typ == protowire.VarintType: // distribution (was hashable bool)
			v, n := protowire.ConsumeVarint(b)
			if n < 0 {
				return
			}
			distribution = int32(v)
			b = b[n:]
		case num == 4 && typ == protowire.VarintType: // presence
			v, n := protowire.ConsumeVarint(b)
			if n < 0 {
				return
			}
			presence = int32(v)
			b = b[n:]
		default:
			n = skipField(b, typ)
			if n < 0 {
				return
			}
			b = b[n:]
		}
	}
	return
}

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
