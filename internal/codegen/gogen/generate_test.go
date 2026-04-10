package gogen

import (
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
