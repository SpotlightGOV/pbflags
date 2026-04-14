package sync

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"

	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/proto"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
	"github.com/SpotlightGOV/pbflags/internal/celenv"
	"github.com/SpotlightGOV/pbflags/internal/codegen/contextutil"
	"github.com/SpotlightGOV/pbflags/internal/configfile"
	"github.com/SpotlightGOV/pbflags/internal/evaluator"
)

// LoadResult reports what the bundle load did.
type LoadResult struct {
	Features          int
	FlagsUpserted     int
	FlagsArchived     int
	ConditionsUpdated int
}

// Compile reads proto descriptors and YAML config files, compiles everything,
// and returns a serialized CompiledBundle protobuf.
func Compile(descriptorData []byte, configDir string) ([]byte, error) {
	files, _, err := evaluator.ParseDescriptorSet(descriptorData)
	if err != nil {
		return nil, fmt.Errorf("parse descriptor set: %w", err)
	}

	contextMsg, err := contextutil.DiscoverContextFromFiles(files)
	if err != nil {
		return nil, err
	}

	compiler, err := celenv.NewCompiler(contextMsg)
	if err != nil {
		return nil, fmt.Errorf("create CEL compiler: %w", err)
	}

	boundedDims := celenv.BoundedDimsFromDescriptor(contextMsg)

	// Parse descriptors for flag definitions.
	defs, err := evaluator.ParseDescriptors(descriptorData)
	if err != nil {
		return nil, fmt.Errorf("parse descriptors: %w", err)
	}

	// Group defs by feature.
	type featureData struct {
		displayName string
		description string
		owner       string
		flags       []evaluator.FlagDef
	}
	featureMap := map[string]*featureData{}
	for _, d := range defs {
		fd, ok := featureMap[d.FeatureID]
		if !ok {
			fd = &featureData{
				displayName: d.FeatureDisplayName,
				description: d.FeatureDescription,
				owner:       d.FeatureOwner,
			}
			featureMap[d.FeatureID] = fd
		}
		fd.flags = append(fd.flags, d)
	}

	// Build flag type index for config parsing.
	flagTypesByFeature := map[string]map[string]pbflagsv1.FlagType{}
	for _, d := range defs {
		if flagTypesByFeature[d.FeatureID] == nil {
			flagTypesByFeature[d.FeatureID] = map[string]pbflagsv1.FlagType{}
		}
		flagTypesByFeature[d.FeatureID][d.Name] = d.FlagType
	}

	hashableDims := celenv.HashableDimsFromDescriptor(contextMsg)
	scopeDims := celenv.ScopeDimsFromFiles(files, contextMsg)
	featureScopes := celenv.FeatureScopesFromFiles(files)

	bundle := &pbflagsv1.CompiledBundle{
		CelVersion: compileCELVersion(),
	}

	// Process config files.
	entries, err := os.ReadDir(configDir)
	if err != nil {
		return nil, fmt.Errorf("read config directory: %w", err)
	}

	configsByFeature := map[string]*configfile.Config{}
	for _, entry := range entries {
		if entry.IsDir() || !isYAML(entry.Name()) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(configDir, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", entry.Name(), err)
		}

		featureID, err := peekFeature(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", entry.Name(), err)
		}

		flagTypes := flagTypesByFeature[featureID]
		if flagTypes == nil {
			return nil, fmt.Errorf("%s: feature %q not found in descriptors", entry.Name(), featureID)
		}

		cfg, warnings, parseErr := configfile.Parse(data, flagTypes)
		if parseErr != nil {
			return nil, fmt.Errorf("%s: %w", entry.Name(), parseErr)
		}
		for _, w := range warnings {
			slog.Warn(w)
		}
		configsByFeature[featureID] = cfg
	}

	// Collect and validate launches (same rules as SyncConditions).
	lc, err := CollectLaunches(configsByFeature, configDir, hashableDims, scopeDims, featureScopes)
	if err != nil {
		return nil, err
	}

	// Build compiled features.
	for featureID, fd := range featureMap {
		cf := &pbflagsv1.CompiledFeature{
			FeatureId:   featureID,
			DisplayName: fd.displayName,
			Description: fd.description,
			Owner:       fd.owner,
		}

		cfg := configsByFeature[featureID]

		for _, d := range fd.flags {
			cflag := &pbflagsv1.CompiledFlag{
				FlagId:      d.FlagID,
				Name:        d.Name,
				FieldNumber: d.FieldNum,
				FlagType:    FlagTypeString(d.FlagType),
			}
			if d.Default != nil {
				cflag.DefaultValue, _ = proto.Marshal(d.Default)
			}
			if d.SupportedValues != nil {
				cflag.SupportedValues, _ = proto.Marshal(d.SupportedValues)
			}

			// Compile conditions from config.
			if cfg != nil {
				if entry, ok := cfg.Flags[d.Name]; ok {
					condJSON, dimJSON, _, compileErr := compileFlag(d.Name, entry, compiler, boundedDims)
					if compileErr != nil {
						return nil, fmt.Errorf("feature %s flag %s: %w", featureID, d.Name, compileErr)
					}
					cflag.ConditionsJson = condJSON
					cflag.DimensionMetadataJson = dimJSON
				}
			}

			cf.Flags = append(cf.Flags, cflag)
		}

		bundle.Features = append(bundle.Features, cf)
	}

	// Emit all launches to bundle level with correct affected_features.
	for launchID, def := range lc.Defined {
		cl := &pbflagsv1.CompiledLaunch{
			LaunchId:         launchID,
			Dimension:        def.Entry.Dimension,
			ScopeFeatureId:   def.ScopeFeatureID,
			AffectedFeatures: lc.AffectedFeatures(launchID),
			Description:      def.Entry.Description,
		}
		if def.Entry.RampPercentage != nil {
			rp := int32(*def.Entry.RampPercentage)
			cl.RampPercentage = &rp
		}
		bundle.Launches = append(bundle.Launches, cl)
	}

	return proto.Marshal(bundle)
}

// LoadBundle deserializes a CompiledBundle and writes it to the database.
// No proto descriptors or CEL compiler needed — all compilation was done
// at compile time.
func LoadBundle(ctx context.Context, conn *pgx.Conn, bundleData []byte, sha string) (LoadResult, error) {
	bundle := &pbflagsv1.CompiledBundle{}
	if err := proto.Unmarshal(bundleData, bundle); err != nil {
		return LoadResult{}, fmt.Errorf("unmarshal bundle: %w", err)
	}

	tx, err := conn.Begin(ctx)
	if err != nil {
		return LoadResult{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var result LoadResult
	allFlagIDs := map[string]struct{}{}
	featureIDs := []string{}

	for _, cf := range bundle.Features {
		// Upsert feature.
		if _, err := tx.Exec(ctx,
			`INSERT INTO feature_flags.features (feature_id, display_name, description, owner)
			 VALUES ($1, $2, $3, $4)
			 ON CONFLICT (feature_id) DO UPDATE SET
			   display_name = EXCLUDED.display_name,
			   description = EXCLUDED.description,
			   owner = EXCLUDED.owner,
			   updated_at = now()`,
			cf.FeatureId, cf.DisplayName, cf.Description, cf.Owner,
		); err != nil {
			return LoadResult{}, fmt.Errorf("upsert feature %q: %w", cf.FeatureId, err)
		}
		result.Features++
		featureIDs = append(featureIDs, cf.FeatureId)

		// Upsert flags with pre-compiled conditions.
		for _, fl := range cf.Flags {
			allFlagIDs[fl.FlagId] = struct{}{}

			var cv *string
			if fl.ConditionsJson != nil {
				v := bundle.CelVersion
				cv = &v
			}

			if _, err := tx.Exec(ctx,
				`INSERT INTO feature_flags.flags
				   (flag_id, feature_id, field_number, display_name, flag_type, description,
				    default_value, supported_values, conditions, dimension_metadata, cel_version)
				 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
				 ON CONFLICT (flag_id) DO UPDATE SET
				   display_name = EXCLUDED.display_name,
				   flag_type = EXCLUDED.flag_type,
				   description = EXCLUDED.description,
				   default_value = EXCLUDED.default_value,
				   supported_values = EXCLUDED.supported_values,
				   conditions = EXCLUDED.conditions,
				   dimension_metadata = EXCLUDED.dimension_metadata,
				   cel_version = EXCLUDED.cel_version,
				   archived_at = NULL,
				   updated_at = now()`,
				fl.FlagId, cf.FeatureId, fl.FieldNumber, fl.Name, fl.FlagType, "",
				fl.DefaultValue, fl.SupportedValues, fl.ConditionsJson, fl.DimensionMetadataJson, cv,
			); err != nil {
				return LoadResult{}, fmt.Errorf("upsert flag %q: %w", fl.FlagId, err)
			}
			result.FlagsUpserted++
			if fl.ConditionsJson != nil {
				result.ConditionsUpdated++
			}
		}

	}

	// Upsert launches from bundle level.
	for _, launch := range bundle.Launches {
		var scopeFeatureID *string
		if launch.ScopeFeatureId != "" {
			scopeFeatureID = &launch.ScopeFeatureId
		}
		var rampPct int32
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
				launch.LaunchId, scopeFeatureID, launch.Dimension, rampPct,
				launch.AffectedFeatures, launch.Description,
			); err != nil {
				return LoadResult{}, fmt.Errorf("upsert launch %q: %w", launch.LaunchId, err)
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
				launch.LaunchId, scopeFeatureID, launch.Dimension, rampPct,
				launch.AffectedFeatures, launch.Description,
			); err != nil {
				return LoadResult{}, fmt.Errorf("upsert launch %q: %w", launch.LaunchId, err)
			}
		}
	}

	// Abandon launches no longer in the bundle.
	bundleLaunchIDs := make([]string, len(bundle.Launches))
	for i, l := range bundle.Launches {
		bundleLaunchIDs[i] = l.LaunchId
	}
	if tag, err := tx.Exec(ctx, `
		UPDATE feature_flags.launches SET status = 'ABANDONED', updated_at = now()
		WHERE launch_id != ALL($1)
		  AND status NOT IN ('COMPLETED', 'ABANDONED')`,
		bundleLaunchIDs,
	); err != nil {
		return LoadResult{}, fmt.Errorf("abandon stale launches: %w", err)
	} else if tag.RowsAffected() > 0 {
		slog.Info("abandoned stale launches", "count", tag.RowsAffected())
	}

	// Archive flags no longer in the bundle.
	rows, err := tx.Query(ctx,
		`SELECT flag_id FROM feature_flags.flags
		 WHERE feature_id = ANY($1::varchar[]) AND archived_at IS NULL`,
		featureIDs,
	)
	if err != nil {
		return LoadResult{}, fmt.Errorf("query active flags: %w", err)
	}
	var toArchive []string
	for rows.Next() {
		var flagID string
		if err := rows.Scan(&flagID); err != nil {
			rows.Close()
			return LoadResult{}, err
		}
		if _, ok := allFlagIDs[flagID]; !ok {
			toArchive = append(toArchive, flagID)
		}
	}
	rows.Close()

	for _, flagID := range toArchive {
		if _, err := tx.Exec(ctx,
			`UPDATE feature_flags.flags SET archived_at = now(), updated_at = now() WHERE flag_id = $1`,
			flagID,
		); err != nil {
			return LoadResult{}, fmt.Errorf("archive flag %q: %w", flagID, err)
		}
		result.FlagsArchived++
	}

	// Update sync_sha.
	if sha != "" {
		for _, fid := range featureIDs {
			if _, err := tx.Exec(ctx,
				`UPDATE feature_flags.features SET sync_sha = $2, updated_at = now() WHERE feature_id = $1`,
				fid, sha,
			); err != nil {
				return LoadResult{}, fmt.Errorf("update sync_sha: %w", err)
			}
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return LoadResult{}, fmt.Errorf("commit: %w", err)
	}

	return result, nil
}

func compileCELVersion() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	for _, dep := range bi.Deps {
		if strings.HasSuffix(dep.Path, "cel-go/cel") || dep.Path == "github.com/google/cel-go" {
			return dep.Version
		}
	}
	return "unknown"
}
