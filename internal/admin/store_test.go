package admin

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
	"github.com/SpotlightGOV/pbflags/internal/integrationtest"
)

func feat(prefix, logicalName string) string { return integrationtest.Feature(prefix, logicalName) }

func flg(featureID string, fieldNum int32) string { return integrationtest.Flag(featureID, fieldNum) }

// testDSN returns the PostgreSQL DSN for integration tests.
// Set PBFLAGS_TEST_DSN to override.
func testDSN() string {
	if dsn := os.Getenv("PBFLAGS_TEST_DSN"); dsn != "" {
		return dsn
	}
	return "postgres://admin:admin@localhost:5433/pbflags?sslmode=disable"
}

// setupTestStore creates the feature_flags schema and returns a ready-to-use Store.
// prefix is from integrationtest.Prefix(t); cleanup deletes only rows under that prefix.
func setupTestStore(t *testing.T, prefix string) (*Store, *pgxpool.Pool) {
	t.Helper()

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, testDSN())
	if err != nil {
		t.Skipf("PostgreSQL not available: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("PostgreSQL not reachable: %v", err)
	}

	// Ensure schema and tables exist (idempotent).
	_, err = pool.Exec(ctx, schema)
	require.NoError(t, err)

	integrationtest.RegisterCleanup(t, pool, prefix)
	t.Cleanup(func() { pool.Close() })

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	store := NewStore(pool, logger)
	return store, pool
}

// schema DDL — matches db/migrations/001_schema.sql.
const schema = `
CREATE SCHEMA IF NOT EXISTS feature_flags;

CREATE TABLE IF NOT EXISTS feature_flags.features (
	feature_id   VARCHAR(255) PRIMARY KEY NOT NULL,
	display_name VARCHAR(255) NOT NULL DEFAULT '',
	description  VARCHAR(1024) NOT NULL DEFAULT '',
	owner        VARCHAR(255) NOT NULL DEFAULT '',
	created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS feature_flags.flags (
	flag_id              VARCHAR(512) PRIMARY KEY NOT NULL,
	feature_id           VARCHAR(255) NOT NULL REFERENCES feature_flags.features(feature_id),
	field_number         INT NOT NULL,
	display_name         VARCHAR(255) NOT NULL DEFAULT '',
	flag_type            VARCHAR(20) NOT NULL,
	layer                VARCHAR(50) NOT NULL DEFAULT 'GLOBAL',
	description          VARCHAR(1024) NOT NULL DEFAULT '',
	default_value        BYTEA,
	state                VARCHAR(20) NOT NULL DEFAULT 'DEFAULT',
	value                BYTEA,
	archived_at          TIMESTAMPTZ,
	created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
	CONSTRAINT valid_state CHECK (state IN ('ENABLED', 'DEFAULT', 'KILLED'))
);

CREATE TABLE IF NOT EXISTS feature_flags.flag_overrides (
	flag_id    VARCHAR(512) NOT NULL REFERENCES feature_flags.flags(flag_id) ON DELETE CASCADE,
	entity_id  VARCHAR(255) NOT NULL,
	state      VARCHAR(20) NOT NULL DEFAULT 'ENABLED',
	value      BYTEA,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	PRIMARY KEY (flag_id, entity_id),
	CONSTRAINT valid_override_state CHECK (state IN ('ENABLED', 'DEFAULT', 'KILLED'))
);

CREATE TABLE IF NOT EXISTS feature_flags.flag_audit_log (
	id         BIGSERIAL PRIMARY KEY,
	flag_id    VARCHAR(512) NOT NULL,
	action     VARCHAR(50) NOT NULL,
	old_value  BYTEA,
	new_value  BYTEA,
	actor      VARCHAR(255) NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
`

// seedFeatureAndFlag inserts a test feature and flag. flag_id is feature_id/field_number
// (same format as internal/evaluator/descriptor.go and protoc-gen-pbflags).
func seedFeatureAndFlag(t *testing.T, pool *pgxpool.Pool, featureID string, fieldNum int32, flagType, layer string) {
	t.Helper()
	ctx := context.Background()
	flagID := integrationtest.Flag(featureID, fieldNum)
	_, err := pool.Exec(ctx, `
		INSERT INTO feature_flags.features (feature_id, display_name, description, owner)
		VALUES ($1, $1, 'test feature', 'test')
		ON CONFLICT DO NOTHING`, featureID)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO feature_flags.flags (flag_id, feature_id, field_number, display_name, flag_type, layer, state)
		VALUES ($1, $2, $3, $4, $5, $6, 'DEFAULT')
		ON CONFLICT DO NOTHING`, flagID, featureID, fieldNum, flagID, flagType, layer)
	require.NoError(t, err)
}

func boolValue(v bool) *pbflagsv1.FlagValue {
	return &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_BoolValue{BoolValue: v}}
}

func stringValue(v string) *pbflagsv1.FlagValue {
	return &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringValue{StringValue: v}}
}

func int64Value(v int64) *pbflagsv1.FlagValue {
	return &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_Int64Value{Int64Value: v}}
}

func doubleValue(v float64) *pbflagsv1.FlagValue {
	return &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_DoubleValue{DoubleValue: v}}
}

// testEnv holds shared resources for admin service tests.
type testEnv struct {
	pool  *pgxpool.Pool
	store *Store
	admin *AdminService
}

func setupTestEnv(t *testing.T, prefix string) *testEnv {
	t.Helper()
	store, pool := setupTestStore(t, prefix)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	admin := NewAdminService(store, logger)
	return &testEnv{pool: pool, store: store, admin: admin}
}

func TestGetFlagState(t *testing.T) {
	p := integrationtest.Prefix(t)
	store, pool := setupTestStore(t, p)
	ctx := context.Background()

	seedFeatureAndFlag(t, pool, feat(p, "notifications"), 1, "BOOL", "USER")

	resp, err := store.GetFlagState(ctx, flg(feat(p, "notifications"), 1))
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Equal(t, pbflagsv1.State_STATE_DEFAULT, resp.Flag.State)
	require.False(t, resp.Archived)

	// Non-existent flag returns nil.
	resp, err = store.GetFlagState(ctx, "nonexistent.flag")
	require.NoError(t, err)
	require.Nil(t, resp)
}

func TestUpdateFlagState(t *testing.T) {
	p := integrationtest.Prefix(t)
	store, pool := setupTestStore(t, p)
	ctx := context.Background()

	seedFeatureAndFlag(t, pool, feat(p, "notifications"), 1, "BOOL", "USER")

	// Update to ENABLED with a value.
	err := store.UpdateFlagState(ctx, flg(feat(p, "notifications"), 1), pbflagsv1.State_STATE_ENABLED, boolValue(true), "test-actor")
	require.NoError(t, err)

	resp, err := store.GetFlagState(ctx, flg(feat(p, "notifications"), 1))
	require.NoError(t, err)
	require.Equal(t, pbflagsv1.State_STATE_ENABLED, resp.Flag.State)
	require.True(t, resp.Flag.Value.GetBoolValue())

	// Update to KILLED.
	err = store.UpdateFlagState(ctx, flg(feat(p, "notifications"), 1), pbflagsv1.State_STATE_KILLED, nil, "test-actor")
	require.NoError(t, err)

	resp, err = store.GetFlagState(ctx, flg(feat(p, "notifications"), 1))
	require.NoError(t, err)
	require.Equal(t, pbflagsv1.State_STATE_KILLED, resp.Flag.State)

	// Update non-existent flag fails.
	err = store.UpdateFlagState(ctx, "nonexistent.flag", pbflagsv1.State_STATE_ENABLED, nil, "test-actor")
	require.Error(t, err)
}

func TestGetKilledFlags(t *testing.T) {
	p := integrationtest.Prefix(t)
	store, pool := setupTestStore(t, p)
	ctx := context.Background()

	seedFeatureAndFlag(t, pool, feat(p, "notifications"), 1, "BOOL", "USER")
	seedFeatureAndFlag(t, pool, feat(p, "notifications"), 2, "STRING", "GLOBAL")

	// Kill one flag globally.
	err := store.UpdateFlagState(ctx, flg(feat(p, "notifications"), 1), pbflagsv1.State_STATE_KILLED, nil, "test-actor")
	require.NoError(t, err)

	// Add a killed override.
	_, err = pool.Exec(ctx, `
		INSERT INTO feature_flags.flag_overrides (flag_id, entity_id, state)
		VALUES ($1, 'user-1', 'KILLED')`, flg(feat(p, "notifications"), 2))
	require.NoError(t, err)

	resp, err := store.GetKilledFlags(ctx)
	require.NoError(t, err)
	flagIDs := integrationtest.FilterKilledFlagIDs(resp.FlagIds, p)
	killedOvers := integrationtest.FilterKilledOverrides(resp.KilledOverrides, p)
	require.Contains(t, flagIDs, flg(feat(p, "notifications"), 1))
	require.Len(t, killedOvers, 1)
	require.Equal(t, flg(feat(p, "notifications"), 2), killedOvers[0].FlagId)
	require.Equal(t, "user-1", killedOvers[0].EntityId)
}

func TestGetOverrides(t *testing.T) {
	p := integrationtest.Prefix(t)
	store, pool := setupTestStore(t, p)
	ctx := context.Background()

	seedFeatureAndFlag(t, pool, feat(p, "notifications"), 1, "BOOL", "USER")

	val := boolValue(true)
	valBytes, err := marshalFlagValue(val)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO feature_flags.flag_overrides (flag_id, entity_id, state, value)
		VALUES ($1, 'user-42', 'ENABLED', $2)`, flg(feat(p, "notifications"), 1), valBytes)
	require.NoError(t, err)

	// Get all overrides for entity.
	resp, err := store.GetOverrides(ctx, "user-42", nil)
	require.NoError(t, err)
	require.Len(t, resp.Overrides, 1)
	require.Equal(t, flg(feat(p, "notifications"), 1), resp.Overrides[0].FlagId)
	require.True(t, resp.Overrides[0].Value.GetBoolValue())

	// Get overrides filtered by flag IDs.
	resp, err = store.GetOverrides(ctx, "user-42", []string{flg(feat(p, "notifications"), 1)})
	require.NoError(t, err)
	require.Len(t, resp.Overrides, 1)

	// Get overrides for non-matching flag returns empty.
	resp, err = store.GetOverrides(ctx, "user-42", []string{"other.flag"})
	require.NoError(t, err)
	require.Empty(t, resp.Overrides)

	// No overrides for unknown entity.
	resp, err = store.GetOverrides(ctx, "nobody", nil)
	require.NoError(t, err)
	require.Empty(t, resp.Overrides)
}

func TestSetFlagOverride(t *testing.T) {
	p := integrationtest.Prefix(t)
	store, pool := setupTestStore(t, p)
	ctx := context.Background()

	seedFeatureAndFlag(t, pool, feat(p, "notifications"), 1, "BOOL", "USER")

	// Set override.
	err := store.SetFlagOverride(ctx, flg(feat(p, "notifications"), 1), "user-1", pbflagsv1.State_STATE_ENABLED, boolValue(true), "admin")
	require.NoError(t, err)

	// Verify via GetOverrides.
	resp, err := store.GetOverrides(ctx, "user-1", nil)
	require.NoError(t, err)
	require.Len(t, resp.Overrides, 1)
	require.True(t, resp.Overrides[0].Value.GetBoolValue())

	// Update (upsert) override.
	err = store.SetFlagOverride(ctx, flg(feat(p, "notifications"), 1), "user-1", pbflagsv1.State_STATE_ENABLED, boolValue(false), "admin")
	require.NoError(t, err)
	resp, err = store.GetOverrides(ctx, "user-1", nil)
	require.NoError(t, err)
	require.False(t, resp.Overrides[0].Value.GetBoolValue())
}

func TestSetFlagOverride_GlobalLayerRejected(t *testing.T) {
	p := integrationtest.Prefix(t)
	store, pool := setupTestStore(t, p)
	ctx := context.Background()

	seedFeatureAndFlag(t, pool, feat(p, "notifications"), 2, "STRING", "GLOBAL")

	err := store.SetFlagOverride(ctx, flg(feat(p, "notifications"), 2), "user-1", pbflagsv1.State_STATE_ENABLED, stringValue("weekly"), "admin")
	require.Error(t, err)
	require.Contains(t, err.Error(), "GLOBAL layer")
}

func TestSetFlagOverride_NonexistentFlag(t *testing.T) {
	p := integrationtest.Prefix(t)
	store, _ := setupTestStore(t, p)
	ctx := context.Background()

	err := store.SetFlagOverride(ctx, "nonexistent.flag", "user-1", pbflagsv1.State_STATE_ENABLED, nil, "admin")
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found")
}

func TestRemoveFlagOverride(t *testing.T) {
	p := integrationtest.Prefix(t)
	store, pool := setupTestStore(t, p)
	ctx := context.Background()

	seedFeatureAndFlag(t, pool, feat(p, "notifications"), 1, "BOOL", "USER")

	err := store.SetFlagOverride(ctx, flg(feat(p, "notifications"), 1), "user-1", pbflagsv1.State_STATE_ENABLED, boolValue(true), "admin")
	require.NoError(t, err)
	err = store.RemoveFlagOverride(ctx, flg(feat(p, "notifications"), 1), "user-1", "admin")
	require.NoError(t, err)

	resp, err := store.GetOverrides(ctx, "user-1", nil)
	require.NoError(t, err)
	require.Empty(t, resp.Overrides)
}

func TestAuditLog(t *testing.T) {
	p := integrationtest.Prefix(t)
	store, pool := setupTestStore(t, p)
	ctx := context.Background()

	seedFeatureAndFlag(t, pool, feat(p, "notifications"), 1, "BOOL", "USER")

	err := store.UpdateFlagState(ctx, flg(feat(p, "notifications"), 1), pbflagsv1.State_STATE_ENABLED, boolValue(true), "deployer")
	require.NoError(t, err)

	err = store.SetFlagOverride(ctx, flg(feat(p, "notifications"), 1), "user-1", pbflagsv1.State_STATE_ENABLED, boolValue(false), "admin")
	require.NoError(t, err)

	err = store.RemoveFlagOverride(ctx, flg(feat(p, "notifications"), 1), "user-1", "admin")
	require.NoError(t, err)

	// Get all audit log.
	entries, err := store.GetAuditLog(ctx, AuditLogFilter{})
	require.NoError(t, err)
	entries = integrationtest.FilterAuditLog(entries, p)
	require.Len(t, entries, 3)

	// Most recent first.
	require.Equal(t, "REMOVE_OVERRIDE", entries[0].Action)
	require.Equal(t, "CREATE_OVERRIDE", entries[1].Action)
	require.Equal(t, "UPDATE_STATE", entries[2].Action)
	require.Equal(t, "deployer", entries[2].Actor)

	// Filter by flag ID with limit.
	entries, err = store.GetAuditLog(ctx, AuditLogFilter{FlagID: flg(feat(p, "notifications"), 1), Limit: 2})
	require.NoError(t, err)
	require.Len(t, entries, 2)

	// Flag with no entries returns empty.
	entries, err = store.GetAuditLog(ctx, AuditLogFilter{FlagID: "other.flag"})
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestListFeatures(t *testing.T) {
	p := integrationtest.Prefix(t)
	store, pool := setupTestStore(t, p)
	ctx := context.Background()

	seedFeatureAndFlag(t, pool, feat(p, "billing"), 1, "INT64", "GLOBAL")
	seedFeatureAndFlag(t, pool, feat(p, "notifications"), 1, "BOOL", "USER")
	seedFeatureAndFlag(t, pool, feat(p, "notifications"), 2, "STRING", "GLOBAL")

	features, err := store.ListFeatures(ctx)
	require.NoError(t, err)
	features = integrationtest.FilterFeatures(features, p)
	require.Len(t, features, 2)

	require.Equal(t, feat(p, "billing"), features[0].FeatureId)
	require.Len(t, features[0].Flags, 1)
	require.Equal(t, feat(p, "notifications"), features[1].FeatureId)
	require.Len(t, features[1].Flags, 2)
}

func TestListFeatures_ArchivedExcluded(t *testing.T) {
	p := integrationtest.Prefix(t)
	store, pool := setupTestStore(t, p)
	ctx := context.Background()

	seedFeatureAndFlag(t, pool, feat(p, "notifications"), 1, "BOOL", "USER")

	_, err := pool.Exec(ctx, `UPDATE feature_flags.flags SET archived_at = now() WHERE flag_id = $1`, flg(feat(p, "notifications"), 1))
	require.NoError(t, err)

	features, err := store.ListFeatures(ctx)
	require.NoError(t, err)
	features = integrationtest.FilterFeatures(features, p)
	require.Empty(t, features)
}

func TestGetFlag(t *testing.T) {
	p := integrationtest.Prefix(t)
	store, pool := setupTestStore(t, p)
	ctx := context.Background()

	seedFeatureAndFlag(t, pool, feat(p, "notifications"), 1, "BOOL", "USER")

	err := store.UpdateFlagState(ctx, flg(feat(p, "notifications"), 1), pbflagsv1.State_STATE_ENABLED, boolValue(true), "actor")
	require.NoError(t, err)

	err = store.SetFlagOverride(ctx, flg(feat(p, "notifications"), 1), "user-1", pbflagsv1.State_STATE_ENABLED, boolValue(false), "admin")
	require.NoError(t, err)

	flag, err := store.GetFlag(ctx, flg(feat(p, "notifications"), 1))
	require.NoError(t, err)
	require.NotNil(t, flag)
	require.Equal(t, pbflagsv1.State_STATE_ENABLED, flag.State)
	require.True(t, flag.CurrentValue.GetBoolValue())
	require.Len(t, flag.Overrides, 1)
	require.Equal(t, "user-1", flag.Overrides[0].EntityId)

	// Non-existent flag returns nil.
	flag, err = store.GetFlag(ctx, "nonexistent")
	require.NoError(t, err)
	require.Nil(t, flag)
}

func TestGetFlag_Archived(t *testing.T) {
	p := integrationtest.Prefix(t)
	store, pool := setupTestStore(t, p)
	ctx := context.Background()

	seedFeatureAndFlag(t, pool, feat(p, "notifications"), 1, "BOOL", "USER")
	_, err := pool.Exec(ctx, `UPDATE feature_flags.flags SET archived_at = now() WHERE flag_id = $1`, flg(feat(p, "notifications"), 1))
	require.NoError(t, err)

	flag, err := store.GetFlag(ctx, flg(feat(p, "notifications"), 1))
	require.NoError(t, err)
	require.NotNil(t, flag)
	require.True(t, flag.Archived)
}

// Adversarial tests.

func TestAdversarial_SQLInjection(t *testing.T) {
	p := integrationtest.Prefix(t)
	store, pool := setupTestStore(t, p)
	ctx := context.Background()

	seedFeatureAndFlag(t, pool, feat(p, "notifications"), 1, "BOOL", "USER")

	// SQL injection in flag ID — just returns not found.
	resp, err := store.GetFlagState(ctx, "'; DROP TABLE feature_flags.flags; --")
	require.NoError(t, err)
	require.Nil(t, resp)

	// SQL injection in entity ID.
	overrides, err := store.GetOverrides(ctx, "'; DROP TABLE feature_flags.flags; --", nil)
	require.NoError(t, err)
	require.Empty(t, overrides.Overrides)

	// SQL injection in actor.
	err = store.UpdateFlagState(ctx, flg(feat(p, "notifications"), 1), pbflagsv1.State_STATE_ENABLED, nil, "'; DROP TABLE feature_flags.flags; --")
	require.NoError(t, err)

	// Verify tables still intact.
	resp, err = store.GetFlagState(ctx, flg(feat(p, "notifications"), 1))
	require.NoError(t, err)
	require.NotNil(t, resp)
}

func TestAdversarial_Unicode(t *testing.T) {
	p := integrationtest.Prefix(t)
	store, pool := setupTestStore(t, p)
	ctx := context.Background()

	seedFeatureAndFlag(t, pool, feat(p, "notifications"), 1, "BOOL", "USER")

	// Unicode entity ID.
	err := store.SetFlagOverride(ctx, flg(feat(p, "notifications"), 1), "用户-αβγ-🎉", pbflagsv1.State_STATE_ENABLED, boolValue(true), "管理员")
	require.NoError(t, err)

	resp, err := store.GetOverrides(ctx, "用户-αβγ-🎉", nil)
	require.NoError(t, err)
	require.Len(t, resp.Overrides, 1)

	// Unicode in audit.
	entries, err := store.GetAuditLog(ctx, AuditLogFilter{FlagID: flg(feat(p, "notifications"), 1), Limit: 10})
	require.NoError(t, err)
	require.NotEmpty(t, entries)
	require.Equal(t, "管理员", entries[0].Actor)
}

// --- Additional tests ported from spotlightgov ---

func TestGetFlagState_EmptyFlagID(t *testing.T) {
	p := integrationtest.Prefix(t)
	store, _ := setupTestStore(t, p)
	resp, err := store.GetFlagState(context.Background(), "")
	require.NoError(t, err)
	require.Nil(t, resp)
}

func TestGetKilledFlags_Empty(t *testing.T) {
	p := integrationtest.Prefix(t)
	store, pool := setupTestStore(t, p)
	seedFeatureAndFlag(t, pool, feat(p, "notifications"), 1, "BOOL", "GLOBAL")
	resp, err := store.GetKilledFlags(context.Background())
	require.NoError(t, err)
	flagIDs := integrationtest.FilterKilledFlagIDs(resp.FlagIds, p)
	killedOvers := integrationtest.FilterKilledOverrides(resp.KilledOverrides, p)
	require.Empty(t, flagIDs)
	require.Empty(t, killedOvers)
}

func TestGetOverrides_EmptyEntityID(t *testing.T) {
	p := integrationtest.Prefix(t)
	store, _ := setupTestStore(t, p)
	resp, err := store.GetOverrides(context.Background(), "", nil)
	require.NoError(t, err)
	require.Empty(t, resp.Overrides)
}

func TestUpdateFlagState_EmptyFlagID(t *testing.T) {
	p := integrationtest.Prefix(t)
	env := setupTestEnv(t, p)
	_, err := env.admin.UpdateFlagState(context.Background(), connect.NewRequest(&pbflagsv1.UpdateFlagStateRequest{
		FlagId: "", State: pbflagsv1.State_STATE_ENABLED, Actor: "admin",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestUpdateFlagState_UnspecifiedState(t *testing.T) {
	p := integrationtest.Prefix(t)
	env := setupTestEnv(t, p)
	_, err := env.admin.UpdateFlagState(context.Background(), connect.NewRequest(&pbflagsv1.UpdateFlagStateRequest{
		FlagId: "feat/1", State: pbflagsv1.State_STATE_UNSPECIFIED, Actor: "admin",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestUpdateFlagState_Lifecycle(t *testing.T) {
	p := integrationtest.Prefix(t)
	env := setupTestEnv(t, p)
	ctx := context.Background()
	seedFeatureAndFlag(t, env.pool, feat(p, "lifecycle"), 1, "BOOL", "GLOBAL")

	// DEFAULT → ENABLED.
	_, err := env.admin.UpdateFlagState(ctx, connect.NewRequest(&pbflagsv1.UpdateFlagStateRequest{
		FlagId: flg(feat(p, "lifecycle"), 1), State: pbflagsv1.State_STATE_ENABLED, Value: boolValue(true), Actor: "admin",
	}))
	require.NoError(t, err)
	resp, _ := env.store.GetFlagState(ctx, flg(feat(p, "lifecycle"), 1))
	assert.Equal(t, pbflagsv1.State_STATE_ENABLED, resp.Flag.State)

	// ENABLED → KILLED.
	_, err = env.admin.UpdateFlagState(ctx, connect.NewRequest(&pbflagsv1.UpdateFlagStateRequest{
		FlagId: flg(feat(p, "lifecycle"), 1), State: pbflagsv1.State_STATE_KILLED, Actor: "admin",
	}))
	require.NoError(t, err)
	resp, _ = env.store.GetFlagState(ctx, flg(feat(p, "lifecycle"), 1))
	assert.Equal(t, pbflagsv1.State_STATE_KILLED, resp.Flag.State)

	// KILLED → DEFAULT.
	_, err = env.admin.UpdateFlagState(ctx, connect.NewRequest(&pbflagsv1.UpdateFlagStateRequest{
		FlagId: flg(feat(p, "lifecycle"), 1), State: pbflagsv1.State_STATE_DEFAULT, Actor: "admin",
	}))
	require.NoError(t, err)
	resp, _ = env.store.GetFlagState(ctx, flg(feat(p, "lifecycle"), 1))
	assert.Equal(t, pbflagsv1.State_STATE_DEFAULT, resp.Flag.State)
}

func TestSetFlagOverride_EmptyFlagID(t *testing.T) {
	p := integrationtest.Prefix(t)
	env := setupTestEnv(t, p)
	_, err := env.admin.SetFlagOverride(context.Background(), connect.NewRequest(&pbflagsv1.SetFlagOverrideRequest{
		FlagId: "", EntityId: "user-1", State: pbflagsv1.State_STATE_ENABLED, Actor: "admin",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestSetFlagOverride_EmptyEntityID(t *testing.T) {
	p := integrationtest.Prefix(t)
	env := setupTestEnv(t, p)
	_, err := env.admin.SetFlagOverride(context.Background(), connect.NewRequest(&pbflagsv1.SetFlagOverrideRequest{
		FlagId: "feat/1", EntityId: "", State: pbflagsv1.State_STATE_ENABLED, Actor: "admin",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestSetFlagOverride_UnspecifiedState(t *testing.T) {
	p := integrationtest.Prefix(t)
	env := setupTestEnv(t, p)
	_, err := env.admin.SetFlagOverride(context.Background(), connect.NewRequest(&pbflagsv1.SetFlagOverrideRequest{
		FlagId: "feat/1", EntityId: "user-1", State: pbflagsv1.State_STATE_UNSPECIFIED, Actor: "admin",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestRemoveFlagOverride_NonexistentIsNoOp(t *testing.T) {
	p := integrationtest.Prefix(t)
	store, pool := setupTestStore(t, p)
	seedFeatureAndFlag(t, pool, feat(p, "notifications"), 1, "BOOL", "USER")
	err := store.RemoveFlagOverride(context.Background(), flg(feat(p, "notifications"), 1), "nonexistent-user", "admin")
	require.NoError(t, err)
}

func TestRemoveFlagOverride_EmptyFlagID(t *testing.T) {
	p := integrationtest.Prefix(t)
	env := setupTestEnv(t, p)
	_, err := env.admin.RemoveFlagOverride(context.Background(), connect.NewRequest(&pbflagsv1.RemoveFlagOverrideRequest{
		FlagId: "", EntityId: "user-1", Actor: "admin",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestRemoveFlagOverride_EmptyEntityID(t *testing.T) {
	p := integrationtest.Prefix(t)
	env := setupTestEnv(t, p)
	_, err := env.admin.RemoveFlagOverride(context.Background(), connect.NewRequest(&pbflagsv1.RemoveFlagOverrideRequest{
		FlagId: "feat/1", EntityId: "", Actor: "admin",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestGetFlag_NotFound(t *testing.T) {
	p := integrationtest.Prefix(t)
	env := setupTestEnv(t, p)
	_, err := env.admin.GetFlag(context.Background(), connect.NewRequest(&pbflagsv1.GetFlagRequest{FlagId: "nonexistent"}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestAuditLog_OldValueRecorded(t *testing.T) {
	p := integrationtest.Prefix(t)
	store, pool := setupTestStore(t, p)
	ctx := context.Background()
	seedFeatureAndFlag(t, pool, feat(p, "notifications"), 1, "STRING", "GLOBAL")

	err := store.UpdateFlagState(ctx, flg(feat(p, "notifications"), 1), pbflagsv1.State_STATE_ENABLED, stringValue("v1"), "admin")
	require.NoError(t, err)
	err = store.UpdateFlagState(ctx, flg(feat(p, "notifications"), 1), pbflagsv1.State_STATE_ENABLED, stringValue("v2"), "admin")
	require.NoError(t, err)

	entries, err := store.GetAuditLog(ctx, AuditLogFilter{FlagID: flg(feat(p, "notifications"), 1), Limit: 1})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.NotNil(t, entries[0].OldValue)
	assert.Equal(t, "v1", entries[0].OldValue.GetStringValue())
	require.NotNil(t, entries[0].NewValue)
	assert.Equal(t, "v2", entries[0].NewValue.GetStringValue())
}

func TestAuditLog_OverrideLifecycle(t *testing.T) {
	p := integrationtest.Prefix(t)
	store, pool := setupTestStore(t, p)
	ctx := context.Background()
	seedFeatureAndFlag(t, pool, feat(p, "notifications"), 1, "STRING", "USER")

	err := store.SetFlagOverride(ctx, flg(feat(p, "notifications"), 1), "user-1", pbflagsv1.State_STATE_ENABLED, stringValue("v1"), "admin")
	require.NoError(t, err)
	err = store.SetFlagOverride(ctx, flg(feat(p, "notifications"), 1), "user-1", pbflagsv1.State_STATE_ENABLED, stringValue("v2"), "admin")
	require.NoError(t, err)
	err = store.RemoveFlagOverride(ctx, flg(feat(p, "notifications"), 1), "user-1", "admin")
	require.NoError(t, err)

	entries, err := store.GetAuditLog(ctx, AuditLogFilter{FlagID: flg(feat(p, "notifications"), 1), Limit: 10})
	require.NoError(t, err)
	require.Len(t, entries, 3)
	assert.Equal(t, "REMOVE_OVERRIDE", entries[0].Action)
	assert.Equal(t, "UPDATE_OVERRIDE", entries[1].Action)
	assert.Equal(t, "CREATE_OVERRIDE", entries[2].Action)
}

func TestAuditLog_LimitCap(t *testing.T) {
	p := integrationtest.Prefix(t)
	store, pool := setupTestStore(t, p)
	ctx := context.Background()
	seedFeatureAndFlag(t, pool, feat(p, "notifications"), 1, "BOOL", "GLOBAL")

	for i := 0; i < 5; i++ {
		err := store.UpdateFlagState(ctx, flg(feat(p, "notifications"), 1), pbflagsv1.State_STATE_ENABLED, boolValue(true), "admin")
		require.NoError(t, err)
	}

	entries, err := store.GetAuditLog(ctx, AuditLogFilter{FlagID: flg(feat(p, "notifications"), 1), Limit: 2})
	require.NoError(t, err)
	assert.Len(t, entries, 2)
}

func TestAuditLog_LimitClampedAt1000(t *testing.T) {
	p := integrationtest.Prefix(t)
	store, pool := setupTestStore(t, p)
	ctx := context.Background()
	seedFeatureAndFlag(t, pool, feat(p, "notifications"), 1, "BOOL", "GLOBAL")

	for i := 0; i < 3; i++ {
		err := store.UpdateFlagState(ctx, flg(feat(p, "notifications"), 1), pbflagsv1.State_STATE_ENABLED, boolValue(true), "admin")
		require.NoError(t, err)
	}

	// Limit > 1000 should be clamped, not rejected.
	entries, err := store.GetAuditLog(ctx, AuditLogFilter{FlagID: flg(feat(p, "notifications"), 1), Limit: 2000})
	require.NoError(t, err)
	assert.Len(t, entries, 3)
}

func TestAllFlagTypes(t *testing.T) {
	p := integrationtest.Prefix(t)
	store, pool := setupTestStore(t, p)
	ctx := context.Background()

	seedFeatureAndFlag(t, pool, feat(p, "alltypes"), 1, "BOOL", "GLOBAL")
	seedFeatureAndFlag(t, pool, feat(p, "alltypes"), 2, "STRING", "GLOBAL")
	seedFeatureAndFlag(t, pool, feat(p, "alltypes"), 3, "INT64", "GLOBAL")
	seedFeatureAndFlag(t, pool, feat(p, "alltypes"), 4, "DOUBLE", "GLOBAL")

	tests := []struct {
		flagID string
		value  *pbflagsv1.FlagValue
		check  func(*pbflagsv1.FlagValue) bool
	}{
		{flg(feat(p, "alltypes"), 1), boolValue(true), func(v *pbflagsv1.FlagValue) bool { return v.GetBoolValue() }},
		{flg(feat(p, "alltypes"), 2), stringValue("hello"), func(v *pbflagsv1.FlagValue) bool { return v.GetStringValue() == "hello" }},
		{flg(feat(p, "alltypes"), 3), int64Value(42), func(v *pbflagsv1.FlagValue) bool { return v.GetInt64Value() == 42 }},
		{flg(feat(p, "alltypes"), 4), doubleValue(3.14159), func(v *pbflagsv1.FlagValue) bool { return v.GetDoubleValue() == 3.14159 }},
	}

	for _, tt := range tests {
		t.Run(tt.flagID, func(t *testing.T) {
			err := store.UpdateFlagState(ctx, tt.flagID, pbflagsv1.State_STATE_ENABLED, tt.value, "admin")
			require.NoError(t, err)
			resp, err := store.GetFlagState(ctx, tt.flagID)
			require.NoError(t, err)
			assert.True(t, tt.check(resp.Flag.Value), "value check failed for %s", tt.flagID)
		})
	}
}

func TestFlagValueRoundtrip(t *testing.T) {
	p := integrationtest.Prefix(t)
	store, pool := setupTestStore(t, p)
	ctx := context.Background()
	seedFeatureAndFlag(t, pool, feat(p, "roundtrip"), 1, "STRING", "USER")

	original := stringValue("test-roundtrip-üñïçöðé-value")
	err := store.UpdateFlagState(ctx, flg(feat(p, "roundtrip"), 1), pbflagsv1.State_STATE_ENABLED, original, "admin")
	require.NoError(t, err)

	resp, err := store.GetFlagState(ctx, flg(feat(p, "roundtrip"), 1))
	require.NoError(t, err)
	assert.True(t, proto.Equal(original, resp.Flag.Value), "roundtrip mismatch: expected %v, got %v", original, resp.Flag.Value)
}

func TestAdversarial_ConcurrentMutations(t *testing.T) {
	p := integrationtest.Prefix(t)
	env := setupTestEnv(t, p)
	ctx := context.Background()
	seedFeatureAndFlag(t, env.pool, feat(p, "notifications"), 1, "STRING", "USER")

	const goroutines = 10
	var wg sync.WaitGroup
	errs := make(chan error, goroutines*3)

	// Concurrent state updates.
	for i := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			states := []pbflagsv1.State{pbflagsv1.State_STATE_ENABLED, pbflagsv1.State_STATE_DEFAULT, pbflagsv1.State_STATE_KILLED}
			err := env.store.UpdateFlagState(ctx, flg(feat(p, "notifications"), 1), states[i%3], stringValue("val"), "worker")
			if err != nil {
				errs <- err
			}
		}()
	}

	// Concurrent override creation for different entities.
	for i := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := env.store.SetFlagOverride(ctx, flg(feat(p, "notifications"), 1), fmt.Sprintf("entity-%d", i),
				pbflagsv1.State_STATE_ENABLED, stringValue("override"), "worker")
			if err != nil {
				errs <- err
			}
		}()
	}

	// Concurrent reads.
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := env.store.GetFlagState(ctx, flg(feat(p, "notifications"), 1))
			if err != nil {
				errs <- err
			}
		}()
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		assert.NoError(t, err)
	}

	// Verify flag still readable.
	resp, err := env.store.GetFlagState(ctx, flg(feat(p, "notifications"), 1))
	require.NoError(t, err)
	validStates := map[pbflagsv1.State]bool{
		pbflagsv1.State_STATE_ENABLED: true,
		pbflagsv1.State_STATE_DEFAULT: true,
		pbflagsv1.State_STATE_KILLED:  true,
	}
	assert.True(t, validStates[resp.Flag.State])
}

func TestAdversarial_ConcurrentOverrideUpsert(t *testing.T) {
	p := integrationtest.Prefix(t)
	store, pool := setupTestStore(t, p)
	ctx := context.Background()
	seedFeatureAndFlag(t, pool, feat(p, "notifications"), 1, "STRING", "USER")

	const goroutines = 10
	var wg sync.WaitGroup
	for i := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := store.SetFlagOverride(ctx, flg(feat(p, "notifications"), 1), "user-1",
				pbflagsv1.State_STATE_ENABLED, stringValue(fmt.Sprintf("v%d", i)), "worker")
			assert.NoError(t, err)
		}()
	}
	wg.Wait()

	// Should have exactly 1 override (not 10).
	resp, err := store.GetOverrides(ctx, "user-1", nil)
	require.NoError(t, err)
	assert.Len(t, resp.Overrides, 1)
}
