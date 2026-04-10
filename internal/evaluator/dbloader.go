package evaluator

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/proto"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
)

// LoadDefinitionsFromDB queries features and flags from the database within a
// single read transaction and returns a []FlagDef slice compatible with what
// ParseDescriptorFile returns. SupportedValues will be nil (not stored in DB).
func LoadDefinitionsFromDB(ctx context.Context, pool *pgxpool.Pool) ([]FlagDef, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin read transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	rows, err := tx.Query(ctx, `
		SELECT f.feature_id, f.display_name, f.description, f.owner,
		       fl.flag_id, fl.field_number, fl.display_name, fl.flag_type,
		       fl.layer, fl.description, fl.default_value
		FROM feature_flags.features f
		JOIN feature_flags.flags fl ON fl.feature_id = f.feature_id
		WHERE fl.archived_at IS NULL
		ORDER BY f.feature_id, fl.field_number`)
	if err != nil {
		return nil, fmt.Errorf("query definitions: %w", err)
	}
	defer rows.Close()

	var defs []FlagDef
	for rows.Next() {
		var (
			featureID          string
			featureDisplayName string
			featureDescription string
			featureOwner       string
			flagID             string
			fieldNumber        int32
			displayName        string
			flagTypeStr        string
			layerStr           string
			flagDesc           string
			defaultBytes       []byte
		)

		if err := rows.Scan(
			&featureID, &featureDisplayName, &featureDescription, &featureOwner,
			&flagID, &fieldNumber, &displayName, &flagTypeStr,
			&layerStr, &flagDesc, &defaultBytes,
		); err != nil {
			return nil, fmt.Errorf("scan flag row: %w", err)
		}

		flagType := parseFlagTypeString(flagTypeStr)
		layer := parseLayerDBString(layerStr)

		var defaultVal *pbflagsv1.FlagValue
		if len(defaultBytes) > 0 {
			defaultVal = &pbflagsv1.FlagValue{}
			if err := proto.Unmarshal(defaultBytes, defaultVal); err != nil {
				return nil, fmt.Errorf("unmarshal default for %q: %w", flagID, err)
			}
		}

		defs = append(defs, FlagDef{
			FlagID:             flagID,
			FeatureID:          featureID,
			FieldNum:           fieldNumber,
			Name:               displayName,
			FlagType:           flagType,
			Layer:              layer,
			Default:            defaultVal,
			FeatureDisplayName: featureDisplayName,
			FeatureDescription: featureDescription,
			FeatureOwner:       featureOwner,
		})
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate flag rows: %w", err)
	}

	return defs, nil
}

// parseFlagTypeString converts the DB string (e.g. "BOOL") back to FlagType.
func parseFlagTypeString(s string) pbflagsv1.FlagType {
	return pbflagsv1.FlagType(pbflagsv1.FlagType_value["FLAG_TYPE_"+s])
}

// parseLayerDBString converts the DB layer string back to FlagDef format.
// "GLOBAL" → "" (unset), "USER" → "user", etc.
func parseLayerDBString(s string) string {
	if strings.EqualFold(s, "GLOBAL") {
		return ""
	}
	return strings.ToLower(s)
}
