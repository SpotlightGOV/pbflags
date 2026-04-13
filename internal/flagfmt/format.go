package flagfmt

import (
	"strconv"
	"strings"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

// DisplayString formats a FlagValue for human display (admin UI, CLI show).
func DisplayString(v *pbflagsv1.FlagValue) string {
	if v == nil {
		return "—"
	}
	switch val := v.Value.(type) {
	case *pbflagsv1.FlagValue_BoolValue:
		if val.BoolValue {
			return "true"
		}
		return "false"
	case *pbflagsv1.FlagValue_StringValue:
		return val.StringValue
	case *pbflagsv1.FlagValue_Int64Value:
		return strconv.FormatInt(val.Int64Value, 10)
	case *pbflagsv1.FlagValue_DoubleValue:
		return strconv.FormatFloat(val.DoubleValue, 'f', -1, 64)
	case *pbflagsv1.FlagValue_StringListValue:
		if val.StringListValue == nil || len(val.StringListValue.Values) == 0 {
			return "[]"
		}
		return "[" + strings.Join(val.StringListValue.Values, ", ") + "]"
	case *pbflagsv1.FlagValue_Int64ListValue:
		if val.Int64ListValue == nil || len(val.Int64ListValue.Values) == 0 {
			return "[]"
		}
		parts := make([]string, len(val.Int64ListValue.Values))
		for i, v := range val.Int64ListValue.Values {
			parts[i] = strconv.FormatInt(v, 10)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case *pbflagsv1.FlagValue_DoubleListValue:
		if val.DoubleListValue == nil || len(val.DoubleListValue.Values) == 0 {
			return "[]"
		}
		parts := make([]string, len(val.DoubleListValue.Values))
		for i, v := range val.DoubleListValue.Values {
			parts[i] = strconv.FormatFloat(v, 'f', -1, 64)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case *pbflagsv1.FlagValue_BoolListValue:
		if val.BoolListValue == nil || len(val.BoolListValue.Values) == 0 {
			return "[]"
		}
		parts := make([]string, len(val.BoolListValue.Values))
		for i, v := range val.BoolListValue.Values {
			parts[i] = strconv.FormatBool(v)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	default:
		return "—"
	}
}

// DisplayConditionValue formats a protojson-encoded FlagValue (from the
// conditions JSONB column) for human display.
func DisplayConditionValue(raw []byte) string {
	if raw == nil {
		return "—"
	}
	var fv pbflagsv1.FlagValue
	if err := protojson.Unmarshal(raw, &fv); err != nil {
		return string(raw)
	}
	return DisplayString(&fv)
}

// AsAny converts a FlagValue to a native Go value suitable for YAML/JSON
// serialization.
func AsAny(v *pbflagsv1.FlagValue) (any, error) {
	if v == nil {
		return nil, nil
	}
	switch val := v.Value.(type) {
	case *pbflagsv1.FlagValue_BoolValue:
		return val.BoolValue, nil
	case *pbflagsv1.FlagValue_StringValue:
		return val.StringValue, nil
	case *pbflagsv1.FlagValue_Int64Value:
		return val.Int64Value, nil
	case *pbflagsv1.FlagValue_DoubleValue:
		return val.DoubleValue, nil
	case *pbflagsv1.FlagValue_BoolListValue:
		if val.BoolListValue == nil {
			return []bool{}, nil
		}
		return val.BoolListValue.Values, nil
	case *pbflagsv1.FlagValue_StringListValue:
		if val.StringListValue == nil {
			return []string{}, nil
		}
		return val.StringListValue.Values, nil
	case *pbflagsv1.FlagValue_Int64ListValue:
		if val.Int64ListValue == nil {
			return []int64{}, nil
		}
		return val.Int64ListValue.Values, nil
	case *pbflagsv1.FlagValue_DoubleListValue:
		if val.DoubleListValue == nil {
			return []float64{}, nil
		}
		return val.DoubleListValue.Values, nil
	default:
		return nil, nil
	}
}
