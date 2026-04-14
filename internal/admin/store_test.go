package admin

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
	"github.com/SpotlightGOV/pbflags/internal/testdb"
)

// setupTestStore returns a ready-to-use Store backed by a testcontainers
// PostgreSQL instance with goose migrations already applied.
func setupTestStore(t *testing.T) (*Store, *pgxpool.Pool) {
	t.Helper()

	_, pool := testdb.Require(t)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	store := NewStore(pool, logger)
	return store, pool
}

// testEnv holds shared resources for admin service tests.
type testEnv struct {
	pool  *pgxpool.Pool
	store *Store
	admin *AdminService
}

func setupTestEnv(t *testing.T) *testEnv {
	t.Helper()
	store, pool := setupTestStore(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	admin := NewAdminService(store, logger)
	return &testEnv{pool: pool, store: store, admin: admin}
}

func TestGetFlagState(t *testing.T) {
	t.Parallel()
	store, pool := setupTestStore(t)
	ctx := context.Background()
	tf := testdb.CreateTestFeature(t, pool, []testdb.FlagSpec{
		{FlagType: "BOOL"},
	})

	resp, err := store.GetFlagState(ctx, tf.FlagID(1))
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
	t.Parallel()
	store, pool := setupTestStore(t)
	ctx := context.Background()
	tf := testdb.CreateTestFeature(t, pool, []testdb.FlagSpec{
		{FlagType: "BOOL"},
	})

	// Update to KILLED.
	err := store.UpdateFlagState(ctx, tf.FlagID(1), pbflagsv1.State_STATE_KILLED, "test-actor")
	require.NoError(t, err)

	resp, err := store.GetFlagState(ctx, tf.FlagID(1))
	require.NoError(t, err)
	require.Equal(t, pbflagsv1.State_STATE_KILLED, resp.Flag.State)

	// Unkill back to DEFAULT.
	err = store.UpdateFlagState(ctx, tf.FlagID(1), pbflagsv1.State_STATE_DEFAULT, "test-actor")
	require.NoError(t, err)

	resp, err = store.GetFlagState(ctx, tf.FlagID(1))
	require.NoError(t, err)
	require.Equal(t, pbflagsv1.State_STATE_DEFAULT, resp.Flag.State)

	// Update non-existent flag fails.
	err = store.UpdateFlagState(ctx, "nonexistent.flag", pbflagsv1.State_STATE_KILLED, "test-actor")
	require.Error(t, err)
}

func TestGetKilledFlags(t *testing.T) {
	t.Parallel()
	store, pool := setupTestStore(t)
	ctx := context.Background()
	tf := testdb.CreateTestFeature(t, pool, []testdb.FlagSpec{
		{FlagType: "BOOL"},
		{FlagType: "STRING"},
	})

	// Kill one flag globally.
	err := store.UpdateFlagState(ctx, tf.FlagID(1), pbflagsv1.State_STATE_KILLED, "test-actor")
	require.NoError(t, err)

	resp, err := store.GetKilledFlags(ctx)
	require.NoError(t, err)
	require.Contains(t, resp.FlagIds, tf.FlagID(1))
	require.Empty(t, resp.KilledOverrides, "per-entity kills are no longer supported")
}

func TestAuditLog(t *testing.T) {
	t.Parallel()
	store, pool := setupTestStore(t)
	ctx := context.Background()
	tf := testdb.CreateTestFeature(t, pool, []testdb.FlagSpec{
		{FlagType: "BOOL"},
	})

	err := store.UpdateFlagState(ctx, tf.FlagID(1), pbflagsv1.State_STATE_KILLED, "deployer")
	require.NoError(t, err)

	err = store.UpdateFlagState(ctx, tf.FlagID(1), pbflagsv1.State_STATE_DEFAULT, "admin")
	require.NoError(t, err)

	// Get audit log filtered by this flag.
	entries, err := store.GetAuditLog(ctx, AuditLogFilter{FlagID: tf.FlagID(1)})
	require.NoError(t, err)
	require.Len(t, entries, 2)

	// Most recent first: unkill (KILLED → DEFAULT), then kill (DEFAULT → KILLED).
	require.Equal(t, "UPDATE_STATE", entries[0].Action)
	require.Equal(t, "admin", entries[0].Actor)
	require.NotNil(t, entries[0].OldValue, "old_value should not be nil for state change")
	require.NotNil(t, entries[0].NewValue, "new_value should not be nil for state change")
	require.Equal(t, "KILLED", entries[0].OldValue.GetStringValue())
	require.Equal(t, "DEFAULT", entries[0].NewValue.GetStringValue())

	require.Equal(t, "UPDATE_STATE", entries[1].Action)
	require.Equal(t, "deployer", entries[1].Actor)
	require.Equal(t, "DEFAULT", entries[1].OldValue.GetStringValue())
	require.Equal(t, "KILLED", entries[1].NewValue.GetStringValue())

	// Filter by flag ID with limit.
	entries, err = store.GetAuditLog(ctx, AuditLogFilter{FlagID: tf.FlagID(1), Limit: 2})
	require.NoError(t, err)
	require.Len(t, entries, 2)

	// Flag with no entries returns empty.
	entries, err = store.GetAuditLog(ctx, AuditLogFilter{FlagID: "other.flag"})
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestListFeatures(t *testing.T) {
	t.Parallel()
	store, pool := setupTestStore(t)
	ctx := context.Background()
	tf1 := testdb.CreateTestFeature(t, pool, []testdb.FlagSpec{
		{FlagType: "INT64"},
	})
	tf2 := testdb.CreateTestFeature(t, pool, []testdb.FlagSpec{
		{FlagType: "BOOL"},
		{FlagType: "STRING"},
	})

	features, _, err := store.ListFeatures(ctx)
	require.NoError(t, err)

	// Find our features in the list (other parallel tests may have data too).
	featureMap := make(map[string]*pbflagsv1.FeatureDetail)
	for _, f := range features {
		featureMap[f.FeatureId] = f
	}
	require.Contains(t, featureMap, tf1.FeatureID)
	require.Len(t, featureMap[tf1.FeatureID].Flags, 1)
	require.Contains(t, featureMap, tf2.FeatureID)
	require.Len(t, featureMap[tf2.FeatureID].Flags, 2)
}

func TestListFeatures_ArchivedExcluded(t *testing.T) {
	t.Parallel()
	store, pool := setupTestStore(t)
	ctx := context.Background()
	tf := testdb.CreateTestFeature(t, pool, []testdb.FlagSpec{
		{FlagType: "BOOL"},
	})

	_, err := pool.Exec(ctx, `UPDATE feature_flags.flags SET archived_at = now() WHERE flag_id = $1`, tf.FlagID(1))
	require.NoError(t, err)

	features, _, err := store.ListFeatures(ctx)
	require.NoError(t, err)

	// Our feature should not appear (its only flag is archived).
	for _, f := range features {
		require.NotEqual(t, tf.FeatureID, f.FeatureId)
	}
}

func TestGetFlag(t *testing.T) {
	t.Parallel()
	store, pool := setupTestStore(t)
	ctx := context.Background()
	tf := testdb.CreateTestFeature(t, pool, []testdb.FlagSpec{
		{FlagType: "BOOL"},
	})

	// Kill the flag and verify state.
	err := store.UpdateFlagState(ctx, tf.FlagID(1), pbflagsv1.State_STATE_KILLED, "actor")
	require.NoError(t, err)

	flag, _, err := store.GetFlag(ctx, tf.FlagID(1))
	require.NoError(t, err)
	require.NotNil(t, flag)
	require.Equal(t, pbflagsv1.State_STATE_KILLED, flag.State)

	// Non-existent flag returns nil.
	flag, _, err = store.GetFlag(ctx, "nonexistent")
	require.NoError(t, err)
	require.Nil(t, flag)
}

func TestGetFlag_Archived(t *testing.T) {
	t.Parallel()
	store, pool := setupTestStore(t)
	ctx := context.Background()
	tf := testdb.CreateTestFeature(t, pool, []testdb.FlagSpec{
		{FlagType: "BOOL"},
	})

	_, err := pool.Exec(ctx, `UPDATE feature_flags.flags SET archived_at = now() WHERE flag_id = $1`, tf.FlagID(1))
	require.NoError(t, err)

	flag, _, err := store.GetFlag(ctx, tf.FlagID(1))
	require.NoError(t, err)
	require.NotNil(t, flag)
	require.True(t, flag.Archived)
}

// Adversarial tests.

func TestAdversarial_SQLInjection(t *testing.T) {
	t.Parallel()
	store, pool := setupTestStore(t)
	ctx := context.Background()
	tf := testdb.CreateTestFeature(t, pool, []testdb.FlagSpec{
		{FlagType: "BOOL"},
	})

	// SQL injection in flag ID — just returns not found.
	resp, err := store.GetFlagState(ctx, "'; DROP TABLE feature_flags.flags; --")
	require.NoError(t, err)
	require.Nil(t, resp)

	// SQL injection in actor.
	err = store.UpdateFlagState(ctx, tf.FlagID(1), pbflagsv1.State_STATE_KILLED, "'; DROP TABLE feature_flags.flags; --")
	require.NoError(t, err)

	// Verify tables still intact.
	resp, err = store.GetFlagState(ctx, tf.FlagID(1))
	require.NoError(t, err)
	require.NotNil(t, resp)
}

func TestAdversarial_Unicode(t *testing.T) {
	t.Parallel()
	store, pool := setupTestStore(t)
	ctx := context.Background()
	tf := testdb.CreateTestFeature(t, pool, []testdb.FlagSpec{
		{FlagType: "BOOL"},
	})

	// Unicode in actor.
	err := store.UpdateFlagState(ctx, tf.FlagID(1), pbflagsv1.State_STATE_KILLED, "管理员")
	require.NoError(t, err)

	// Unicode in audit.
	entries, err := store.GetAuditLog(ctx, AuditLogFilter{FlagID: tf.FlagID(1), Limit: 10})
	require.NoError(t, err)
	require.NotEmpty(t, entries)
	require.Equal(t, "管理员", entries[0].Actor)
}

// --- Additional tests ported from spotlightgov ---

func TestGetFlagState_EmptyFlagID(t *testing.T) {
	t.Parallel()
	store, _ := setupTestStore(t)
	resp, err := store.GetFlagState(context.Background(), "")
	require.NoError(t, err)
	require.Nil(t, resp)
}

func TestGetKilledFlags_Empty(t *testing.T) {
	t.Parallel()
	store, pool := setupTestStore(t)
	tf := testdb.CreateTestFeature(t, pool, []testdb.FlagSpec{
		{FlagType: "BOOL"},
	})

	// Verify our flag is not killed.
	resp, err := store.GetKilledFlags(context.Background())
	require.NoError(t, err)
	require.NotContains(t, resp.FlagIds, tf.FlagID(1))
}

func TestUpdateFlagState_EmptyFlagID(t *testing.T) {
	t.Parallel()
	env := setupTestEnv(t)
	_, err := env.admin.UpdateFlagState(context.Background(), connect.NewRequest(&pbflagsv1.UpdateFlagStateRequest{
		FlagId: "", State: pbflagsv1.State_STATE_ENABLED, Actor: "admin",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestUpdateFlagState_UnspecifiedState(t *testing.T) {
	t.Parallel()
	env := setupTestEnv(t)
	_, err := env.admin.UpdateFlagState(context.Background(), connect.NewRequest(&pbflagsv1.UpdateFlagStateRequest{
		FlagId: "feat/1", State: pbflagsv1.State_STATE_UNSPECIFIED, Actor: "admin",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestUpdateFlagState_Lifecycle(t *testing.T) {
	t.Parallel()
	env := setupTestEnv(t)
	ctx := context.Background()
	tf := testdb.CreateTestFeature(t, env.pool, []testdb.FlagSpec{
		{FlagType: "BOOL"},
	})

	// DEFAULT → KILLED.
	_, err := env.admin.UpdateFlagState(ctx, connect.NewRequest(&pbflagsv1.UpdateFlagStateRequest{
		FlagId: tf.FlagID(1), State: pbflagsv1.State_STATE_KILLED, Actor: "admin",
	}))
	require.NoError(t, err)
	resp, _ := env.store.GetFlagState(ctx, tf.FlagID(1))
	assert.Equal(t, pbflagsv1.State_STATE_KILLED, resp.Flag.State)

	// KILLED → DEFAULT.
	_, err = env.admin.UpdateFlagState(ctx, connect.NewRequest(&pbflagsv1.UpdateFlagStateRequest{
		FlagId: tf.FlagID(1), State: pbflagsv1.State_STATE_DEFAULT, Actor: "admin",
	}))
	require.NoError(t, err)
	resp, _ = env.store.GetFlagState(ctx, tf.FlagID(1))
	assert.Equal(t, pbflagsv1.State_STATE_DEFAULT, resp.Flag.State)
}

func TestGetFlag_NotFound(t *testing.T) {
	t.Parallel()
	env := setupTestEnv(t)
	_, err := env.admin.GetFlag(context.Background(), connect.NewRequest(&pbflagsv1.GetFlagRequest{FlagId: "nonexistent"}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestAuditLog_StateTransitionRecorded(t *testing.T) {
	t.Parallel()
	store, pool := setupTestStore(t)
	ctx := context.Background()
	tf := testdb.CreateTestFeature(t, pool, []testdb.FlagSpec{
		{FlagType: "STRING"},
	})

	err := store.UpdateFlagState(ctx, tf.FlagID(1), pbflagsv1.State_STATE_KILLED, "admin")
	require.NoError(t, err)
	err = store.UpdateFlagState(ctx, tf.FlagID(1), pbflagsv1.State_STATE_DEFAULT, "admin")
	require.NoError(t, err)

	entries, err := store.GetAuditLog(ctx, AuditLogFilter{FlagID: tf.FlagID(1), Limit: 2})
	require.NoError(t, err)
	require.Len(t, entries, 2)
	require.Equal(t, "UPDATE_STATE", entries[0].Action)
	require.Equal(t, "UPDATE_STATE", entries[1].Action)
}

func TestAuditLog_LimitCap(t *testing.T) {
	t.Parallel()
	store, pool := setupTestStore(t)
	ctx := context.Background()
	tf := testdb.CreateTestFeature(t, pool, []testdb.FlagSpec{
		{FlagType: "BOOL"},
	})

	for i := 0; i < 5; i++ {
		err := store.UpdateFlagState(ctx, tf.FlagID(1), pbflagsv1.State_STATE_KILLED, "admin")
		require.NoError(t, err)
	}

	entries, err := store.GetAuditLog(ctx, AuditLogFilter{FlagID: tf.FlagID(1), Limit: 2})
	require.NoError(t, err)
	assert.Len(t, entries, 2)
}

func TestAuditLog_LimitClampedAt1000(t *testing.T) {
	t.Parallel()
	store, pool := setupTestStore(t)
	ctx := context.Background()
	tf := testdb.CreateTestFeature(t, pool, []testdb.FlagSpec{
		{FlagType: "BOOL"},
	})

	for i := 0; i < 3; i++ {
		err := store.UpdateFlagState(ctx, tf.FlagID(1), pbflagsv1.State_STATE_KILLED, "admin")
		require.NoError(t, err)
	}

	// Limit > 1000 should be clamped, not rejected.
	entries, err := store.GetAuditLog(ctx, AuditLogFilter{FlagID: tf.FlagID(1), Limit: 2000})
	require.NoError(t, err)
	assert.Len(t, entries, 3)
}

func TestAllFlagTypes_KillAndUnkill(t *testing.T) {
	t.Parallel()
	store, pool := setupTestStore(t)
	ctx := context.Background()
	tf := testdb.CreateTestFeature(t, pool, []testdb.FlagSpec{
		{FlagType: "BOOL"},
		{FlagType: "STRING"},
		{FlagType: "INT64"},
		{FlagType: "DOUBLE"},
	})

	for i := 1; i <= 4; i++ {
		flagID := tf.FlagID(i)
		t.Run(flagID, func(t *testing.T) {
			// Kill the flag.
			err := store.UpdateFlagState(ctx, flagID, pbflagsv1.State_STATE_KILLED, "admin")
			require.NoError(t, err)
			resp, err := store.GetFlagState(ctx, flagID)
			require.NoError(t, err)
			assert.Equal(t, pbflagsv1.State_STATE_KILLED, resp.Flag.State)

			// Unkill.
			err = store.UpdateFlagState(ctx, flagID, pbflagsv1.State_STATE_DEFAULT, "admin")
			require.NoError(t, err)
			resp, err = store.GetFlagState(ctx, flagID)
			require.NoError(t, err)
			assert.Equal(t, pbflagsv1.State_STATE_DEFAULT, resp.Flag.State)
		})
	}
}

func TestFlagStateRoundtrip(t *testing.T) {
	t.Parallel()
	store, pool := setupTestStore(t)
	ctx := context.Background()
	tf := testdb.CreateTestFeature(t, pool, []testdb.FlagSpec{
		{FlagType: "STRING"},
	})

	// Kill and verify.
	err := store.UpdateFlagState(ctx, tf.FlagID(1), pbflagsv1.State_STATE_KILLED, "admin")
	require.NoError(t, err)
	resp, err := store.GetFlagState(ctx, tf.FlagID(1))
	require.NoError(t, err)
	assert.Equal(t, pbflagsv1.State_STATE_KILLED, resp.Flag.State)

	// Unkill and verify.
	err = store.UpdateFlagState(ctx, tf.FlagID(1), pbflagsv1.State_STATE_DEFAULT, "admin")
	require.NoError(t, err)
	resp, err = store.GetFlagState(ctx, tf.FlagID(1))
	require.NoError(t, err)
	assert.Equal(t, pbflagsv1.State_STATE_DEFAULT, resp.Flag.State)
}

func TestAdversarial_ConcurrentMutations(t *testing.T) {
	t.Parallel()
	env := setupTestEnv(t)
	ctx := context.Background()
	tf := testdb.CreateTestFeature(t, env.pool, []testdb.FlagSpec{
		{FlagType: "STRING"},
	})

	const goroutines = 10
	var wg sync.WaitGroup
	errs := make(chan error, goroutines*2)

	// Concurrent state updates.
	for i := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			states := []pbflagsv1.State{pbflagsv1.State_STATE_DEFAULT, pbflagsv1.State_STATE_KILLED}
			err := env.store.UpdateFlagState(ctx, tf.FlagID(1), states[i%2], "worker")
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
			_, err := env.store.GetFlagState(ctx, tf.FlagID(1))
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
	resp, err := env.store.GetFlagState(ctx, tf.FlagID(1))
	require.NoError(t, err)
	validStates := map[pbflagsv1.State]bool{
		pbflagsv1.State_STATE_DEFAULT: true,
		pbflagsv1.State_STATE_KILLED:  true,
	}
	assert.True(t, validStates[resp.Flag.State])
}

// createTestLaunch inserts a launch directly into the DB for testing.
func createTestLaunch(t *testing.T, pool *pgxpool.Pool, launchID, scopeFeatureID, dimension string, rampPct int) {
	t.Helper()
	ctx := context.Background()
	var scopePtr *string
	if scopeFeatureID != "" {
		scopePtr = &scopeFeatureID
	}
	_, err := pool.Exec(ctx, `
		INSERT INTO feature_flags.launches
			(launch_id, scope_feature_id, dimension, ramp_percentage, affected_features, status)
		VALUES ($1, $2, $3, $4, $5, 'ACTIVE')`,
		launchID, scopePtr, dimension, rampPct, []string{scopeFeatureID})
	require.NoError(t, err, "create test launch %s", launchID)
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM feature_flags.launches WHERE launch_id = $1`, launchID)
	})
}

func TestGetLaunch(t *testing.T) {
	t.Parallel()
	store, pool := setupTestStore(t)
	ctx := context.Background()
	tf := testdb.CreateTestFeature(t, pool, []testdb.FlagSpec{{FlagType: "BOOL"}})

	createTestLaunch(t, pool, "test-launch-get-"+tf.FeatureID, tf.FeatureID, "user_id", 25)

	launch, err := store.GetLaunch(ctx, "test-launch-get-"+tf.FeatureID)
	require.NoError(t, err)
	require.NotNil(t, launch)
	assert.Equal(t, "user_id", launch.Dimension)
	assert.Equal(t, 25, launch.RampPct)
	assert.Equal(t, "ACTIVE", launch.Status)
	assert.Nil(t, launch.KilledAt)

	// Non-existent launch returns nil.
	missing, err := store.GetLaunch(ctx, "does-not-exist")
	require.NoError(t, err)
	assert.Nil(t, missing)
}

func TestListLaunches(t *testing.T) {
	t.Parallel()
	store, pool := setupTestStore(t)
	ctx := context.Background()
	tf := testdb.CreateTestFeature(t, pool, []testdb.FlagSpec{{FlagType: "BOOL"}})

	createTestLaunch(t, pool, "test-list-"+tf.FeatureID, tf.FeatureID, "user_id", 50)

	launches, err := store.ListLaunches(ctx, tf.FeatureID)
	require.NoError(t, err)
	assert.Len(t, launches, 1)
	assert.Equal(t, "test-list-"+tf.FeatureID, launches[0].LaunchID)
}

func TestListLaunchesAffecting(t *testing.T) {
	t.Parallel()
	store, pool := setupTestStore(t)
	ctx := context.Background()
	tf := testdb.CreateTestFeature(t, pool, []testdb.FlagSpec{{FlagType: "BOOL"}})

	createTestLaunch(t, pool, "test-affect-"+tf.FeatureID, tf.FeatureID, "user_id", 10)

	launches, err := store.ListLaunchesAffecting(ctx, tf.FeatureID)
	require.NoError(t, err)
	assert.Len(t, launches, 1)
}

func TestKillUnkillLaunch(t *testing.T) {
	t.Parallel()
	store, pool := setupTestStore(t)
	ctx := context.Background()
	tf := testdb.CreateTestFeature(t, pool, []testdb.FlagSpec{{FlagType: "BOOL"}})
	launchID := "test-kill-" + tf.FeatureID

	createTestLaunch(t, pool, launchID, tf.FeatureID, "user_id", 100)

	// Kill it.
	err := store.KillLaunch(ctx, launchID, "test")
	require.NoError(t, err)

	launch, err := store.GetLaunch(ctx, launchID)
	require.NoError(t, err)
	assert.NotNil(t, launch.KilledAt)

	// Kill again should error.
	err = store.KillLaunch(ctx, launchID, "test")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already killed")

	// Unkill it.
	err = store.UnkillLaunch(ctx, launchID, "test")
	require.NoError(t, err)

	launch, err = store.GetLaunch(ctx, launchID)
	require.NoError(t, err)
	assert.Nil(t, launch.KilledAt)

	// Unkill again should error.
	err = store.UnkillLaunch(ctx, launchID, "test")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not killed")
}

func TestUpdateLaunchRamp(t *testing.T) {
	t.Parallel()
	store, pool := setupTestStore(t)
	ctx := context.Background()
	tf := testdb.CreateTestFeature(t, pool, []testdb.FlagSpec{{FlagType: "BOOL"}})
	launchID := "test-ramp-" + tf.FeatureID

	createTestLaunch(t, pool, launchID, tf.FeatureID, "user_id", 0)

	err := store.UpdateLaunchRamp(ctx, launchID, 50, "test")
	require.NoError(t, err)

	launch, err := store.GetLaunch(ctx, launchID)
	require.NoError(t, err)
	assert.Equal(t, 50, launch.RampPct)

	// Out of range.
	err = store.UpdateLaunchRamp(ctx, launchID, 150, "test")
	assert.Error(t, err)
}
