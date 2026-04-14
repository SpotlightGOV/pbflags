package main

import (
	"bytes"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
	"github.com/SpotlightGOV/pbflags/internal/configfile"
	"github.com/SpotlightGOV/pbflags/internal/evaluator"
)

func runFormat(args []string) {
	fs := flag.NewFlagSet("pb format", flag.ExitOnError)
	descriptors := fs.String("descriptors", "", "path to descriptors.pb (or PBFLAGS_DESCRIPTORS)")
	configDir := fs.String("features", "", "directory of YAML config files (or PBFLAGS_FEATURES)")
	check := fs.Bool("check", false, "Check formatting without writing; exit 1 if any file would change")
	fs.Parse(args)

	resolveEnv(nil, descriptors, configDir, nil)
	resolveProjectConfig(descriptors, configDir)

	if *descriptors == "" || *configDir == "" {
		slog.Error("--descriptors and --features are required (or set PBFLAGS_DESCRIPTORS / PBFLAGS_FEATURES)")
		os.Exit(1)
	}

	descData, err := os.ReadFile(*descriptors)
	if err != nil {
		slog.Error("read descriptors", "error", err)
		os.Exit(1)
	}

	defs, err := evaluator.ParseDescriptors(descData)
	if err != nil {
		slog.Error("parse descriptors", "error", err)
		os.Exit(1)
	}

	// Build per-feature flag type maps.
	featureFlags := map[string]map[string]pbflagsv1.FlagType{}
	for _, d := range defs {
		if featureFlags[d.FeatureID] == nil {
			featureFlags[d.FeatureID] = map[string]pbflagsv1.FlagType{}
		}
		featureFlags[d.FeatureID][d.Name] = d.FlagType
	}

	entries, err := os.ReadDir(*configDir)
	if err != nil {
		slog.Error("read config directory", "error", err)
		os.Exit(1)
	}

	unformatted := 0
	formatted := 0

	for _, entry := range entries {
		if entry.IsDir() || (!strings.HasSuffix(entry.Name(), ".yaml") && !strings.HasSuffix(entry.Name(), ".yml")) {
			continue
		}
		path := filepath.Join(*configDir, entry.Name())
		changed, err := formatFeatureFile(path, featureFlags, *check)
		if err != nil {
			slog.Error("format", "file", entry.Name(), "error", err)
			unformatted++
			continue
		}
		if changed {
			unformatted++
		} else {
			formatted++
		}
	}

	// Format cross-feature launch files.
	launchDir := filepath.Join(*configDir, "launches")
	if launchEntries, err := os.ReadDir(launchDir); err == nil {
		for _, entry := range launchEntries {
			if entry.IsDir() || (!strings.HasSuffix(entry.Name(), ".yaml") && !strings.HasSuffix(entry.Name(), ".yml")) {
				continue
			}
			path := filepath.Join(launchDir, entry.Name())
			changed, err := formatCrossFeatureLaunchFile(path, *check)
			if err != nil {
				slog.Error("format", "file", filepath.Join("launches", entry.Name()), "error", err)
				unformatted++
				continue
			}
			if changed {
				unformatted++
			} else {
				formatted++
			}
		}
	}

	if *check {
		if unformatted > 0 {
			fmt.Fprintf(os.Stderr, "%d file(s) need formatting\n", unformatted)
			os.Exit(1)
		}
		fmt.Printf("All %d file(s) formatted correctly\n", formatted)
		return
	}

	total := formatted + unformatted
	if unformatted > 0 {
		fmt.Printf("Formatted %d of %d file(s)\n", unformatted, total)
	} else {
		fmt.Printf("All %d file(s) already formatted\n", total)
	}
}

// formatFeatureFile formats a single feature config file. Returns true if
// the file was changed (or would be changed in check mode).
func formatFeatureFile(path string, featureFlags map[string]map[string]pbflagsv1.FlagType, check bool) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}

	// Peek at feature name to find flag types.
	var peek struct {
		Feature string `yaml:"feature"`
	}
	if err := yaml.Unmarshal(data, &peek); err != nil {
		return false, fmt.Errorf("invalid YAML: %w", err)
	}
	flagTypes, ok := featureFlags[peek.Feature]
	if !ok {
		return false, fmt.Errorf("feature %q not found in proto descriptors", peek.Feature)
	}

	cfg, _, err := configfile.Parse(data, flagTypes)
	if err != nil {
		return false, fmt.Errorf("parse: %w", err)
	}

	canonical, err := configfile.Marshal(cfg)
	if err != nil {
		return false, fmt.Errorf("marshal: %w", err)
	}

	if bytes.Equal(data, canonical) {
		return false, nil
	}

	if check {
		fmt.Printf("%s needs formatting\n", filepath.Base(path))
		return true, nil
	}

	if err := os.WriteFile(path, canonical, 0o644); err != nil {
		return false, err
	}
	fmt.Printf("formatted %s\n", filepath.Base(path))
	return true, nil
}

// formatCrossFeatureLaunchFile formats a standalone cross-feature launch file.
func formatCrossFeatureLaunchFile(path string, check bool) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}

	entry, err := configfile.ParseCrossFeatureLaunch(data)
	if err != nil {
		return false, fmt.Errorf("parse: %w", err)
	}

	canonical, err := configfile.MarshalCrossFeatureLaunch(entry)
	if err != nil {
		return false, fmt.Errorf("marshal: %w", err)
	}

	if bytes.Equal(data, canonical) {
		return false, nil
	}

	if check {
		fmt.Printf("%s needs formatting\n", filepath.Join("launches", filepath.Base(path)))
		return true, nil
	}

	if err := os.WriteFile(path, canonical, 0o644); err != nil {
		return false, err
	}
	fmt.Printf("formatted %s\n", filepath.Join("launches", filepath.Base(path)))
	return true, nil
}
