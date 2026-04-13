package configexport

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
	"github.com/SpotlightGOV/pbflags/internal/flagfmt"
	"github.com/SpotlightGOV/pbflags/internal/testdb"
)

func TestExport_StaticValues(t *testing.T) {
	_, pool := testdb.Require(t)
	ctx := context.Background()

	tf := testdb.CreateTestFeature(t, pool, []testdb.FlagSpec{
		{FlagType: "BOOL"},
		{FlagType: "STRING"},
	})

	val1, _ := proto.Marshal(&pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_BoolValue{BoolValue: true}})
	val2, _ := proto.Marshal(&pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringValue{StringValue: "weekly"}})
	pool.Exec(ctx, `UPDATE feature_flags.flags SET display_name='email_enabled', default_value=$2 WHERE flag_id=$1`, tf.FlagIDs[0], val1)
	pool.Exec(ctx, `UPDATE feature_flags.flags SET display_name='digest_frequency', default_value=$2 WHERE flag_id=$1`, tf.FlagIDs[1], val2)

	configs, err := Export(ctx, pool, Options{})
	require.NoError(t, err)

	var found *ExportedConfig
	for i := range configs {
		if configs[i].FeatureID == tf.FeatureID {
			found = &configs[i]
			break
		}
	}
	require.NotNil(t, found, "expected config for feature %s", tf.FeatureID)

	yaml := string(found.YAML)
	require.Contains(t, yaml, "feature: "+tf.FeatureID)
	require.Contains(t, yaml, "value: true")
	require.Contains(t, yaml, "value: weekly")
}

func TestFlagValueToYAML(t *testing.T) {
	tests := []struct {
		name string
		fv   *pbflagsv1.FlagValue
		want any
	}{
		{"nil", nil, nil},
		{"bool", &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_BoolValue{BoolValue: true}}, true},
		{"string", &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringValue{StringValue: "hi"}}, "hi"},
		{"int64", &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_Int64Value{Int64Value: 42}}, int64(42)},
		{"double", &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_DoubleValue{DoubleValue: 3.14}}, 3.14},
		{"string_list", &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringListValue{
			StringListValue: &pbflagsv1.StringList{Values: []string{"a", "b"}},
		}}, []string{"a", "b"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := flagfmt.AsAny(tt.fv)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}
