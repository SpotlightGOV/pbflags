package pbflags

import (
	"context"
	"fmt"
	"strings"

	"github.com/SpotlightGOV/pbflags/gen/pbflags/flagmeta"
	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
)

// Override pairs a flag ID with a type-safe value for use with [WithOverrides].
type Override struct {
	flagID string
	value  *pbflagsv1.FlagValue
}

// FlagID returns the flag ID this override targets.
func (o Override) FlagID() string { return o.flagID }

// Value returns the override's flag value.
func (o Override) Value() *pbflagsv1.FlagValue { return o.value }

// --- Typed override constructors ---

// BoolOverride creates an override that sets a bool flag.
func BoolOverride(flagID string, val bool) Override {
	return Override{flagID: flagID, value: &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_BoolValue{BoolValue: val}}}
}

// StringOverride creates an override that sets a string flag.
func StringOverride(flagID string, val string) Override {
	return Override{flagID: flagID, value: &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringValue{StringValue: val}}}
}

// Int64Override creates an override that sets an int64 flag.
func Int64Override(flagID string, val int64) Override {
	return Override{flagID: flagID, value: &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_Int64Value{Int64Value: val}}}
}

// DoubleOverride creates an override that sets a float64 flag.
func DoubleOverride(flagID string, val float64) Override {
	return Override{flagID: flagID, value: &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_DoubleValue{DoubleValue: val}}}
}

// BoolListOverride creates an override that sets a bool list flag.
func BoolListOverride(flagID string, vals ...bool) Override {
	return Override{flagID: flagID, value: &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_BoolListValue{BoolListValue: &pbflagsv1.BoolList{Values: vals}}}}
}

// StringListOverride creates an override that sets a string list flag.
func StringListOverride(flagID string, vals ...string) Override {
	return Override{flagID: flagID, value: &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringListValue{StringListValue: &pbflagsv1.StringList{Values: vals}}}}
}

// Int64ListOverride creates an override that sets an int64 list flag.
func Int64ListOverride(flagID string, vals ...int64) Override {
	return Override{flagID: flagID, value: &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_Int64ListValue{Int64ListValue: &pbflagsv1.Int64List{Values: vals}}}}
}

// DoubleListOverride creates an override that sets a float64 list flag.
func DoubleListOverride(flagID string, vals ...float64) Override {
	return Override{flagID: flagID, value: &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_DoubleListValue{DoubleListValue: &pbflagsv1.DoubleList{Values: vals}}}}
}

// WithOverrides wraps an Evaluator so that the specified flags return
// hard-coded values without hitting the evaluation server. Non-overridden
// flags delegate to the underlying evaluator.
//
// This function is intended for use in tests. Overrides are preserved
// across [Evaluator.With] calls — adding dimensions does not discard them.
//
// If descriptors is non-nil, each override is validated against the flag
// definitions: unknown flag IDs and type mismatches cause a panic.
// Pass nil to skip validation.
//
// Panics if eval is nil or if any override fails validation.
func WithOverrides(eval Evaluator, descriptors []flagmeta.FlagDescriptor, overrides ...Override) Evaluator {
	if isNilEvaluator(eval) {
		panic("pbflags.WithOverrides: eval must not be nil")
	}
	if len(overrides) == 0 {
		return eval
	}
	if descriptors != nil {
		if err := ValidateOverrides(overrides, descriptors); err != nil {
			panic("pbflags.WithOverrides: " + err.Error())
		}
	}
	m := make(map[string]*pbflagsv1.FlagValue, len(overrides))
	for _, o := range overrides {
		m[o.flagID] = o.value
	}
	return &overrideEvaluator{inner: eval, overrides: m}
}

// ValidateOverrides checks that each override's value type matches the
// flag definition in descriptors. Returns an error listing all mismatches.
// An override targeting a flag ID not present in descriptors is an error.
func ValidateOverrides(overrides []Override, descriptors []flagmeta.FlagDescriptor) error {
	byID := make(map[string]flagmeta.FlagDescriptor, len(descriptors))
	for _, d := range descriptors {
		byID[d.ID] = d
	}

	var errs []string
	for _, o := range overrides {
		desc, ok := byID[o.flagID]
		if !ok {
			errs = append(errs, fmt.Sprintf("unknown flag %q", o.flagID))
			continue
		}
		gotType, gotList := flagValueType(o.value)
		if gotType != desc.Type || gotList != desc.IsList {
			want := desc.Type.String()
			if desc.IsList {
				want = "[]" + want
			}
			got := gotType.String()
			if gotList {
				got = "[]" + got
			}
			errs = append(errs, fmt.Sprintf("flag %q: want %s, got %s", o.flagID, want, got))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("override type mismatch: %s", strings.Join(errs, "; "))
	}
	return nil
}

// flagValueType extracts the flagmeta.FlagType and list-ness from a FlagValue.
func flagValueType(v *pbflagsv1.FlagValue) (flagmeta.FlagType, bool) {
	switch v.GetValue().(type) {
	case *pbflagsv1.FlagValue_BoolValue:
		return flagmeta.FlagTypeBool, false
	case *pbflagsv1.FlagValue_StringValue:
		return flagmeta.FlagTypeString, false
	case *pbflagsv1.FlagValue_Int64Value:
		return flagmeta.FlagTypeInt64, false
	case *pbflagsv1.FlagValue_DoubleValue:
		return flagmeta.FlagTypeDouble, false
	case *pbflagsv1.FlagValue_BoolListValue:
		return flagmeta.FlagTypeBool, true
	case *pbflagsv1.FlagValue_StringListValue:
		return flagmeta.FlagTypeString, true
	case *pbflagsv1.FlagValue_Int64ListValue:
		return flagmeta.FlagTypeInt64, true
	case *pbflagsv1.FlagValue_DoubleListValue:
		return flagmeta.FlagTypeDouble, true
	default:
		return flagmeta.FlagTypeBool, false // unreachable with typed constructors
	}
}

type overrideEvaluator struct {
	inner     Evaluator
	overrides map[string]*pbflagsv1.FlagValue
}

func (o *overrideEvaluator) With(dims ...Dimension) Evaluator {
	return &overrideEvaluator{
		inner:     o.inner.With(dims...),
		overrides: o.overrides, // shared read-only map
	}
}

func (o *overrideEvaluator) Evaluate(ctx context.Context, flagID string) (*Result, error) {
	if val, ok := o.overrides[flagID]; ok {
		return &Result{
			Value:  val,
			Source: pbflagsv1.EvaluationSource_EVALUATION_SOURCE_OVERRIDE,
		}, nil
	}
	return o.inner.Evaluate(ctx, flagID)
}

func (o *overrideEvaluator) BulkEvaluate(ctx context.Context, flagIDs []string) ([]*Result, error) {
	results := make([]*Result, len(flagIDs))
	var delegateIDs []string
	var delegateIdx []int
	for i, id := range flagIDs {
		if val, ok := o.overrides[id]; ok {
			results[i] = &Result{
				Value:  val,
				Source: pbflagsv1.EvaluationSource_EVALUATION_SOURCE_OVERRIDE,
			}
		} else {
			delegateIDs = append(delegateIDs, id)
			delegateIdx = append(delegateIdx, i)
		}
	}
	if len(delegateIDs) == 0 {
		return results, nil
	}
	inner, err := o.inner.BulkEvaluate(ctx, delegateIDs)
	if err != nil {
		return nil, err
	}
	for j, idx := range delegateIdx {
		results[idx] = inner[j]
	}
	return results, nil
}
