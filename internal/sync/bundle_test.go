package sync

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	example "github.com/SpotlightGOV/pbflags/gen/example"
	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
)

func TestCompile(t *testing.T) {
	t.Parallel()

	// Build descriptor data from the example proto types.
	descData, err := buildDescriptorSet(&example.Notifications{}, &example.EvaluationContext{})
	require.NoError(t, err)

	t.Run("static value config", func(t *testing.T) {
		t.Parallel()
		configDir := t.TempDir()

		// Write a config that sets a static value for email_enabled.
		configYAML := `
feature: notifications
flags:
  email_enabled:
    value: true
`
		require.NoError(t, os.WriteFile(filepath.Join(configDir, "notifications.yaml"), []byte(configYAML), 0o644))

		bundleData, err := Compile(descData, configDir)
		require.NoError(t, err)
		require.NotEmpty(t, bundleData)

		// Unmarshal and verify.
		bundle := &pbflagsv1.CompiledBundle{}
		require.NoError(t, proto.Unmarshal(bundleData, bundle))

		require.NotEmpty(t, bundle.Features, "should have at least one feature")

		// Find the notifications feature.
		var notif *pbflagsv1.CompiledFeature
		for _, f := range bundle.Features {
			if f.FeatureId == "notifications" {
				notif = f
				break
			}
		}
		require.NotNil(t, notif, "notifications feature should be in bundle")

		// Find the email_enabled flag.
		var emailFlag *pbflagsv1.CompiledFlag
		for _, f := range notif.Flags {
			if f.Name == "email_enabled" {
				emailFlag = f
				break
			}
		}
		require.NotNil(t, emailFlag, "email_enabled flag should be in bundle")
		require.NotNil(t, emailFlag.ConditionsJson, "static value should produce conditions JSON")
	})

	t.Run("condition chain config", func(t *testing.T) {
		t.Parallel()
		configDir := t.TempDir()

		configYAML := `
feature: notifications
flags:
  email_enabled:
    conditions:
      - when: "ctx.plan == PlanLevel.ENTERPRISE"
        value: true
      - otherwise: false
`
		require.NoError(t, os.WriteFile(filepath.Join(configDir, "notifications.yaml"), []byte(configYAML), 0o644))

		bundleData, err := Compile(descData, configDir)
		require.NoError(t, err)
		require.NotEmpty(t, bundleData)

		bundle := &pbflagsv1.CompiledBundle{}
		require.NoError(t, proto.Unmarshal(bundleData, bundle))

		var notif *pbflagsv1.CompiledFeature
		for _, f := range bundle.Features {
			if f.FeatureId == "notifications" {
				notif = f
				break
			}
		}
		require.NotNil(t, notif)

		var emailFlag *pbflagsv1.CompiledFlag
		for _, f := range notif.Flags {
			if f.Name == "email_enabled" {
				emailFlag = f
				break
			}
		}
		require.NotNil(t, emailFlag)
		require.NotNil(t, emailFlag.ConditionsJson)
		require.NotNil(t, emailFlag.DimensionMetadataJson, "conditions with CEL should produce dimension metadata")
	})

	t.Run("empty config dir", func(t *testing.T) {
		t.Parallel()
		configDir := t.TempDir()

		bundleData, err := Compile(descData, configDir)
		require.NoError(t, err)
		require.NotEmpty(t, bundleData)

		bundle := &pbflagsv1.CompiledBundle{}
		require.NoError(t, proto.Unmarshal(bundleData, bundle))
		require.NotEmpty(t, bundle.Features, "features should still be extracted from descriptors")
	})

	t.Run("unknown feature in config fails", func(t *testing.T) {
		t.Parallel()
		configDir := t.TempDir()

		configYAML := `
feature: nonexistent_feature
flags:
  some_flag:
    value: true
`
		require.NoError(t, os.WriteFile(filepath.Join(configDir, "nonexistent.yaml"), []byte(configYAML), 0o644))

		_, err := Compile(descData, configDir)
		require.Error(t, err)
		require.Contains(t, err.Error(), "nonexistent_feature")
	})

	t.Run("cel version is populated", func(t *testing.T) {
		t.Parallel()
		configDir := t.TempDir()

		bundleData, err := Compile(descData, configDir)
		require.NoError(t, err)

		bundle := &pbflagsv1.CompiledBundle{}
		require.NoError(t, proto.Unmarshal(bundleData, bundle))
		require.NotEmpty(t, bundle.CelVersion, "CEL version should be populated")
	})
}

func TestFlagTypeString(t *testing.T) {
	t.Parallel()
	tests := []struct {
		ft   pbflagsv1.FlagType
		want string
	}{
		{pbflagsv1.FlagType_FLAG_TYPE_BOOL, "BOOL"},
		{pbflagsv1.FlagType_FLAG_TYPE_STRING, "STRING"},
		{pbflagsv1.FlagType_FLAG_TYPE_INT64, "INT64"},
		{pbflagsv1.FlagType_FLAG_TYPE_DOUBLE, "DOUBLE"},
		{pbflagsv1.FlagType_FLAG_TYPE_BOOL_LIST, "BOOL_LIST"},
		{pbflagsv1.FlagType_FLAG_TYPE_STRING_LIST, "STRING_LIST"},
		{pbflagsv1.FlagType_FLAG_TYPE_INT64_LIST, "INT64_LIST"},
		{pbflagsv1.FlagType_FLAG_TYPE_DOUBLE_LIST, "DOUBLE_LIST"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, FlagTypeString(tt.ft))
		})
	}
}
