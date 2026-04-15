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

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
	"github.com/SpotlightGOV/pbflags/gen/pbflags/v1/pbflagsv1connect"
	"github.com/SpotlightGOV/pbflags/internal/admin"
	"github.com/SpotlightGOV/pbflags/internal/evaluator"
	"github.com/SpotlightGOV/pbflags/internal/testdb"
)

type serviceTestEnv struct {
	pool            *pgxpool.Pool
	cache           *evaluator.CacheStore
	tracker         *evaluator.HealthTracker
	evaluatorClient pbflagsv1connect.FlagEvaluatorServiceClient
	adminClient     pbflagsv1connect.FlagAdminServiceClient
}

func stringVal(v string) *pbflagsv1.FlagValue {
	return &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringValue{StringValue: v}}
}

// notifSpecs returns the standard 4-flag spec used by most service tests.
func notifSpecs() []testdb.FlagSpec {
	return []testdb.FlagSpec{
		{FlagType: "BOOL"},
		{FlagType: "STRING"},
		{FlagType: "INT64"},
		{FlagType: "DOUBLE"},
	}
}

func setupServiceEnv(t *testing.T) *serviceTestEnv {
	t.Helper()
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	_, pool := testdb.Require(t)

	noopM := evaluator.NewNoopMetrics()
	noopT := tracenoop.NewTracerProvider().Tracer("test")
	tracker := evaluator.NewHealthTracker(noopM)

	cache, err := evaluator.NewCacheStore(evaluator.CacheStoreConfig{
		FlagTTL: 100 * time.Millisecond, JitterPercent: 0,
	})
	require.NoError(t, err)

	dbFetcher := evaluator.NewDBFetcher(pool, tracker, logger, noopM, noopT)
	eval := evaluator.NewEvaluator(cache, dbFetcher, logger, noopM)

	pollerCtx, pollerCancel := context.WithCancel(ctx)
	poller := evaluator.NewKillPoller(dbFetcher, cache, tracker, 200*time.Millisecond, 2*time.Second, logger, noopM)
	go poller.Run(pollerCtx)

	svc := evaluator.NewService(eval, tracker, cache, dbFetcher)

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
	})

	return &serviceTestEnv{
		pool: pool, cache: cache, tracker: tracker,
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
	t.Parallel()
	env := setupServiceEnv(t)
	tf := testdb.CreateTestFeature(t, env.pool, notifSpecs())

	_, err := env.adminClient.UpdateFlagState(context.Background(), connect.NewRequest(&pbflagsv1.UpdateFlagStateRequest{
		FlagId: tf.FlagID(3), State: pbflagsv1.State_STATE_KILLED, Actor: "test",
	}))
	require.NoError(t, err)
	expireCache(env)
	waitForKillPoll(env)

	resp, err := env.evaluatorClient.BulkEvaluate(context.Background(), connect.NewRequest(&pbflagsv1.BulkEvaluateRequest{
		FlagIds: []string{tf.FlagID(1), tf.FlagID(2), tf.FlagID(3), tf.FlagID(4)},
	}))
	require.NoError(t, err)
	require.Len(t, resp.Msg.Evaluations, 4)

	byID := make(map[string]*pbflagsv1.EvaluateResponse)
	for _, e := range resp.Msg.Evaluations {
		byID[e.FlagId] = e
	}

	assert.Nil(t, byID[tf.FlagID(1)].Value) // DEFAULT -> nil (client has compiled defaults)
	assert.Nil(t, byID[tf.FlagID(2)].Value) // DEFAULT -> nil (conditions handle values)
	assert.Nil(t, byID[tf.FlagID(3)].Value) // KILLED -> nil (client has compiled defaults)
	assert.Nil(t, byID[tf.FlagID(4)].Value) // DEFAULT -> nil (client has compiled defaults)
}

func TestRootModeStateRPCs(t *testing.T) {
	t.Parallel()
	env := setupServiceEnv(t)
	tf := testdb.CreateTestFeature(t, env.pool, notifSpecs())
	ctx := context.Background()

	_, err := env.adminClient.UpdateFlagState(ctx, connect.NewRequest(&pbflagsv1.UpdateFlagStateRequest{
		FlagId: tf.FlagID(3), State: pbflagsv1.State_STATE_KILLED, Actor: "test",
	}))
	require.NoError(t, err)

	flagResp, err := env.evaluatorClient.GetFlagState(ctx, connect.NewRequest(&pbflagsv1.GetFlagStateRequest{FlagId: tf.FlagID(2)}))
	require.NoError(t, err)
	assert.Equal(t, pbflagsv1.State_STATE_DEFAULT, flagResp.Msg.Flag.State)

	killResp, err := env.evaluatorClient.GetKilledFlags(ctx, connect.NewRequest(&pbflagsv1.GetKilledFlagsRequest{}))
	require.NoError(t, err)
	assert.Contains(t, killResp.Msg.FlagIds, tf.FlagID(3))

	overResp, err := env.evaluatorClient.GetOverrides(ctx, connect.NewRequest(&pbflagsv1.GetOverridesRequest{EntityId: "user-100"}))
	require.NoError(t, err)
	require.Empty(t, overResp.Msg.Overrides) // overrides table removed
}

func TestSetFlagOverrideIsUnimplemented(t *testing.T) {
	t.Parallel()
	env := setupServiceEnv(t)
	tf := testdb.CreateTestFeature(t, env.pool, notifSpecs())

	_, err := env.adminClient.SetFlagOverride(context.Background(), connect.NewRequest(&pbflagsv1.SetFlagOverrideRequest{
		FlagId: tf.FlagID(2), EntityId: "user-1",
		State: pbflagsv1.State_STATE_ENABLED, Value: stringVal("rejected"), Actor: "test",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnimplemented, connect.CodeOf(err))
}

func TestServiceAuditLog(t *testing.T) {
	t.Parallel()
	env := setupServiceEnv(t)
	tf := testdb.CreateTestFeature(t, env.pool, notifSpecs())
	ctx := context.Background()

	_, err := env.adminClient.UpdateFlagState(ctx, connect.NewRequest(&pbflagsv1.UpdateFlagStateRequest{
		FlagId: tf.FlagID(2), State: pbflagsv1.State_STATE_KILLED, Actor: "test",
	}))
	require.NoError(t, err)
	_, err = env.adminClient.UpdateFlagState(ctx, connect.NewRequest(&pbflagsv1.UpdateFlagStateRequest{
		FlagId: tf.FlagID(2), State: pbflagsv1.State_STATE_DEFAULT, Actor: "test",
	}))
	require.NoError(t, err)

	resp, err := env.adminClient.GetAuditLog(ctx, connect.NewRequest(&pbflagsv1.GetAuditLogRequest{
		FlagId: tf.FlagID(2), Limit: 10,
	}))
	require.NoError(t, err)
	require.Len(t, resp.Msg.Entries, 2)
	assert.Equal(t, "UPDATE_STATE", resp.Msg.Entries[0].Action)
	assert.Less(t, resp.Msg.Entries[1].Id, resp.Msg.Entries[0].Id)
}
