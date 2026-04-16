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
		require.NotEmpty(t, emailFlag.Conditions, "static value should produce conditions")
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
		require.NotEmpty(t, emailFlag.Conditions)
		require.NotEmpty(t, emailFlag.DimensionMetadata, "conditions with CEL should produce dimension metadata")
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

// TestCompileStaticValueOverridesDefault verifies that a YAML static value
// override replaces the proto default in the compiled bundle's DefaultValue.
// Regression test for pb-ecs: YAML value overrides were silently dropped.
func TestCompileStaticValueOverridesDefault(t *testing.T) {
	t.Parallel()

	descData, err := buildDescriptorSet(&example.Notifications{}, &example.EvaluationContext{})
	require.NoError(t, err)

	configDir := t.TempDir()

	// Proto default for max_retries is 3; override to 10 via YAML.
	configYAML := `
feature: notifications
flags:
  max_retries:
    value: 10
`
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "notifications.yaml"), []byte(configYAML), 0o644))

	bundleData, err := Compile(descData, configDir)
	require.NoError(t, err)

	bundle := &pbflagsv1.CompiledBundle{}
	require.NoError(t, proto.Unmarshal(bundleData, bundle))

	// Find the max_retries flag.
	var retriesFlag *pbflagsv1.CompiledFlag
	for _, f := range bundle.Features {
		if f.FeatureId == "notifications" {
			for _, fl := range f.Flags {
				if fl.Name == "max_retries" {
					retriesFlag = fl
				}
			}
		}
	}
	require.NotNil(t, retriesFlag, "max_retries flag should be in bundle")

	// The DefaultValue must reflect the YAML override (10), not the proto default (3).
	var defaultVal pbflagsv1.FlagValue
	require.NoError(t, proto.Unmarshal(retriesFlag.DefaultValue, &defaultVal))
	require.Equal(t, int64(10), defaultVal.GetInt64Value(),
		"DefaultValue should be the YAML override (10), not proto default (3)")
}

// TestCompileOtherwiseOverridesDefault verifies that a condition chain with
// only an otherwise clause also overrides the proto default in DefaultValue.
func TestCompileOtherwiseOverridesDefault(t *testing.T) {
	t.Parallel()

	descData, err := buildDescriptorSet(&example.Notifications{}, &example.EvaluationContext{})
	require.NoError(t, err)

	configDir := t.TempDir()

	// Proto default for max_retries is 3; override via bare otherwise.
	configYAML := `
feature: notifications
flags:
  max_retries:
    conditions:
      - otherwise: 10
`
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "notifications.yaml"), []byte(configYAML), 0o644))

	bundleData, err := Compile(descData, configDir)
	require.NoError(t, err)

	bundle := &pbflagsv1.CompiledBundle{}
	require.NoError(t, proto.Unmarshal(bundleData, bundle))

	var retriesFlag *pbflagsv1.CompiledFlag
	for _, f := range bundle.Features {
		if f.FeatureId == "notifications" {
			for _, fl := range f.Flags {
				if fl.Name == "max_retries" {
					retriesFlag = fl
				}
			}
		}
	}
	require.NotNil(t, retriesFlag)

	var defaultVal pbflagsv1.FlagValue
	require.NoError(t, proto.Unmarshal(retriesFlag.DefaultValue, &defaultVal))
	require.Equal(t, int64(10), defaultVal.GetInt64Value(),
		"DefaultValue should be the otherwise value (10), not proto default (3)")
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
