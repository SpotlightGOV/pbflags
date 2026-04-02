package javagen

import (
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
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
