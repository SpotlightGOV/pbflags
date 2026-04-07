// Package integration provides end-to-end integration tests with real Connect
// servers and PostgreSQL. These tests exercise the full stack: admin service,
// evaluator service, kill poller, and database.
package integration

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/protobuf/proto"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
	"github.com/SpotlightGOV/pbflags/gen/pbflags/v1/pbflagsv1connect"
	"github.com/SpotlightGOV/pbflags/internal/admin"
	"github.com/SpotlightGOV/pbflags/internal/evaluator"
	"github.com/SpotlightGOV/pbflags/internal/integrationtest"
)

func feat(prefix, logicalName string) string { return integrationtest.Feature(prefix, logicalName) }

func flg(featureID string, fieldNum int32) string { return integrationtest.Flag(featureID, fieldNum) }

func testDSN() string {
	if dsn := os.Getenv("PBFLAGS_TEST_DSN"); dsn != "" {
		return dsn
	}
	return "postgres://admin:admin@localhost:5433/pbflags?sslmode=disable"
}

type serviceTestEnv struct {
	prefix          string
	pool            *pgxpool.Pool
	cache           *evaluator.CacheStore
	tracker         *evaluator.HealthTracker
	evaluatorClient pbflagsv1connect.FlagEvaluatorServiceClient
	adminClient     pbflagsv1connect.FlagAdminServiceClient
}

func notificationsDefs(prefix string) []evaluator.FlagDef {
	n := feat(prefix, "notifications")
	return []evaluator.FlagDef{
		{FlagID: flg(n, 1), FeatureID: n, FieldNum: 1, FlagType: pbflagsv1.FlagType_FLAG_TYPE_BOOL, Layer: 2, Default: boolVal(true)},
		{FlagID: flg(n, 2), FeatureID: n, FieldNum: 2, FlagType: pbflagsv1.FlagType_FLAG_TYPE_STRING, Layer: 1, Default: stringVal("daily")},
		{FlagID: flg(n, 3), FeatureID: n, FieldNum: 3, FlagType: pbflagsv1.FlagType_FLAG_TYPE_INT64, Layer: 1, Default: int64Val(3)},
		{FlagID: flg(n, 4), FeatureID: n, FieldNum: 4, FlagType: pbflagsv1.FlagType_FLAG_TYPE_DOUBLE, Layer: 1, Default: doubleVal(0.75)},
	}
}

func boolVal(v bool) *pbflagsv1.FlagValue {
	return &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_BoolValue{BoolValue: v}}
}
func stringVal(v string) *pbflagsv1.FlagValue {
	return &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringValue{StringValue: v}}
}
func int64Val(v int64) *pbflagsv1.FlagValue {
	return &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_Int64Value{Int64Value: v}}
}
func doubleVal(v float64) *pbflagsv1.FlagValue {
	return &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_DoubleValue{DoubleValue: v}}
}

const schemaDDL = `
CREATE SCHEMA IF NOT EXISTS feature_flags;
CREATE TABLE IF NOT EXISTS feature_flags.features (feature_id VARCHAR(255) PRIMARY KEY NOT NULL, display_name VARCHAR(255) NOT NULL DEFAULT '', description VARCHAR(1024) NOT NULL DEFAULT '', owner VARCHAR(255) NOT NULL DEFAULT '', created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now());
CREATE TABLE IF NOT EXISTS feature_flags.flags (flag_id VARCHAR(512) PRIMARY KEY NOT NULL, feature_id VARCHAR(255) NOT NULL REFERENCES feature_flags.features(feature_id), field_number INT NOT NULL, display_name VARCHAR(255) NOT NULL DEFAULT '', flag_type VARCHAR(20) NOT NULL, layer VARCHAR(50) NOT NULL DEFAULT 'GLOBAL', description VARCHAR(1024) NOT NULL DEFAULT '', default_value BYTEA, state VARCHAR(20) NOT NULL DEFAULT 'DEFAULT', value BYTEA, archived_at TIMESTAMPTZ, created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now(), CONSTRAINT valid_state CHECK (state IN ('ENABLED', 'DEFAULT', 'KILLED')));
CREATE TABLE IF NOT EXISTS feature_flags.flag_overrides (flag_id VARCHAR(512) NOT NULL REFERENCES feature_flags.flags(flag_id) ON DELETE CASCADE, entity_id VARCHAR(255) NOT NULL, state VARCHAR(20) NOT NULL DEFAULT 'ENABLED', value BYTEA, created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now(), PRIMARY KEY (flag_id, entity_id), CONSTRAINT valid_override_state CHECK (state IN ('ENABLED', 'DEFAULT', 'KILLED')));
CREATE TABLE IF NOT EXISTS feature_flags.flag_audit_log (id BIGSERIAL PRIMARY KEY, flag_id VARCHAR(512) NOT NULL, action VARCHAR(50) NOT NULL, old_value BYTEA, new_value BYTEA, actor VARCHAR(255) NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT now());
`

func seedAllFlags(t *testing.T, prefix string, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	n := feat(prefix, "notifications")
	_, err := pool.Exec(ctx, `INSERT INTO feature_flags.features (feature_id) VALUES ($1) ON CONFLICT DO NOTHING`, n)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO feature_flags.flags (flag_id, feature_id, field_number, flag_type, layer, state) VALUES
			($1, $5, 1, 'BOOL', 'USER', 'DEFAULT'),
			($2, $5, 2, 'STRING', 'GLOBAL', 'DEFAULT'),
			($3, $5, 3, 'INT64', 'GLOBAL', 'DEFAULT'),
			($4, $5, 4, 'DOUBLE', 'GLOBAL', 'DEFAULT')
		ON CONFLICT DO NOTHING`,
		flg(n, 1),
		flg(n, 2),
		flg(n, 3),
		flg(n, 4),
		n)
	require.NoError(t, err)
}

func setOverride(t *testing.T, pool *pgxpool.Pool, flagID, entityID, state string, value *pbflagsv1.FlagValue) {
	t.Helper()
	var valBytes []byte
	if value != nil {
		var err error
		valBytes, err = proto.Marshal(value)
		require.NoError(t, err)
	}
	_, err := pool.Exec(context.Background(), `
		INSERT INTO feature_flags.flag_overrides (flag_id, entity_id, state, value) VALUES ($1, $2, $3, $4)
		ON CONFLICT (flag_id, entity_id) DO UPDATE SET state = EXCLUDED.state, value = EXCLUDED.value`,
		flagID, entityID, state, valBytes)
	require.NoError(t, err)
}

func setupServiceEnv(t *testing.T) *serviceTestEnv {
	t.Helper()
	prefix := integrationtest.Prefix(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	pool, err := pgxpool.New(ctx, testDSN())
	if err != nil {
		t.Skipf("PostgreSQL not available: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("PostgreSQL not reachable: %v", err)
	}

	_, err = pool.Exec(ctx, schemaDDL)
	require.NoError(t, err)

	integrationtest.RegisterCleanup(t, pool, prefix)

	defs := notificationsDefs(prefix)
	defaults := evaluator.NewDefaults(defs)
	reg := evaluator.NewRegistry(defaults)
	noopM := evaluator.NewNoopMetrics()
	noopT := tracenoop.NewTracerProvider().Tracer("test")
	tracker := evaluator.NewHealthTracker(noopM)

	cache, err := evaluator.NewCacheStore(evaluator.CacheStoreConfig{
		FlagTTL: 100 * time.Millisecond, OverrideTTL: 100 * time.Millisecond,
		OverrideMaxSize: 1000, JitterPercent: 0,
	})
	require.NoError(t, err)

	dbFetcher := evaluator.NewDBFetcher(pool, tracker, logger, noopM, noopT)
	eval := evaluator.NewEvaluator(reg, cache, dbFetcher, logger, noopM, noopT)

	pollerCtx, pollerCancel := context.WithCancel(ctx)
	poller := evaluator.NewKillPoller(dbFetcher, cache, tracker, 200*time.Millisecond, 2*time.Second, logger, noopM)
	go poller.Run(pollerCtx)

	svc := evaluator.NewService(eval, reg, tracker, cache, dbFetcher)

	// Evaluator server.
	evalMux := http.NewServeMux()
	evalPath, evalHandler := pbflagsv1connect.NewFlagEvaluatorServiceHandler(svc)
	evalMux.Handle(evalPath, evalHandler)
	evalLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	evalSrv := &http.Server{Handler: h2c.NewHandler(evalMux, &http2.Server{})}
	go evalSrv.Serve(evalLn)

	// Admin server.
	store := admin.NewStore(pool, logger)
	adminSvc := admin.NewAdminService(store, logger)
	adminMux := http.NewServeMux()
	adminPath, adminHandler := pbflagsv1connect.NewFlagAdminServiceHandler(adminSvc)
	adminMux.Handle(adminPath, adminHandler)
	adminLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	adminSrv := &http.Server{Handler: h2c.NewHandler(adminMux, &http2.Server{})}
	go adminSrv.Serve(adminLn)

	t.Cleanup(func() {
		pollerCancel()
		evalSrv.Close()
		adminSrv.Close()
		cache.Close()
		pool.Close()
	})

	return &serviceTestEnv{
		prefix: prefix,
		pool:   pool, cache: cache, tracker: tracker,
		evaluatorClient: pbflagsv1connect.NewFlagEvaluatorServiceClient(http.DefaultClient, fmt.Sprintf("http://%s", evalLn.Addr())),
		adminClient:     pbflagsv1connect.NewFlagAdminServiceClient(http.DefaultClient, fmt.Sprintf("http://%s", adminLn.Addr())),
	}
}

func expireCache(env *serviceTestEnv) {
	time.Sleep(300 * time.Millisecond)
	env.cache.FlushAll()
	env.cache.WaitAll()
	time.Sleep(100 * time.Millisecond)
}

func waitForKillPoll(env *serviceTestEnv) {
	time.Sleep(1 * time.Second)
	env.cache.FlushAll()
	env.cache.WaitAll()
}

func TestBulkEvaluate(t *testing.T) {
	env := setupServiceEnv(t)
	seedAllFlags(t, env.prefix, env.pool)

	_, err := env.adminClient.UpdateFlagState(context.Background(), connect.NewRequest(&pbflagsv1.UpdateFlagStateRequest{
		FlagId: flg(feat(env.prefix, "notifications"), 2), State: pbflagsv1.State_STATE_ENABLED, Value: stringVal("weekly"), Actor: "test",
	}))
	require.NoError(t, err)
	_, err = env.adminClient.UpdateFlagState(context.Background(), connect.NewRequest(&pbflagsv1.UpdateFlagStateRequest{
		FlagId: flg(feat(env.prefix, "notifications"), 3), State: pbflagsv1.State_STATE_KILLED, Actor: "test",
	}))
	require.NoError(t, err)
	expireCache(env)
	waitForKillPoll(env)

	resp, err := env.evaluatorClient.BulkEvaluate(context.Background(), connect.NewRequest(&pbflagsv1.BulkEvaluateRequest{
		FlagIds: []string{flg(feat(env.prefix, "notifications"), 1), flg(feat(env.prefix, "notifications"), 2), flg(feat(env.prefix, "notifications"), 3), flg(feat(env.prefix, "notifications"), 4)},
	}))
	require.NoError(t, err)
	require.Len(t, resp.Msg.Evaluations, 4)

	byID := make(map[string]*pbflagsv1.EvaluateResponse)
	for _, e := range resp.Msg.Evaluations {
		byID[e.FlagId] = e
	}

	assert.True(t, byID[flg(feat(env.prefix, "notifications"), 1)].Value.GetBoolValue())
	assert.Equal(t, "weekly", byID[flg(feat(env.prefix, "notifications"), 2)].Value.GetStringValue())
	assert.Equal(t, int64(3), byID[flg(feat(env.prefix, "notifications"), 3)].Value.GetInt64Value()) // killed → default
	assert.Equal(t, 0.75, byID[flg(feat(env.prefix, "notifications"), 4)].Value.GetDoubleValue())
}

func TestBulkEvaluateWithEntityId(t *testing.T) {
	env := setupServiceEnv(t)
	seedAllFlags(t, env.prefix, env.pool)

	setOverride(t, env.pool, flg(feat(env.prefix, "notifications"), 1), "user-bulk", "ENABLED", boolVal(false))
	_, err := env.adminClient.UpdateFlagState(context.Background(), connect.NewRequest(&pbflagsv1.UpdateFlagStateRequest{
		FlagId: flg(feat(env.prefix, "notifications"), 2), State: pbflagsv1.State_STATE_ENABLED, Value: stringVal("weekly"), Actor: "test",
	}))
	require.NoError(t, err)
	expireCache(env)

	resp, err := env.evaluatorClient.BulkEvaluate(context.Background(), connect.NewRequest(&pbflagsv1.BulkEvaluateRequest{
		FlagIds: []string{flg(feat(env.prefix, "notifications"), 1), flg(feat(env.prefix, "notifications"), 2)}, EntityId: "user-bulk",
	}))
	require.NoError(t, err)
	require.Len(t, resp.Msg.Evaluations, 2)

	byID := make(map[string]*pbflagsv1.EvaluateResponse)
	for _, e := range resp.Msg.Evaluations {
		byID[e.FlagId] = e
	}

	assert.False(t, byID[flg(feat(env.prefix, "notifications"), 1)].Value.GetBoolValue())
	assert.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_OVERRIDE, byID[flg(feat(env.prefix, "notifications"), 1)].Source)
	assert.Equal(t, "weekly", byID[flg(feat(env.prefix, "notifications"), 2)].Value.GetStringValue())
}

func TestRootModeStateRPCs(t *testing.T) {
	env := setupServiceEnv(t)
	seedAllFlags(t, env.prefix, env.pool)
	ctx := context.Background()

	_, err := env.adminClient.UpdateFlagState(ctx, connect.NewRequest(&pbflagsv1.UpdateFlagStateRequest{
		FlagId: flg(feat(env.prefix, "notifications"), 2), State: pbflagsv1.State_STATE_ENABLED, Value: stringVal("weekly"), Actor: "test",
	}))
	require.NoError(t, err)
	_, err = env.adminClient.UpdateFlagState(ctx, connect.NewRequest(&pbflagsv1.UpdateFlagStateRequest{
		FlagId: flg(feat(env.prefix, "notifications"), 3), State: pbflagsv1.State_STATE_KILLED, Actor: "test",
	}))
	require.NoError(t, err)
	setOverride(t, env.pool, flg(feat(env.prefix, "notifications"), 1), "user-100", "ENABLED", boolVal(false))

	flagResp, err := env.evaluatorClient.GetFlagState(ctx, connect.NewRequest(&pbflagsv1.GetFlagStateRequest{FlagId: flg(feat(env.prefix, "notifications"), 2)}))
	require.NoError(t, err)
	assert.Equal(t, pbflagsv1.State_STATE_ENABLED, flagResp.Msg.Flag.State)
	assert.Equal(t, "weekly", flagResp.Msg.Flag.Value.GetStringValue())

	killResp, err := env.evaluatorClient.GetKilledFlags(ctx, connect.NewRequest(&pbflagsv1.GetKilledFlagsRequest{}))
	require.NoError(t, err)
	assert.Contains(t, integrationtest.FilterKilledFlagIDs(killResp.Msg.FlagIds, env.prefix), flg(feat(env.prefix, "notifications"), 3))

	overResp, err := env.evaluatorClient.GetOverrides(ctx, connect.NewRequest(&pbflagsv1.GetOverridesRequest{EntityId: "user-100"}))
	require.NoError(t, err)
	require.Len(t, overResp.Msg.Overrides, 1)
	assert.Equal(t, flg(feat(env.prefix, "notifications"), 1), overResp.Msg.Overrides[0].FlagId)
	assert.False(t, overResp.Msg.Overrides[0].Value.GetBoolValue())
}

func TestGlobalLayerRejectsOverride(t *testing.T) {
	env := setupServiceEnv(t)
	seedAllFlags(t, env.prefix, env.pool)

	_, err := env.adminClient.SetFlagOverride(context.Background(), connect.NewRequest(&pbflagsv1.SetFlagOverrideRequest{
		FlagId: flg(feat(env.prefix, "notifications"), 2), EntityId: "user-1",
		State: pbflagsv1.State_STATE_ENABLED, Value: stringVal("rejected"), Actor: "test",
	}))
	require.Error(t, err)
}

func TestServiceAuditLog(t *testing.T) {
	env := setupServiceEnv(t)
	seedAllFlags(t, env.prefix, env.pool)
	ctx := context.Background()

	_, err := env.adminClient.UpdateFlagState(ctx, connect.NewRequest(&pbflagsv1.UpdateFlagStateRequest{
		FlagId: flg(feat(env.prefix, "notifications"), 2), State: pbflagsv1.State_STATE_ENABLED, Value: stringVal("weekly"), Actor: "test",
	}))
	require.NoError(t, err)
	_, err = env.adminClient.UpdateFlagState(ctx, connect.NewRequest(&pbflagsv1.UpdateFlagStateRequest{
		FlagId: flg(feat(env.prefix, "notifications"), 2), State: pbflagsv1.State_STATE_KILLED, Actor: "test",
	}))
	require.NoError(t, err)

	resp, err := env.adminClient.GetAuditLog(ctx, connect.NewRequest(&pbflagsv1.GetAuditLogRequest{
		FlagId: flg(feat(env.prefix, "notifications"), 2), Limit: 10,
	}))
	require.NoError(t, err)
	require.Len(t, resp.Msg.Entries, 2)
	assert.Equal(t, "UPDATE_STATE", resp.Msg.Entries[0].Action)
	assert.Less(t, resp.Msg.Entries[1].Id, resp.Msg.Entries[0].Id)
}
