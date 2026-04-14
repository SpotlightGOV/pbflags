package configfile

import (
	"strings"
	"testing"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
)

// flagTypes mirrors the notifications example feature.
var flagTypes = map[string]pbflagsv1.FlagType{
	"email_enabled":       pbflagsv1.FlagType_FLAG_TYPE_BOOL,
	"digest_frequency":    pbflagsv1.FlagType_FLAG_TYPE_STRING,
	"max_retries":         pbflagsv1.FlagType_FLAG_TYPE_INT64,
	"score_threshold":     pbflagsv1.FlagType_FLAG_TYPE_DOUBLE,
	"notification_emails": pbflagsv1.FlagType_FLAG_TYPE_STRING_LIST,
	"retry_delays":        pbflagsv1.FlagType_FLAG_TYPE_INT64_LIST,
}

func TestParseStaticValues(t *testing.T) {
	yaml := `
feature: notifications
flags:
  email_enabled:
    value: true
  digest_frequency:
    value: "weekly"
  max_retries:
    value: 5
  score_threshold:
    value: 0.75
  notification_emails:
    value: ["ops@example.com", "admin@example.com"]
  retry_delays:
    value: [1, 2, 4]
`
	cfg, warnings, err := Parse([]byte(yaml), flagTypes)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(warnings) > 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}
	if cfg.Feature != "notifications" {
		t.Errorf("feature = %q, want notifications", cfg.Feature)
	}
	if len(cfg.Flags) != 6 {
		t.Errorf("got %d flags, want 6", len(cfg.Flags))
	}

	// Spot-check types.
	if !cfg.Flags["email_enabled"].Value.GetBoolValue() {
		t.Error("email_enabled should be true")
	}
	if cfg.Flags["digest_frequency"].Value.GetStringValue() != "weekly" {
		t.Error("digest_frequency should be weekly")
	}
	if cfg.Flags["max_retries"].Value.GetInt64Value() != 5 {
		t.Error("max_retries should be 5")
	}
	if cfg.Flags["score_threshold"].Value.GetDoubleValue() != 0.75 {
		t.Error("score_threshold should be 0.75")
	}
	emails := cfg.Flags["notification_emails"].Value.GetStringListValue().GetValues()
	if len(emails) != 2 || emails[0] != "ops@example.com" {
		t.Errorf("notification_emails = %v", emails)
	}
	delays := cfg.Flags["retry_delays"].Value.GetInt64ListValue().GetValues()
	if len(delays) != 3 || delays[0] != 1 || delays[2] != 4 {
		t.Errorf("retry_delays = %v", delays)
	}
}

func TestParseConditions(t *testing.T) {
	yaml := `
feature: notifications
flags:
  email_enabled:
    value: true
  digest_frequency:
    conditions:
      - when: 'ctx.plan == PlanLevel.ENTERPRISE'
        value: "daily"
      - otherwise: "weekly"
  max_retries:
    conditions:
      - when: 'ctx.plan == PlanLevel.ENTERPRISE'
        value: 10
      - when: 'ctx.plan == PlanLevel.PRO'
        value: 5
      - otherwise: 3
  score_threshold:
    value: 0.5
  notification_emails:
    conditions:
      - when: 'ctx.user_id == "user-99"'
        value: ["special@example.com"]
      - otherwise: ["ops@example.com"]
  retry_delays:
    value: [1, 2, 4]
`
	cfg, warnings, err := Parse([]byte(yaml), flagTypes)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(warnings) > 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}

	df := cfg.Flags["digest_frequency"]
	if df.Value != nil {
		t.Error("digest_frequency should not have a static value")
	}
	if len(df.Conditions) != 2 {
		t.Fatalf("digest_frequency: got %d conditions, want 2", len(df.Conditions))
	}
	if df.Conditions[0].When != "ctx.plan == PlanLevel.ENTERPRISE" {
		t.Errorf("condition 0 when = %q", df.Conditions[0].When)
	}
	if df.Conditions[0].Value.GetStringValue() != "daily" {
		t.Errorf("condition 0 value = %v", df.Conditions[0].Value)
	}
	if df.Conditions[1].When != "" {
		t.Error("condition 1 should be otherwise (empty when)")
	}
	if df.Conditions[1].Value.GetStringValue() != "weekly" {
		t.Errorf("otherwise value = %v", df.Conditions[1].Value)
	}

	mr := cfg.Flags["max_retries"]
	if len(mr.Conditions) != 3 {
		t.Fatalf("max_retries: got %d conditions, want 3", len(mr.Conditions))
	}
	if mr.Conditions[2].Value.GetInt64Value() != 3 {
		t.Errorf("max_retries otherwise = %v", mr.Conditions[2].Value)
	}
}

func TestParseMissingOtherwise(t *testing.T) {
	yaml := `
feature: notifications
flags:
  email_enabled:
    value: true
  digest_frequency:
    conditions:
      - when: 'ctx.plan == PlanLevel.ENTERPRISE'
        value: "daily"
  max_retries:
    value: 3
  score_threshold:
    value: 0.5
  notification_emails:
    value: ["ops@example.com"]
  retry_delays:
    value: [1, 2]
`
	cfg, warnings, err := Parse([]byte(yaml), flagTypes)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected config")
	}
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "digest_frequency") && strings.Contains(w, "otherwise") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected otherwise warning, got %v", warnings)
	}
}

func TestParseMissingFlagWarns(t *testing.T) {
	// A config that omits some proto flags should succeed with a warning,
	// not fail with an error. Only flags with overrides need to be present.
	yaml := `
feature: notifications
flags:
  email_enabled:
    value: true
  digest_frequency:
    value: "weekly"
`
	cfg, warnings, err := Parse([]byte(yaml), flagTypes)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected config")
	}
	if len(cfg.Flags) != 2 {
		t.Errorf("expected 2 flags, got %d", len(cfg.Flags))
	}
	// Should have warnings for the missing flags.
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "not in config") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected warning about missing flags, got %v", warnings)
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			"missing feature",
			`flags:
  email_enabled:
    value: true`,
			"missing required field: feature",
		},
		{
			"missing flags",
			`feature: notifications`,
			"missing required field: flags",
		},
		{
			"unknown flag",
			`feature: notifications
flags:
  email_enabled:
    value: true
  digest_frequency:
    value: "weekly"
  max_retries:
    value: 3
  score_threshold:
    value: 0.5
  notification_emails:
    value: ["ops@example.com"]
  retry_delays:
    value: [1]
  bogus:
    value: true`,
			`not defined in proto`,
		},
		{
			"both value and conditions",
			`feature: notifications
flags:
  email_enabled:
    value: true
    conditions:
      - when: 'ctx.is_internal'
        value: false
  digest_frequency:
    value: "weekly"
  max_retries:
    value: 3
  score_threshold:
    value: 0.5
  notification_emails:
    value: ["ops@example.com"]
  retry_delays:
    value: [1]`,
			"cannot have both value and conditions",
		},
		{
			"neither value nor conditions",
			`feature: notifications
flags:
  email_enabled: {}
  digest_frequency:
    value: "weekly"
  max_retries:
    value: 3
  score_threshold:
    value: 0.5
  notification_emails:
    value: ["ops@example.com"]
  retry_delays:
    value: [1]`,
			"must have either value or conditions",
		},
		{
			"wrong type bool",
			`feature: notifications
flags:
  email_enabled:
    value: "not a bool"
  digest_frequency:
    value: "weekly"
  max_retries:
    value: 3
  score_threshold:
    value: 0.5
  notification_emails:
    value: ["ops@example.com"]
  retry_delays:
    value: [1]`,
			"expected bool",
		},
		{
			"wrong type int64",
			`feature: notifications
flags:
  email_enabled:
    value: true
  digest_frequency:
    value: "weekly"
  max_retries:
    value: "not a number"
  score_threshold:
    value: 0.5
  notification_emails:
    value: ["ops@example.com"]
  retry_delays:
    value: [1]`,
			"expected integer",
		},
		{
			"fractional int64",
			`feature: notifications
flags:
  email_enabled:
    value: true
  digest_frequency:
    value: "weekly"
  max_retries:
    value: 3.5
  score_threshold:
    value: 0.5
  notification_emails:
    value: ["ops@example.com"]
  retry_delays:
    value: [1]`,
			"expected integer",
		},
		{
			"otherwise not last",
			`feature: notifications
flags:
  email_enabled:
    value: true
  digest_frequency:
    conditions:
      - otherwise: "weekly"
      - when: 'ctx.plan == PlanLevel.ENTERPRISE'
        value: "daily"
  max_retries:
    value: 3
  score_threshold:
    value: 0.5
  notification_emails:
    value: ["ops@example.com"]
  retry_delays:
    value: [1]`,
			"otherwise must be the last condition",
		},
		{
			"when and otherwise",
			`feature: notifications
flags:
  email_enabled:
    value: true
  digest_frequency:
    conditions:
      - when: 'ctx.plan == PlanLevel.ENTERPRISE'
        otherwise: "daily"
  max_retries:
    value: 3
  score_threshold:
    value: 0.5
  notification_emails:
    value: ["ops@example.com"]
  retry_delays:
    value: [1]`,
			"has both when and otherwise",
		},
		{
			"when missing value",
			`feature: notifications
flags:
  email_enabled:
    value: true
  digest_frequency:
    conditions:
      - when: 'ctx.plan == PlanLevel.ENTERPRISE'
  max_retries:
    value: 3
  score_threshold:
    value: 0.5
  notification_emails:
    value: ["ops@example.com"]
  retry_delays:
    value: [1]`,
			"missing value",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := Parse([]byte(tt.yaml), flagTypes)
			if err == nil {
				t.Fatalf("expected error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want substring %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestParseFalseValue(t *testing.T) {
	// Ensure value: false is not confused with "value absent".
	yaml := `
feature: notifications
flags:
  email_enabled:
    value: false
  digest_frequency:
    value: "weekly"
  max_retries:
    value: 0
  score_threshold:
    value: 0.0
  notification_emails:
    value: []
  retry_delays:
    value: []
`
	cfg, _, err := Parse([]byte(yaml), flagTypes)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Flags["email_enabled"].Value.GetBoolValue() != false {
		t.Error("email_enabled should be false")
	}
	if cfg.Flags["max_retries"].Value.GetInt64Value() != 0 {
		t.Error("max_retries should be 0")
	}
}

func TestToInt64EdgeCases(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want int64
	}{
		{"int", int(42), 42},
		{"int64", int64(9000000000), 9000000000},
		{"uint64", uint64(123), 123},
		{"float64 integer", float64(7), 7},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := toInt64(tt.in)
			if err != nil {
				t.Fatalf("toInt64(%T(%v)): %v", tt.in, tt.in, err)
			}
			if got != tt.want {
				t.Errorf("got %d, want %d", got, tt.want)
			}
		})
	}
}

func TestToFloat64EdgeCases(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want float64
	}{
		{"float64", float64(3.14), 3.14},
		{"int", int(7), 7.0},
		{"int64", int64(9000000000), 9000000000.0},
		{"uint64", uint64(42), 42.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := toFloat64(tt.in)
			if err != nil {
				t.Fatalf("toFloat64(%T(%v)): %v", tt.in, tt.in, err)
			}
			if got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseIntAsDouble(t *testing.T) {
	// YAML integer should be accepted for double flags.
	yaml := `
feature: notifications
flags:
  email_enabled:
    value: true
  digest_frequency:
    value: "weekly"
  max_retries:
    value: 3
  score_threshold:
    value: 1
  notification_emails:
    value: ["ops@example.com"]
  retry_delays:
    value: [1]
`
	cfg, _, err := Parse([]byte(yaml), flagTypes)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Flags["score_threshold"].Value.GetDoubleValue() != 1.0 {
		t.Errorf("score_threshold = %v, want 1.0", cfg.Flags["score_threshold"].Value.GetDoubleValue())
	}
}
