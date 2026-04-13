package evaluator

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
)

// DBFetcher fetches flag state directly from PostgreSQL. It implements Fetcher
// (for the Evaluator), KillFetcher (for the KillPoller), and StateServer
// (for the Service to serve state RPCs in root mode).
type DBFetcher struct {
	pool     *pgxpool.Pool
	tracker  *HealthTracker
	logger   *slog.Logger
	metrics  *Metrics
	tracer   trace.Tracer
	condEval *ConditionEvaluator // nil when conditions are not configured
}

// DBFetcherOption configures optional DBFetcher behavior.
type DBFetcherOption func(*DBFetcher)

// WithDBConditionEvaluator sets the condition evaluator for compiling
// conditions loaded from the database.
func WithDBConditionEvaluator(ce *ConditionEvaluator) DBFetcherOption {
	return func(f *DBFetcher) { f.condEval = ce }
}

// NewDBFetcher creates a fetcher backed by direct database access.
func NewDBFetcher(pool *pgxpool.Pool, tracker *HealthTracker, logger *slog.Logger, m *Metrics, tracer trace.Tracer, opts ...DBFetcherOption) *DBFetcher {
	f := &DBFetcher{pool: pool, tracker: tracker, logger: logger, metrics: m, tracer: tracer}
	for _, opt := range opts {
		opt(f)
	}
	return f
}

// FetchFlagState implements Fetcher.
func (f *DBFetcher) FetchFlagState(ctx context.Context, flagID string) (*CachedFlagState, error) {
	ctx, span := f.tracer.Start(ctx, "DBFetcher.FetchFlagState",
		trace.WithAttributes(attribute.String("flag_id", flagID)))
	defer span.End()

	timer := prometheus.NewTimer(f.metrics.FetchDuration.WithLabelValues("db", "flag_state"))
	defer timer.ObserveDuration()

	var killedAt *time.Time
	var archivedAt *time.Time
	var conditionsJSON []byte
	var dimMetaJSON []byte
	var celVersion *string
	var defaultValueBytes []byte

	err := f.pool.QueryRow(ctx, `
		SELECT killed_at, archived_at, conditions, dimension_metadata, cel_version, default_value
		FROM feature_flags.flags
		WHERE flag_id = $1`, flagID).Scan(&killedAt, &archivedAt, &conditionsJSON, &dimMetaJSON, &celVersion, &defaultValueBytes)
	if err == pgx.ErrNoRows {
		f.tracker.RecordSuccess()
		return nil, nil
	}
	if err != nil {
		f.tracker.RecordFailure()
		return nil, fmt.Errorf("query flag %s: %w", flagID, err)
	}
	f.tracker.RecordSuccess()

	cs := &CachedFlagState{
		FlagID:   flagID,
		Archived: archivedAt != nil,
	}
	if killedAt != nil {
		cs.State = pbflagsv1.State_STATE_KILLED
	} else {
		cs.State = pbflagsv1.State_STATE_DEFAULT
	}
	if f.condEval != nil && len(conditionsJSON) > 0 {
		cs.Conditions = f.condEval.CompileConditions(flagID, conditionsJSON)
		cs.DimMeta = ParseDimMeta(dimMetaJSON)
	}
	return cs, nil
}

// GetKilledFlags implements KillFetcher.
func (f *DBFetcher) GetKilledFlags(ctx context.Context) (*KillSet, error) {
	ctx, span := f.tracer.Start(ctx, "DBFetcher.GetKilledFlags")
	defer span.End()

	timer := prometheus.NewTimer(f.metrics.FetchDuration.WithLabelValues("db", "killed_flags"))
	defer timer.ObserveDuration()

	ks := &KillSet{
		FlagIDs: make(map[string]struct{}),
	}

	rows, err := f.pool.Query(ctx, `
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
		ks.FlagIDs[id] = struct{}{}
	}

	return ks, rows.Err()
}

// GetFlagStateProto implements StateServer.
func (f *DBFetcher) GetFlagStateProto(ctx context.Context, flagID string) (*pbflagsv1.GetFlagStateResponse, error) {
	var killedAt *time.Time
	var archivedAt *time.Time

	err := f.pool.QueryRow(ctx, `
		SELECT killed_at, archived_at
		FROM feature_flags.flags
		WHERE flag_id = $1`, flagID).Scan(&killedAt, &archivedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query flag %s: %w", flagID, err)
	}

	state := pbflagsv1.State_STATE_DEFAULT
	if killedAt != nil {
		state = pbflagsv1.State_STATE_KILLED
	}

	return &pbflagsv1.GetFlagStateResponse{
		Flag: &pbflagsv1.FlagState{
			FlagId: flagID,
			State:  state,
		},
		Archived: archivedAt != nil,
	}, nil
}

// GetKilledFlagsProto implements StateServer.
func (f *DBFetcher) GetKilledFlagsProto(ctx context.Context) (*pbflagsv1.GetKilledFlagsResponse, error) {
	resp := &pbflagsv1.GetKilledFlagsResponse{}

	rows, err := f.pool.Query(ctx, `
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
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return resp, nil
}

// GetOverridesProto implements StateServer. Overrides table has been removed;
// always returns an empty response.
func (f *DBFetcher) GetOverridesProto(_ context.Context, _ string, _ []string) (*pbflagsv1.GetOverridesResponse, error) {
	return &pbflagsv1.GetOverridesResponse{}, nil
}
