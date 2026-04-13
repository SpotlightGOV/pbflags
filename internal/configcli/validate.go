// Package configcli implements the "config validate" and "config show"
// CLI commands for offline YAML config validation and inspection.
package configcli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/google/cel-go/cel"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
	"github.com/SpotlightGOV/pbflags/internal/celenv"
	"github.com/SpotlightGOV/pbflags/internal/codegen/contextutil"
	"github.com/SpotlightGOV/pbflags/internal/configfile"
	"github.com/SpotlightGOV/pbflags/internal/evaluator"
)

// ValidateResult holds the result of config validation.
type ValidateResult struct {
	Files    int
	Flags    int
	Errors   []string
	Warnings []string
}

// Validate checks all YAML config files in configDir against the proto
// descriptors in descriptorData. No database is required.
func Validate(descriptorData []byte, configDir string) (*ValidateResult, error) {
	files, _, err := evaluator.ParseDescriptorSet(descriptorData)
	if err != nil {
		return nil, fmt.Errorf("parse descriptors: %w", err)
	}

	contextMsg, err := contextutil.DiscoverContextFromFiles(files)
	if err != nil {
		return nil, err
	}

	compiler, err := celenv.NewCompiler(contextMsg)
	if err != nil {
		return nil, fmt.Errorf("create CEL compiler: %w", err)
	}

	defs, err := evaluator.ParseDescriptors(descriptorData)
	if err != nil {
		return nil, fmt.Errorf("parse flag definitions: %w", err)
	}

	// Build per-feature flag type maps.
	featureFlags := map[string]map[string]pbflagsv1.FlagType{}
	for _, d := range defs {
		if featureFlags[d.FeatureID] == nil {
			featureFlags[d.FeatureID] = map[string]pbflagsv1.FlagType{}
		}
		featureFlags[d.FeatureID][d.Name] = d.FlagType
	}

	boundedDims := celenv.BoundedDimsFromDescriptor(contextMsg)

	entries, err := os.ReadDir(configDir)
	if err != nil {
		return nil, fmt.Errorf("read config directory: %w", err)
	}

	result := &ValidateResult{}

	for _, entry := range entries {
		if entry.IsDir() || (!strings.HasSuffix(entry.Name(), ".yaml") && !strings.HasSuffix(entry.Name(), ".yml")) {
			continue
		}
		result.Files++
		path := filepath.Join(configDir, entry.Name())

		fileErrors, fileWarnings, flagCount := validateFile(path, featureFlags, compiler, boundedDims)
		result.Flags += flagCount
		result.Errors = append(result.Errors, fileErrors...)
		result.Warnings = append(result.Warnings, fileWarnings...)
	}

	return result, nil
}

func validateFile(
	path string,
	featureFlags map[string]map[string]pbflagsv1.FlagType,
	compiler *celenv.Compiler,
	boundedDims map[string]bool,
) (errs []string, warns []string, flagCount int) {
	data, err := os.ReadFile(path)
	if err != nil {
		return []string{fmt.Sprintf("%s: %v", filepath.Base(path), err)}, nil, 0
	}

	// Peek at feature name.
	var peek struct {
		Feature string `yaml:"feature"`
	}
	if err := yaml.Unmarshal(data, &peek); err != nil {
		return []string{fmt.Sprintf("%s: invalid YAML: %v", filepath.Base(path), err)}, nil, 0
	}

	flagTypes, ok := featureFlags[peek.Feature]
	if !ok {
		return []string{fmt.Sprintf("%s: feature %q not found in proto descriptors", filepath.Base(path), peek.Feature)}, nil, 0
	}

	cfg, parseWarnings, parseErr := configfile.Parse(data, flagTypes)
	if parseErr != nil {
		return []string{fmt.Sprintf("%s: %v", filepath.Base(path), parseErr)}, nil, 0
	}
	for _, w := range parseWarnings {
		warns = append(warns, fmt.Sprintf("%s: %s", filepath.Base(path), w))
	}

	flagCount = len(cfg.Flags)

	// Compile CEL expressions and classify dimensions.
	for flagName, entry := range cfg.Flags {
		var asts []*cel.Ast
		var values []*pbflagsv1.FlagValue
		for i, cond := range entry.Conditions {
			if cond.When == "" {
				asts = append(asts, nil)
				values = append(values, cond.Value)
				continue
			}
			compiled, compileErr := compiler.Compile(cond.When)
			if compileErr != nil {
				errs = append(errs, fmt.Sprintf("%s: flag %q condition %d: %v", filepath.Base(path), flagName, i, compileErr))
				asts = append(asts, nil)
			} else {
				asts = append(asts, compiled.AST)
			}
			values = append(values, cond.Value)
		}

		// Warn on unbounded dimensions (same as sync would).
		if len(asts) > 0 {
			dimMeta := celenv.ClassifyDimensions(asts, values, boundedDims)
			for dimName, meta := range dimMeta {
				if meta.Classification == celenv.Unbounded {
					warns = append(warns, fmt.Sprintf("%s: flag %q: dimension %q is unbounded (cache will use LRU)", filepath.Base(path), flagName, dimName))
				}
			}
		}
	}

	return errs, warns, flagCount
}
