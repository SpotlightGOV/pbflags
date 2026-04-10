package evaluator

import (
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testdataPath(name string) string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "testdata", name)
}

func TestParseDescriptorFile_RealDescriptors(t *testing.T) {
	path := testdataPath("descriptors.pb")
	defs, err := ParseDescriptorFile(path)
	require.NoError(t, err, "ParseDescriptorFile")
	require.NotEmpty(t, defs, "expected at least one flag definition from real descriptors")

	byID := make(map[string]FlagDef, len(defs))
	for _, d := range defs {
		byID[d.FlagID] = d
	}

	email, ok := byID["notifications/1"]
	require.True(t, ok, "expected notifications/1 (email_enabled) in parsed defs")
	require.Equal(t, "notifications", email.FeatureID, "email_enabled.FeatureID")
	require.Equal(t, pbflagsv1.FlagType_FLAG_TYPE_BOOL, email.FlagType, "email_enabled type")
	require.Equal(t, "user", email.Layer, "email_enabled layer")
	require.NotNil(t, email.Default, "email_enabled default should not be nil")
	require.Equal(t, true, email.Default.GetBoolValue(), "email_enabled default")
	require.Equal(t, "email_enabled", email.Name, "email_enabled name")

	digest, ok := byID["notifications/2"]
	require.True(t, ok, "expected notifications/2 (digest_frequency)")
	require.Equal(t, pbflagsv1.FlagType_FLAG_TYPE_STRING, digest.FlagType, "digest type")
	require.Equal(t, "", digest.Layer, "digest layer (global = empty)")
	require.NotNil(t, digest.Default, "digest default should not be nil")
	require.Equal(t, "daily", digest.Default.GetStringValue(), "digest default")

	retries, ok := byID["notifications/3"]
	require.True(t, ok, "expected notifications/3 (max_retries)")
	require.Equal(t, pbflagsv1.FlagType_FLAG_TYPE_INT64, retries.FlagType, "max_retries type")
	require.NotNil(t, retries.Default, "max_retries default should not be nil")
	require.Equal(t, int64(3), retries.Default.GetInt64Value(), "max_retries default")

	score, ok := byID["notifications/4"]
	require.True(t, ok, "expected notifications/4 (score_threshold)")
	require.Equal(t, pbflagsv1.FlagType_FLAG_TYPE_DOUBLE, score.FlagType, "score_threshold type")
	require.NotNil(t, score.Default, "score_threshold default should not be nil")
	require.Equal(t, 0.75, score.Default.GetDoubleValue(), "score_threshold default")
}

func TestParseDescriptorFile_ListFlags(t *testing.T) {
	path := testdataPath("descriptors.pb")
	defs, err := ParseDescriptorFile(path)
	require.NoError(t, err, "ParseDescriptorFile")

	byID := make(map[string]FlagDef, len(defs))
	for _, d := range defs {
		byID[d.FlagID] = d
	}

	// notification_emails — STRING_LIST, entity layer
	emails, ok := byID["notifications/5"]
	require.True(t, ok, "expected notifications/5 (notification_emails) in parsed defs")
	assert.Equal(t, pbflagsv1.FlagType_FLAG_TYPE_STRING_LIST, emails.FlagType, "notification_emails type")
	assert.Equal(t, "entity", emails.Layer, "notification_emails layer")
	assert.False(t, emails.IsGlobalLayer(), "notification_emails should not be global")
	require.NotNil(t, emails.Default, "notification_emails default should not be nil")
	slv := emails.Default.GetStringListValue()
	require.NotNil(t, slv, "notification_emails default should be a StringList")
	assert.Equal(t, []string{"ops@example.com"}, slv.GetValues(), "notification_emails default values")

	// retry_delays — INT64_LIST, global layer
	delays, ok := byID["notifications/6"]
	require.True(t, ok, "expected notifications/6 (retry_delays) in parsed defs")
	assert.Equal(t, pbflagsv1.FlagType_FLAG_TYPE_INT64_LIST, delays.FlagType, "retry_delays type")
	assert.Equal(t, "", delays.Layer, "retry_delays layer (global = empty)")
	assert.True(t, delays.IsGlobalLayer(), "retry_delays should be global")
	require.NotNil(t, delays.Default, "retry_delays default should not be nil")
	ilv := delays.Default.GetInt64ListValue()
	require.NotNil(t, ilv, "retry_delays default should be an Int64List")
	assert.Equal(t, []int64{1, 5, 30}, ilv.GetValues(), "retry_delays default values")
}

func TestParseDescriptorFile_AllEightTypes(t *testing.T) {
	path := testdataPath("descriptors.pb")
	defs, err := ParseDescriptorFile(path)
	require.NoError(t, err, "ParseDescriptorFile")

	types := make(map[pbflagsv1.FlagType]bool)
	for _, d := range defs {
		types[d.FlagType] = true
	}

	for _, ft := range []pbflagsv1.FlagType{
		pbflagsv1.FlagType_FLAG_TYPE_BOOL,
		pbflagsv1.FlagType_FLAG_TYPE_STRING,
		pbflagsv1.FlagType_FLAG_TYPE_INT64,
		pbflagsv1.FlagType_FLAG_TYPE_DOUBLE,
		pbflagsv1.FlagType_FLAG_TYPE_STRING_LIST,
		pbflagsv1.FlagType_FLAG_TYPE_INT64_LIST,
	} {
		assert.True(t, types[ft], "expected flag type %v in parsed defs", ft)
	}
}

func TestParseDescriptorFile_AllFourTypes(t *testing.T) {
	path := testdataPath("descriptors.pb")
	defs, err := ParseDescriptorFile(path)
	require.NoError(t, err, "ParseDescriptorFile")

	types := make(map[pbflagsv1.FlagType]bool)
	for _, d := range defs {
		types[d.FlagType] = true
	}

	for _, ft := range []pbflagsv1.FlagType{
		pbflagsv1.FlagType_FLAG_TYPE_BOOL,
		pbflagsv1.FlagType_FLAG_TYPE_STRING,
		pbflagsv1.FlagType_FLAG_TYPE_INT64,
		pbflagsv1.FlagType_FLAG_TYPE_DOUBLE,
	} {
		assert.True(t, types[ft], "expected flag type %v in parsed defs", ft)
	}
}

func TestParseDescriptors_CorruptData(t *testing.T) {
	_, err := ParseDescriptors([]byte("this is not a valid protobuf"))
	require.Error(t, err, "expected error for corrupt descriptor data")
}

func TestParseDescriptors_EmptyData(t *testing.T) {
	_, err := ParseDescriptors([]byte{})
	require.Error(t, err, "expected error for empty descriptor data")
}

func TestParseDescriptorFile_MissingFile(t *testing.T) {
	_, err := ParseDescriptorFile("/nonexistent/path/descriptors.pb")
	require.Error(t, err, "expected error for missing file")
}

func TestParseDescriptorFile_IsGlobalLayer(t *testing.T) {
	path := testdataPath("descriptors.pb")
	defs, err := ParseDescriptorFile(path)
	require.NoError(t, err, "ParseDescriptorFile")

	byID := make(map[string]FlagDef, len(defs))
	for _, d := range defs {
		byID[d.FlagID] = d
	}

	email := byID["notifications/1"]
	require.False(t, email.IsGlobalLayer(), "email_enabled (USER layer) should not be global")

	digest := byID["notifications/2"]
	require.True(t, digest.IsGlobalLayer(), "digest_frequency (GLOBAL layer) should be global")
}

func TestDescriptorWatcher_ReloadValid(t *testing.T) {
	path := testdataPath("descriptors.pb")
	watcher := NewDescriptorWatcher(path, 0, nil, slog.Default())

	// No sync callback — just verify tryReload does not panic.
	watcher.tryReload("test")
}

func TestDescriptorWatcher_ReloadCorrupt(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "bad.pb")
	require.NoError(t, os.WriteFile(tmpFile, []byte("corrupt data"), 0644))

	watcher := NewDescriptorWatcher(tmpFile, 0, nil, slog.Default())

	// Should log error but not panic.
	watcher.tryReload("test")
}

func TestDescriptorWatcher_ReloadMissingFile(t *testing.T) {
	watcher := NewDescriptorWatcher("/nonexistent/path.pb", 0, nil, slog.Default())

	// Should log error but not panic.
	watcher.tryReload("test")
}
