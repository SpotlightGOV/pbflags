package evaluator

import (
	"fmt"
	"os"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
)

const (
	featureExtNum protoreflect.FieldNumber = 51000 // (pbflags.feature) on MessageOptions
	flagExtNum    protoreflect.FieldNumber = 51001 // (pbflags.flag) on FieldOptions
)

// FlagDef is a flag definition extracted from a proto descriptor.
type FlagDef struct {
	FlagID    string
	FeatureID string
	FieldNum  int32
	Name      string
	FlagType  pbflagsv1.FlagType
	Layer     int32 // Layer enum ordinal
	Default   *pbflagsv1.FlagValue

	FeatureDisplayName string
	FeatureDescription string
	FeatureOwner       string
}

// IsGlobalLayer returns true if the flag layer is global (including unspecified).
func (d *FlagDef) IsGlobalLayer() bool {
	return d.Layer == 0 || d.Layer == 1 // LAYER_UNSPECIFIED or LAYER_GLOBAL
}

// ParseDescriptorFile reads and parses a FileDescriptorSet from the given path.
func ParseDescriptorFile(path string) ([]FlagDef, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read descriptors: %w", err)
	}
	return ParseDescriptors(data)
}

// ParseDescriptors extracts flag definitions from a serialized FileDescriptorSet.
func ParseDescriptors(data []byte) ([]FlagDef, error) {
	fds := &descriptorpb.FileDescriptorSet{}
	if err := proto.Unmarshal(data, fds); err != nil {
		return nil, fmt.Errorf("unmarshal descriptor set: %w", err)
	}

	files, err := protodesc.NewFiles(fds)
	if err != nil {
		return nil, fmt.Errorf("build file registry: %w", err)
	}

	types := new(protoregistry.Types)
	files.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		registerAllTypes(types, fd)
		return true
	})

	featureExt, err := types.FindExtensionByNumber(
		(&descriptorpb.MessageOptions{}).ProtoReflect().Descriptor().FullName(),
		featureExtNum,
	)
	if err != nil {
		return nil, fmt.Errorf("feature extension (51000) not found in descriptors: %w", err)
	}

	flagExt, err := types.FindExtensionByNumber(
		(&descriptorpb.FieldOptions{}).ProtoReflect().Descriptor().FullName(),
		flagExtNum,
	)
	if err != nil {
		return nil, fmt.Errorf("flag extension (51001) not found in descriptors: %w", err)
	}

	var defs []FlagDef
	files.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		for i := 0; i < fd.Messages().Len(); i++ {
			extracted, extractErr := extractFromMessage(fd.Messages().Get(i), featureExt, flagExt, types)
			if extractErr != nil {
				err = extractErr
				return false
			}
			defs = append(defs, extracted...)
		}
		return true
	})
	if err != nil {
		return nil, err
	}

	return defs, nil
}

func registerAllTypes(types *protoregistry.Types, fd protoreflect.FileDescriptor) {
	for i := 0; i < fd.Extensions().Len(); i++ {
		types.RegisterExtension(dynamicpb.NewExtensionType(fd.Extensions().Get(i))) //nolint:errcheck
	}
	for i := 0; i < fd.Messages().Len(); i++ {
		registerMessageTypes(types, fd.Messages().Get(i))
	}
}

func registerMessageTypes(types *protoregistry.Types, msg protoreflect.MessageDescriptor) {
	types.RegisterMessage(dynamicpb.NewMessageType(msg)) //nolint:errcheck
	for i := 0; i < msg.Extensions().Len(); i++ {
		types.RegisterExtension(dynamicpb.NewExtensionType(msg.Extensions().Get(i))) //nolint:errcheck
	}
	for i := 0; i < msg.Messages().Len(); i++ {
		registerMessageTypes(types, msg.Messages().Get(i))
	}
}

func extractFromMessage(
	msg protoreflect.MessageDescriptor,
	featureExt, flagExt protoreflect.ExtensionType,
	types *protoregistry.Types,
) ([]FlagDef, error) {
	fi, err := getFeatureInfo(msg, featureExt, types)
	if err != nil || fi.id == "" {
		return nil, err
	}

	var defs []FlagDef
	for i := 0; i < msg.Fields().Len(); i++ {
		field := msg.Fields().Get(i)
		flagDef, err := extractFlagDef(fi.id, field, flagExt, types)
		if err != nil {
			return nil, err
		}
		if flagDef != nil {
			flagDef.FeatureDisplayName = fi.displayName
			flagDef.FeatureDescription = fi.description
			flagDef.FeatureOwner = fi.owner
			defs = append(defs, *flagDef)
		}
	}
	return defs, nil
}

type featureInfo struct {
	id, displayName, description, owner string
}

func getFeatureInfo(
	msg protoreflect.MessageDescriptor,
	featureExt protoreflect.ExtensionType,
	types *protoregistry.Types,
) (featureInfo, error) {
	opts := msg.Options()
	if opts == nil {
		return featureInfo{}, nil
	}

	raw, err := proto.Marshal(opts.(proto.Message))
	if err != nil {
		return featureInfo{}, nil
	}

	resolved := &descriptorpb.MessageOptions{}
	if err := (proto.UnmarshalOptions{Resolver: types}).Unmarshal(raw, resolved); err != nil {
		return featureInfo{}, nil
	}

	extVal := safeGetExtension(resolved, featureExt)
	if extVal == nil {
		return featureInfo{}, nil
	}

	extMsg, ok := extVal.(*dynamicpb.Message)
	if !ok {
		return featureInfo{}, nil
	}

	fi := featureInfo{
		displayName: string(msg.Name()),
	}
	if f := extMsg.Descriptor().Fields().ByNumber(1); f != nil {
		fi.id = extMsg.Get(f).String()
	}
	if f := extMsg.Descriptor().Fields().ByNumber(2); f != nil {
		fi.description = extMsg.Get(f).String()
	}
	if f := extMsg.Descriptor().Fields().ByNumber(3); f != nil {
		fi.owner = extMsg.Get(f).String()
	}
	return fi, nil
}

func extractFlagDef(
	featureID string,
	field protoreflect.FieldDescriptor,
	flagExt protoreflect.ExtensionType,
	types *protoregistry.Types,
) (*FlagDef, error) {
	opts := field.Options()
	if opts == nil {
		return nil, nil
	}

	raw, err := proto.Marshal(opts.(proto.Message))
	if err != nil {
		return nil, nil
	}

	resolved := &descriptorpb.FieldOptions{}
	if err := (proto.UnmarshalOptions{Resolver: types}).Unmarshal(raw, resolved); err != nil {
		return nil, nil
	}

	extVal := safeGetExtension(resolved, flagExt)
	if extVal == nil {
		return nil, nil
	}

	extMsg, ok := extVal.(*dynamicpb.Message)
	if !ok {
		return nil, nil
	}

	flagType := kindToFlagType(field.Kind())
	if flagType == pbflagsv1.FlagType_FLAG_TYPE_UNSPECIFIED {
		return nil, nil
	}

	def := &FlagDef{
		FlagID:    fmt.Sprintf("%s/%d", featureID, field.Number()),
		FeatureID: featureID,
		FieldNum:  int32(field.Number()),
		Name:      string(field.Name()),
		FlagType:  flagType,
	}

	layerField := extMsg.Descriptor().Fields().ByNumber(3)
	if layerField != nil && extMsg.Has(layerField) {
		def.Layer = int32(extMsg.Get(layerField).Enum())
	}

	defaultField := extMsg.Descriptor().Fields().ByNumber(2)
	if defaultField != nil && extMsg.Has(defaultField) {
		def.Default = parseFlagDefault(extMsg.Get(defaultField).Message(), flagType)
	}

	return def, nil
}

func parseFlagDefault(msg protoreflect.Message, flagType pbflagsv1.FlagType) *pbflagsv1.FlagValue {
	if msg == nil {
		return nil
	}

	var fv *pbflagsv1.FlagValue
	msg.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		wrapper := v.Message()
		valField := wrapper.Descriptor().Fields().ByNumber(1)
		if valField == nil {
			return true
		}
		wv := wrapper.Get(valField)

		switch fd.Number() {
		case 1: // BoolValue
			fv = &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_BoolValue{BoolValue: wv.Bool()}}
		case 2: // StringValue
			fv = &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringValue{StringValue: wv.String()}}
		case 3: // Int64Value
			fv = &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_Int64Value{Int64Value: wv.Int()}}
		case 4: // DoubleValue
			fv = &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_DoubleValue{DoubleValue: wv.Float()}}
		}
		return false
	})

	return fv
}

func kindToFlagType(k protoreflect.Kind) pbflagsv1.FlagType {
	switch k {
	case protoreflect.BoolKind:
		return pbflagsv1.FlagType_FLAG_TYPE_BOOL
	case protoreflect.StringKind:
		return pbflagsv1.FlagType_FLAG_TYPE_STRING
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return pbflagsv1.FlagType_FLAG_TYPE_INT64
	case protoreflect.DoubleKind:
		return pbflagsv1.FlagType_FLAG_TYPE_DOUBLE
	default:
		return pbflagsv1.FlagType_FLAG_TYPE_UNSPECIFIED
	}
}

func safeGetExtension(msg proto.Message, ext protoreflect.ExtensionType) (val interface{}) {
	defer func() {
		if r := recover(); r != nil {
			val = nil
		}
	}()
	return proto.GetExtension(msg, ext)
}
