package configcli

import (
	"testing"

	"github.com/stretchr/testify/require"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
)

func TestParseFlagQuery_WithFeature(t *testing.T) {
	t.Parallel()
	featureID, flagName := parseFlagQuery("notifications/enable_push")
	require.Equal(t, "notifications", featureID)
	require.Equal(t, "enable_push", flagName)
}

func TestParseFlagQuery_WithNestedFeature(t *testing.T) {
	t.Parallel()
	featureID, flagName := parseFlagQuery("com.example/notifications/enable_push")
	require.Equal(t, "com.example/notifications", featureID)
	require.Equal(t, "enable_push", flagName)
}

func TestParseFlagQuery_FlagNameOnly(t *testing.T) {
	t.Parallel()
	featureID, flagName := parseFlagQuery("enable_push")
	require.Equal(t, "", featureID)
	require.Equal(t, "enable_push", flagName)
}

func TestFormatFlagValue_Nil(t *testing.T) {
	t.Parallel()
	require.Equal(t, "(nil)", formatFlagValue(nil))
}

func TestFormatFlagValue_String(t *testing.T) {
	t.Parallel()
	fv := &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringValue{StringValue: "hello"}}
	require.Equal(t, `"hello"`, formatFlagValue(fv))
}

func TestFormatFlagValue_Bool(t *testing.T) {
	t.Parallel()
	fv := &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_BoolValue{BoolValue: true}}
	require.Equal(t, "true", formatFlagValue(fv))
}

func TestFormatFlagValue_Int64(t *testing.T) {
	t.Parallel()
	fv := &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_Int64Value{Int64Value: 42}}
	require.Equal(t, "42", formatFlagValue(fv))
}
