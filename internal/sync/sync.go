// Package sync provides flag definition synchronization from parsed descriptors
// into PostgreSQL. Both pbflags-sync and pbflags-admin (standalone mode) use
// this package to write definitions to the database.
package sync

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/proto"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
	"github.com/SpotlightGOV/pbflags/internal/evaluator"
)

// Result reports what the sync did.
type Result struct {
	Features      int
	FlagsUpserted int
	FlagsArchived int
}

// SyncDefinitions writes the given flag definitions to the database in a single
// transaction. It upserts features and flags, and archives flags that are no
// longer present in the descriptor. Runtime state (state, value) is never
// modified.
func SyncDefinitions(ctx context.Context, conn *pgx.Conn, defs []evaluator.FlagDef, logger *slog.Logger) (Result, error) {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	result, err := syncInTx(ctx, tx, defs, logger)
	if err != nil {
		return Result{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return Result{}, fmt.Errorf("commit transaction: %w", err)
	}

	return result, nil
}

func syncInTx(ctx context.Context, tx pgx.Tx, defs []evaluator.FlagDef, logger *slog.Logger) (Result, error) {
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
			return Result{}, fmt.Errorf("upsert feature %q: %w", featureID, err)
		}
	}
	logger.Info("features synced", "count", len(features))

	// Upsert flags.
	for _, d := range defs {
		var defaultBytes []byte
		var err error
		if d.Default != nil {
			defaultBytes, err = proto.Marshal(d.Default)
			if err != nil {
				return Result{}, fmt.Errorf("marshal default for %q: %w", d.FlagID, err)
			}
		}

		var supportedBytes []byte
		if d.SupportedValues != nil {
			supportedBytes, err = proto.Marshal(d.SupportedValues)
			if err != nil {
				return Result{}, fmt.Errorf("marshal supported_values for %q: %w", d.FlagID, err)
			}
		}

		flagType := FlagTypeString(d.FlagType)

		if _, err := tx.Exec(ctx,
			`INSERT INTO feature_flags.flags (flag_id, feature_id, field_number, display_name, flag_type, description, default_value, supported_values)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			 ON CONFLICT (flag_id) DO UPDATE SET
			   display_name = EXCLUDED.display_name,
			   flag_type = EXCLUDED.flag_type,
			   description = EXCLUDED.description,
			   default_value = EXCLUDED.default_value,
			   supported_values = EXCLUDED.supported_values,
			   archived_at = NULL,
			   updated_at = now()`,
			d.FlagID, d.FeatureID, d.FieldNum, d.Name, flagType, "", defaultBytes, supportedBytes,
		); err != nil {
			return Result{}, fmt.Errorf("upsert flag %q: %w", d.FlagID, err)
		}
	}
	logger.Info("flags upserted", "count", len(defs))

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
		return Result{}, fmt.Errorf("query active flags: %w", err)
	}

	var toArchive []string
	for rows.Next() {
		var flagID string
		if err := rows.Scan(&flagID); err != nil {
			rows.Close()
			return Result{}, fmt.Errorf("scan flag_id: %w", err)
		}
		if _, ok := upsertedFlagIDs[flagID]; !ok {
			toArchive = append(toArchive, flagID)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return Result{}, fmt.Errorf("iterate active flags: %w", err)
	}

	for _, flagID := range toArchive {
		if _, err := tx.Exec(ctx,
			`UPDATE feature_flags.flags SET archived_at = now(), updated_at = now() WHERE flag_id = $1`,
			flagID,
		); err != nil {
			return Result{}, fmt.Errorf("archive flag %q: %w", flagID, err)
		}
	}
	if len(toArchive) > 0 {
		logger.Info("flags archived", "count", len(toArchive))
	}

	return Result{
		Features:      len(features),
		FlagsUpserted: len(defs),
		FlagsArchived: len(toArchive),
	}, nil
}

// FlagTypeString converts a FlagType enum to the string stored in the DB,
// stripping the "FLAG_TYPE_" prefix (e.g. FLAG_TYPE_BOOL -> "BOOL").
func FlagTypeString(ft pbflagsv1.FlagType) string {
	s := ft.String()
	return strings.TrimPrefix(s, "FLAG_TYPE_")
}
