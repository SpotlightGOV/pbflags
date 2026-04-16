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

func TestParseConditionComments(t *testing.T) {
	t.Run("head comment", func(t *testing.T) {
		yaml := `
feature: notifications
flags:
  email_enabled:
    conditions:
      # Dogfood for internal users
      - when: "ctx.is_internal"
        value: true
      - otherwise: false
`
		cfg, _, err := Parse([]byte(yaml), flagTypes)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		conds := cfg.Flags["email_enabled"].Conditions
		if len(conds) != 2 {
			t.Fatalf("expected 2 conditions, got %d", len(conds))
		}
		if conds[0].Comment != "Dogfood for internal users" {
			t.Errorf("Comment = %q, want %q", conds[0].Comment, "Dogfood for internal users")
		}
		if conds[1].Comment != "" {
			t.Errorf("otherwise Comment = %q, want empty", conds[1].Comment)
		}
	})

	t.Run("inline comment", func(t *testing.T) {
		yaml := `
feature: notifications
flags:
  email_enabled:
    conditions:
      - when: "ctx.is_internal" # internal dogfood
        value: true
      - otherwise: false
`
		cfg, _, err := Parse([]byte(yaml), flagTypes)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		conds := cfg.Flags["email_enabled"].Conditions
		if conds[0].Comment != "internal dogfood" {
			t.Errorf("Comment = %q, want %q", conds[0].Comment, "internal dogfood")
		}
	})

	t.Run("no comment", func(t *testing.T) {
		yaml := `
feature: notifications
flags:
  email_enabled:
    conditions:
      - when: "ctx.is_internal"
        value: true
      - otherwise: false
`
		cfg, _, err := Parse([]byte(yaml), flagTypes)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		for _, c := range cfg.Flags["email_enabled"].Conditions {
			if c.Comment != "" {
				t.Errorf("expected empty comment, got %q", c.Comment)
			}
		}
	})
}

func TestParseLaunchDefinition(t *testing.T) {
	t.Run("valid launch definition", func(t *testing.T) {
		yaml := `
feature: notifications
launches:
  rollout_1:
    dimension: user_id
    ramp_percentage: 50
    description: "Pro email rollout"
flags:
  email_enabled:
    value: true
`
		cfg, _, err := Parse([]byte(yaml), flagTypes)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		launch, ok := cfg.Launches["rollout_1"]
		if !ok {
			t.Fatal("expected launch rollout_1")
		}
		if launch.Dimension != "user_id" {
			t.Errorf("Dimension = %q, want user_id", launch.Dimension)
		}
		if launch.RampPercentage == nil || *launch.RampPercentage != 50 {
			t.Errorf("RampPercentage = %v, want 50", launch.RampPercentage)
		}
		if launch.Description != "Pro email rollout" {
			t.Errorf("Description = %q, want 'Pro email rollout'", launch.Description)
		}
	})

	t.Run("omitted ramp defaults to 0", func(t *testing.T) {
		yaml := `
feature: notifications
launches:
  rollout_1:
    dimension: user_id
flags:
  email_enabled:
    value: true
`
		cfg, _, err := Parse([]byte(yaml), flagTypes)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if cfg.Launches["rollout_1"].RampPercentage != nil {
			t.Errorf("RampPercentage = %v, want nil", cfg.Launches["rollout_1"].RampPercentage)
		}
	})

	t.Run("ramp out of range", func(t *testing.T) {
		yaml := `
feature: notifications
launches:
  rollout_1:
    dimension: user_id
    ramp_percentage: 150
flags:
  email_enabled:
    value: true
`
		_, _, err := Parse([]byte(yaml), flagTypes)
		if err == nil {
			t.Fatal("expected error for ramp_percentage > 100")
		}
		if !strings.Contains(err.Error(), "ramp_percentage must be 0-100") {
			t.Errorf("error = %q, want substring about ramp_percentage range", err.Error())
		}
	})

	t.Run("missing dimension", func(t *testing.T) {
		yaml := `
feature: notifications
launches:
  rollout_1:
    ramp_percentage: 10
flags:
  email_enabled:
    value: true
`
		_, _, err := Parse([]byte(yaml), flagTypes)
		if err == nil {
			t.Fatal("expected error for missing dimension")
		}
		if !strings.Contains(err.Error(), "missing required field: dimension") {
			t.Errorf("error = %q, want dimension error", err.Error())
		}
	})

	t.Run("ramp_steps parses and validates", func(t *testing.T) {
		yaml := `
feature: notifications
launches:
  rollout_1:
    dimension: user_id
    ramp_steps: [0, 1, 5, 10, 25, 100]
flags:
  email_enabled:
    value: true
`
		cfg, _, err := Parse([]byte(yaml), flagTypes)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		got := cfg.Launches["rollout_1"].RampSteps
		want := []int{0, 1, 5, 10, 25, 100}
		if len(got) != len(want) {
			t.Fatalf("RampSteps len = %d, want %d (got %v)", len(got), len(want), got)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("RampSteps[%d] = %d, want %d", i, got[i], want[i])
			}
		}
	})

	t.Run("ramp_steps rejects out-of-range value", func(t *testing.T) {
		yaml := `
feature: notifications
launches:
  rollout_1:
    dimension: user_id
    ramp_steps: [0, 50, 150]
flags:
  email_enabled:
    value: true
`
		_, _, err := Parse([]byte(yaml), flagTypes)
		if err == nil || !strings.Contains(err.Error(), "must be 0-100") {
			t.Fatalf("expected 0-100 range error, got %v", err)
		}
	})

	t.Run("ramp_steps rejects non-ascending sequence", func(t *testing.T) {
		yaml := `
feature: notifications
launches:
  rollout_1:
    dimension: user_id
    ramp_steps: [10, 5]
flags:
  email_enabled:
    value: true
`
		_, _, err := Parse([]byte(yaml), flagTypes)
		if err == nil || !strings.Contains(err.Error(), "strictly ascending") {
			t.Fatalf("expected ascending error, got %v", err)
		}
	})

	t.Run("ramp_steps rejects duplicates", func(t *testing.T) {
		yaml := `
feature: notifications
launches:
  rollout_1:
    dimension: user_id
    ramp_steps: [0, 25, 25, 100]
flags:
  email_enabled:
    value: true
`
		_, _, err := Parse([]byte(yaml), flagTypes)
		if err == nil || !strings.Contains(err.Error(), "strictly ascending") {
			t.Fatalf("expected duplicate error (via ascending check), got %v", err)
		}
	})
}

func TestParseConditionLaunchOverride(t *testing.T) {
	t.Run("condition with launch override", func(t *testing.T) {
		yaml := `
feature: notifications
launches:
  pro_rollout:
    dimension: user_id
    ramp_percentage: 25
flags:
  digest_frequency:
    conditions:
      - when: "ctx.plan == PlanLevel.PRO"
        value: "daily"
        launch:
          id: pro_rollout
          value: "hourly"
      - otherwise: "weekly"
`
		cfg, _, err := Parse([]byte(yaml), flagTypes)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		conds := cfg.Flags["digest_frequency"].Conditions
		if len(conds) != 2 {
			t.Fatalf("expected 2 conditions, got %d", len(conds))
		}
		if conds[0].Launch == nil {
			t.Fatal("expected launch override on condition 0")
		}
		if conds[0].Launch.ID != "pro_rollout" {
			t.Errorf("Launch.ID = %q, want pro_rollout", conds[0].Launch.ID)
		}
		if conds[0].Launch.Value.GetStringValue() != "hourly" {
			t.Errorf("Launch.Value = %q, want hourly", conds[0].Launch.Value.GetStringValue())
		}
		// otherwise should have no launch
		if conds[1].Launch != nil {
			t.Errorf("expected no launch on otherwise condition")
		}
	})

	t.Run("launch override missing id", func(t *testing.T) {
		yaml := `
feature: notifications
flags:
  email_enabled:
    conditions:
      - when: "ctx.is_internal"
        value: true
        launch:
          value: false
      - otherwise: false
`
		_, _, err := Parse([]byte(yaml), flagTypes)
		if err == nil {
			t.Fatal("expected error for launch override missing id")
		}
		if !strings.Contains(err.Error(), "launch override missing id") {
			t.Errorf("error = %q, want launch override missing id", err.Error())
		}
	})
}

func TestParseStaticValueLaunchOverride(t *testing.T) {
	t.Run("static value with launch override", func(t *testing.T) {
		yaml := `
feature: notifications
launches:
  email_rollout:
    dimension: user_id
    ramp_percentage: 10
flags:
  email_enabled:
    value: false
    launch:
      id: email_rollout
      value: true
`
		cfg, _, err := Parse([]byte(yaml), flagTypes)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		entry := cfg.Flags["email_enabled"]
		if entry.Value == nil || entry.Value.GetBoolValue() != false {
			t.Errorf("static value should be false")
		}
		if entry.Launch == nil {
			t.Fatal("expected launch override on static value")
		}
		if entry.Launch.ID != "email_rollout" {
			t.Errorf("Launch.ID = %q, want email_rollout", entry.Launch.ID)
		}
		if entry.Launch.Value.GetBoolValue() != true {
			t.Errorf("Launch.Value = %v, want true", entry.Launch.Value.GetBoolValue())
		}
	})

	t.Run("static value launch override type mismatch", func(t *testing.T) {
		yaml := `
feature: notifications
flags:
  email_enabled:
    value: false
    launch:
      id: some_launch
      value: "not_a_bool"
`
		_, _, err := Parse([]byte(yaml), flagTypes)
		if err == nil {
			t.Fatal("expected error for type mismatch in launch override")
		}
		if !strings.Contains(err.Error(), "expected bool") {
			t.Errorf("error = %q, want type mismatch error", err.Error())
		}
	})
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
