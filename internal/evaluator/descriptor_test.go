package evaluator

import (
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
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
	require.Equal(t, int32(2), email.Layer, "email_enabled layer (USER)")
	require.NotNil(t, email.Default, "email_enabled default should not be nil")
	require.Equal(t, true, email.Default.GetBoolValue(), "email_enabled default")
	require.Equal(t, "email_enabled", email.Name, "email_enabled name")

	digest, ok := byID["notifications/2"]
	require.True(t, ok, "expected notifications/2 (digest_frequency)")
	require.Equal(t, pbflagsv1.FlagType_FLAG_TYPE_STRING, digest.FlagType, "digest type")
	require.Equal(t, int32(1), digest.Layer, "digest layer (GLOBAL)")
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

func TestDiffFlagIDs(t *testing.T) {
	old := NewDefaults([]FlagDef{
		{FlagID: "f/1"},
		{FlagID: "f/2"},
		{FlagID: "f/3"},
	})
	next := NewDefaults([]FlagDef{
		{FlagID: "f/2"},
		{FlagID: "f/4"},
	})

	added, removed := diffFlagIDs(old, next)

	addedSet := make(map[string]struct{})
	for _, id := range added {
		addedSet[id] = struct{}{}
	}
	removedSet := make(map[string]struct{})
	for _, id := range removed {
		removedSet[id] = struct{}{}
	}

	assert.Contains(t, addedSet, "f/4", "expected f/4 in added set")
	assert.Contains(t, removedSet, "f/1", "expected f/1 in removed set")
	assert.Contains(t, removedSet, "f/3", "expected f/3 in removed set")
	assert.Len(t, added, 1, "added count")
	assert.Len(t, removed, 2, "removed count")
}

func TestDescriptorWatcher_ReloadValid(t *testing.T) {
	path := testdataPath("descriptors.pb")
	reg := NewRegistry(&Defaults{flags: make(map[string]FlagDef)})
	watcher := NewDescriptorWatcher(path, reg, 0, nil, slog.Default())

	require.Equal(t, 0, reg.Load().Len(), "expected 0 flags before reload")
	watcher.tryReload("test")
	require.NotEqual(t, 0, reg.Load().Len(), "expected flags after reload")
}

func TestDescriptorWatcher_ReloadCorrupt(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "bad.pb")
	require.NoError(t, os.WriteFile(tmpFile, []byte("corrupt data"), 0644))

	existing := NewDefaults([]FlagDef{
		{FlagID: "f/1", Default: boolVal(true)},
	})
	reg := NewRegistry(existing)
	watcher := NewDescriptorWatcher(tmpFile, reg, 0, nil, slog.Default())

	watcher.tryReload("test")
	require.Equal(t, 1, reg.Load().Len(), "expected 1 flag after corrupt reload")
}

func TestDescriptorWatcher_ReloadMissingFile(t *testing.T) {
	reg := NewRegistry(NewDefaults([]FlagDef{{FlagID: "f/1"}}))
	watcher := NewDescriptorWatcher("/nonexistent/path.pb", reg, 0, nil, slog.Default())

	watcher.tryReload("test")
	require.Equal(t, 1, reg.Load().Len(), "expected 1 flag after missing file reload")
}
