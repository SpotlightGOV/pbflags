package configfile

import (
	"strings"
	"testing"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
)

// allFlagTypes covers every supported flag type for round-trip testing.
var allFlagTypes = map[string]pbflagsv1.FlagType{
	"bool_flag":        pbflagsv1.FlagType_FLAG_TYPE_BOOL,
	"string_flag":      pbflagsv1.FlagType_FLAG_TYPE_STRING,
	"int_flag":         pbflagsv1.FlagType_FLAG_TYPE_INT64,
	"double_flag":      pbflagsv1.FlagType_FLAG_TYPE_DOUBLE,
	"bool_list_flag":   pbflagsv1.FlagType_FLAG_TYPE_BOOL_LIST,
	"string_list_flag": pbflagsv1.FlagType_FLAG_TYPE_STRING_LIST,
	"int_list_flag":    pbflagsv1.FlagType_FLAG_TYPE_INT64_LIST,
	"double_list_flag": pbflagsv1.FlagType_FLAG_TYPE_DOUBLE_LIST,
	"cond_flag":        pbflagsv1.FlagType_FLAG_TYPE_BOOL,
	"launch_static":    pbflagsv1.FlagType_FLAG_TYPE_STRING,
	"launch_cond":      pbflagsv1.FlagType_FLAG_TYPE_INT64,
}

const roundTripInput = `feature: roundtrip_test
launches:
  my_launch:
    dimension: user_id
    ramp_percentage: 50
    description: "Test launch"
  no_ramp_launch:
    dimension: session_id
flags:
  bool_flag:
    value: true
  string_flag:
    value: "hello"
  int_flag:
    value: 42
  double_flag:
    value: 3.14
  bool_list_flag:
    value: [true, false, true]
  string_list_flag:
    value: ["a", "b", "c"]
  int_list_flag:
    value: [1, 2, 3]
  double_list_flag:
    value: [1.1, 2.2, 3.3]
  cond_flag:
    conditions:
      - when: "ctx.is_internal"
        value: true
      - otherwise: false
  launch_static:
    value: "base"
    launch:
      id: my_launch
      value: "override"
  launch_cond:
    conditions:
      - when: "ctx.plan == 3"
        value: 10
        launch:
          id: my_launch
          value: 20
      - otherwise: 5
`

func TestMarshalRoundTrip(t *testing.T) {
	// Parse original YAML.
	cfg1, _, err := Parse([]byte(roundTripInput), allFlagTypes)
	if err != nil {
		t.Fatalf("first Parse: %v", err)
	}

	// Marshal back to YAML.
	out, err := Marshal(cfg1)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	// Re-parse the marshaled output.
	cfg2, _, err := Parse(out, allFlagTypes)
	if err != nil {
		t.Fatalf("second Parse: %v\nmarshaled YAML:\n%s", err, out)
	}

	// Compare the two configs.
	assertConfigEqual(t, cfg1, cfg2)
}

func TestMarshalStaticValues(t *testing.T) {
	input := `feature: test
flags:
  bool_flag:
    value: false
  string_flag:
    value: "hello world"
`
	types := map[string]pbflagsv1.FlagType{
		"bool_flag":   pbflagsv1.FlagType_FLAG_TYPE_BOOL,
		"string_flag": pbflagsv1.FlagType_FLAG_TYPE_STRING,
	}
	cfg, _, err := Parse([]byte(input), types)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)

	// Canonical output should have sorted flag names.
	if !strings.Contains(got, "feature: test") {
		t.Error("missing feature")
	}
	boolIdx := strings.Index(got, "bool_flag")
	strIdx := strings.Index(got, "string_flag")
	if boolIdx < 0 || strIdx < 0 || boolIdx > strIdx {
		t.Errorf("flags should be sorted alphabetically; got:\n%s", got)
	}
}

func TestMarshalSortedLaunches(t *testing.T) {
	input := `feature: test
launches:
  zebra:
    dimension: user_id
  alpha:
    dimension: session_id
flags:
  bool_flag:
    value: true
`
	types := map[string]pbflagsv1.FlagType{
		"bool_flag": pbflagsv1.FlagType_FLAG_TYPE_BOOL,
	}
	cfg, _, err := Parse([]byte(input), types)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)

	alphaIdx := strings.Index(got, "alpha")
	zebraIdx := strings.Index(got, "zebra")
	if alphaIdx < 0 || zebraIdx < 0 || alphaIdx > zebraIdx {
		t.Errorf("launches should be sorted; got:\n%s", got)
	}
}

func TestMarshalNilRampPercentage(t *testing.T) {
	input := `feature: test
launches:
  no_ramp:
    dimension: user_id
flags:
  bool_flag:
    value: true
`
	types := map[string]pbflagsv1.FlagType{
		"bool_flag": pbflagsv1.FlagType_FLAG_TYPE_BOOL,
	}
	cfg, _, err := Parse([]byte(input), types)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Launches["no_ramp"].RampPercentage != nil {
		t.Fatal("expected nil ramp_percentage")
	}
	out, err := Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "ramp_percentage") {
		t.Errorf("nil ramp_percentage should be omitted; got:\n%s", out)
	}
}

func TestMarshalCrossFeatureLaunch(t *testing.T) {
	input := `dimension: session_id
ramp_percentage: 75
description: "Cross-feature experiment"
`
	entry, err := ParseCrossFeatureLaunch([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	out, err := MarshalCrossFeatureLaunch(entry)
	if err != nil {
		t.Fatal(err)
	}

	// Round-trip.
	entry2, err := ParseCrossFeatureLaunch(out)
	if err != nil {
		t.Fatalf("re-parse: %v\noutput:\n%s", err, out)
	}
	if entry.Dimension != entry2.Dimension {
		t.Errorf("dimension: %q != %q", entry.Dimension, entry2.Dimension)
	}
	if *entry.RampPercentage != *entry2.RampPercentage {
		t.Errorf("ramp: %d != %d", *entry.RampPercentage, *entry2.RampPercentage)
	}
	if entry.Description != entry2.Description {
		t.Errorf("description: %q != %q", entry.Description, entry2.Description)
	}
}

// assertConfigEqual deeply compares two Config structs.
func assertConfigEqual(t *testing.T, a, b *Config) {
	t.Helper()
	if a.Feature != b.Feature {
		t.Errorf("Feature: %q != %q", a.Feature, b.Feature)
	}

	// Launches.
	if len(a.Launches) != len(b.Launches) {
		t.Fatalf("Launches count: %d != %d", len(a.Launches), len(b.Launches))
	}
	for id, la := range a.Launches {
		lb, ok := b.Launches[id]
		if !ok {
			t.Errorf("launch %q missing after round-trip", id)
			continue
		}
		if la.Dimension != lb.Dimension {
			t.Errorf("launch %q dimension: %q != %q", id, la.Dimension, lb.Dimension)
		}
		if (la.RampPercentage == nil) != (lb.RampPercentage == nil) {
			t.Errorf("launch %q ramp nil mismatch", id)
		} else if la.RampPercentage != nil && *la.RampPercentage != *lb.RampPercentage {
			t.Errorf("launch %q ramp: %d != %d", id, *la.RampPercentage, *lb.RampPercentage)
		}
		if la.Description != lb.Description {
			t.Errorf("launch %q description: %q != %q", id, la.Description, lb.Description)
		}
	}

	// Flags.
	if len(a.Flags) != len(b.Flags) {
		t.Fatalf("Flags count: %d != %d", len(a.Flags), len(b.Flags))
	}
	for name, fa := range a.Flags {
		fb, ok := b.Flags[name]
		if !ok {
			t.Errorf("flag %q missing after round-trip", name)
			continue
		}
		// Static value.
		assertFlagValueEqual(t, name, "value", fa.Value, fb.Value)

		// Static launch override.
		if (fa.Launch == nil) != (fb.Launch == nil) {
			t.Errorf("flag %q launch override nil mismatch", name)
		} else if fa.Launch != nil {
			if fa.Launch.ID != fb.Launch.ID {
				t.Errorf("flag %q launch.id: %q != %q", name, fa.Launch.ID, fb.Launch.ID)
			}
			assertFlagValueEqual(t, name, "launch.value", fa.Launch.Value, fb.Launch.Value)
		}

		// Conditions.
		if len(fa.Conditions) != len(fb.Conditions) {
			t.Errorf("flag %q conditions count: %d != %d", name, len(fa.Conditions), len(fb.Conditions))
			continue
		}
		for i, ca := range fa.Conditions {
			cb := fb.Conditions[i]
			if ca.When != cb.When {
				t.Errorf("flag %q cond[%d] when: %q != %q", name, i, ca.When, cb.When)
			}
			assertFlagValueEqual(t, name, "cond.value", ca.Value, cb.Value)
			if (ca.Launch == nil) != (cb.Launch == nil) {
				t.Errorf("flag %q cond[%d] launch nil mismatch", name, i)
			} else if ca.Launch != nil {
				if ca.Launch.ID != cb.Launch.ID {
					t.Errorf("flag %q cond[%d] launch.id: %q != %q", name, i, ca.Launch.ID, cb.Launch.ID)
				}
				assertFlagValueEqual(t, name, "cond.launch.value", ca.Launch.Value, cb.Launch.Value)
			}
		}
	}
}

func assertFlagValueEqual(t *testing.T, flag, field string, a, b *pbflagsv1.FlagValue) {
	t.Helper()
	if (a == nil) != (b == nil) {
		t.Errorf("flag %q %s: nil mismatch", flag, field)
		return
	}
	if a == nil {
		return
	}
	if a.String() != b.String() {
		t.Errorf("flag %q %s: %v != %v", flag, field, a, b)
	}
}
