package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"

	"github.com/google/cel-go/cel"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/encoding/protojson"
	"gopkg.in/yaml.v3"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
	"github.com/SpotlightGOV/pbflags/internal/celenv"
	"github.com/SpotlightGOV/pbflags/internal/codegen/contextutil"
	"github.com/SpotlightGOV/pbflags/internal/configfile"
	"github.com/SpotlightGOV/pbflags/internal/evaluator"
	"github.com/SpotlightGOV/pbflags/internal/flagfmt"
)

// ConditionResult reports what the condition sync did.
type ConditionResult struct {
	FlagsUpdated int
	Warnings     []string
}

// featureIndex maps feature ID → (flag name → flagInfo).
type featureIndex map[string]map[string]flagInfo

type flagInfo struct {
	FlagID   string
	FlagType pbflagsv1.FlagType
}

func buildFeatureIndex(defs []evaluator.FlagDef) featureIndex {
	idx := featureIndex{}
	for _, d := range defs {
		if idx[d.FeatureID] == nil {
			idx[d.FeatureID] = map[string]flagInfo{}
		}
		idx[d.FeatureID][d.Name] = flagInfo{FlagID: d.FlagID, FlagType: d.FlagType}
	}
	return idx
}

// SyncConditions compiles YAML config files from configDir and writes
// conditions, dimension_metadata, and cel_version to the flags table.
func SyncConditions(
	ctx context.Context,
	conn *pgx.Conn,
	configDir string,
	descriptorData []byte,
	defs []evaluator.FlagDef,
	logger *slog.Logger,
	sha string,
) (ConditionResult, error) {
	files, _, err := evaluator.ParseDescriptorSet(descriptorData)
	if err != nil {
		return ConditionResult{}, fmt.Errorf("parse descriptor set: %w", err)
	}

	contextMsg, err := contextutil.DiscoverContextFromFiles(files)
	if err != nil {
		return ConditionResult{}, err
	}
	logger.Info("discovered evaluation context", "message", contextMsg.FullName())

	compiler, err := celenv.NewCompiler(contextMsg)
	if err != nil {
		return ConditionResult{}, fmt.Errorf("create CEL compiler: %w", err)
	}

	boundedDims := celenv.BoundedDimsFromDescriptor(contextMsg)
	celVersion := getCELVersion()
	idx := buildFeatureIndex(defs)

	entries, err := os.ReadDir(configDir)
	if err != nil {
		return ConditionResult{}, fmt.Errorf("read config directory %q: %w", configDir, err)
	}

	tx, err := conn.Begin(ctx)
	if err != nil {
		return ConditionResult{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var result ConditionResult
	processedFeatures := map[string]bool{}

	for _, entry := range entries {
		if entry.IsDir() || !isYAML(entry.Name()) {
			continue
		}
		path := filepath.Join(configDir, entry.Name())
		featureID, n, warns, err := processConfigFile(ctx, tx, path, idx, compiler, boundedDims, celVersion, logger)
		if err != nil {
			return ConditionResult{}, fmt.Errorf("config %s: %w", entry.Name(), err)
		}
		processedFeatures[featureID] = true
		result.FlagsUpdated += n
		result.Warnings = append(result.Warnings, warns...)
	}

	// Clear stale conditions for features that no longer have config files.
	for featureID := range idx {
		if processedFeatures[featureID] {
			continue
		}
		tag, err := tx.Exec(ctx,
			`UPDATE feature_flags.flags
			 SET conditions = NULL, dimension_metadata = NULL, cel_version = NULL, updated_at = now()
			 WHERE feature_id = $1 AND conditions IS NOT NULL`,
			featureID,
		)
		if err != nil {
			return ConditionResult{}, fmt.Errorf("clear stale conditions for %q: %w", featureID, err)
		}
		if tag.RowsAffected() > 0 {
			logger.Info("cleared stale conditions", "feature", featureID, "flags", tag.RowsAffected())
		}
	}

	// Update sync_sha for processed features.
	if sha != "" {
		for featureID := range processedFeatures {
			if _, err := tx.Exec(ctx,
				`UPDATE feature_flags.features SET sync_sha = $2, updated_at = now() WHERE feature_id = $1`,
				featureID, sha,
			); err != nil {
				return ConditionResult{}, fmt.Errorf("update sync_sha for %q: %w", featureID, err)
			}
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return ConditionResult{}, fmt.Errorf("commit transaction: %w", err)
	}

	return result, nil
}

func processConfigFile(
	ctx context.Context,
	tx pgx.Tx,
	path string,
	idx featureIndex,
	compiler *celenv.Compiler,
	boundedDims map[string]bool,
	celVersion string,
	logger *slog.Logger,
) (featureID string, updated int, warnings []string, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", 0, nil, err
	}

	featureID, err = peekFeature(data)
	if err != nil {
		return "", 0, nil, err
	}

	featureFlags, ok := idx[featureID]
	if !ok {
		return featureID, 0, nil, fmt.Errorf("feature %q not found in proto descriptors", featureID)
	}

	flagTypes := make(map[string]pbflagsv1.FlagType, len(featureFlags))
	for name, info := range featureFlags {
		flagTypes[name] = info.FlagType
	}

	var cfg *configfile.Config
	cfg, warnings, err = configfile.Parse(data, flagTypes)
	if err != nil {
		return featureID, 0, nil, fmt.Errorf("parse: %w", err)
	}

	logger.Info("processing config", "feature", featureID, "flags", len(cfg.Flags))

	for flagName, entry := range cfg.Flags {
		info := featureFlags[flagName]

		condJSON, dimJSON, warns, compileErr := compileFlag(flagName, entry, compiler, boundedDims)
		if compileErr != nil {
			return featureID, 0, nil, fmt.Errorf("flag %q: %w", flagName, compileErr)
		}
		warnings = append(warnings, warns...)

		// Only set cel_version when conditions are present; static-value
		// flags get NULL for all three condition columns.
		var cv *string
		if condJSON != nil {
			cv = &celVersion
		}
		if _, err := tx.Exec(ctx,
			`UPDATE feature_flags.flags
			 SET conditions = $2, dimension_metadata = $3, cel_version = $4, updated_at = now()
			 WHERE flag_id = $1`,
			info.FlagID, condJSON, dimJSON, cv,
		); err != nil {
			return featureID, 0, nil, fmt.Errorf("update flag %q: %w", info.FlagID, err)
		}
		updated++
	}

	return featureID, updated, warnings, nil
}

func compileFlag(
	flagName string,
	entry configfile.FlagEntry,
	compiler *celenv.Compiler,
	boundedDims map[string]bool,
) (condJSON []byte, dimJSON []byte, warnings []string, err error) {
	if entry.Value != nil {
		// Static value — no conditions or dimension metadata needed.
		return nil, nil, nil, nil
	}

	var conditions []flagfmt.StoredCondition
	var asts []*cel.Ast
	var values []*pbflagsv1.FlagValue

	for i, cond := range entry.Conditions {
		fvBytes, marshalErr := protojson.Marshal(cond.Value)
		if marshalErr != nil {
			return nil, nil, nil, fmt.Errorf("condition %d: marshal value: %w", i, marshalErr)
		}

		if cond.When == "" {
			conditions = append(conditions, flagfmt.StoredCondition{CEL: nil, Value: fvBytes})
			asts = append(asts, nil)
		} else {
			compiled, compileErr := compiler.Compile(cond.When)
			if compileErr != nil {
				return nil, nil, nil, fmt.Errorf("condition %d: %w", i, compileErr)
			}
			celStr := cond.When
			conditions = append(conditions, flagfmt.StoredCondition{CEL: &celStr, Value: fvBytes})
			asts = append(asts, compiled.AST)
		}
		values = append(values, cond.Value)
	}

	condJSON, err = json.Marshal(conditions)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("marshal conditions: %w", err)
	}

	dimMeta := celenv.ClassifyDimensions(asts, values, boundedDims)
	if len(dimMeta) > 0 {
		dimJSON, err = json.Marshal(dimMeta)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("marshal dimension_metadata: %w", err)
		}
	}

	for name, meta := range dimMeta {
		if meta.Classification == celenv.Unbounded {
			warnings = append(warnings, fmt.Sprintf("flag %q: dimension %q is unbounded (cache will use LRU)", flagName, name))
		}
	}

	return condJSON, dimJSON, warnings, nil
}

func peekFeature(data []byte) (string, error) {
	var peek struct {
		Feature string `yaml:"feature"`
	}
	if err := yaml.Unmarshal(data, &peek); err != nil {
		return "", fmt.Errorf("peek feature: %w", err)
	}
	if peek.Feature == "" {
		return "", fmt.Errorf("missing feature field in config")
	}
	return peek.Feature, nil
}

func getCELVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	for _, dep := range info.Deps {
		if dep.Path == "github.com/google/cel-go" {
			return dep.Version
		}
	}
	return "unknown"
}

func isYAML(name string) bool {
	return strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml")
}
