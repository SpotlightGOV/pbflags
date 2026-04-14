package flagfmt

import (
	"testing"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
	"github.com/stretchr/testify/require"
)

func TestDisplayString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		v    *pbflagsv1.FlagValue
		want string
	}{
		// nil outer value
		{
			name: "nil value returns em dash",
			v:    nil,
			want: "—",
		},
		// nil inner Value (oneof not set)
		{
			name: "non-nil FlagValue with nil inner value returns em dash",
			v:    &pbflagsv1.FlagValue{},
			want: "—",
		},

		// bool scalar
		{
			name: "bool true",
			v:    &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_BoolValue{BoolValue: true}},
			want: "true",
		},
		{
			name: "bool false",
			v:    &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_BoolValue{BoolValue: false}},
			want: "false",
		},

		// string scalar
		{
			name: "string value",
			v:    &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringValue{StringValue: "hello"}},
			want: "hello",
		},
		{
			name: "empty string value",
			v:    &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringValue{StringValue: ""}},
			want: "",
		},

		// int64 scalar
		{
			name: "positive int64",
			v:    &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_Int64Value{Int64Value: 42}},
			want: "42",
		},
		{
			name: "negative int64",
			v:    &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_Int64Value{Int64Value: -100}},
			want: "-100",
		},
		{
			name: "zero int64",
			v:    &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_Int64Value{Int64Value: 0}},
			want: "0",
		},

		// double scalar
		{
			name: "double value",
			v:    &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_DoubleValue{DoubleValue: 3.14}},
			want: "3.14",
		},
		{
			name: "double integer-like value",
			v:    &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_DoubleValue{DoubleValue: 5.0}},
			want: "5",
		},
		{
			name: "negative double",
			v:    &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_DoubleValue{DoubleValue: -0.5}},
			want: "-0.5",
		},

		// string list
		{
			name: "string list with values",
			v: &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringListValue{
				StringListValue: &pbflagsv1.StringList{Values: []string{"a", "b", "c"}},
			}},
			want: "[a, b, c]",
		},
		{
			name: "string list single value",
			v: &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringListValue{
				StringListValue: &pbflagsv1.StringList{Values: []string{"only"}},
			}},
			want: "[only]",
		},
		{
			name: "string list empty values",
			v: &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringListValue{
				StringListValue: &pbflagsv1.StringList{Values: []string{}},
			}},
			want: "[]",
		},
		{
			name: "string list nil inner",
			v: &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringListValue{
				StringListValue: nil,
			}},
			want: "[]",
		},

		// int64 list
		{
			name: "int64 list with values",
			v: &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_Int64ListValue{
				Int64ListValue: &pbflagsv1.Int64List{Values: []int64{1, 2, 3}},
			}},
			want: "[1, 2, 3]",
		},
		{
			name: "int64 list single value",
			v: &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_Int64ListValue{
				Int64ListValue: &pbflagsv1.Int64List{Values: []int64{99}},
			}},
			want: "[99]",
		},
		{
			name: "int64 list empty values",
			v: &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_Int64ListValue{
				Int64ListValue: &pbflagsv1.Int64List{Values: []int64{}},
			}},
			want: "[]",
		},
		{
			name: "int64 list nil inner",
			v: &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_Int64ListValue{
				Int64ListValue: nil,
			}},
			want: "[]",
		},

		// double list
		{
			name: "double list with values",
			v: &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_DoubleListValue{
				DoubleListValue: &pbflagsv1.DoubleList{Values: []float64{1.1, 2.2, 3.3}},
			}},
			want: "[1.1, 2.2, 3.3]",
		},
		{
			name: "double list single value",
			v: &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_DoubleListValue{
				DoubleListValue: &pbflagsv1.DoubleList{Values: []float64{0.5}},
			}},
			want: "[0.5]",
		},
		{
			name: "double list empty values",
			v: &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_DoubleListValue{
				DoubleListValue: &pbflagsv1.DoubleList{Values: []float64{}},
			}},
			want: "[]",
		},
		{
			name: "double list nil inner",
			v: &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_DoubleListValue{
				DoubleListValue: nil,
			}},
			want: "[]",
		},

		// bool list
		{
			name: "bool list with values",
			v: &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_BoolListValue{
				BoolListValue: &pbflagsv1.BoolList{Values: []bool{true, false, true}},
			}},
			want: "[true, false, true]",
		},
		{
			name: "bool list single value",
			v: &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_BoolListValue{
				BoolListValue: &pbflagsv1.BoolList{Values: []bool{false}},
			}},
			want: "[false]",
		},
		{
			name: "bool list empty values",
			v: &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_BoolListValue{
				BoolListValue: &pbflagsv1.BoolList{Values: []bool{}},
			}},
			want: "[]",
		},
		{
			name: "bool list nil inner",
			v: &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_BoolListValue{
				BoolListValue: nil,
			}},
			want: "[]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := DisplayString(tt.v)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestDisplayConditionValue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  []byte
		want string
	}{
		{
			name: "nil input returns em dash",
			raw:  nil,
			want: "—",
		},
		{
			name: "valid protojson bool true",
			raw:  []byte(`{"boolValue":true}`),
			want: "true",
		},
		{
			name: "valid protojson bool false",
			raw:  []byte(`{"boolValue":false}`),
			want: "false",
		},
		{
			name: "valid protojson string value",
			raw:  []byte(`{"stringValue":"hello"}`),
			want: "hello",
		},
		{
			name: "valid protojson int64 value",
			raw:  []byte(`{"int64Value":"42"}`),
			want: "42",
		},
		{
			name: "valid protojson double value",
			raw:  []byte(`{"doubleValue":3.14}`),
			want: "3.14",
		},
		{
			name: "invalid protojson returns raw string",
			raw:  []byte(`not valid json`),
			want: "not valid json",
		},
		{
			name: "empty JSON object returns em dash",
			raw:  []byte(`{}`),
			want: "—",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := DisplayConditionValue(tt.raw)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestAsAny(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		v       *pbflagsv1.FlagValue
		want    any
		wantErr bool
	}{
		// nil outer value
		{
			name: "nil value returns nil",
			v:    nil,
			want: nil,
		},
		// nil inner Value (oneof not set, default case)
		{
			name: "non-nil FlagValue with nil inner value returns nil",
			v:    &pbflagsv1.FlagValue{},
			want: nil,
		},

		// bool scalar
		{
			name: "bool true",
			v:    &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_BoolValue{BoolValue: true}},
			want: true,
		},
		{
			name: "bool false",
			v:    &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_BoolValue{BoolValue: false}},
			want: false,
		},

		// string scalar
		{
			name: "string value",
			v:    &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringValue{StringValue: "hello"}},
			want: "hello",
		},
		{
			name: "empty string value",
			v:    &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringValue{StringValue: ""}},
			want: "",
		},

		// int64 scalar
		{
			name: "int64 value",
			v:    &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_Int64Value{Int64Value: 42}},
			want: int64(42),
		},
		{
			name: "zero int64 value",
			v:    &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_Int64Value{Int64Value: 0}},
			want: int64(0),
		},
		{
			name: "negative int64 value",
			v:    &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_Int64Value{Int64Value: -7}},
			want: int64(-7),
		},

		// double scalar
		{
			name: "double value",
			v:    &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_DoubleValue{DoubleValue: 3.14}},
			want: float64(3.14),
		},
		{
			name: "zero double value",
			v:    &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_DoubleValue{DoubleValue: 0}},
			want: float64(0),
		},

		// bool list
		{
			name: "bool list with values",
			v: &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_BoolListValue{
				BoolListValue: &pbflagsv1.BoolList{Values: []bool{true, false}},
			}},
			want: []bool{true, false},
		},
		{
			name: "bool list nil inner returns empty slice",
			v: &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_BoolListValue{
				BoolListValue: nil,
			}},
			want: []bool{},
		},

		// string list
		{
			name: "string list with values",
			v: &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringListValue{
				StringListValue: &pbflagsv1.StringList{Values: []string{"a", "b"}},
			}},
			want: []string{"a", "b"},
		},
		{
			name: "string list nil inner returns empty slice",
			v: &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringListValue{
				StringListValue: nil,
			}},
			want: []string{},
		},

		// int64 list
		{
			name: "int64 list with values",
			v: &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_Int64ListValue{
				Int64ListValue: &pbflagsv1.Int64List{Values: []int64{10, 20, 30}},
			}},
			want: []int64{10, 20, 30},
		},
		{
			name: "int64 list nil inner returns empty slice",
			v: &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_Int64ListValue{
				Int64ListValue: nil,
			}},
			want: []int64{},
		},

		// double list
		{
			name: "double list with values",
			v: &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_DoubleListValue{
				DoubleListValue: &pbflagsv1.DoubleList{Values: []float64{1.5, 2.5}},
			}},
			want: []float64{1.5, 2.5},
		},
		{
			name: "double list nil inner returns empty slice",
			v: &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_DoubleListValue{
				DoubleListValue: nil,
			}},
			want: []float64{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := AsAny(tt.v)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}
