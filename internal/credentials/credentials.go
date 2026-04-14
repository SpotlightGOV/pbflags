// Package credentials manages CLI authentication tokens stored in the user's
// config directory (~/.config/pbflags/credentials.yaml).
//
// Lookup order:
//  1. PBFLAGS_TOKEN environment variable (wins if set)
//  2. ~/.config/pbflags/credentials.yaml
//  3. No token (unauthenticated)
//
// The credentials file is created with 0600 permissions and the package
// refuses to read files that are group- or world-readable.
package credentials

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"

	"gopkg.in/yaml.v3"
)

const (
	dirName  = "pbflags"
	fileName = "credentials.yaml"
)

// Credentials holds stored authentication details.
type Credentials struct {
	Token string `yaml:"token"`
	Actor string `yaml:"actor,omitempty"`
}

// configDir returns the platform-appropriate config directory.
func configDir() (string, error) {
	if d := os.Getenv("PBFLAGS_CONFIG_DIR"); d != "" {
		return d, nil
	}
	// os.UserConfigDir returns ~/.config on Linux, ~/Library/Application Support
	// on macOS, %AppData% on Windows.
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("credentials: cannot determine config directory: %w", err)
	}
	return filepath.Join(base, dirName), nil
}

// Path returns the full path to the credentials file.
func Path() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, fileName), nil
}

// Load returns the active credentials using the lookup order:
// PBFLAGS_TOKEN env var, then credentials.yaml file.
// Returns a zero Credentials (no error) if neither source is available.
func Load() (Credentials, error) {
	// 1. Environment variable wins.
	if tok := os.Getenv("PBFLAGS_TOKEN"); tok != "" {
		return Credentials{
			Token: tok,
			Actor: os.Getenv("PBFLAGS_ACTOR"),
		}, nil
	}

	// 2. Credentials file.
	path, err := Path()
	if err != nil {
		return Credentials{}, err
	}

	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Credentials{}, nil // no credentials, not an error
	}
	if err != nil {
		return Credentials{}, fmt.Errorf("credentials: read %s: %w", path, err)
	}

	// Refuse to use credentials from a file that is group- or world-readable.
	if err := checkPermissions(path); err != nil {
		return Credentials{}, err
	}

	var creds Credentials
	if err := yaml.Unmarshal(data, &creds); err != nil {
		return Credentials{}, fmt.Errorf("credentials: parse %s: %w", path, err)
	}
	return creds, nil
}

// Save writes credentials to ~/.config/pbflags/credentials.yaml with 0600
// permissions, creating the directory if needed.
func Save(creds Credentials) error {
	path, err := Path()
	if err != nil {
		return err
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("credentials: create config dir: %w", err)
	}

	data, err := yaml.Marshal(&creds)
	if err != nil {
		return fmt.Errorf("credentials: marshal: %w", err)
	}

	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("credentials: write %s: %w", path, err)
	}
	return nil
}

// Remove deletes the credentials file if it exists.
func Remove() error {
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("credentials: remove %s: %w", path, err)
	}
	return nil
}

// checkPermissions verifies the credentials file is not group- or
// world-readable. On Windows this check is skipped (ACLs, not Unix mode).
func checkPermissions(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("credentials: stat %s: %w", path, err)
	}
	mode := info.Mode().Perm()
	if mode&0o077 != 0 {
		return fmt.Errorf("credentials: %s has permissions %04o; must not be group- or world-readable (expected 0600). Fix with: chmod 600 %s", path, mode, path)
	}
	return nil
}
