package configexport

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
	"github.com/SpotlightGOV/pbflags/internal/testdb"
)

func TestExport_StaticValues(t *testing.T) {
	_, pool := testdb.Require(t)
	ctx := context.Background()

	tf := testdb.CreateTestFeature(t, pool, []testdb.FlagSpec{
		{FlagType: "BOOL", Layer: "GLOBAL"},
		{FlagType: "STRING", Layer: "GLOBAL"},
	})

	// Set display_name (proto field name) and values.
	val1, _ := proto.Marshal(&pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_BoolValue{BoolValue: true}})
	val2, _ := proto.Marshal(&pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringValue{StringValue: "weekly"}})
	pool.Exec(ctx, `UPDATE feature_flags.flags SET display_name='email_enabled', state='ENABLED', value=$2 WHERE flag_id=$1`, tf.FlagIDs[0], val1)
	pool.Exec(ctx, `UPDATE feature_flags.flags SET display_name='digest_frequency', state='ENABLED', value=$2 WHERE flag_id=$1`, tf.FlagIDs[1], val2)

	configs, err := Export(ctx, pool)
	require.NoError(t, err)

	// Find our feature.
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

func TestExport_WithOverrides(t *testing.T) {
	_, pool := testdb.Require(t)
	ctx := context.Background()

	tf := testdb.CreateTestFeature(t, pool, []testdb.FlagSpec{
		{FlagType: "BOOL", Layer: "USER"},
	})

	// Set global value.
	globalVal, _ := proto.Marshal(&pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_BoolValue{BoolValue: false}})
	pool.Exec(ctx, `UPDATE feature_flags.flags SET display_name='enabled', state='ENABLED', value=$2 WHERE flag_id=$1`, tf.FlagIDs[0], globalVal)

	// Set per-entity override.
	overrideVal, _ := proto.Marshal(&pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_BoolValue{BoolValue: true}})
	pool.Exec(ctx, `INSERT INTO feature_flags.flag_overrides (flag_id, entity_id, state, value) VALUES ($1, 'user-99', 'ENABLED', $2)`,
		tf.FlagIDs[0], overrideVal)

	configs, err := Export(ctx, pool)
	require.NoError(t, err)

	var found *ExportedConfig
	for i := range configs {
		if configs[i].FeatureID == tf.FeatureID {
			found = &configs[i]
			break
		}
	}
	require.NotNil(t, found)

	yaml := string(found.YAML)
	require.Contains(t, yaml, "conditions:")
	require.Contains(t, yaml, `ctx.user_id == "user-99"`)
	require.Contains(t, yaml, "otherwise:")
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
			got, err := flagValueToYAML(tt.fv)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestBuildConditionEntry_GroupsOverrides(t *testing.T) {
	fl := flag{
		name:     "test_flag",
		flagType: "STRING",
		value:    &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringValue{StringValue: "default"}},
		overrides: []override{
			{entityID: "user-1", state: "ENABLED", value: &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringValue{StringValue: "beta"}}},
			{entityID: "user-2", state: "ENABLED", value: &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringValue{StringValue: "beta"}}},
			{entityID: "user-99", state: "ENABLED", value: &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringValue{StringValue: "special"}}},
		},
	}

	entry, err := buildFlagEntry(fl)
	require.NoError(t, err)
	require.Len(t, entry.Conditions, 3) // 2 groups + otherwise

	// First group: user-1 and user-2 with same value → `in` expression.
	require.Contains(t, entry.Conditions[0].When, "ctx.user_id in")
	require.Contains(t, entry.Conditions[0].When, "user-1")
	require.Contains(t, entry.Conditions[0].When, "user-2")

	// Second group: user-99 with different value.
	require.True(t, strings.Contains(entry.Conditions[1].When, "user-99"))

	// Otherwise.
	require.Equal(t, "default", entry.Conditions[2].Otherwise)
}
