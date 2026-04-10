package gogen

import (
	"math"
	"strings"
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
		{"max_retries", "MaxRetries"},
		{"score_threshold", "ScoreThreshold"},
		{"upcoming_meeting_window_days", "UpcomingMeetingWindowDays"},
		{"gemini_model", "GeminiModel"},
		{"single", "Single"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := toPascalCase(tt.in); got != tt.want {
			t.Errorf("toPascalCase(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestLowerFirst(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"Notifications", "notifications"},
		{"EmailEnabled", "emailEnabled"},
		{"", ""},
		{"a", "a"},
	}
	for _, tt := range tests {
		if got := lowerFirst(tt.in); got != tt.want {
			t.Errorf("lowerFirst(%q) = %q, want %q", tt.in, got, tt.want)
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

func TestParseFeatureFromUnknown(t *testing.T) {
	var featureBytes []byte
	featureBytes = protowire.AppendTag(featureBytes, 1, protowire.BytesType)
	featureBytes = protowire.AppendString(featureBytes, "test_feature")

	var b []byte
	b = protowire.AppendTag(b, 51000, protowire.BytesType)
	b = protowire.AppendBytes(b, featureBytes)

	info := parseFeatureFromUnknown(b)
	if info == nil {
		t.Fatal("parseFeatureFromUnknown returned nil")
	}
	if info.id != "test_feature" {
		t.Errorf("id = %q, want %q", info.id, "test_feature")
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
	if val != "int64(3)" {
		t.Errorf("val = %s, want %s", val, "int64(3)")
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
	if val != "float64(0.75)" {
		t.Errorf("val = %s, want %s", val, "float64(0.75)")
	}
}

func TestGoTypeInfoNilSafeGetters(t *testing.T) {
	kinds := []struct {
		kind        protoreflect.Kind
		wantType    string
		wantGetter  string
		wantOneofTy string
	}{
		{protoreflect.BoolKind, "bool", "GetBoolValue", "FlagValue_BoolValue"},
		{protoreflect.StringKind, "string", "GetStringValue", "FlagValue_StringValue"},
		{protoreflect.Int64Kind, "int64", "GetInt64Value", "FlagValue_Int64Value"},
		{protoreflect.DoubleKind, "float64", "GetDoubleValue", "FlagValue_DoubleValue"},
	}
	for _, tt := range kinds {
		goType, getter, oneofTy := goTypeInfo(tt.kind, false)
		if goType != tt.wantType {
			t.Errorf("goTypeInfo(%v) type = %q, want %q", tt.kind, goType, tt.wantType)
		}
		if getter != tt.wantGetter {
			t.Errorf("goTypeInfo(%v) getter = %q, want %q", tt.kind, getter, tt.wantGetter)
		}
		if oneofTy != tt.wantOneofTy {
			t.Errorf("goTypeInfo(%v) oneofType = %q, want %q", tt.kind, oneofTy, tt.wantOneofTy)
		}
	}
}

func TestParseBoolListMessage(t *testing.T) {
	// Multiple values: [true, false, true]
	var b []byte
	b = protowire.AppendTag(b, 1, protowire.VarintType)
	b = protowire.AppendVarint(b, 1)
	b = protowire.AppendTag(b, 1, protowire.VarintType)
	b = protowire.AppendVarint(b, 0)
	b = protowire.AppendTag(b, 1, protowire.VarintType)
	b = protowire.AppendVarint(b, 1)

	val, ok := parseBoolListMessage(b)
	if !ok {
		t.Fatal("parseBoolListMessage returned not ok")
	}
	want := "[]bool{true, false, true}"
	if val != want {
		t.Errorf("val = %q, want %q", val, want)
	}

	// Empty list
	val, ok = parseBoolListMessage(nil)
	if !ok {
		t.Fatal("parseBoolListMessage(nil) returned not ok")
	}
	if val != "[]bool{}" {
		t.Errorf("empty val = %q, want %q", val, "[]bool{}")
	}
}

func TestParseStringListMessage(t *testing.T) {
	// Two items
	var b []byte
	b = protowire.AppendTag(b, 1, protowire.BytesType)
	b = protowire.AppendString(b, "ops@example.com")
	b = protowire.AppendTag(b, 1, protowire.BytesType)
	b = protowire.AppendString(b, "alerts@example.com")

	val, ok := parseStringListMessage(b)
	if !ok {
		t.Fatal("parseStringListMessage returned not ok")
	}
	want := `[]string{"ops@example.com", "alerts@example.com"}`
	if val != want {
		t.Errorf("val = %s, want %s", val, want)
	}

	// Empty list
	val, ok = parseStringListMessage(nil)
	if !ok {
		t.Fatal("parseStringListMessage(nil) returned not ok")
	}
	if val != "[]string{}" {
		t.Errorf("empty val = %q, want %q", val, "[]string{}")
	}

	// Single item
	var single []byte
	single = protowire.AppendTag(single, 1, protowire.BytesType)
	single = protowire.AppendString(single, "solo@example.com")

	val, ok = parseStringListMessage(single)
	if !ok {
		t.Fatal("parseStringListMessage single returned not ok")
	}
	wantSingle := `[]string{"solo@example.com"}`
	if val != wantSingle {
		t.Errorf("single val = %s, want %s", val, wantSingle)
	}
}

func TestParseInt64ListMessage(t *testing.T) {
	// Multiple values: [1, 5, 30]
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
	want := "[]int64{int64(1), int64(5), int64(30)}"
	if val != want {
		t.Errorf("val = %q, want %q", val, want)
	}

	// Empty list
	val, ok = parseInt64ListMessage(nil)
	if !ok {
		t.Fatal("parseInt64ListMessage(nil) returned not ok")
	}
	if val != "[]int64{}" {
		t.Errorf("empty val = %q, want %q", val, "[]int64{}")
	}
}

func TestParseDoubleListMessage(t *testing.T) {
	// Multiple values: [0.75, 1.5]
	var b []byte
	b = protowire.AppendTag(b, 1, protowire.Fixed64Type)
	b = protowire.AppendFixed64(b, math.Float64bits(0.75))
	b = protowire.AppendTag(b, 1, protowire.Fixed64Type)
	b = protowire.AppendFixed64(b, math.Float64bits(1.5))

	val, ok := parseDoubleListMessage(b)
	if !ok {
		t.Fatal("parseDoubleListMessage returned not ok")
	}
	want := "[]float64{float64(0.75), float64(1.5)}"
	if val != want {
		t.Errorf("val = %q, want %q", val, want)
	}

	// Empty list
	val, ok = parseDoubleListMessage(nil)
	if !ok {
		t.Fatal("parseDoubleListMessage(nil) returned not ok")
	}
	if val != "[]float64{}" {
		t.Errorf("empty val = %q, want %q", val, "[]float64{}")
	}
}

func TestGoTypeInfoList(t *testing.T) {
	tests := []struct {
		kind        protoreflect.Kind
		wantType    string
		wantGetter  string
		wantOneofTy string
	}{
		{protoreflect.BoolKind, "[]bool", "GetBoolListValue", "FlagValue_BoolListValue"},
		{protoreflect.StringKind, "[]string", "GetStringListValue", "FlagValue_StringListValue"},
		{protoreflect.Int64Kind, "[]int64", "GetInt64ListValue", "FlagValue_Int64ListValue"},
		{protoreflect.DoubleKind, "[]float64", "GetDoubleListValue", "FlagValue_DoubleListValue"},
	}
	for _, tt := range tests {
		goType, getter, oneofTy := goTypeInfo(tt.kind, true)
		if goType != tt.wantType {
			t.Errorf("goTypeInfo(%v, true) type = %q, want %q", tt.kind, goType, tt.wantType)
		}
		if getter != tt.wantGetter {
			t.Errorf("goTypeInfo(%v, true) getter = %q, want %q", tt.kind, getter, tt.wantGetter)
		}
		if oneofTy != tt.wantOneofTy {
			t.Errorf("goTypeInfo(%v, true) oneofType = %q, want %q", tt.kind, oneofTy, tt.wantOneofTy)
		}
		if !strings.Contains(getter, "List") {
			t.Errorf("goTypeInfo(%v, true) getter %q should contain 'List'", tt.kind, getter)
		}
		if !strings.Contains(oneofTy, "List") {
			t.Errorf("goTypeInfo(%v, true) oneofType %q should contain 'List'", tt.kind, oneofTy)
		}
	}
}

func TestParseFlagDefaultListCases(t *testing.T) {
	tests := []struct {
		name      string
		fieldNum  protowire.Number // 5=bool_list, 6=string_list, 7=int64_list, 8=double_list
		innerData []byte
		fieldKind protoreflect.Kind
		wantVal   string
		wantOk    bool
	}{
		{
			name:     "bool_list",
			fieldNum: 5,
			innerData: func() []byte {
				var inner []byte
				inner = protowire.AppendTag(inner, 1, protowire.VarintType)
				inner = protowire.AppendVarint(inner, 1)
				inner = protowire.AppendTag(inner, 1, protowire.VarintType)
				inner = protowire.AppendVarint(inner, 0)
				return inner
			}(),
			fieldKind: protoreflect.BoolKind,
			wantVal:   "[]bool{true, false}",
			wantOk:    true,
		},
		{
			name:     "string_list",
			fieldNum: 6,
			innerData: func() []byte {
				var inner []byte
				inner = protowire.AppendTag(inner, 1, protowire.BytesType)
				inner = protowire.AppendString(inner, "alpha")
				inner = protowire.AppendTag(inner, 1, protowire.BytesType)
				inner = protowire.AppendString(inner, "beta")
				return inner
			}(),
			fieldKind: protoreflect.StringKind,
			wantVal:   `[]string{"alpha", "beta"}`,
			wantOk:    true,
		},
		{
			name:     "int64_list",
			fieldNum: 7,
			innerData: func() []byte {
				var inner []byte
				inner = protowire.AppendTag(inner, 1, protowire.VarintType)
				inner = protowire.AppendVarint(inner, 10)
				inner = protowire.AppendTag(inner, 1, protowire.VarintType)
				inner = protowire.AppendVarint(inner, 20)
				return inner
			}(),
			fieldKind: protoreflect.Int64Kind,
			wantVal:   "[]int64{int64(10), int64(20)}",
			wantOk:    true,
		},
		{
			name:     "double_list",
			fieldNum: 8,
			innerData: func() []byte {
				var inner []byte
				inner = protowire.AppendTag(inner, 1, protowire.Fixed64Type)
				inner = protowire.AppendFixed64(inner, math.Float64bits(2.5))
				inner = protowire.AppendTag(inner, 1, protowire.Fixed64Type)
				inner = protowire.AppendFixed64(inner, math.Float64bits(3.75))
				return inner
			}(),
			fieldKind: protoreflect.DoubleKind,
			wantVal:   "[]float64{float64(2.5), float64(3.75)}",
			wantOk:    true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Wrap innerData as FlagDefault field (field number = tt.fieldNum, bytes type).
			var flagDefault []byte
			flagDefault = protowire.AppendTag(flagDefault, tt.fieldNum, protowire.BytesType)
			flagDefault = protowire.AppendBytes(flagDefault, tt.innerData)

			val, ok := parseFlagDefault(flagDefault, tt.fieldKind)
			if ok != tt.wantOk {
				t.Fatalf("parseFlagDefault ok = %v, want %v", ok, tt.wantOk)
			}
			if val != tt.wantVal {
				t.Errorf("parseFlagDefault val = %q, want %q", val, tt.wantVal)
			}
		})
	}
}

func TestEmitReturnDefault(t *testing.T) {
	tests := []struct {
		name    string
		fl      flagInfo
		wantSub string // substring expected in the output
	}{
		{
			name: "scalar with default",
			fl: flagInfo{
				goName:     "EmailEnabled",
				goType:     "bool",
				hasDefault: true,
				isList:     false,
			},
			wantSub: "return EmailEnabledDefault",
		},
		{
			name: "scalar without default",
			fl: flagInfo{
				goName:     "MaxRetries",
				goType:     "int64",
				hasDefault: false,
				isList:     false,
			},
			wantSub: "var zero int64",
		},
		{
			name: "list with default",
			fl: flagInfo{
				goName:     "AllowedEmails",
				goType:     "[]string",
				hasDefault: true,
				isList:     true,
			},
			wantSub: "return AllowedEmailsDefault()",
		},
		{
			name: "list without default",
			fl: flagInfo{
				goName:     "Scores",
				goType:     "[]float64",
				hasDefault: false,
				isList:     true,
			},
			wantSub: "return nil",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf strings.Builder
			p := func(args ...interface{}) {
				for _, a := range args {
					buf.WriteString(a.(string))
				}
				buf.WriteString("\n")
			}
			emitReturnDefault(p, tt.fl)
			got := buf.String()
			if !strings.Contains(got, tt.wantSub) {
				t.Errorf("emitReturnDefault output = %q, want substring %q", got, tt.wantSub)
			}
		})
	}
}
