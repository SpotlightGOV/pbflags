package admin

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	pbflagspb "github.com/SpotlightGOV/pbflags/gen/pbflags"
	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
	"github.com/SpotlightGOV/pbflags/internal/flagfmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const maxAuditLogLimit = 1000

// Store provides PostgreSQL persistence for flag state.
type Store struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
}

// NewStore creates a Store backed by the given connection pool.
func NewStore(pool *pgxpool.Pool, logger *slog.Logger) *Store {
	return &Store{pool: pool, logger: logger}
}

// FlagCondition represents a single condition in a flag's condition chain.
type FlagCondition struct {
	CEL         string // CEL expression; empty string means "otherwise" (default fallback)
	Value       string // formatted display value
	Comment     string // annotation from YAML comment
	LaunchID    string // launch override ID (empty if none)
	LaunchValue string // formatted launch override value
}

// FlagExtra holds non-proto data loaded alongside a FlagDetail.
type FlagExtra struct {
	Conditions      []FlagCondition
	ConditionsError string
	SyncSHA         string
}

// GetFlagState returns the state and value for a single flag.
func (s *Store) GetFlagState(ctx context.Context, flagID string) (*pbflagsv1.GetFlagStateResponse, error) {
	var killedAt *time.Time
	var archivedAt *time.Time

	err := s.pool.QueryRow(ctx, `
		SELECT killed_at, archived_at
		FROM feature_flags.flags
		WHERE flag_id = $1`, flagID).Scan(&killedAt, &archivedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query flag %s: %w", flagID, err)
	}

	st := pbflagsv1.State_STATE_DEFAULT
	if killedAt != nil {
		st = pbflagsv1.State_STATE_KILLED
	}

	return &pbflagsv1.GetFlagStateResponse{
		Flag: &pbflagsv1.FlagState{
			FlagId: flagID,
			State:  st,
		},
		Archived: archivedAt != nil,
	}, nil
}

// GetKilledFlags returns globally killed flag IDs.
func (s *Store) GetKilledFlags(ctx context.Context) (*pbflagsv1.GetKilledFlagsResponse, error) {
	resp := &pbflagsv1.GetKilledFlagsResponse{}

	rows, err := s.pool.Query(ctx, `
		SELECT flag_id FROM feature_flags.flags WHERE killed_at IS NOT NULL`)
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

// UpdateFlagState sets the killed state for a flag (kill or unkill).
func (s *Store) UpdateFlagState(ctx context.Context, flagID string, state pbflagsv1.State, actor string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	var oldKilledAt *time.Time
	err = tx.QueryRow(ctx, `
		SELECT killed_at FROM feature_flags.flags WHERE flag_id = $1`, flagID).Scan(&oldKilledAt)
	if err == pgx.ErrNoRows {
		return fmt.Errorf("flag %s not found", flagID)
	}
	if err != nil {
		return fmt.Errorf("read old state: %w", err)
	}

	oldState := pbflagsv1.State_STATE_DEFAULT
	if oldKilledAt != nil {
		oldState = pbflagsv1.State_STATE_KILLED
	}

	switch state {
	case pbflagsv1.State_STATE_KILLED:
		_, err = tx.Exec(ctx, `
			UPDATE feature_flags.flags SET killed_at = now(), updated_at = now()
			WHERE flag_id = $1`, flagID)
	default:
		_, err = tx.Exec(ctx, `
			UPDATE feature_flags.flags SET killed_at = NULL, updated_at = now()
			WHERE flag_id = $1`, flagID)
	}
	if err != nil {
		return fmt.Errorf("update flag state: %w", err)
	}

	// Record the state transition in the audit log as proto-encoded FlagValues.
	oldBytes, err := proto.Marshal(&pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringValue{StringValue: stateToString(oldState)}})
	if err != nil {
		return fmt.Errorf("marshal old state: %w", err)
	}
	newBytes, err := proto.Marshal(&pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringValue{StringValue: stateToString(state)}})
	if err != nil {
		return fmt.Errorf("marshal new state: %w", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO feature_flags.flag_audit_log (flag_id, action, old_value, new_value, actor)
		VALUES ($1, 'UPDATE_STATE', $2, $3, $4)`, flagID, oldBytes, newBytes, actor)
	if err != nil {
		return fmt.Errorf("insert audit log: %w", err)
	}

	return tx.Commit(ctx)
}

// ListFeatures returns all features with their non-archived flags.
// The second return value maps flag_id → condition count (0 = static/default).
func (s *Store) ListFeatures(ctx context.Context) ([]*pbflagsv1.FeatureDetail, map[string]int, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT f.feature_id, f.description, f.owner,
		       fl.flag_id, fl.display_name, fl.description,
		       fl.flag_type, fl.killed_at,
		       fl.default_value, fl.supported_values,
		       fl.archived_at IS NOT NULL as archived,
		       fl.condition_count
		FROM feature_flags.features f
		JOIN feature_flags.flags fl ON fl.feature_id = f.feature_id
		WHERE fl.archived_at IS NULL
		ORDER BY f.feature_id, fl.field_number`)
	if err != nil {
		return nil, nil, fmt.Errorf("query features: %w", err)
	}
	defer rows.Close()

	features := make(map[string]*pbflagsv1.FeatureDetail)
	condCounts := make(map[string]int)
	var order []string
	for rows.Next() {
		var featureID, fDesc, fOwner string
		var flagID, flagDisplayName, flagDesc string
		var flagType string
		var killedAt *time.Time
		var defaultBytes, supportedBytes []byte
		var archived bool
		var condCount int

		if err := rows.Scan(
			&featureID, &fDesc, &fOwner,
			&flagID, &flagDisplayName, &flagDesc,
			&flagType, &killedAt,
			&defaultBytes, &supportedBytes,
			&archived, &condCount,
		); err != nil {
			return nil, nil, err
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

		defaultVal, err := unmarshalFlagValue(defaultBytes)
		if err != nil {
			s.logger.Warn("failed to unmarshal default value", "flag_id", flagID, "error", err)
		}
		supportedVals, err := unmarshalSupportedValues(supportedBytes)
		if err != nil {
			s.logger.Warn("failed to unmarshal supported_values", "flag_id", flagID, "error", err)
		}

		st := pbflagsv1.State_STATE_DEFAULT
		if killedAt != nil {
			st = pbflagsv1.State_STATE_KILLED
		}

		fd := &pbflagsv1.FlagDetail{
			FlagId:          flagID,
			DisplayName:     flagDisplayName,
			Description:     flagDesc,
			FlagType:        parseFlagType(flagType),
			State:           st,
			DefaultValue:    defaultVal,
			SupportedValues: supportedVals,
			Archived:        archived,
		}

		feat.Flags = append(feat.Flags, fd)
		if condCount > 0 {
			condCounts[flagID] = condCount
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	result := make([]*pbflagsv1.FeatureDetail, 0, len(order))
	for _, id := range order {
		result = append(result, features[id])
	}
	return result, condCounts, nil
}

// GetFlag returns details for a single flag.
func (s *Store) GetFlag(ctx context.Context, flagID string) (*pbflagsv1.FlagDetail, *FlagExtra, error) {
	var displayName, description, flagType string
	var killedAt *time.Time
	var defaultBytes, supportedBytes []byte
	var archivedAt *time.Time
	var conditionsJSON []byte
	var syncSHA *string

	err := s.pool.QueryRow(ctx, `
		SELECT f.display_name, f.description, f.flag_type, f.killed_at,
		       f.default_value, f.supported_values, f.archived_at,
		       f.conditions, ft.sync_sha
		FROM feature_flags.flags f
		LEFT JOIN feature_flags.features ft ON ft.feature_id = f.feature_id
		WHERE f.flag_id = $1`, flagID).Scan(
		&displayName, &description, &flagType, &killedAt,
		&defaultBytes, &supportedBytes, &archivedAt,
		&conditionsJSON, &syncSHA)
	if err == pgx.ErrNoRows {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("query flag: %w", err)
	}

	defaultVal, err := unmarshalFlagValue(defaultBytes)
	if err != nil {
		s.logger.Warn("failed to unmarshal default value", "flag_id", flagID, "error", err)
	}
	supportedVals, err := unmarshalSupportedValues(supportedBytes)
	if err != nil {
		s.logger.Warn("failed to unmarshal supported_values", "flag_id", flagID, "error", err)
	}

	st := pbflagsv1.State_STATE_DEFAULT
	if killedAt != nil {
		st = pbflagsv1.State_STATE_KILLED
	}

	fd := &pbflagsv1.FlagDetail{
		FlagId:          flagID,
		DisplayName:     displayName,
		Description:     description,
		FlagType:        parseFlagType(flagType),
		State:           st,
		DefaultValue:    defaultVal,
		SupportedValues: supportedVals,
		Archived:        archivedAt != nil,
		ConfigManaged:   syncSHA != nil && *syncSHA != "",
	}

	// Defer loading overrides until after the condition chain is built so
	// we can populate original_value (the chain entry being shadowed).
	overrides, overridesErr := s.ListOverridesForFlag(ctx, flagID)
	if overridesErr != nil {
		s.logger.Warn("failed to load condition overrides", "flag_id", flagID, "error", overridesErr)
	}

	extra := &FlagExtra{}
	if syncSHA != nil {
		extra.SyncSHA = *syncSHA
	}
	if conditionsJSON != nil {
		var stored pbflagsv1.StoredConditions
		if err := proto.Unmarshal(conditionsJSON, &stored); err != nil {
			s.logger.Warn("failed to unmarshal conditions", "flag_id", flagID, "error", err)
			extra.ConditionsError = err.Error()
		} else {
			for i, e := range stored.Conditions {
				fc := FlagCondition{
					Value:   flagfmt.DisplayConditionValue(e.Value),
					Comment: e.Comment,
					CEL:     e.Cel,
				}
				if e.LaunchId != "" {
					fc.LaunchID = e.LaunchId
					fc.LaunchValue = flagfmt.DisplayConditionValue(e.LaunchValue)
				}
				extra.Conditions = append(extra.Conditions, fc)

				// Also surface the chain on FlagDetail so it travels over
				// the wire (CLI / future UI consumers).
				cd := &pbflagsv1.ConditionDetail{
					Index:    int32(i),
					Cel:      e.Cel,
					Comment:  e.Comment,
					LaunchId: e.LaunchId,
				}
				if v, vErr := unmarshalFlagValue(e.Value); vErr == nil {
					cd.Value = v
				}
				if e.LaunchId != "" {
					if lv, lvErr := unmarshalFlagValue(e.LaunchValue); lvErr == nil {
						cd.LaunchValue = lv
					}
				}
				fd.Conditions = append(fd.Conditions, cd)
			}
		}
	}

	// Now attach overrides to FlagDetail, populating original_value from
	// the chain entry (or static default for nil index).
	for _, o := range overrides {
		d := &pbflagsv1.ConditionOverrideDetail{
			OverrideValue: o.Value,
			Source:        sourceStringToProto(o.Source),
			Actor:         o.Actor,
			Reason:        o.Reason,
			CreatedAt:     timestamppb.New(o.CreatedAt),
		}
		if o.ConditionIndex == nil {
			// Static-default override: original is the compiled default.
			d.OriginalValue = fd.DefaultValue
		} else {
			d.ConditionIndex = o.ConditionIndex
			idx := int(*o.ConditionIndex)
			if idx >= 0 && idx < len(fd.Conditions) {
				d.OriginalValue = fd.Conditions[idx].Value
			}
		}
		fd.ConditionOverrides = append(fd.ConditionOverrides, d)
	}

	return fd, extra, nil
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
	} else if filter.Action == "" || (filter.Action != ActionAcquireSyncLock && filter.Action != ActionReleaseSyncLock) {
		// Hide global sync-lock sentinel rows from un-filtered listings —
		// they have no real flag_id and would otherwise render in the UI
		// as a phantom flag named "__sync_lock__". Surfaced only when
		// the caller explicitly filters by the sentinel ID or by one of
		// the sync-lock actions.
		query += fmt.Sprintf(" AND flag_id <> $%d", argN)
		args = append(args, auditFlagIDSyncLock)
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

func unmarshalSupportedValues(b []byte) (*pbflagspb.SupportedValues, error) {
	if len(b) == 0 {
		return nil, nil
	}
	v := &pbflagspb.SupportedValues{}
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
	case "BOOL_LIST":
		return pbflagsv1.FlagType_FLAG_TYPE_BOOL_LIST
	case "STRING_LIST":
		return pbflagsv1.FlagType_FLAG_TYPE_STRING_LIST
	case "INT64_LIST":
		return pbflagsv1.FlagType_FLAG_TYPE_INT64_LIST
	case "DOUBLE_LIST":
		return pbflagsv1.FlagType_FLAG_TYPE_DOUBLE_LIST
	default:
		return pbflagsv1.FlagType_FLAG_TYPE_UNSPECIFIED
	}
}

// Launch represents a launch (gradual rollout) in the new schema.
type Launch struct {
	LaunchID         string
	ScopeFeatureID   *string // nil for cross-feature launches
	Dimension        string
	RampPct          int
	RampSource       string // "unspecified", "config", "cli", "ui"
	Status           string
	KilledAt         *time.Time
	AffectedFeatures []string
	Description      *string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// GetLaunch returns a single launch by ID.
func (s *Store) GetLaunch(ctx context.Context, launchID string) (*Launch, error) {
	var l Launch
	err := s.pool.QueryRow(ctx, `
		SELECT launch_id, scope_feature_id, dimension, ramp_percentage, ramp_source, status,
		       killed_at, affected_features, description, created_at, updated_at
		FROM feature_flags.launches
		WHERE launch_id = $1`, launchID).Scan(
		&l.LaunchID, &l.ScopeFeatureID, &l.Dimension, &l.RampPct, &l.RampSource, &l.Status,
		&l.KilledAt, &l.AffectedFeatures, &l.Description, &l.CreatedAt, &l.UpdatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query launch %s: %w", launchID, err)
	}
	return &l, nil
}

// ListLaunches returns launches scoped to a feature (defined in the feature config).
func (s *Store) ListLaunches(ctx context.Context, featureID string) ([]Launch, error) {
	return s.queryLaunches(ctx, `
		SELECT launch_id, scope_feature_id, dimension, ramp_percentage, ramp_source, status,
		       killed_at, affected_features, description, created_at, updated_at
		FROM feature_flags.launches
		WHERE scope_feature_id = $1
		ORDER BY created_at ASC`, featureID)
}

// ListLaunchesAffecting returns all launches that affect a feature (including cross-feature).
func (s *Store) ListLaunchesAffecting(ctx context.Context, featureID string) ([]Launch, error) {
	return s.queryLaunches(ctx, `
		SELECT launch_id, scope_feature_id, dimension, ramp_percentage, ramp_source, status,
		       killed_at, affected_features, description, created_at, updated_at
		FROM feature_flags.launches
		WHERE $1 = ANY(affected_features)
		ORDER BY created_at ASC`, featureID)
}

// ListAllLaunches returns all launches.
func (s *Store) ListAllLaunches(ctx context.Context) ([]Launch, error) {
	return s.queryLaunches(ctx, `
		SELECT launch_id, scope_feature_id, dimension, ramp_percentage, ramp_source, status,
		       killed_at, affected_features, description, created_at, updated_at
		FROM feature_flags.launches
		ORDER BY created_at ASC`)
}

func (s *Store) queryLaunches(ctx context.Context, query string, args ...any) ([]Launch, error) {
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query launches: %w", err)
	}
	defer rows.Close()

	var launches []Launch
	for rows.Next() {
		var l Launch
		if err := rows.Scan(
			&l.LaunchID, &l.ScopeFeatureID, &l.Dimension, &l.RampPct, &l.RampSource, &l.Status,
			&l.KilledAt, &l.AffectedFeatures, &l.Description, &l.CreatedAt, &l.UpdatedAt,
		); err != nil {
			return nil, err
		}
		launches = append(launches, l)
	}
	return launches, rows.Err()
}

// KillLaunch sets killed_at on a launch (reversible emergency disable).
func (s *Store) KillLaunch(ctx context.Context, launchID string, actor string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	var killedAt *time.Time
	err = tx.QueryRow(ctx, `SELECT killed_at FROM feature_flags.launches WHERE launch_id = $1`, launchID).Scan(&killedAt)
	if err == pgx.ErrNoRows {
		return fmt.Errorf("launch %s not found", launchID)
	}
	if err != nil {
		return fmt.Errorf("read launch: %w", err)
	}
	if killedAt != nil {
		return fmt.Errorf("launch %s is already killed", launchID)
	}

	if _, err = tx.Exec(ctx, `
		UPDATE feature_flags.launches SET killed_at = now(), updated_at = now() WHERE launch_id = $1`,
		launchID,
	); err != nil {
		return fmt.Errorf("kill launch: %w", err)
	}

	// Audit log (use launch_id as the flag_id field for launch audit entries).
	oldBytes, _ := proto.Marshal(&pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringValue{StringValue: "alive"}})
	newBytes, _ := proto.Marshal(&pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringValue{StringValue: "killed"}})
	if _, err = tx.Exec(ctx, `
		INSERT INTO feature_flags.flag_audit_log (flag_id, action, old_value, new_value, actor)
		VALUES ($1, 'KILL_LAUNCH', $2, $3, $4)`,
		launchID, oldBytes, newBytes, actor,
	); err != nil {
		return fmt.Errorf("insert audit log: %w", err)
	}

	return tx.Commit(ctx)
}

// UnkillLaunch clears killed_at on a launch (resume where it left off).
func (s *Store) UnkillLaunch(ctx context.Context, launchID string, actor string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	var killedAt *time.Time
	err = tx.QueryRow(ctx, `SELECT killed_at FROM feature_flags.launches WHERE launch_id = $1`, launchID).Scan(&killedAt)
	if err == pgx.ErrNoRows {
		return fmt.Errorf("launch %s not found", launchID)
	}
	if err != nil {
		return fmt.Errorf("read launch: %w", err)
	}
	if killedAt == nil {
		return fmt.Errorf("launch %s is not killed", launchID)
	}

	if _, err = tx.Exec(ctx, `
		UPDATE feature_flags.launches SET killed_at = NULL, updated_at = now() WHERE launch_id = $1`,
		launchID,
	); err != nil {
		return fmt.Errorf("unkill launch: %w", err)
	}

	oldBytes, _ := proto.Marshal(&pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringValue{StringValue: "killed"}})
	newBytes, _ := proto.Marshal(&pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringValue{StringValue: "alive"}})
	if _, err = tx.Exec(ctx, `
		INSERT INTO feature_flags.flag_audit_log (flag_id, action, old_value, new_value, actor)
		VALUES ($1, 'UNKILL_LAUNCH', $2, $3, $4)`,
		launchID, oldBytes, newBytes, actor,
	); err != nil {
		return fmt.Errorf("insert audit log: %w", err)
	}

	return tx.Commit(ctx)
}

// UpdateLaunchRamp changes the ramp percentage for a launch and records an audit log entry.
// Returns the previous ramp_source so callers can warn when overriding config-managed ramp.
func (s *Store) UpdateLaunchRamp(ctx context.Context, launchID string, pct int, source, actor string) (prevSource string, err error) {
	if pct < 0 || pct > 100 {
		return "", fmt.Errorf("ramp percentage must be 0-100, got %d", pct)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	var oldPct int
	var oldSource string
	err = tx.QueryRow(ctx, `SELECT ramp_percentage, ramp_source FROM feature_flags.launches WHERE launch_id = $1`, launchID).Scan(&oldPct, &oldSource)
	if err == pgx.ErrNoRows {
		return "", fmt.Errorf("launch %s not found", launchID)
	}
	if err != nil {
		return "", fmt.Errorf("read launch: %w", err)
	}

	if _, err = tx.Exec(ctx, `
		UPDATE feature_flags.launches SET ramp_percentage = $2, ramp_source = $3, updated_at = now() WHERE launch_id = $1`,
		launchID, pct, source,
	); err != nil {
		return "", fmt.Errorf("update ramp: %w", err)
	}

	oldBytes, _ := proto.Marshal(&pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringValue{StringValue: fmt.Sprintf("%d%%", oldPct)}})
	newBytes, _ := proto.Marshal(&pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringValue{StringValue: fmt.Sprintf("%d%%", pct)}})
	if _, err = tx.Exec(ctx, `
		INSERT INTO feature_flags.flag_audit_log (flag_id, action, old_value, new_value, actor)
		VALUES ($1, 'UPDATE_LAUNCH_RAMP', $2, $3, $4)`,
		launchID, oldBytes, newBytes, actor,
	); err != nil {
		return "", fmt.Errorf("insert audit log: %w", err)
	}

	return oldSource, tx.Commit(ctx)
}

// UpdateLaunchStatus changes the lifecycle status of a launch.
func (s *Store) UpdateLaunchStatus(ctx context.Context, launchID string, status string, actor string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	var oldStatus string
	err = tx.QueryRow(ctx, `SELECT status FROM feature_flags.launches WHERE launch_id = $1`, launchID).Scan(&oldStatus)
	if err == pgx.ErrNoRows {
		return fmt.Errorf("launch %s not found", launchID)
	}
	if err != nil {
		return fmt.Errorf("read launch: %w", err)
	}

	if _, err = tx.Exec(ctx, `
		UPDATE feature_flags.launches SET status = $2, updated_at = now() WHERE launch_id = $1`,
		launchID, status,
	); err != nil {
		return fmt.Errorf("update status: %w", err)
	}

	oldBytes, _ := proto.Marshal(&pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringValue{StringValue: oldStatus}})
	newBytes, _ := proto.Marshal(&pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringValue{StringValue: status}})
	if _, err = tx.Exec(ctx, `
		INSERT INTO feature_flags.flag_audit_log (flag_id, action, old_value, new_value, actor)
		VALUES ($1, 'UPDATE_LAUNCH_STATUS', $2, $3, $4)`,
		launchID, oldBytes, newBytes, actor,
	); err != nil {
		return fmt.Errorf("insert audit log: %w", err)
	}

	return tx.Commit(ctx)
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
