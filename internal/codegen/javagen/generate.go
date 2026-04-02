// Package javagen generates Java flag client code from feature proto definitions.
package javagen

import (
	"fmt"
	"math"
	"strings"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/reflect/protoreflect"
)

type featureEntry struct {
	feat     *featureInfo
	flags    []flagInfo
	msg      *protogen.Message
	protoSrc *protogen.File
}

// Generate produces Java flag client code for all feature messages.
func Generate(plugin *protogen.Plugin, javaPackage string) error {
	var features []featureEntry
	for _, f := range plugin.Files {
		if !f.Generate {
			continue
		}
		for _, msg := range f.Messages {
			feat := extractFeatureOptions(msg)
			if feat == nil {
				continue
			}
			flags := extractFlags(msg)
			if len(flags) == 0 {
				continue
			}
			features = append(features, featureEntry{feat: feat, flags: flags, msg: msg, protoSrc: f})
		}
	}
	if len(features) == 0 {
		return nil
	}

	pkgDir := strings.ReplaceAll(javaPackage, ".", "/")

	for _, entry := range features {
		if err := generateInterface(plugin, entry.feat, entry.flags, javaPackage, pkgDir); err != nil {
			return fmt.Errorf("generating interface for %s: %w", entry.feat.id, err)
		}
		if err := generateImpl(plugin, entry.feat, entry.flags, javaPackage, pkgDir); err != nil {
			return fmt.Errorf("generating impl for %s: %w", entry.feat.id, err)
		}
	}

	return nil
}

func generateInterface(plugin *protogen.Plugin, feat *featureInfo, flags []flagInfo, javaPackage, pkgDir string) error {
	pascalFeat := toPascalCase(feat.id)
	className := pascalFeat + "Flags"
	outPath := pkgDir + "/" + className + ".java"

	g := plugin.NewGeneratedFile(outPath, "")
	p := g.P

	p("package ", javaPackage, ";")
	p()
	p("import org.spotlightgov.pbflags.Flag;")
	p("import org.spotlightgov.pbflags.FlagEvaluator;")
	p()
	p("/**")
	p(" * Generated type-safe flag accessors for the {@code ", feat.id, "} feature.")
	if feat.description != "" {
		p(" *")
		p(" * <p>", feat.description)
	}
	p(" */")
	p("public interface ", className, " {")
	p()

	p("  String FEATURE_ID = ", quote(feat.id), ";")
	p()

	for _, fl := range flags {
		p("  String ", fl.constName, "_ID = ", quote(fmt.Sprintf("%s/%d", feat.id, fl.fieldNumber)), ";")
	}
	p()

	for _, fl := range flags {
		if fl.hasDefault {
			p("  ", fl.javaType, " ", fl.constName, "_DEFAULT = ", fl.defaultVal, ";")
		}
	}
	p()

	for _, fl := range flags {
		if fl.description != "" {
			p("  /** ", fl.description, " */")
		}
		p("  Flag<", fl.javaBoxedType, "> ", fl.camelName, "();")
		p()
	}

	p("  /**")
	p("   * Creates an instance backed by a {@link FlagEvaluator}.")
	p("   */")
	p("  static ", className, " forEvaluator(FlagEvaluator evaluator) {")
	p("    return new ", className, "() {")
	p()
	for i, fl := range flags {
		p("      @Override")
		p("      public Flag<", fl.javaBoxedType, "> ", fl.camelName, "() {")
		p("        return evaluator.flag(", fl.constName, "_ID, ", fl.javaClassLiteral, ", ", javaDefaultExpr(fl), ");")
		p("      }")
		if i < len(flags)-1 {
			p()
		}
	}
	p("    };")
	p("  }")
	p("}")

	return nil
}

func generateImpl(plugin *protogen.Plugin, feat *featureInfo, flags []flagInfo, javaPackage, pkgDir string) error {
	pascalFeat := toPascalCase(feat.id)
	interfaceName := pascalFeat + "Flags"
	className := pascalFeat + "FlagsImpl"
	outPath := pkgDir + "/" + className + ".java"

	g := plugin.NewGeneratedFile(outPath, "")
	p := g.P

	p("package ", javaPackage, ";")
	p()
	p("import org.spotlightgov.pbflags.Flag;")
	p("import org.spotlightgov.pbflags.FlagEvaluator;")
	p()
	p("/** Generated implementation of {@link ", interfaceName, "}. */")
	p("public final class ", className, " implements ", interfaceName, " {")
	p()

	for _, fl := range flags {
		p("  private final Flag<", fl.javaBoxedType, "> ", fl.camelName, ";")
	}
	p()

	p("  public ", className, "(FlagEvaluator evaluator) {")
	for _, fl := range flags {
		p("    this.", fl.camelName, " =")
		p("        evaluator.flag(", fl.constName, "_ID, ", fl.javaClassLiteral, ", ", javaDefaultExpr(fl), ");")
	}
	p("  }")
	p()

	for _, fl := range flags {
		p("  @Override")
		p("  public Flag<", fl.javaBoxedType, "> ", fl.camelName, "() {")
		p("    return ", fl.camelName, ";")
		p("  }")
		p()
	}

	p("}")

	return nil
}

// --- Types ---

type featureInfo struct {
	id          string
	description string
	owner       string
}

type flagInfo struct {
	camelName        string
	constName        string
	fieldNumber      int32
	javaType         string
	javaBoxedType    string
	javaClassLiteral string
	defaultVal       string
	hasDefault       bool
	description      string
}

func javaDefaultExpr(fl flagInfo) string {
	if fl.hasDefault {
		return fl.constName + "_DEFAULT"
	}
	switch fl.javaType {
	case "boolean":
		return "false"
	case "String":
		return `""`
	case "long":
		return "0L"
	case "double":
		return "0.0"
	default:
		return "null"
	}
}

// --- Extraction (mirrors gogen) ---

func extractFeatureOptions(msg *protogen.Message) *featureInfo {
	opts := msg.Desc.Options()
	if opts == nil {
		return nil
	}
	protoMsg := opts.(interface{ ProtoReflect() protoreflect.Message })
	rm := protoMsg.ProtoReflect()

	var feat *featureInfo
	rm.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if fd.Number() == 51000 && fd.IsExtension() {
			m := v.Message()
			info := &featureInfo{}
			m.Range(func(innerFd protoreflect.FieldDescriptor, innerV protoreflect.Value) bool {
				switch innerFd.Name() {
				case "id":
					info.id = innerV.String()
				case "description":
					info.description = innerV.String()
				case "owner":
					info.owner = innerV.String()
				}
				return true
			})
			if info.id != "" {
				feat = info
			}
			return false
		}
		return true
	})
	if feat != nil {
		return feat
	}

	unk := rm.GetUnknown()
	if len(unk) == 0 {
		return nil
	}
	return parseFeatureFromUnknown(unk)
}

func parseFeatureFromUnknown(b []byte) *featureInfo {
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return nil
		}
		b = b[n:]
		if num == 51000 && typ == protowire.BytesType {
			data, n := protowire.ConsumeBytes(b)
			if n < 0 {
				return nil
			}
			return parseFeatureMessage(data)
		}
		n = skipField(b, typ)
		if n < 0 {
			return nil
		}
		b = b[n:]
	}
	return nil
}

func parseFeatureMessage(b []byte) *featureInfo {
	info := &featureInfo{}
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return nil
		}
		b = b[n:]
		if typ != protowire.BytesType {
			n = skipField(b, typ)
			if n < 0 {
				return nil
			}
			b = b[n:]
			continue
		}
		data, n := protowire.ConsumeBytes(b)
		if n < 0 {
			return nil
		}
		b = b[n:]
		switch num {
		case 1:
			info.id = string(data)
		case 2:
			info.description = string(data)
		case 3:
			info.owner = string(data)
		}
	}
	if info.id == "" {
		return nil
	}
	return info
}

func extractFlags(msg *protogen.Message) []flagInfo {
	var flags []flagInfo
	for _, field := range msg.Fields {
		fl := extractFlagFromField(field)
		if fl != nil {
			flags = append(flags, *fl)
		}
	}
	return flags
}

func extractFlagFromField(field *protogen.Field) *flagInfo {
	opts := field.Desc.Options()
	if opts == nil {
		return nil
	}
	protoMsg := opts.(interface{ ProtoReflect() protoreflect.Message })
	rm := protoMsg.ProtoReflect()

	var found bool
	var defaultVal string
	var hasDefault bool
	var description string

	rm.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if fd.Number() == 51001 && fd.IsExtension() {
			found = true
			m := v.Message()
			m.Range(func(innerFd protoreflect.FieldDescriptor, innerV protoreflect.Value) bool {
				switch innerFd.Name() {
				case "default":
					defaultVal, hasDefault = extractDefaultReflect(innerV.Message(), field.Desc.Kind())
				case "description":
					description = innerV.String()
				}
				return true
			})
			return false
		}
		return true
	})

	if !found {
		unk := rm.GetUnknown()
		if len(unk) == 0 {
			return nil
		}
		found, _, defaultVal, hasDefault, description = parseFlagFromUnknown(unk, field.Desc.Kind())
		if !found {
			return nil
		}
	}

	fieldName := string(field.Desc.Name())
	javaType, javaBoxedType, javaClassLiteral := javaTypeInfo(field.Desc.Kind())

	return &flagInfo{
		camelName:        toCamelCase(fieldName),
		constName:        toScreamingSnake(fieldName),
		fieldNumber:      int32(field.Desc.Number()),
		javaType:         javaType,
		javaBoxedType:    javaBoxedType,
		javaClassLiteral: javaClassLiteral,
		defaultVal:       defaultVal,
		hasDefault:       hasDefault,
		description:      description,
	}
}

func extractDefaultReflect(defMsg protoreflect.Message, fieldKind protoreflect.Kind) (string, bool) {
	var val string
	var ok bool
	defMsg.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		wrapperMsg := v.Message()
		wrapperMsg.Range(func(wfd protoreflect.FieldDescriptor, wv protoreflect.Value) bool {
			if wfd.Name() != "value" {
				return true
			}
			switch fd.Name() {
			case "bool_value":
				ok = true
				if wv.Bool() {
					val = "true"
				} else {
					val = "false"
				}
			case "string_value":
				ok = true
				val = fmt.Sprintf("%q", wv.String())
			case "int64_value":
				ok = true
				val = fmt.Sprintf("%dL", wv.Int())
			case "double_value":
				ok = true
				val = formatJavaDouble(wv.Float())
			}
			return false
		})
		return false
	})
	return val, ok
}

func parseFlagFromUnknown(b []byte, fieldKind protoreflect.Kind) (found, hasEntity bool, defaultVal string, hasDefault bool, description string) {
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return
		}
		b = b[n:]
		if num == 51001 && typ == protowire.BytesType {
			data, n := protowire.ConsumeBytes(b)
			if n < 0 {
				return
			}
			found = true
			hasEntity, defaultVal, hasDefault, description = parseFlagOptionsMessage(data, fieldKind)
			return
		}
		n = skipField(b, typ)
		if n < 0 {
			return
		}
		b = b[n:]
	}
	return
}

func parseFlagOptionsMessage(b []byte, fieldKind protoreflect.Kind) (hasEntity bool, defaultVal string, hasDefault bool, description string) {
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return
		}
		b = b[n:]
		switch num {
		case 1:
			if typ == protowire.BytesType {
				data, n := protowire.ConsumeBytes(b)
				if n < 0 {
					return
				}
				b = b[n:]
				description = string(data)
				continue
			}
		case 2:
			if typ == protowire.BytesType {
				data, n := protowire.ConsumeBytes(b)
				if n < 0 {
					return
				}
				b = b[n:]
				defaultVal, hasDefault = parseFlagDefault(data, fieldKind)
				continue
			}
		case 3:
			if typ == protowire.VarintType {
				v, n := protowire.ConsumeVarint(b)
				if n < 0 {
					return
				}
				b = b[n:]
				hasEntity = v == 2
				continue
			}
		}
		n = skipField(b, typ)
		if n < 0 {
			return
		}
		b = b[n:]
	}
	return
}

func parseFlagDefault(b []byte, fieldKind protoreflect.Kind) (string, bool) {
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return "", false
		}
		b = b[n:]
		if typ == protowire.BytesType {
			data, n := protowire.ConsumeBytes(b)
			if n < 0 {
				return "", false
			}
			b = b[n:]
			switch num {
			case 1:
				return parseBoolWrapper(data)
			case 2:
				return parseStringWrapper(data)
			case 3:
				return parseInt64Wrapper(data)
			case 4:
				return parseDoubleWrapper(data)
			}
			continue
		}
		n = skipField(b, typ)
		if n < 0 {
			return "", false
		}
		b = b[n:]
	}
	return "", false
}

func parseBoolWrapper(b []byte) (string, bool) {
	val := false
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return "", false
		}
		b = b[n:]
		if num == 1 && typ == protowire.VarintType {
			v, n := protowire.ConsumeVarint(b)
			if n < 0 {
				return "", false
			}
			b = b[n:]
			val = v != 0
			continue
		}
		n = skipField(b, typ)
		if n < 0 {
			return "", false
		}
		b = b[n:]
	}
	if val {
		return "true", true
	}
	return "false", true
}

func parseStringWrapper(b []byte) (string, bool) {
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return "", false
		}
		b = b[n:]
		if num == 1 && typ == protowire.BytesType {
			data, n := protowire.ConsumeBytes(b)
			if n < 0 {
				return "", false
			}
			return fmt.Sprintf("%q", string(data)), true
		}
		n = skipField(b, typ)
		if n < 0 {
			return "", false
		}
		b = b[n:]
	}
	return `""`, true
}

func parseInt64Wrapper(b []byte) (string, bool) {
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return "", false
		}
		b = b[n:]
		if num == 1 && typ == protowire.VarintType {
			v, n := protowire.ConsumeVarint(b)
			if n < 0 {
				return "", false
			}
			return fmt.Sprintf("%dL", int64(v)), true
		}
		n = skipField(b, typ)
		if n < 0 {
			return "", false
		}
		b = b[n:]
	}
	return "0L", true
}

func parseDoubleWrapper(b []byte) (string, bool) {
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return "", false
		}
		b = b[n:]
		if num == 1 && typ == protowire.Fixed64Type {
			v, n := protowire.ConsumeFixed64(b)
			if n < 0 {
				return "", false
			}
			return formatJavaDouble(math.Float64frombits(v)), true
		}
		n = skipField(b, typ)
		if n < 0 {
			return "", false
		}
		b = b[n:]
	}
	return "0.0", true
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

func javaTypeInfo(kind protoreflect.Kind) (javaType, javaBoxedType, classLiteral string) {
	switch kind {
	case protoreflect.BoolKind:
		return "boolean", "Boolean", "Boolean.class"
	case protoreflect.StringKind:
		return "String", "String", "String.class"
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return "long", "Long", "Long.class"
	case protoreflect.DoubleKind:
		return "double", "Double", "Double.class"
	default:
		return "Object", "Object", "Object.class"
	}
}

func formatJavaDouble(f float64) string {
	s := fmt.Sprintf("%v", f)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}

func toPascalCase(s string) string {
	parts := strings.Split(s, "_")
	for i, p := range parts {
		if len(p) > 0 {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, "")
}

func toCamelCase(s string) string {
	pascal := toPascalCase(s)
	if len(pascal) == 0 {
		return pascal
	}
	return strings.ToLower(pascal[:1]) + pascal[1:]
}

func toScreamingSnake(s string) string {
	return strings.ToUpper(s)
}

func quote(s string) string {
	return fmt.Sprintf("%q", s)
}
