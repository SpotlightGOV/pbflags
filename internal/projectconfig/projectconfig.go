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
	FeaturesPath    string `yaml:"features_path"`
	DescriptorsPath string `yaml:"descriptors_path"`
	ProtoPath       string `yaml:"proto_path"`
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
	return c.resolvePath(c.FeaturesPath, projectRoot)
}

// DescriptorsFile returns the absolute path to the descriptors file,
// resolved relative to the project root. Returns empty string if
// descriptors_path is not configured.
func (c Config) DescriptorsFile(projectRoot string) string {
	return c.resolvePath(c.DescriptorsPath, projectRoot)
}

// ProtoDir returns the absolute path to the proto directory,
// resolved relative to the project root. Returns empty string if
// proto_path is not configured.
func (c Config) ProtoDir(projectRoot string) string {
	return c.resolvePath(c.ProtoPath, projectRoot)
}

func (c Config) resolvePath(p, projectRoot string) string {
	if p == "" {
		return ""
	}
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(projectRoot, p)
}
