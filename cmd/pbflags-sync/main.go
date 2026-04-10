// pbflags-sync reads a descriptors.pb file and syncs feature/flag definitions
// into PostgreSQL. It runs schema migrations automatically before syncing.
// Intended to run once per deploy in CI/CD pipelines.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/jackc/pgx/v5"

	"github.com/SpotlightGOV/pbflags/db"
	"github.com/SpotlightGOV/pbflags/internal/evaluator"
	defsync "github.com/SpotlightGOV/pbflags/internal/sync"
)

func main() {
	database := flag.String("database", "", "PostgreSQL connection string (or PBFLAGS_DATABASE)")
	descriptors := flag.String("descriptors", "", "path to descriptors.pb (or PBFLAGS_DESCRIPTORS)")
	flag.Parse()

	if *database == "" {
		*database = os.Getenv("PBFLAGS_DATABASE")
	}
	if *descriptors == "" {
		*descriptors = os.Getenv("PBFLAGS_DESCRIPTORS")
	}

	if *database == "" {
		slog.Error("--database flag or PBFLAGS_DATABASE env var is required")
		os.Exit(1)
	}
	if *descriptors == "" {
		slog.Error("--descriptors flag or PBFLAGS_DESCRIPTORS env var is required")
		os.Exit(1)
	}

	if err := run(context.Background(), *database, *descriptors); err != nil {
		slog.Error("sync failed", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, dsn, descriptorPath string) error {
	slog.Info("running database migrations")
	if err := db.Migrate(ctx, dsn); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}
	slog.Info("migrations complete")

	defs, err := evaluator.ParseDescriptorFile(descriptorPath)
	if err != nil {
		return fmt.Errorf("parse descriptors: %w", err)
	}

	if len(defs) == 0 {
		slog.Info("no flag definitions found in descriptors")
		return nil
	}

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer conn.Close(ctx)

	logger := slog.Default()
	result, err := defsync.SyncDefinitions(ctx, conn, defs, logger)
	if err != nil {
		return err
	}

	slog.Info("sync complete",
		"features", result.Features,
		"flags_upserted", result.FlagsUpserted,
		"flags_archived", result.FlagsArchived,
	)
	return nil
}
