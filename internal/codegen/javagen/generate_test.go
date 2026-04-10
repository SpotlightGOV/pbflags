package javagen

import (
	"math"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestToPascalCase(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"email_enabled", "EmailEnabled"},
		{"digest_frequency", "DigestFrequency"},
		{"notifications", "Notifications"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := toPascalCase(tt.in); got != tt.want {
			t.Errorf("toPascalCase(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestToCamelCase(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"email_enabled", "emailEnabled"},
		{"digest_frequency", "digestFrequency"},
		{"gemini_model", "geminiModel"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := toCamelCase(tt.in); got != tt.want {
			t.Errorf("toCamelCase(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestToScreamingSnake(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"email_enabled", "EMAIL_ENABLED"},
		{"digest_frequency", "DIGEST_FREQUENCY"},
		{"max_retries", "MAX_RETRIES"},
	}
	for _, tt := range tests {
		if got := toScreamingSnake(tt.in); got != tt.want {
			t.Errorf("toScreamingSnake(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestFormatJavaDouble(t *testing.T) {
	tests := []struct {
		in   float64
		want string
	}{
		{0.75, "0.75"},
		{1.0, "1.0"},
		{3.14, "3.14"},
	}
	for _, tt := range tests {
		if got := formatJavaDouble(tt.in); got != tt.want {
			t.Errorf("formatJavaDouble(%v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestParseBoolWrapper(t *testing.T) {
	var b []byte
	b = protowire.AppendTag(b, 1, protowire.VarintType)
	b = protowire.AppendVarint(b, 1)

	val, ok := parseBoolWrapper(b)
	if !ok {
		t.Fatal("parseBoolWrapper returned not ok")
	}
	if val != "true" {
		t.Errorf("val = %q, want %q", val, "true")
	}

	val, ok = parseBoolWrapper(nil)
	if !ok {
		t.Fatal("parseBoolWrapper(nil) returned not ok")
	}
	if val != "false" {
		t.Errorf("val = %q, want %q", val, "false")
	}
}

func TestParseStringWrapper(t *testing.T) {
	var b []byte
	b = protowire.AppendTag(b, 1, protowire.BytesType)
	b = protowire.AppendString(b, "daily")

	val, ok := parseStringWrapper(b)
	if !ok {
		t.Fatal("not ok")
	}
	if val != `"daily"` {
		t.Errorf("val = %s, want %q", val, `"daily"`)
	}
}

func TestParseInt64Wrapper(t *testing.T) {
	var b []byte
	b = protowire.AppendTag(b, 1, protowire.VarintType)
	b = protowire.AppendVarint(b, 3)

	val, ok := parseInt64Wrapper(b)
	if !ok {
		t.Fatal("not ok")
	}
	if val != "3L" {
		t.Errorf("val = %s, want %s", val, "3L")
	}
}

func TestParseDoubleWrapper(t *testing.T) {
	var b []byte
	b = protowire.AppendTag(b, 1, protowire.Fixed64Type)
	b = protowire.AppendFixed64(b, 0x3FE8000000000000)

	val, ok := parseDoubleWrapper(b)
	if !ok {
		t.Fatal("not ok")
	}
	if val != "0.75" {
		t.Errorf("val = %s, want %s", val, "0.75")
	}
}

func TestToUnderscoreCamelCase(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"notifications", "Notifications"},
		{"my_service", "MyService"},
		{"hello-world", "HelloWorld"},
		{"simple", "Simple"},
		{"a_b_c", "ABC"},
	}
	for _, tt := range tests {
		if got := toUnderscoreCamelCase(tt.in); got != tt.want {
			t.Errorf("toUnderscoreCamelCase(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestParseFeatureMessage(t *testing.T) {
	var b []byte
	b = protowire.AppendTag(b, 1, protowire.BytesType)
	b = protowire.AppendString(b, "notifications")
	b = protowire.AppendTag(b, 2, protowire.BytesType)
	b = protowire.AppendString(b, "Controls notification delivery behavior")
	b = protowire.AppendTag(b, 3, protowire.BytesType)
	b = protowire.AppendString(b, "platform-team")

	info := parseFeatureMessage(b)
	if info == nil {
		t.Fatal("parseFeatureMessage returned nil")
	}
	if info.id != "notifications" {
		t.Errorf("id = %q, want %q", info.id, "notifications")
	}
	if info.description != "Controls notification delivery behavior" {
		t.Errorf("description = %q", info.description)
	}
	if info.owner != "platform-team" {
		t.Errorf("owner = %q", info.owner)
	}
}

func TestParseBoolListMessage(t *testing.T) {
	// [true, false]
	var b []byte
	b = protowire.AppendTag(b, 1, protowire.VarintType)
	b = protowire.AppendVarint(b, 1)
	b = protowire.AppendTag(b, 1, protowire.VarintType)
	b = protowire.AppendVarint(b, 0)

	val, ok := parseBoolListMessage(b)
	if !ok {
		t.Fatal("parseBoolListMessage returned not ok")
	}
	want := "java.util.List.of(true, false)"
	if val != want {
		t.Errorf("val = %q, want %q", val, want)
	}

	// empty list
	val, ok = parseBoolListMessage(nil)
	if !ok {
		t.Fatal("parseBoolListMessage(nil) returned not ok")
	}
	if val != "java.util.List.of()" {
		t.Errorf("val = %q, want %q", val, "java.util.List.of()")
	}
}

func TestParseStringListMessage(t *testing.T) {
	// ["ops@example.com", "alerts@example.com"]
	var b []byte
	b = protowire.AppendTag(b, 1, protowire.BytesType)
	b = protowire.AppendString(b, "ops@example.com")
	b = protowire.AppendTag(b, 1, protowire.BytesType)
	b = protowire.AppendString(b, "alerts@example.com")

	val, ok := parseStringListMessage(b)
	if !ok {
		t.Fatal("parseStringListMessage returned not ok")
	}
	want := `java.util.List.of("ops@example.com", "alerts@example.com")`
	if val != want {
		t.Errorf("val = %q, want %q", val, want)
	}

	// empty list
	val, ok = parseStringListMessage(nil)
	if !ok {
		t.Fatal("parseStringListMessage(nil) returned not ok")
	}
	if val != "java.util.List.of()" {
		t.Errorf("val = %q, want %q", val, "java.util.List.of()")
	}
}

func TestParseInt64ListMessage(t *testing.T) {
	// [1, 5, 30]
	var b []byte
	b = protowire.AppendTag(b, 1, protowire.VarintType)
	b = protowire.AppendVarint(b, 1)
	b = protowire.AppendTag(b, 1, protowire.VarintType)
	b = protowire.AppendVarint(b, 5)
	b = protowire.AppendTag(b, 1, protowire.VarintType)
	b = protowire.AppendVarint(b, 30)

	val, ok := parseInt64ListMessage(b)
	if !ok {
		t.Fatal("parseInt64ListMessage returned not ok")
	}
	want := "java.util.List.of(1L, 5L, 30L)"
	if val != want {
		t.Errorf("val = %q, want %q", val, want)
	}

	// empty list
	val, ok = parseInt64ListMessage(nil)
	if !ok {
		t.Fatal("parseInt64ListMessage(nil) returned not ok")
	}
	if val != "java.util.List.of()" {
		t.Errorf("val = %q, want %q", val, "java.util.List.of()")
	}
}

func TestParseDoubleListMessage(t *testing.T) {
	// [0.75, 1.5]
	var b []byte
	b = protowire.AppendTag(b, 1, protowire.Fixed64Type)
	b = protowire.AppendFixed64(b, math.Float64bits(0.75))
	b = protowire.AppendTag(b, 1, protowire.Fixed64Type)
	b = protowire.AppendFixed64(b, math.Float64bits(1.5))

	val, ok := parseDoubleListMessage(b)
	if !ok {
		t.Fatal("parseDoubleListMessage returned not ok")
	}
	want := "java.util.List.of(" + formatJavaDouble(0.75) + ", " + formatJavaDouble(1.5) + ")"
	if val != want {
		t.Errorf("val = %q, want %q", val, want)
	}

	// empty list
	val, ok = parseDoubleListMessage(nil)
	if !ok {
		t.Fatal("parseDoubleListMessage(nil) returned not ok")
	}
	if val != "java.util.List.of()" {
		t.Errorf("val = %q, want %q", val, "java.util.List.of()")
	}
}

func TestJavaTypeInfoList(t *testing.T) {
	tests := []struct {
		kind             protoreflect.Kind
		wantJavaType     string
		wantBoxedType    string
		wantClassLiteral string
	}{
		{protoreflect.BoolKind, "java.util.List<Boolean>", "Boolean", "Boolean.class"},
		{protoreflect.StringKind, "java.util.List<String>", "String", "String.class"},
		{protoreflect.Int64Kind, "java.util.List<Long>", "Long", "Long.class"},
		{protoreflect.DoubleKind, "java.util.List<Double>", "Double", "Double.class"},
	}
	for _, tt := range tests {
		javaType, boxedType, classLit := javaTypeInfo(tt.kind, true)
		if javaType != tt.wantJavaType {
			t.Errorf("javaTypeInfo(%v, true) javaType = %q, want %q", tt.kind, javaType, tt.wantJavaType)
		}
		if boxedType != tt.wantBoxedType {
			t.Errorf("javaTypeInfo(%v, true) javaBoxedType = %q, want %q", tt.kind, boxedType, tt.wantBoxedType)
		}
		if classLit != tt.wantClassLiteral {
			t.Errorf("javaTypeInfo(%v, true) classLiteral = %q, want %q", tt.kind, classLit, tt.wantClassLiteral)
		}
	}
}

func TestJavaDefaultExprList(t *testing.T) {
	// With hasDefault=true, should return CONST_NAME_DEFAULT
	fl := flagInfo{
		constName:  "EMAIL_RECIPIENTS",
		hasDefault: true,
		isList:     true,
		javaType:   "java.util.List<String>",
	}
	got := javaDefaultExpr(fl)
	if got != "EMAIL_RECIPIENTS_DEFAULT" {
		t.Errorf("javaDefaultExpr (hasDefault=true) = %q, want %q", got, "EMAIL_RECIPIENTS_DEFAULT")
	}

	// With hasDefault=false and isList=true, should return java.util.List.of()
	fl2 := flagInfo{
		constName:  "ALERT_LEVELS",
		hasDefault: false,
		isList:     true,
		javaType:   "java.util.List<Long>",
	}
	got = javaDefaultExpr(fl2)
	if got != "java.util.List.of()" {
		t.Errorf("javaDefaultExpr (hasDefault=false, isList=true) = %q, want %q", got, "java.util.List.of()")
	}
}

func TestParseFlagDefaultListCases(t *testing.T) {
	// field 5 = bool list
	t.Run("bool_list", func(t *testing.T) {
		var inner []byte
		inner = protowire.AppendTag(inner, 1, protowire.VarintType)
		inner = protowire.AppendVarint(inner, 1)
		inner = protowire.AppendTag(inner, 1, protowire.VarintType)
		inner = protowire.AppendVarint(inner, 0)

		var b []byte
		b = protowire.AppendTag(b, 5, protowire.BytesType)
		b = protowire.AppendBytes(b, inner)

		val, ok := parseFlagDefault(b, protoreflect.BoolKind)
		if !ok {
			t.Fatal("parseFlagDefault returned not ok")
		}
		want := "java.util.List.of(true, false)"
		if val != want {
			t.Errorf("val = %q, want %q", val, want)
		}
	})

	// field 6 = string list
	t.Run("string_list", func(t *testing.T) {
		var inner []byte
		inner = protowire.AppendTag(inner, 1, protowire.BytesType)
		inner = protowire.AppendString(inner, "ops@example.com")

		var b []byte
		b = protowire.AppendTag(b, 6, protowire.BytesType)
		b = protowire.AppendBytes(b, inner)

		val, ok := parseFlagDefault(b, protoreflect.StringKind)
		if !ok {
			t.Fatal("parseFlagDefault returned not ok")
		}
		want := `java.util.List.of("ops@example.com")`
		if val != want {
			t.Errorf("val = %q, want %q", val, want)
		}
	})

	// field 7 = int64 list
	t.Run("int64_list", func(t *testing.T) {
		var inner []byte
		inner = protowire.AppendTag(inner, 1, protowire.VarintType)
		inner = protowire.AppendVarint(inner, 42)

		var b []byte
		b = protowire.AppendTag(b, 7, protowire.BytesType)
		b = protowire.AppendBytes(b, inner)

		val, ok := parseFlagDefault(b, protoreflect.Int64Kind)
		if !ok {
			t.Fatal("parseFlagDefault returned not ok")
		}
		want := "java.util.List.of(42L)"
		if val != want {
			t.Errorf("val = %q, want %q", val, want)
		}
	})

	// field 8 = double list
	t.Run("double_list", func(t *testing.T) {
		var inner []byte
		inner = protowire.AppendTag(inner, 1, protowire.Fixed64Type)
		inner = protowire.AppendFixed64(inner, math.Float64bits(3.14))

		var b []byte
		b = protowire.AppendTag(b, 8, protowire.BytesType)
		b = protowire.AppendBytes(b, inner)

		val, ok := parseFlagDefault(b, protoreflect.DoubleKind)
		if !ok {
			t.Fatal("parseFlagDefault returned not ok")
		}
		want := "java.util.List.of(" + formatJavaDouble(3.14) + ")"
		if val != want {
			t.Errorf("val = %q, want %q", val, want)
		}
	})
}
