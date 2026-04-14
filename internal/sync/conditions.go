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
	hashableDims := celenv.HashableDimsFromDescriptor(contextMsg)
	scopeDims := celenv.ScopeDimsFromFiles(files, contextMsg)
	featureScopes := celenv.FeatureScopesFromFiles(files)
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

	// ── Pass 1: parse all feature configs. ──
	parsedConfigs := map[string]*configfile.Config{}
	for _, entry := range entries {
		if entry.IsDir() || !isYAML(entry.Name()) {
			continue
		}
		path := filepath.Join(configDir, entry.Name())
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return ConditionResult{}, fmt.Errorf("read %s: %w", entry.Name(), readErr)
		}
		featureID, peekErr := peekFeature(data)
		if peekErr != nil {
			return ConditionResult{}, fmt.Errorf("%s: %w", entry.Name(), peekErr)
		}
		featureFlags, ok := idx[featureID]
		if !ok {
			return ConditionResult{}, fmt.Errorf("%s: feature %q not found in proto descriptors", entry.Name(), featureID)
		}
		flagTypes := make(map[string]pbflagsv1.FlagType, len(featureFlags))
		for name, info := range featureFlags {
			flagTypes[name] = info.FlagType
		}
		cfg, warns, parseErr := configfile.Parse(data, flagTypes)
		if parseErr != nil {
			return ConditionResult{}, fmt.Errorf("%s: %w", entry.Name(), parseErr)
		}
		result.Warnings = append(result.Warnings, warns...)
		parsedConfigs[featureID] = cfg
	}

	// Collect and validate launches (references, scope, dimensions, scope-presence).
	lc, err := CollectLaunches(parsedConfigs, configDir, hashableDims, scopeDims, featureScopes)
	if err != nil {
		return ConditionResult{}, err
	}

	// ── Pass 2: compile and write flags + upsert launches. ──

	for featureID, cfg := range parsedConfigs {
		featureFlags := idx[featureID]
		processedFeatures[featureID] = true
		logger.Info("processing config", "feature", featureID, "flags", len(cfg.Flags))

		for flagName, entry := range cfg.Flags {
			info := featureFlags[flagName]
			condJSON, dimJSON, warns, compileErr := compileFlag(flagName, entry, compiler, boundedDims)
			if compileErr != nil {
				return ConditionResult{}, fmt.Errorf("feature %s flag %q: %w", featureID, flagName, compileErr)
			}
			result.Warnings = append(result.Warnings, warns...)

			var cv *string
			if condJSON != nil {
				cv = &celVersion
			}
			if _, err := tx.Exec(ctx,
				`UPDATE feature_flags.flags
				 SET conditions = $2, dimension_metadata = $3, cel_version = $4,
				     updated_at = now()
				 WHERE flag_id = $1`,
				info.FlagID, condJSON, dimJSON, cv,
			); err != nil {
				return ConditionResult{}, fmt.Errorf("update flag %q: %w", info.FlagID, err)
			}
			result.FlagsUpdated++
		}

		// Upsert feature-scoped launches.
		for launchID, launch := range cfg.Launches {
			var rampPct int
			if launch.RampPercentage != nil {
				rampPct = *launch.RampPercentage
			}
			if launch.RampPercentage != nil {
				// Config specifies ramp — authoritative on every sync.
				if _, err := tx.Exec(ctx, `
					INSERT INTO feature_flags.launches
						(launch_id, scope_feature_id, dimension, ramp_percentage, ramp_source, affected_features, description)
					VALUES ($1, $2, $3, $4, 'config', $5, $6)
					ON CONFLICT (launch_id) DO UPDATE SET
						dimension = EXCLUDED.dimension,
						ramp_percentage = EXCLUDED.ramp_percentage,
						ramp_source = 'config',
						affected_features = EXCLUDED.affected_features,
						description = EXCLUDED.description,
						updated_at = now()`,
					launchID, featureID, launch.Dimension, rampPct,
					lc.AffectedFeatures(launchID), launch.Description,
				); err != nil {
					return ConditionResult{}, fmt.Errorf("upsert launch %q: %w", launchID, err)
				}
			} else {
				// No ramp in config — preserve runtime ramp value and source.
				if _, err := tx.Exec(ctx, `
					INSERT INTO feature_flags.launches
						(launch_id, scope_feature_id, dimension, ramp_percentage, affected_features, description)
					VALUES ($1, $2, $3, $4, $5, $6)
					ON CONFLICT (launch_id) DO UPDATE SET
						dimension = EXCLUDED.dimension,
						affected_features = EXCLUDED.affected_features,
						description = EXCLUDED.description,
						updated_at = now()`,
					launchID, featureID, launch.Dimension, rampPct,
					lc.AffectedFeatures(launchID), launch.Description,
				); err != nil {
					return ConditionResult{}, fmt.Errorf("upsert launch %q: %w", launchID, err)
				}
			}
		}
		if len(cfg.Launches) > 0 {
			logger.Info("synced launches", "feature", featureID, "launches", len(cfg.Launches))
		}
	}

	// Upsert cross-feature launches.
	for launchID, def := range lc.Defined {
		if def.ScopeFeatureID != "" {
			continue
		}
		var rampPct int
		if def.Entry.RampPercentage != nil {
			rampPct = *def.Entry.RampPercentage
		}
		if def.Entry.RampPercentage != nil {
			if _, err := tx.Exec(ctx, `
				INSERT INTO feature_flags.launches
					(launch_id, scope_feature_id, dimension, ramp_percentage, ramp_source, affected_features, description)
				VALUES ($1, NULL, $2, $3, 'config', $4, $5)
				ON CONFLICT (launch_id) DO UPDATE SET
					dimension = EXCLUDED.dimension,
					ramp_percentage = EXCLUDED.ramp_percentage,
					ramp_source = 'config',
					affected_features = EXCLUDED.affected_features,
					description = EXCLUDED.description,
					updated_at = now()`,
				launchID, def.Entry.Dimension, rampPct,
				lc.AffectedFeatures(launchID), def.Entry.Description,
			); err != nil {
				return ConditionResult{}, fmt.Errorf("upsert cross-feature launch %q: %w", launchID, err)
			}
		} else {
			if _, err := tx.Exec(ctx, `
				INSERT INTO feature_flags.launches
					(launch_id, scope_feature_id, dimension, ramp_percentage, affected_features, description)
				VALUES ($1, NULL, $2, $3, $4, $5)
				ON CONFLICT (launch_id) DO UPDATE SET
					dimension = EXCLUDED.dimension,
					affected_features = EXCLUDED.affected_features,
					description = EXCLUDED.description,
					updated_at = now()`,
				launchID, def.Entry.Dimension, rampPct,
				lc.AffectedFeatures(launchID), def.Entry.Description,
			); err != nil {
				return ConditionResult{}, fmt.Errorf("upsert cross-feature launch %q: %w", launchID, err)
			}
		}
		logger.Info("synced cross-feature launch", "launch_id", launchID)
	}

	// Abandon stale launches no longer defined in any config.
	if tag, err := tx.Exec(ctx, `
		UPDATE feature_flags.launches SET status = 'ABANDONED', updated_at = now()
		WHERE launch_id != ALL($1)
		  AND status NOT IN ('COMPLETED', 'ABANDONED')`,
		lc.IDs(),
	); err != nil {
		return ConditionResult{}, fmt.Errorf("abandon stale launches: %w", err)
	} else if tag.RowsAffected() > 0 {
		logger.Info("abandoned stale launches", "count", tag.RowsAffected())
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

func compileFlag(
	flagName string,
	entry configfile.FlagEntry,
	compiler *celenv.Compiler,
	boundedDims map[string]bool,
) (condJSON []byte, dimJSON []byte, warnings []string, err error) {
	if entry.Value != nil {
		// Static value — store as a single "otherwise" condition entry
		// so all flag behavior flows through the conditions column.
		fvBytes, marshalErr := protojson.Marshal(entry.Value)
		if marshalErr != nil {
			return nil, nil, nil, fmt.Errorf("marshal static value: %w", marshalErr)
		}
		sc := flagfmt.StoredCondition{CEL: nil, Value: fvBytes}
		// Static value launch override.
		if entry.Launch != nil {
			lvBytes, lvErr := protojson.Marshal(entry.Launch.Value)
			if lvErr != nil {
				return nil, nil, nil, fmt.Errorf("marshal launch override value: %w", lvErr)
			}
			sc.LaunchID = entry.Launch.ID
			sc.LaunchValue = lvBytes
		}
		conds := []flagfmt.StoredCondition{sc}
		condJSON, jsonErr := json.Marshal(conds)
		if jsonErr != nil {
			return nil, nil, nil, fmt.Errorf("marshal static conditions: %w", jsonErr)
		}
		return condJSON, nil, nil, nil
	}

	var conditions []flagfmt.StoredCondition
	var asts []*cel.Ast
	var values []*pbflagsv1.FlagValue

	for i, cond := range entry.Conditions {
		fvBytes, marshalErr := protojson.Marshal(cond.Value)
		if marshalErr != nil {
			return nil, nil, nil, fmt.Errorf("condition %d: marshal value: %w", i, marshalErr)
		}

		sc := flagfmt.StoredCondition{Value: fvBytes, Comment: cond.Comment}

		if cond.When == "" {
			asts = append(asts, nil)
		} else {
			compiled, compileErr := compiler.Compile(cond.When)
			if compileErr != nil {
				return nil, nil, nil, fmt.Errorf("condition %d: %w", i, compileErr)
			}
			celStr := cond.When
			sc.CEL = &celStr
			asts = append(asts, compiled.AST)
		}

		// Per-condition launch override.
		if cond.Launch != nil {
			lvBytes, lvErr := protojson.Marshal(cond.Launch.Value)
			if lvErr != nil {
				return nil, nil, nil, fmt.Errorf("condition %d: marshal launch value: %w", i, lvErr)
			}
			sc.LaunchID = cond.Launch.ID
			sc.LaunchValue = lvBytes
		}

		conditions = append(conditions, sc)
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
