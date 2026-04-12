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
	"github.com/SpotlightGOV/pbflags/internal/flagfile"
	defsync "github.com/SpotlightGOV/pbflags/internal/sync"
)

func main() {
	args, err := flagfile.ExpandArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	fs := flag.NewFlagSet("pbflags-sync", flag.ExitOnError)
	database := fs.String("database", "", "PostgreSQL connection string (or PBFLAGS_DATABASE)")
	descriptors := fs.String("descriptors", "", "path to descriptors.pb (or PBFLAGS_DESCRIPTORS)")
	configDir := fs.String("config", "", "directory of YAML flag config files (or PBFLAGS_CONFIG)")
	fs.Parse(args)

	if *database == "" {
		*database = os.Getenv("PBFLAGS_DATABASE")
	}
	if *descriptors == "" {
		*descriptors = os.Getenv("PBFLAGS_DESCRIPTORS")
	}
	if *configDir == "" {
		*configDir = os.Getenv("PBFLAGS_CONFIG")
	}

	if *database == "" {
		slog.Error("--database flag or PBFLAGS_DATABASE env var is required")
		os.Exit(1)
	}
	if *descriptors == "" {
		slog.Error("--descriptors flag or PBFLAGS_DESCRIPTORS env var is required")
		os.Exit(1)
	}

	if err := run(context.Background(), *database, *descriptors, *configDir); err != nil {
		slog.Error("sync failed", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, dsn, descriptorPath, configDir string) error {
	slog.Info("running database migrations")
	if err := db.Migrate(ctx, dsn); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}
	slog.Info("migrations complete")

	descriptorData, err := os.ReadFile(descriptorPath)
	if err != nil {
		return fmt.Errorf("read descriptors: %w", err)
	}

	defs, err := evaluator.ParseDescriptors(descriptorData)
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

	if configDir != "" {
		condResult, condErr := defsync.SyncConditions(ctx, conn, configDir, descriptorData, defs, logger)
		if condErr != nil {
			return fmt.Errorf("sync conditions: %w", condErr)
		}
		for _, w := range condResult.Warnings {
			slog.Warn(w)
		}
		slog.Info("conditions sync complete", "flags_updated", condResult.FlagsUpdated)
	}

	return nil
}
