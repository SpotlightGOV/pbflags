// Package projectconfig loads .pbflags.yaml project configuration.
// The CLI discovers the file by walking up from the working directory,
// similar to buf.yaml or .goreleaser.yaml.
package projectconfig

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

const FileName = ".pbflags.yaml"

// Config holds project-level pbflags configuration.
type Config struct {
	FeaturesPath string `yaml:"features_path"`
}

// Discover walks up from startDir looking for .pbflags.yaml.
// Returns the parsed config and the directory it was found in.
// Returns a zero Config and empty dir if no file is found (not an error).
func Discover(startDir string) (Config, string, error) {
	dir, err := filepath.Abs(startDir)
	if err != nil {
		return Config{}, "", err
	}
	for {
		path := filepath.Join(dir, FileName)
		data, err := os.ReadFile(path)
		if err == nil {
			var cfg Config
			if err := yaml.Unmarshal(data, &cfg); err != nil {
				return Config{}, "", fmt.Errorf("parse %s: %w", path, err)
			}
			return cfg, dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return Config{}, "", nil // reached filesystem root
		}
		dir = parent
	}
}

// FeaturesDir returns the absolute path to the features directory,
// resolved relative to the project root. Returns empty string if
// features_path is not configured.
func (c Config) FeaturesDir(projectRoot string) string {
	if c.FeaturesPath == "" {
		return ""
	}
	if filepath.IsAbs(c.FeaturesPath) {
		return c.FeaturesPath
	}
	return filepath.Join(projectRoot, c.FeaturesPath)
}
