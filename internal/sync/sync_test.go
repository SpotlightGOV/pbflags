package sync

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
	"github.com/SpotlightGOV/pbflags/internal/evaluator"
	"github.com/SpotlightGOV/pbflags/internal/testdb"
)

func TestSyncDefinitions(t *testing.T) {
	t.Parallel()
	dsn, _ := testdb.Require(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	conn, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	defer conn.Close(ctx)

	defs := []evaluator.FlagDef{
		{
			FlagID:             "synctest/1",
			FeatureID:          "synctest",
			FeatureDisplayName: "Sync Test",
			FeatureDescription: "Tests SyncDefinitions",
			FeatureOwner:       "test",
			FieldNum:           1,
			Name:               "enabled",
			FlagType:           pbflagsv1.FlagType_FLAG_TYPE_BOOL,
		},
		{
			FlagID:    "synctest/2",
			FeatureID: "synctest",
			FieldNum:  2,
			Name:      "frequency",
			FlagType:  pbflagsv1.FlagType_FLAG_TYPE_STRING,
		},
	}

	result, err := SyncDefinitions(ctx, conn, defs, logger)
	require.NoError(t, err, "SyncDefinitions should succeed against current schema")
	require.Equal(t, 1, result.Features)
	require.Equal(t, 2, result.FlagsUpserted)
	require.Equal(t, 0, result.FlagsArchived)

	// Verify flags were actually written.
	conn2, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	defer conn2.Close(ctx)

	var flagType string
	err = conn2.QueryRow(ctx,
		`SELECT flag_type FROM feature_flags.flags WHERE flag_id = $1`, "synctest/1",
	).Scan(&flagType)
	require.NoError(t, err)
	require.Equal(t, "BOOL", flagType)

	// Cleanup.
	t.Cleanup(func() {
		c, _ := pgx.Connect(context.Background(), dsn)
		if c != nil {
			c.Exec(context.Background(), `DELETE FROM feature_flags.flags WHERE feature_id = 'synctest'`)
			c.Exec(context.Background(), `DELETE FROM feature_flags.features WHERE feature_id = 'synctest'`)
			c.Close(context.Background())
		}
	})
}

func TestSyncDefinitionsIdempotent(t *testing.T) {
	t.Parallel()
	dsn, _ := testdb.Require(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	defs := []evaluator.FlagDef{
		{
			FlagID:    "syncidemp/1",
			FeatureID: "syncidemp",
			FieldNum:  1,
			Name:      "flag_a",
			FlagType:  pbflagsv1.FlagType_FLAG_TYPE_BOOL,
		},
	}

	// Run twice — second should succeed without error.
	conn1, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	_, err = SyncDefinitions(ctx, conn1, defs, logger)
	conn1.Close(ctx)
	require.NoError(t, err, "first sync should succeed")

	conn2, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	result, err := SyncDefinitions(ctx, conn2, defs, logger)
	conn2.Close(ctx)
	require.NoError(t, err, "second sync (idempotent) should succeed")
	require.Equal(t, 1, result.FlagsUpserted)

	t.Cleanup(func() {
		c, _ := pgx.Connect(context.Background(), dsn)
		if c != nil {
			c.Exec(context.Background(), `DELETE FROM feature_flags.flags WHERE feature_id = 'syncidemp'`)
			c.Exec(context.Background(), `DELETE FROM feature_flags.features WHERE feature_id = 'syncidemp'`)
			c.Close(context.Background())
		}
	})
}

func TestSyncDefinitionsArchivesRemovedFlags(t *testing.T) {
	t.Parallel()
	dsn, _ := testdb.Require(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	// First sync: two flags.
	defs := []evaluator.FlagDef{
		{FlagID: "syncarch/1", FeatureID: "syncarch", FieldNum: 1, Name: "a", FlagType: pbflagsv1.FlagType_FLAG_TYPE_BOOL},
		{FlagID: "syncarch/2", FeatureID: "syncarch", FieldNum: 2, Name: "b", FlagType: pbflagsv1.FlagType_FLAG_TYPE_STRING},
	}

	conn1, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	_, err = SyncDefinitions(ctx, conn1, defs, logger)
	conn1.Close(ctx)
	require.NoError(t, err)

	// Second sync: only one flag — the other should be archived.
	conn2, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	result, err := SyncDefinitions(ctx, conn2, defs[:1], logger)
	conn2.Close(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, result.FlagsArchived, "removed flag should be archived")

	t.Cleanup(func() {
		c, _ := pgx.Connect(context.Background(), dsn)
		if c != nil {
			c.Exec(context.Background(), `DELETE FROM feature_flags.flags WHERE feature_id = 'syncarch'`)
			c.Exec(context.Background(), `DELETE FROM feature_flags.features WHERE feature_id = 'syncarch'`)
			c.Close(context.Background())
		}
	})
}
