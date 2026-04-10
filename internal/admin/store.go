package admin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
	"github.com/SpotlightGOV/pbflags/internal/evaluator"
)

const maxAuditLogLimit = 1000

// Store provides PostgreSQL persistence for flag state.
type Store struct {
	pool     *pgxpool.Pool
	logger   *slog.Logger
	descs    map[string]evaluator.FlagDef // static fallback (classic mode)
	registry *evaluator.Registry          // live registry (monolithic/distributed mode)
}

// NewStore creates a Store backed by the given connection pool.
// In classic mode, pass a static []FlagDef for metadata enrichment.
// In monolithic/distributed mode, call SetRegistry instead.
func NewStore(pool *pgxpool.Pool, logger *slog.Logger, descriptors ...[]evaluator.FlagDef) *Store {
	descs := make(map[string]evaluator.FlagDef)
	for _, defs := range descriptors {
		for _, d := range defs {
			descs[d.FlagID] = d
		}
	}
	return &Store{pool: pool, logger: logger, descs: descs}
}

// SetRegistry sets the live registry for metadata enrichment. When set,
// the store reads from registry.Load() instead of the static descs map,
// so it stays current after definition reloads.
func (s *Store) SetRegistry(reg *evaluator.Registry) {
	s.registry = reg
}

func (s *Store) getDesc(flagID string) (evaluator.FlagDef, bool) {
	if s.registry != nil {
		return s.registry.Load().Get(flagID)
	}
	d, ok := s.descs[flagID]
	return d, ok
}

// GetFlagState returns the state and value for a single flag.
func (s *Store) GetFlagState(ctx context.Context, flagID string) (*pbflagsv1.GetFlagStateResponse, error) {
	var state string
	var valueBytes []byte
	var archivedAt *time.Time

	err := s.pool.QueryRow(ctx, `
		SELECT state, value, archived_at
		FROM feature_flags.flags
		WHERE flag_id = $1`, flagID).Scan(&state, &valueBytes, &archivedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query flag %s: %w", flagID, err)
	}

	val, err := unmarshalFlagValue(valueBytes)
	if err != nil {
		s.logger.Warn("failed to unmarshal flag value", "flag_id", flagID, "error", err)
	}

	return &pbflagsv1.GetFlagStateResponse{
		Flag: &pbflagsv1.FlagState{
			FlagId: flagID,
			State:  parseState(state),
			Value:  val,
		},
		Archived: archivedAt != nil,
	}, nil
}

// GetKilledFlags returns globally killed flag IDs.
func (s *Store) GetKilledFlags(ctx context.Context) (*pbflagsv1.GetKilledFlagsResponse, error) {
	resp := &pbflagsv1.GetKilledFlagsResponse{}

	rows, err := s.pool.Query(ctx, `
		SELECT flag_id FROM feature_flags.flags WHERE state = 'KILLED'`)
	if err != nil {
		return nil, fmt.Errorf("query killed flags: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		resp.FlagIds = append(resp.FlagIds, id)
	}
	return resp, rows.Err()
}

// GetOverrides returns overrides for a specific entity.
func (s *Store) GetOverrides(ctx context.Context, entityID string, flagIDs []string) (*pbflagsv1.GetOverridesResponse, error) {
	resp := &pbflagsv1.GetOverridesResponse{}

	var rows pgx.Rows
	var err error
	if len(flagIDs) == 0 {
		rows, err = s.pool.Query(ctx, `
			SELECT flag_id, entity_id, state, value
			FROM feature_flags.flag_overrides
			WHERE entity_id = $1`, entityID)
	} else {
		rows, err = s.pool.Query(ctx, `
			SELECT flag_id, entity_id, state, value
			FROM feature_flags.flag_overrides
			WHERE entity_id = $1 AND flag_id = ANY($2)`, entityID, flagIDs)
	}
	if err != nil {
		return nil, fmt.Errorf("query overrides: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var flagID, eid, state string
		var valueBytes []byte
		if err := rows.Scan(&flagID, &eid, &state, &valueBytes); err != nil {
			return nil, err
		}
		val, err := unmarshalFlagValue(valueBytes)
		if err != nil {
			s.logger.Warn("failed to unmarshal override value", "flag_id", flagID, "entity_id", eid, "error", err)
		}
		resp.Overrides = append(resp.Overrides, &pbflagsv1.OverrideState{
			FlagId:   flagID,
			EntityId: eid,
			State:    parseState(state),
			Value:    val,
		})
	}
	return resp, rows.Err()
}

// UpdateFlagState sets the state (and optionally value) for a flag.
func (s *Store) UpdateFlagState(ctx context.Context, flagID string, state pbflagsv1.State, value *pbflagsv1.FlagValue, actor string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	var oldState string
	var oldValueBytes []byte
	err = tx.QueryRow(ctx, `
		SELECT state, value FROM feature_flags.flags WHERE flag_id = $1`, flagID).Scan(&oldState, &oldValueBytes)
	if err == pgx.ErrNoRows {
		return fmt.Errorf("flag %s not found", flagID)
	}
	if err != nil {
		return fmt.Errorf("read old state: %w", err)
	}

	valueBytes, err := marshalFlagValue(value)
	if err != nil {
		return fmt.Errorf("marshal value: %w", err)
	}

	stateStr := stateToString(state)
	_, err = tx.Exec(ctx, `
		UPDATE feature_flags.flags
		SET state = $2, value = $3, updated_at = now()
		WHERE flag_id = $1`, flagID, stateStr, valueBytes)
	if err != nil {
		return fmt.Errorf("update flag state: %w", err)
	}

	oldVal, umErr := unmarshalFlagValue(oldValueBytes)
	if umErr != nil {
		s.logger.Warn("failed to unmarshal old flag value for audit", "flag_id", flagID, "error", umErr)
	}
	if err := insertAuditLog(ctx, tx, flagID, "UPDATE_STATE", oldVal, value, actor); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// SetFlagOverride upserts a per-entity override.
func (s *Store) SetFlagOverride(ctx context.Context, flagID, entityID string, state pbflagsv1.State, value *pbflagsv1.FlagValue, actor string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	var layerStr string
	err = tx.QueryRow(ctx, `
		SELECT layer FROM feature_flags.flags WHERE flag_id = $1`, flagID).Scan(&layerStr)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("flag %s not found", flagID)
	}
	if err != nil {
		return fmt.Errorf("read flag layer: %w", err)
	}
	if isGlobalLayer(layerStr) {
		return fmt.Errorf("flag %s has GLOBAL layer and does not support per-entity overrides", flagID)
	}
	if state == pbflagsv1.State_STATE_KILLED {
		return fmt.Errorf("per-entity kill is not supported; use global kill instead")
	}

	valueBytes, err := marshalFlagValue(value)
	if err != nil {
		return fmt.Errorf("marshal value: %w", err)
	}

	var oldValueBytes []byte
	action := "CREATE_OVERRIDE"
	err = tx.QueryRow(ctx, `
		SELECT value FROM feature_flags.flag_overrides
		WHERE flag_id = $1 AND entity_id = $2`, flagID, entityID).Scan(&oldValueBytes)
	if err == nil {
		action = "UPDATE_OVERRIDE"
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("read old override: %w", err)
	}

	stateStr := stateToString(state)
	_, err = tx.Exec(ctx, `
		INSERT INTO feature_flags.flag_overrides (flag_id, entity_id, state, value)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (flag_id, entity_id) DO UPDATE SET
			state = EXCLUDED.state,
			value = EXCLUDED.value,
			updated_at = now()`, flagID, entityID, stateStr, valueBytes)
	if err != nil {
		return fmt.Errorf("upsert override: %w", err)
	}

	oldVal, umErr := unmarshalFlagValue(oldValueBytes)
	if umErr != nil {
		s.logger.Warn("failed to unmarshal old override value for audit", "flag_id", flagID, "error", umErr)
	}
	if err := insertAuditLog(ctx, tx, flagID, action, oldVal, value, actor); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// RemoveFlagOverride deletes a per-entity override.
func (s *Store) RemoveFlagOverride(ctx context.Context, flagID, entityID, actor string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	var oldValueBytes []byte
	err = tx.QueryRow(ctx, `
		SELECT value FROM feature_flags.flag_overrides
		WHERE flag_id = $1 AND entity_id = $2`, flagID, entityID).Scan(&oldValueBytes)
	if errors.Is(err, pgx.ErrNoRows) {
		oldValueBytes = nil
	} else if err != nil {
		return fmt.Errorf("read old override for audit: %w", err)
	}

	_, err = tx.Exec(ctx, `
		DELETE FROM feature_flags.flag_overrides
		WHERE flag_id = $1 AND entity_id = $2`, flagID, entityID)
	if err != nil {
		return fmt.Errorf("delete override: %w", err)
	}

	oldVal, umErr := unmarshalFlagValue(oldValueBytes)
	if umErr != nil {
		s.logger.Warn("failed to unmarshal old override value for audit", "flag_id", flagID, "error", umErr)
	}
	if err := insertAuditLog(ctx, tx, flagID, "REMOVE_OVERRIDE", oldVal, nil, actor); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// ListFeatures returns all features with their non-archived flags.
func (s *Store) ListFeatures(ctx context.Context) ([]*pbflagsv1.FeatureDetail, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT f.feature_id, f.description, f.owner,
		       fl.flag_id, fl.display_name, fl.description,
		       fl.flag_type, fl.layer, fl.state, fl.value,
		       fl.archived_at IS NOT NULL as archived
		FROM feature_flags.features f
		JOIN feature_flags.flags fl ON fl.feature_id = f.feature_id
		WHERE fl.archived_at IS NULL
		ORDER BY f.feature_id, fl.field_number`)
	if err != nil {
		return nil, fmt.Errorf("query features: %w", err)
	}
	defer rows.Close()

	features := make(map[string]*pbflagsv1.FeatureDetail)
	var order []string
	for rows.Next() {
		var featureID, fDesc, fOwner string
		var flagID, flagDisplayName, flagDesc string
		var flagType, layer, state string
		var valueBytes []byte
		var archived bool

		if err := rows.Scan(
			&featureID, &fDesc, &fOwner,
			&flagID, &flagDisplayName, &flagDesc,
			&flagType, &layer, &state, &valueBytes,
			&archived,
		); err != nil {
			return nil, err
		}

		feat, ok := features[featureID]
		if !ok {
			feat = &pbflagsv1.FeatureDetail{
				FeatureId:   featureID,
				Description: fDesc,
				Owner:       fOwner,
			}
			features[featureID] = feat
			order = append(order, featureID)
		}

		val, err := unmarshalFlagValue(valueBytes)
		if err != nil {
			s.logger.Warn("failed to unmarshal flag value", "flag_id", flagID, "error", err)
		}

		fd := &pbflagsv1.FlagDetail{
			FlagId:       flagID,
			DisplayName:  flagDisplayName,
			Description:  flagDesc,
			FlagType:     parseFlagType(flagType),
			Layer:        layer,
			State:        parseState(state),
			CurrentValue: val,
			Archived:     archived,
		}

		if desc, ok := s.getDesc(flagID); ok {
			fd.DefaultValue = desc.Default
			fd.FlagType = desc.FlagType
			fd.SupportedValues = desc.SupportedValues
		}

		feat.Flags = append(feat.Flags, fd)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	result := make([]*pbflagsv1.FeatureDetail, 0, len(order))
	for _, id := range order {
		result = append(result, features[id])
	}
	return result, nil
}

// GetFlag returns details for a single flag including overrides.
func (s *Store) GetFlag(ctx context.Context, flagID string) (*pbflagsv1.FlagDetail, error) {
	var displayName, description, flagType, layer, state string
	var valueBytes []byte
	var archivedAt *time.Time

	err := s.pool.QueryRow(ctx, `
		SELECT display_name, description, flag_type, layer, state, value, archived_at
		FROM feature_flags.flags
		WHERE flag_id = $1`, flagID).Scan(
		&displayName, &description, &flagType, &layer, &state, &valueBytes, &archivedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query flag: %w", err)
	}

	val, err := unmarshalFlagValue(valueBytes)
	if err != nil {
		s.logger.Warn("failed to unmarshal flag value", "flag_id", flagID, "error", err)
	}

	fd := &pbflagsv1.FlagDetail{
		FlagId:       flagID,
		DisplayName:  displayName,
		Description:  description,
		FlagType:     parseFlagType(flagType),
		Layer:        layer,
		State:        parseState(state),
		CurrentValue: val,
		Archived:     archivedAt != nil,
	}

	if desc, ok := s.getDesc(flagID); ok {
		fd.DefaultValue = desc.Default
		fd.FlagType = desc.FlagType
		fd.SupportedValues = desc.SupportedValues
	}

	rows, err := s.pool.Query(ctx, `
		SELECT entity_id, state, value
		FROM feature_flags.flag_overrides
		WHERE flag_id = $1`, flagID)
	if err != nil {
		return nil, fmt.Errorf("query overrides: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var eid, oState string
		var oValueBytes []byte
		if err := rows.Scan(&eid, &oState, &oValueBytes); err != nil {
			return nil, err
		}
		oVal, err := unmarshalFlagValue(oValueBytes)
		if err != nil {
			s.logger.Warn("failed to unmarshal override value", "flag_id", flagID, "entity_id", eid, "error", err)
		}
		fd.Overrides = append(fd.Overrides, &pbflagsv1.FlagOverrideDetail{
			EntityId: eid,
			State:    parseState(oState),
			Value:    oVal,
		})
	}

	return fd, rows.Err()
}

// AuditLogFilter specifies optional filters for audit log queries.
type AuditLogFilter struct {
	FlagID string
	Action string
	Actor  string
	Limit  int32
}

// GetAuditLog returns audit log entries, optionally filtered by flag ID, action, and actor.
func (s *Store) GetAuditLog(ctx context.Context, filter AuditLogFilter) ([]*pbflagsv1.AuditLogEntry, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > maxAuditLogLimit {
		limit = maxAuditLogLimit
	}

	query := `SELECT id, flag_id, action, old_value, new_value, actor, created_at
		FROM feature_flags.flag_audit_log WHERE 1=1`
	args := []any{}
	argN := 1

	if filter.FlagID != "" {
		query += fmt.Sprintf(" AND flag_id = $%d", argN)
		args = append(args, filter.FlagID)
		argN++
	}
	if filter.Action != "" {
		query += fmt.Sprintf(" AND action = $%d", argN)
		args = append(args, filter.Action)
		argN++
	}
	if filter.Actor != "" {
		query += fmt.Sprintf(" AND actor ILIKE $%d", argN)
		args = append(args, "%"+filter.Actor+"%")
		argN++
	}
	query += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d", argN)
	args = append(args, limit)

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query audit log: %w", err)
	}
	defer rows.Close()

	var entries []*pbflagsv1.AuditLogEntry
	for rows.Next() {
		var id int64
		var fid, action, actor string
		var oldValueBytes, newValueBytes []byte
		var createdAt time.Time
		if err := rows.Scan(&id, &fid, &action, &oldValueBytes, &newValueBytes, &actor, &createdAt); err != nil {
			return nil, err
		}
		oldVal, err := unmarshalFlagValue(oldValueBytes)
		if err != nil {
			s.logger.Warn("failed to unmarshal audit old value", "audit_id", id, "error", err)
		}
		newVal, err := unmarshalFlagValue(newValueBytes)
		if err != nil {
			s.logger.Warn("failed to unmarshal audit new value", "audit_id", id, "error", err)
		}
		entries = append(entries, &pbflagsv1.AuditLogEntry{
			Id:        id,
			FlagId:    fid,
			Action:    action,
			OldValue:  oldVal,
			NewValue:  newVal,
			Actor:     actor,
			CreatedAt: timestamppb.New(createdAt),
		})
	}
	return entries, rows.Err()
}

func insertAuditLog(ctx context.Context, tx pgx.Tx, flagID, action string, oldVal, newVal *pbflagsv1.FlagValue, actor string) error {
	oldBytes, err := marshalFlagValue(oldVal)
	if err != nil {
		return fmt.Errorf("marshal old value for audit: %w", err)
	}
	newBytes, err := marshalFlagValue(newVal)
	if err != nil {
		return fmt.Errorf("marshal new value for audit: %w", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO feature_flags.flag_audit_log (flag_id, action, old_value, new_value, actor)
		VALUES ($1, $2, $3, $4, $5)`, flagID, action, oldBytes, newBytes, actor)
	if err != nil {
		return fmt.Errorf("insert audit log: %w", err)
	}
	return nil
}

func marshalFlagValue(v *pbflagsv1.FlagValue) ([]byte, error) {
	if v == nil || v.Value == nil {
		return nil, nil
	}
	return proto.Marshal(v)
}

func unmarshalFlagValue(b []byte) (*pbflagsv1.FlagValue, error) {
	if len(b) == 0 {
		return nil, nil
	}
	v := &pbflagsv1.FlagValue{}
	if err := proto.Unmarshal(b, v); err != nil {
		return nil, err
	}
	return v, nil
}

func parseFlagType(s string) pbflagsv1.FlagType {
	switch s {
	case "BOOL":
		return pbflagsv1.FlagType_FLAG_TYPE_BOOL
	case "STRING":
		return pbflagsv1.FlagType_FLAG_TYPE_STRING
	case "INT64":
		return pbflagsv1.FlagType_FLAG_TYPE_INT64
	case "DOUBLE":
		return pbflagsv1.FlagType_FLAG_TYPE_DOUBLE
	default:
		return pbflagsv1.FlagType_FLAG_TYPE_UNSPECIFIED
	}
}

func stateToString(st pbflagsv1.State) string {
	switch st {
	case pbflagsv1.State_STATE_ENABLED:
		return "ENABLED"
	case pbflagsv1.State_STATE_DEFAULT:
		return "DEFAULT"
	case pbflagsv1.State_STATE_KILLED:
		return "KILLED"
	default:
		return "DEFAULT"
	}
}

func parseState(s string) pbflagsv1.State {
	switch s {
	case "ENABLED":
		return pbflagsv1.State_STATE_ENABLED
	case "DEFAULT":
		return pbflagsv1.State_STATE_DEFAULT
	case "KILLED":
		return pbflagsv1.State_STATE_KILLED
	default:
		return pbflagsv1.State_STATE_UNSPECIFIED
	}
}

func isGlobalLayer(s string) bool {
	return s == "" || strings.EqualFold(s, "GLOBAL")
}
