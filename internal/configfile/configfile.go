// Package configfile parses and validates YAML flag configuration files.
// Each file defines flag behavior for a single feature: static values
// and/or condition chains with CEL expressions.
package configfile

import (
	"errors"
	"fmt"
	"sort"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
	"gopkg.in/yaml.v3"
)

// Config is a parsed and validated flag configuration for a single feature.
type Config struct {
	Feature string
	Flags   map[string]FlagEntry
}

// FlagEntry is a single flag's configuration — either a static value or
// a condition chain, never both.
type FlagEntry struct {
	Value      *pbflagsv1.FlagValue // non-nil for static values
	Conditions []Condition          // non-nil for condition chains
}

// Condition is one entry in a condition chain.
type Condition struct {
	When  string               // CEL expression; empty means "otherwise" (default)
	Value *pbflagsv1.FlagValue // the value to return when the condition matches
}

// YAML unmarshaling types.
type rawConfig struct {
	Feature string                  `yaml:"feature"`
	Flags   map[string]rawFlagEntry `yaml:"flags"`
}

type rawFlagEntry struct {
	Value      any            `yaml:"value"`
	Conditions []rawCondition `yaml:"conditions"`
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
	return nil
}

type rawCondition struct {
	When      string `yaml:"when"`
	Value     any    `yaml:"value"`
	Otherwise any    `yaml:"otherwise"`
	hasValue  bool
	hasOther  bool
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
	if len(raw.Flags) == 0 {
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

	// Check for proto flags missing from config.
	for name := range flagTypes {
		if _, ok := raw.Flags[name]; !ok {
			errs = append(errs, fmt.Errorf("flag %q: defined in proto but missing from config", name))
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
		return FlagEntry{Value: fv}, nil, nil
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
		conds = append(conds, Condition{When: rc.When, Value: fv})
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
	default:
		return 0, fmt.Errorf("expected number, got %T(%v)", v, v)
	}
}
