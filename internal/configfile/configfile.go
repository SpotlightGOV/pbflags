// Package configfile parses and validates YAML flag configuration files.
// Each file defines flag behavior for a single feature: static values
// and/or condition chains with CEL expressions.
package configfile

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
	"gopkg.in/yaml.v3"
)

// Config is a parsed and validated flag configuration for a single feature.
type Config struct {
	Feature  string
	Flags    map[string]FlagEntry
	Launches map[string]LaunchEntry // keyed by launch ID
}

// LaunchEntry defines a launch (gradual rollout) at the feature level.
// The launch-to-flag binding is expressed inline on individual conditions
// via LaunchOverride on Condition, not on the launch definition.
type LaunchEntry struct {
	Dimension      string // hashable dimension to hash on (must be UNIFORM)
	RampPercentage *int   // ramp percentage (0-100); nil = not set in config (CLI/UI controls persist)
	Description    string // optional human-readable description
}

// FlagEntry is a single flag's configuration — either a static value or
// a condition chain, never both.
type FlagEntry struct {
	Value      *pbflagsv1.FlagValue // non-nil for static values
	Conditions []Condition          // non-nil for condition chains
	Launch     *FlagLaunchOverride  // optional launch override on static value
}

// Condition is one entry in a condition chain.
type Condition struct {
	When    string               // CEL expression; empty means "otherwise" (default)
	Value   *pbflagsv1.FlagValue // the value to return when the condition matches
	Comment string               // annotation from YAML comment (head or inline)
	Launch  *LaunchOverride      // optional launch override (at most one per condition)
}

// LaunchOverride is a per-condition value override under a launch.
type LaunchOverride struct {
	ID    string               // launch ID (must resolve to a defined launch)
	Value *pbflagsv1.FlagValue // value to return when the entity is in the launch ramp
}

// FlagLaunchOverride is a launch override on a static flag value.
type FlagLaunchOverride struct {
	ID    string               // launch ID
	Value *pbflagsv1.FlagValue // value for entities in the ramp
}

// YAML unmarshaling types.
type rawConfig struct {
	Feature  string                    `yaml:"feature"`
	Flags    map[string]rawFlagEntry   `yaml:"flags"`
	Launches map[string]rawLaunchEntry `yaml:"launches"`
}

type rawLaunchEntry struct {
	Dimension      string `yaml:"dimension"`
	RampPercentage *int   `yaml:"ramp_percentage"`
	Description    string `yaml:"description"`
}

type rawFlagEntry struct {
	Value      any                `yaml:"value"`
	Conditions []rawCondition     `yaml:"conditions"`
	Launch     *rawLaunchOverride `yaml:"launch"`
	hasValue   bool
}

// UnmarshalYAML implements custom unmarshaling to distinguish "value absent"
// from "value: null" and "value: false".
func (r *rawFlagEntry) UnmarshalYAML(node *yaml.Node) error {
	// Decode into a temporary map to detect which keys are present.
	var m map[string]yaml.Node
	if err := node.Decode(&m); err != nil {
		return err
	}
	if vNode, ok := m["value"]; ok {
		r.hasValue = true
		if err := vNode.Decode(&r.Value); err != nil {
			return err
		}
	}
	if cNode, ok := m["conditions"]; ok {
		if err := cNode.Decode(&r.Conditions); err != nil {
			return err
		}
	}
	if lNode, ok := m["launch"]; ok {
		r.Launch = &rawLaunchOverride{}
		if err := lNode.Decode(r.Launch); err != nil {
			return err
		}
	}
	return nil
}

type rawLaunchOverride struct {
	ID    string `yaml:"id"`
	Value any    `yaml:"value"`
}

type rawCondition struct {
	When      string             `yaml:"when"`
	Value     any                `yaml:"value"`
	Otherwise any                `yaml:"otherwise"`
	Launch    *rawLaunchOverride `yaml:"launch"`
	hasValue  bool
	hasOther  bool
	comment   string
}

func (r *rawCondition) UnmarshalYAML(node *yaml.Node) error {
	var m map[string]yaml.Node
	if err := node.Decode(&m); err != nil {
		return err
	}
	if wNode, ok := m["when"]; ok {
		if err := wNode.Decode(&r.When); err != nil {
			return err
		}
	}
	if vNode, ok := m["value"]; ok {
		r.hasValue = true
		if err := vNode.Decode(&r.Value); err != nil {
			return err
		}
	}
	if oNode, ok := m["otherwise"]; ok {
		r.hasOther = true
		if err := oNode.Decode(&r.Otherwise); err != nil {
			return err
		}
	}
	if lNode, ok := m["launch"]; ok {
		r.Launch = &rawLaunchOverride{}
		if err := lNode.Decode(r.Launch); err != nil {
			return err
		}
	}

	// Capture YAML comments: prefer head comment (line above the entry),
	// fall back to line comment (inline after the when/otherwise key).
	r.comment = stripComment(node.HeadComment)
	if r.comment == "" {
		if wNode, ok := m["when"]; ok {
			r.comment = stripComment(wNode.LineComment)
		}
		if r.comment == "" {
			if oNode, ok := m["otherwise"]; ok {
				r.comment = stripComment(oNode.LineComment)
			}
		}
	}
	return nil
}

// Parse parses a YAML config file and validates it against the given flag type
// map (flag field name → FlagType). Returns the parsed config, any warnings,
// and an error if validation fails.
func Parse(data []byte, flagTypes map[string]pbflagsv1.FlagType) (*Config, []string, error) {
	var raw rawConfig
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, nil, fmt.Errorf("parse YAML: %w", err)
	}

	if raw.Feature == "" {
		return nil, nil, errors.New("missing required field: feature")
	}
	if len(raw.Flags) == 0 && len(raw.Launches) == 0 {
		return nil, nil, errors.New("missing required field: flags")
	}

	var errs []error
	var warnings []string
	cfg := &Config{
		Feature: raw.Feature,
		Flags:   make(map[string]FlagEntry, len(raw.Flags)),
	}

	for name, rawEntry := range raw.Flags {
		ft, ok := flagTypes[name]
		if !ok {
			errs = append(errs, fmt.Errorf("flag %q: not defined in proto", name))
			continue
		}
		entry, warns, err := convertFlagEntry(name, rawEntry, ft)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		warnings = append(warnings, warns...)
		cfg.Flags[name] = entry
	}

	// Warn (not error) for proto flags missing from config — only flags
	// with overrides need to be present in the YAML.
	for name := range flagTypes {
		if _, ok := raw.Flags[name]; !ok {
			warnings = append(warnings, fmt.Sprintf("flag %q: defined in proto but not in config (will use compiled default)", name))
		}
	}

	// Parse launches (feature-scoped launch definitions).
	if len(raw.Launches) > 0 {
		cfg.Launches = make(map[string]LaunchEntry, len(raw.Launches))
		for launchID, rawLaunch := range raw.Launches {
			if launchID == "" {
				errs = append(errs, errors.New("launch: empty launch ID"))
				continue
			}
			if rawLaunch.Dimension == "" {
				errs = append(errs, fmt.Errorf("launch %q: missing required field: dimension", launchID))
				continue
			}
			if rawLaunch.RampPercentage != nil {
				if *rawLaunch.RampPercentage < 0 || *rawLaunch.RampPercentage > 100 {
					errs = append(errs, fmt.Errorf("launch %q: ramp_percentage must be 0-100, got %d", launchID, *rawLaunch.RampPercentage))
					continue
				}
			}
			cfg.Launches[launchID] = LaunchEntry(rawLaunch)
		}
	}

	if len(errs) > 0 {
		sort.Slice(errs, func(i, j int) bool { return errs[i].Error() < errs[j].Error() })
		return nil, warnings, errors.Join(errs...)
	}
	sort.Strings(warnings)
	return cfg, warnings, nil
}

func convertFlagEntry(name string, raw rawFlagEntry, ft pbflagsv1.FlagType) (FlagEntry, []string, error) {
	hasValue := raw.hasValue
	hasConds := len(raw.Conditions) > 0

	if hasValue && hasConds {
		return FlagEntry{}, nil, fmt.Errorf("flag %q: cannot have both value and conditions", name)
	}
	if !hasValue && !hasConds {
		return FlagEntry{}, nil, fmt.Errorf("flag %q: must have either value or conditions", name)
	}

	if hasValue {
		fv, err := convertValue(raw.Value, ft, name)
		if err != nil {
			return FlagEntry{}, nil, err
		}
		entry := FlagEntry{Value: fv}

		// Static value launch override.
		if raw.Launch != nil {
			if raw.Launch.ID == "" {
				return FlagEntry{}, nil, fmt.Errorf("flag %q: launch override missing id", name)
			}
			lv, err := convertValue(raw.Launch.Value, ft, name)
			if err != nil {
				return FlagEntry{}, nil, fmt.Errorf("flag %q: launch override: %w", name, err)
			}
			entry.Launch = &FlagLaunchOverride{ID: raw.Launch.ID, Value: lv}
		}

		return entry, nil, nil
	}

	// Condition chain.
	var conds []Condition
	var warnings []string
	hasOtherwise := false

	for i, rc := range raw.Conditions {
		isWhen := rc.When != ""
		isOther := rc.hasOther

		if isWhen && isOther {
			return FlagEntry{}, nil, fmt.Errorf("flag %q: condition %d has both when and otherwise", name, i)
		}
		if !isWhen && !isOther {
			return FlagEntry{}, nil, fmt.Errorf("flag %q: condition %d has neither when nor otherwise", name, i)
		}
		if isOther && i != len(raw.Conditions)-1 {
			return FlagEntry{}, nil, fmt.Errorf("flag %q: otherwise must be the last condition", name)
		}

		var val any
		if isOther {
			hasOtherwise = true
			val = rc.Otherwise
		} else {
			if !rc.hasValue {
				return FlagEntry{}, nil, fmt.Errorf("flag %q: condition %d (when) missing value", name, i)
			}
			val = rc.Value
		}

		fv, err := convertValue(val, ft, name)
		if err != nil {
			return FlagEntry{}, nil, err
		}

		cond := Condition{When: rc.When, Value: fv, Comment: rc.comment}

		// Per-condition launch override.
		if rc.Launch != nil {
			if rc.Launch.ID == "" {
				return FlagEntry{}, nil, fmt.Errorf("flag %q: condition %d launch override missing id", name, i)
			}
			lv, err := convertValue(rc.Launch.Value, ft, name)
			if err != nil {
				return FlagEntry{}, nil, fmt.Errorf("flag %q: condition %d launch override: %w", name, i, err)
			}
			cond.Launch = &LaunchOverride{ID: rc.Launch.ID, Value: lv}
		}

		conds = append(conds, cond)
	}

	if !hasOtherwise {
		warnings = append(warnings, fmt.Sprintf("flag %q: condition chain has no otherwise clause", name))
	}

	return FlagEntry{Conditions: conds}, warnings, nil
}

func convertValue(raw any, ft pbflagsv1.FlagType, flagName string) (*pbflagsv1.FlagValue, error) {
	if raw == nil {
		return nil, fmt.Errorf("flag %q: value is null", flagName)
	}

	switch ft {
	case pbflagsv1.FlagType_FLAG_TYPE_BOOL:
		b, ok := raw.(bool)
		if !ok {
			return nil, fmt.Errorf("flag %q: expected bool, got %T(%v)", flagName, raw, raw)
		}
		return &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_BoolValue{BoolValue: b}}, nil

	case pbflagsv1.FlagType_FLAG_TYPE_STRING:
		s, ok := raw.(string)
		if !ok {
			return nil, fmt.Errorf("flag %q: expected string, got %T(%v)", flagName, raw, raw)
		}
		return &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringValue{StringValue: s}}, nil

	case pbflagsv1.FlagType_FLAG_TYPE_INT64:
		v, err := toInt64(raw)
		if err != nil {
			return nil, fmt.Errorf("flag %q: %w", flagName, err)
		}
		return &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_Int64Value{Int64Value: v}}, nil

	case pbflagsv1.FlagType_FLAG_TYPE_DOUBLE:
		v, err := toFloat64(raw)
		if err != nil {
			return nil, fmt.Errorf("flag %q: %w", flagName, err)
		}
		return &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_DoubleValue{DoubleValue: v}}, nil

	case pbflagsv1.FlagType_FLAG_TYPE_BOOL_LIST:
		return convertList(raw, flagName, func(elem any) (*pbflagsv1.FlagValue, error) {
			b, ok := elem.(bool)
			if !ok {
				return nil, fmt.Errorf("expected bool element, got %T(%v)", elem, elem)
			}
			return &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_BoolValue{BoolValue: b}}, nil
		}, func(vals []*pbflagsv1.FlagValue) *pbflagsv1.FlagValue {
			bools := make([]bool, len(vals))
			for i, v := range vals {
				bools[i] = v.GetBoolValue()
			}
			return &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_BoolListValue{
				BoolListValue: &pbflagsv1.BoolList{Values: bools},
			}}
		})

	case pbflagsv1.FlagType_FLAG_TYPE_STRING_LIST:
		return convertList(raw, flagName, func(elem any) (*pbflagsv1.FlagValue, error) {
			s, ok := elem.(string)
			if !ok {
				return nil, fmt.Errorf("expected string element, got %T(%v)", elem, elem)
			}
			return &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringValue{StringValue: s}}, nil
		}, func(vals []*pbflagsv1.FlagValue) *pbflagsv1.FlagValue {
			strs := make([]string, len(vals))
			for i, v := range vals {
				strs[i] = v.GetStringValue()
			}
			return &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringListValue{
				StringListValue: &pbflagsv1.StringList{Values: strs},
			}}
		})

	case pbflagsv1.FlagType_FLAG_TYPE_INT64_LIST:
		return convertList(raw, flagName, func(elem any) (*pbflagsv1.FlagValue, error) {
			v, err := toInt64(elem)
			if err != nil {
				return nil, err
			}
			return &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_Int64Value{Int64Value: v}}, nil
		}, func(vals []*pbflagsv1.FlagValue) *pbflagsv1.FlagValue {
			ints := make([]int64, len(vals))
			for i, v := range vals {
				ints[i] = v.GetInt64Value()
			}
			return &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_Int64ListValue{
				Int64ListValue: &pbflagsv1.Int64List{Values: ints},
			}}
		})

	case pbflagsv1.FlagType_FLAG_TYPE_DOUBLE_LIST:
		return convertList(raw, flagName, func(elem any) (*pbflagsv1.FlagValue, error) {
			v, err := toFloat64(elem)
			if err != nil {
				return nil, err
			}
			return &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_DoubleValue{DoubleValue: v}}, nil
		}, func(vals []*pbflagsv1.FlagValue) *pbflagsv1.FlagValue {
			floats := make([]float64, len(vals))
			for i, v := range vals {
				floats[i] = v.GetDoubleValue()
			}
			return &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_DoubleListValue{
				DoubleListValue: &pbflagsv1.DoubleList{Values: floats},
			}}
		})

	default:
		return nil, fmt.Errorf("flag %q: unsupported flag type %v", flagName, ft)
	}
}

func convertList(
	raw any,
	flagName string,
	elemFn func(any) (*pbflagsv1.FlagValue, error),
	buildFn func([]*pbflagsv1.FlagValue) *pbflagsv1.FlagValue,
) (*pbflagsv1.FlagValue, error) {
	slice, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("flag %q: expected list, got %T(%v)", flagName, raw, raw)
	}
	vals := make([]*pbflagsv1.FlagValue, len(slice))
	for i, elem := range slice {
		v, err := elemFn(elem)
		if err != nil {
			return nil, fmt.Errorf("flag %q[%d]: %w", flagName, i, err)
		}
		vals[i] = v
	}
	return buildFn(vals), nil
}

func toInt64(v any) (int64, error) {
	switch n := v.(type) {
	case int:
		return int64(n), nil
	case int64:
		return n, nil
	case uint64:
		return int64(n), nil
	case float64:
		if n != float64(int64(n)) {
			return 0, fmt.Errorf("expected integer, got %v", n)
		}
		return int64(n), nil
	default:
		return 0, fmt.Errorf("expected integer, got %T(%v)", v, v)
	}
}

func toFloat64(v any) (float64, error) {
	switch n := v.(type) {
	case float64:
		return n, nil
	case int:
		return float64(n), nil
	case int64:
		return float64(n), nil
	case uint64:
		return float64(n), nil
	default:
		return 0, fmt.Errorf("expected number, got %T(%v)", v, v)
	}
}

// ParseCrossFeatureLaunch parses a standalone cross-feature launch YAML file.
// The launch ID is derived from the filename (sans extension), not from within
// the file. The file contains only {dimension, ramp_percentage, description}.
func ParseCrossFeatureLaunch(data []byte) (LaunchEntry, error) {
	var raw rawLaunchEntry
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return LaunchEntry{}, fmt.Errorf("parse launch YAML: %w", err)
	}
	if raw.Dimension == "" {
		return LaunchEntry{}, errors.New("missing required field: dimension")
	}
	if raw.RampPercentage != nil {
		if *raw.RampPercentage < 0 || *raw.RampPercentage > 100 {
			return LaunchEntry{}, fmt.Errorf("ramp_percentage must be 0-100, got %d", *raw.RampPercentage)
		}
	}
	return LaunchEntry(raw), nil
}

// stripComment removes the leading "# " (or "#") from a yaml.Node comment
// field and trims surrounding whitespace. Returns "" if the input is empty.
func stripComment(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "#")
	return strings.TrimSpace(s)
}
