package credentials

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PBFLAGS_CONFIG_DIR", dir)
	t.Setenv("PBFLAGS_TOKEN", "")

	creds := Credentials{Token: "my-token", Actor: "alice@example.com"}
	require.NoError(t, Save(creds))

	// Verify file permissions.
	path := filepath.Join(dir, fileName)
	info, err := os.Stat(path)
	require.NoError(t, err)
	if runtime.GOOS != "windows" {
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "credentials file should be 0600")
	}

	loaded, err := Load()
	require.NoError(t, err)
	assert.Equal(t, creds, loaded)
}

func TestLoad_EnvVarWins(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PBFLAGS_CONFIG_DIR", dir)

	// Save file credentials.
	require.NoError(t, Save(Credentials{Token: "file-token", Actor: "file-actor"}))

	// Env var should win.
	t.Setenv("PBFLAGS_TOKEN", "env-token")
	t.Setenv("PBFLAGS_ACTOR", "env-actor")

	loaded, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "env-token", loaded.Token)
	assert.Equal(t, "env-actor", loaded.Actor)
}

func TestLoad_NoCredentials(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PBFLAGS_CONFIG_DIR", dir)
	t.Setenv("PBFLAGS_TOKEN", "")

	loaded, err := Load()
	require.NoError(t, err)
	assert.Equal(t, Credentials{}, loaded)
}

func TestLoad_WorldReadableRejected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission check not applicable on Windows")
	}

	dir := t.TempDir()
	t.Setenv("PBFLAGS_CONFIG_DIR", dir)
	t.Setenv("PBFLAGS_TOKEN", "")

	require.NoError(t, Save(Credentials{Token: "tok"}))

	// Make it world-readable.
	path := filepath.Join(dir, fileName)
	require.NoError(t, os.Chmod(path, 0o644))

	_, err := Load()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "must not be group- or world-readable")
}

func TestRemove(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PBFLAGS_CONFIG_DIR", dir)
	t.Setenv("PBFLAGS_TOKEN", "")

	require.NoError(t, Save(Credentials{Token: "tok"}))
	require.NoError(t, Remove())

	loaded, err := Load()
	require.NoError(t, err)
	assert.Equal(t, Credentials{}, loaded)
}

func TestRemove_NotExist(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PBFLAGS_CONFIG_DIR", dir)

	// Should not error if file doesn't exist.
	require.NoError(t, Remove())
}

func TestPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PBFLAGS_CONFIG_DIR", dir)

	path, err := Path()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "credentials.yaml"), path)
}
