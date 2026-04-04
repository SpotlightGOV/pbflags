package evaluator

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/protobuf/proto"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
)

// DBFetcher fetches flag state directly from PostgreSQL. It implements Fetcher
// (for the Evaluator), KillFetcher (for the KillPoller), and StateServer
// (for the Service to serve state RPCs in root mode).
type DBFetcher struct {
	pool    *pgxpool.Pool
	tracker *HealthTracker
	logger  *slog.Logger
	metrics *Metrics
}

// NewDBFetcher creates a fetcher backed by direct database access.
func NewDBFetcher(pool *pgxpool.Pool, tracker *HealthTracker, logger *slog.Logger, m *Metrics) *DBFetcher {
	return &DBFetcher{pool: pool, tracker: tracker, logger: logger, metrics: m}
}

// FetchFlagState implements Fetcher.
func (f *DBFetcher) FetchFlagState(ctx context.Context, flagID string) (*CachedFlagState, error) {
	timer := prometheus.NewTimer(f.metrics.FetchDuration.WithLabelValues("db", "flag_state"))
	defer timer.ObserveDuration()

	var stateStr string
	var valueBytes []byte
	var archivedAt *time.Time

	err := f.pool.QueryRow(ctx, `
		SELECT state, value, archived_at
		FROM feature_flags.flags
		WHERE flag_id = $1`, flagID).Scan(&stateStr, &valueBytes, &archivedAt)
	if err == pgx.ErrNoRows {
		f.tracker.RecordSuccess()
		return nil, nil
	}
	if err != nil {
		f.tracker.RecordFailure()
		return nil, fmt.Errorf("query flag %s: %w", flagID, err)
	}
	f.tracker.RecordSuccess()

	val := unmarshalValue(valueBytes)
	return &CachedFlagState{
		FlagID:   flagID,
		State:    dbParseState(stateStr),
		Value:    val,
		Archived: archivedAt != nil,
	}, nil
}

// FetchOverrides implements Fetcher.
func (f *DBFetcher) FetchOverrides(ctx context.Context, entityID string, flagIDs []string) ([]*CachedOverride, error) {
	timer := prometheus.NewTimer(f.metrics.FetchDuration.WithLabelValues("db", "overrides"))
	defer timer.ObserveDuration()

	var rows pgx.Rows
	var err error
	if len(flagIDs) == 0 {
		rows, err = f.pool.Query(ctx, `
			SELECT flag_id, entity_id, state, value
			FROM feature_flags.flag_overrides
			WHERE entity_id = $1`, entityID)
	} else {
		rows, err = f.pool.Query(ctx, `
			SELECT flag_id, entity_id, state, value
			FROM feature_flags.flag_overrides
			WHERE entity_id = $1 AND flag_id = ANY($2)`, entityID, flagIDs)
	}
	if err != nil {
		f.tracker.RecordFailure()
		return nil, fmt.Errorf("query overrides: %w", err)
	}
	defer rows.Close()
	f.tracker.RecordSuccess()

	var result []*CachedOverride
	for rows.Next() {
		var fid, eid, stateStr string
		var valueBytes []byte
		if err := rows.Scan(&fid, &eid, &stateStr, &valueBytes); err != nil {
			return nil, err
		}
		result = append(result, &CachedOverride{
			FlagID:   fid,
			EntityID: eid,
			State:    dbParseState(stateStr),
			Value:    unmarshalValue(valueBytes),
		})
	}
	return result, rows.Err()
}

// GetKilledFlags implements KillFetcher.
func (f *DBFetcher) GetKilledFlags(ctx context.Context) (*KillSet, error) {
	timer := prometheus.NewTimer(f.metrics.FetchDuration.WithLabelValues("db", "killed_flags"))
	defer timer.ObserveDuration()

	ks := &KillSet{
		FlagIDs:         make(map[string]struct{}),
		KilledOverrides: make(map[KillKey]struct{}),
	}

	rows, err := f.pool.Query(ctx, `
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
		ks.FlagIDs[id] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows2, err := f.pool.Query(ctx, `
		SELECT flag_id, entity_id FROM feature_flags.flag_overrides WHERE state = 'KILLED'`)
	if err != nil {
		return nil, fmt.Errorf("query killed overrides: %w", err)
	}
	defer rows2.Close()
	for rows2.Next() {
		var flagID, entityID string
		if err := rows2.Scan(&flagID, &entityID); err != nil {
			return nil, err
		}
		ks.KilledOverrides[KillKey{FlagID: flagID, EntityID: entityID}] = struct{}{}
	}

	return ks, rows2.Err()
}

// GetFlagStateProto implements StateServer.
func (f *DBFetcher) GetFlagStateProto(ctx context.Context, flagID string) (*pbflagsv1.GetFlagStateResponse, error) {
	var stateStr string
	var valueBytes []byte
	var archivedAt *time.Time

	err := f.pool.QueryRow(ctx, `
		SELECT state, value, archived_at
		FROM feature_flags.flags
		WHERE flag_id = $1`, flagID).Scan(&stateStr, &valueBytes, &archivedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query flag %s: %w", flagID, err)
	}

	return &pbflagsv1.GetFlagStateResponse{
		Flag: &pbflagsv1.FlagState{
			FlagId: flagID,
			State:  dbParseState(stateStr),
			Value:  unmarshalValue(valueBytes),
		},
		Archived: archivedAt != nil,
	}, nil
}

// GetKilledFlagsProto implements StateServer.
func (f *DBFetcher) GetKilledFlagsProto(ctx context.Context) (*pbflagsv1.GetKilledFlagsResponse, error) {
	resp := &pbflagsv1.GetKilledFlagsResponse{}

	rows, err := f.pool.Query(ctx, `
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
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows2, err := f.pool.Query(ctx, `
		SELECT flag_id, entity_id FROM feature_flags.flag_overrides WHERE state = 'KILLED'`)
	if err != nil {
		return nil, fmt.Errorf("query killed overrides: %w", err)
	}
	defer rows2.Close()
	for rows2.Next() {
		var flagID, entityID string
		if err := rows2.Scan(&flagID, &entityID); err != nil {
			return nil, err
		}
		resp.KilledOverrides = append(resp.KilledOverrides, &pbflagsv1.KilledOverride{
			FlagId:   flagID,
			EntityId: entityID,
		})
	}
	return resp, rows2.Err()
}

// GetOverridesProto implements StateServer.
func (f *DBFetcher) GetOverridesProto(ctx context.Context, entityID string, flagIDs []string) (*pbflagsv1.GetOverridesResponse, error) {
	resp := &pbflagsv1.GetOverridesResponse{}

	var rows pgx.Rows
	var err error
	if len(flagIDs) == 0 {
		rows, err = f.pool.Query(ctx, `
			SELECT flag_id, entity_id, state, value
			FROM feature_flags.flag_overrides
			WHERE entity_id = $1`, entityID)
	} else {
		rows, err = f.pool.Query(ctx, `
			SELECT flag_id, entity_id, state, value
			FROM feature_flags.flag_overrides
			WHERE entity_id = $1 AND flag_id = ANY($2)`, entityID, flagIDs)
	}
	if err != nil {
		return nil, fmt.Errorf("query overrides: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var fid, eid, stateStr string
		var valueBytes []byte
		if err := rows.Scan(&fid, &eid, &stateStr, &valueBytes); err != nil {
			return nil, err
		}
		resp.Overrides = append(resp.Overrides, &pbflagsv1.OverrideState{
			FlagId:   fid,
			EntityId: eid,
			State:    dbParseState(stateStr),
			Value:    unmarshalValue(valueBytes),
		})
	}
	return resp, rows.Err()
}

func unmarshalValue(b []byte) *pbflagsv1.FlagValue {
	if len(b) == 0 {
		return nil
	}
	v := &pbflagsv1.FlagValue{}
	if err := proto.Unmarshal(b, v); err != nil {
		return nil
	}
	return v
}

func dbParseState(s string) pbflagsv1.State {
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
