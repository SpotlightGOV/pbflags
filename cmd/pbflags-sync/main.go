// pbflags-sync reads a descriptors.pb file and syncs feature/flag definitions
// into PostgreSQL. It is intended to run once at deploy time.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/proto"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
	"github.com/SpotlightGOV/pbflags/internal/evaluator"
)

func main() {
	database := flag.String("database", "", "PostgreSQL connection string (or PBFLAGS_DATABASE)")
	descriptors := flag.String("descriptors", "", "path to descriptors.pb (or PBFLAGS_DESCRIPTORS)")
	flag.Parse()

	if *database == "" {
		*database = os.Getenv("PBFLAGS_DATABASE")
	}
	if *descriptors == "" {
		*descriptors = os.Getenv("PBFLAGS_DESCRIPTORS")
	}

	if *database == "" {
		slog.Error("--database flag or PBFLAGS_DATABASE env var is required")
		os.Exit(1)
	}
	if *descriptors == "" {
		slog.Error("--descriptors flag or PBFLAGS_DESCRIPTORS env var is required")
		os.Exit(1)
	}

	if err := run(context.Background(), *database, *descriptors); err != nil {
		slog.Error("sync failed", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, dsn, descriptorPath string) error {
	defs, err := evaluator.ParseDescriptorFile(descriptorPath)
	if err != nil {
		return fmt.Errorf("parse descriptors: %w", err)
	}

	if len(defs) == 0 {
		slog.Info("no flag definitions found in descriptors")
		return nil
	}

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer conn.Close(ctx)

	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Collect unique features and the set of flag IDs we upsert.
	type featureInfo struct {
		displayName string
		description string
		owner       string
	}
	features := make(map[string]featureInfo)
	upsertedFlagIDs := make(map[string]struct{})

	for _, d := range defs {
		if _, ok := features[d.FeatureID]; !ok {
			features[d.FeatureID] = featureInfo{
				displayName: d.FeatureDisplayName,
				description: d.FeatureDescription,
				owner:       d.FeatureOwner,
			}
		}
		upsertedFlagIDs[d.FlagID] = struct{}{}
	}

	// Upsert features.
	for featureID, fi := range features {
		if _, err := tx.Exec(ctx,
			`INSERT INTO feature_flags.features (feature_id, display_name, description, owner)
			 VALUES ($1, $2, $3, $4)
			 ON CONFLICT (feature_id) DO UPDATE SET
			   display_name = EXCLUDED.display_name,
			   description = EXCLUDED.description,
			   owner = EXCLUDED.owner,
			   updated_at = now()`,
			featureID, fi.displayName, fi.description, fi.owner,
		); err != nil {
			return fmt.Errorf("upsert feature %q: %w", featureID, err)
		}
	}
	slog.Info("features synced", "count", len(features))

	// Upsert flags.
	for _, d := range defs {
		var defaultBytes []byte
		if d.Default != nil {
			defaultBytes, err = proto.Marshal(d.Default)
			if err != nil {
				return fmt.Errorf("marshal default for %q: %w", d.FlagID, err)
			}
		}

		layer := layerDBString(d.Layer)
		flagType := flagTypeString(d.FlagType)

		if _, err := tx.Exec(ctx,
			`INSERT INTO feature_flags.flags (flag_id, feature_id, field_number, display_name, flag_type, layer, description, default_value)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			 ON CONFLICT (flag_id) DO UPDATE SET
			   display_name = EXCLUDED.display_name,
			   flag_type = EXCLUDED.flag_type,
			   layer = EXCLUDED.layer,
			   description = EXCLUDED.description,
			   default_value = EXCLUDED.default_value,
			   archived_at = NULL,
			   updated_at = now()`,
			d.FlagID, d.FeatureID, d.FieldNum, d.Name, flagType, layer, "", defaultBytes,
		); err != nil {
			return fmt.Errorf("upsert flag %q: %w", d.FlagID, err)
		}
	}
	slog.Info("flags upserted", "count", len(defs))

	// Archive flags that exist in the DB for these features but are not in the descriptors.
	featureIDs := make([]string, 0, len(features))
	for fid := range features {
		featureIDs = append(featureIDs, fid)
	}

	rows, err := tx.Query(ctx,
		`SELECT flag_id FROM feature_flags.flags
		 WHERE feature_id = ANY($1::varchar[])
		   AND archived_at IS NULL`,
		featureIDs,
	)
	if err != nil {
		return fmt.Errorf("query active flags: %w", err)
	}

	var toArchive []string
	for rows.Next() {
		var flagID string
		if err := rows.Scan(&flagID); err != nil {
			rows.Close()
			return fmt.Errorf("scan flag_id: %w", err)
		}
		if _, ok := upsertedFlagIDs[flagID]; !ok {
			toArchive = append(toArchive, flagID)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate active flags: %w", err)
	}

	for _, flagID := range toArchive {
		if _, err := tx.Exec(ctx,
			`UPDATE feature_flags.flags SET archived_at = now(), updated_at = now() WHERE flag_id = $1`,
			flagID,
		); err != nil {
			return fmt.Errorf("archive flag %q: %w", flagID, err)
		}
	}
	if len(toArchive) > 0 {
		slog.Info("flags archived", "count", len(toArchive))
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}

	slog.Info("sync complete",
		"features", len(features),
		"flags_upserted", len(defs),
		"flags_archived", len(toArchive),
	)
	return nil
}

// flagTypeString converts a FlagType enum to the string stored in the DB,
// stripping the "FLAG_TYPE_" prefix (e.g. FLAG_TYPE_BOOL -> "BOOL").
func flagTypeString(ft pbflagsv1.FlagType) string {
	s := ft.String()
	return strings.TrimPrefix(s, "FLAG_TYPE_")
}

// layerDBString normalizes the layer name for DB storage (uppercase).
// Empty or "global" maps to "GLOBAL".
func layerDBString(layer string) string {
	if layer == "" || strings.EqualFold(layer, "global") {
		return "GLOBAL"
	}
	return strings.ToUpper(layer)
}
