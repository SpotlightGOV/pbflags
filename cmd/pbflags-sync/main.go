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
	"github.com/SpotlightGOV/pbflags/internal/configcli"
	"github.com/SpotlightGOV/pbflags/internal/evaluator"
	"github.com/SpotlightGOV/pbflags/internal/flagfile"
	"github.com/SpotlightGOV/pbflags/internal/projectconfig"
	defsync "github.com/SpotlightGOV/pbflags/internal/sync"
)

func main() {
	args, err := flagfile.ExpandArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	// Check for subcommands: "validate" or "show".
	if len(args) > 0 {
		switch args[0] {
		case "validate":
			runValidate(args[1:])
			return
		case "show":
			runShow(args[1:])
			return
		}
	}

	fs := flag.NewFlagSet("pbflags-sync", flag.ExitOnError)
	database := fs.String("database", "", "PostgreSQL connection string (or PBFLAGS_DATABASE)")
	descriptors := fs.String("descriptors", "", "path to descriptors.pb (or PBFLAGS_DESCRIPTORS)")
	configDir := fs.String("features", "", "directory of YAML flag config files (or PBFLAGS_FEATURES)")
	sha := fs.String("sha", "", "Git commit SHA to record on synced features (or PBFLAGS_SHA)")
	fs.Parse(args)

	if *database == "" {
		*database = os.Getenv("PBFLAGS_DATABASE")
	}
	if *descriptors == "" {
		*descriptors = os.Getenv("PBFLAGS_DESCRIPTORS")
	}
	if *configDir == "" {
		*configDir = os.Getenv("PBFLAGS_FEATURES")
	}
	if *sha == "" {
		*sha = os.Getenv("PBFLAGS_SHA")
	}

	// Load project config for defaults.
	projCfg, projRoot, projErr := projectconfig.Discover(".")
	if projErr != nil {
		slog.Warn("failed to load .pbflags.yaml", "error", projErr)
	}
	if projCfg.FeaturesPath != "" {
		featDir := projCfg.FeaturesDir(projRoot)
		if *configDir == "" {
			*configDir = featDir
		}
	}

	if *database == "" {
		slog.Error("--database flag or PBFLAGS_DATABASE env var is required")
		os.Exit(1)
	}
	if *descriptors == "" {
		slog.Error("--descriptors flag or PBFLAGS_DESCRIPTORS env var is required")
		os.Exit(1)
	}

	if err := run(context.Background(), *database, *descriptors, *configDir, *sha); err != nil {
		slog.Error("sync failed", "error", err)
		os.Exit(1)
	}
}

func runValidate(args []string) {
	fs := flag.NewFlagSet("validate", flag.ExitOnError)
	descriptors := fs.String("descriptors", "", "path to descriptors.pb")
	configDir := fs.String("features", "", "directory of YAML config files")
	fs.Parse(args)

	if *descriptors == "" {
		*descriptors = os.Getenv("PBFLAGS_DESCRIPTORS")
	}
	if *configDir == "" {
		*configDir = os.Getenv("PBFLAGS_FEATURES")
	}

	// Load project config for defaults.
	projCfg, projRoot, projErr := projectconfig.Discover(".")
	if projErr != nil {
		slog.Warn("failed to load .pbflags.yaml", "error", projErr)
	}
	if projCfg.FeaturesPath != "" {
		featDir := projCfg.FeaturesDir(projRoot)
		if *configDir == "" {
			*configDir = featDir
		}
	}

	if *descriptors == "" || *configDir == "" {
		slog.Error("--descriptors and --features are required for validate")
		os.Exit(1)
	}

	descData, err := os.ReadFile(*descriptors)
	if err != nil {
		slog.Error("read descriptors", "error", err)
		os.Exit(1)
	}

	result, err := configcli.Validate(descData, *configDir)
	if err != nil {
		slog.Error("validation failed", "error", err)
		os.Exit(1)
	}

	for _, w := range result.Warnings {
		slog.Warn(w)
	}
	for _, e := range result.Errors {
		slog.Error(e)
	}

	if len(result.Errors) > 0 {
		fmt.Fprintf(os.Stderr, "\nValidation FAILED: %d error(s) in %d file(s)\n", len(result.Errors), result.Files)
		os.Exit(1)
	}
	fmt.Printf("Validation OK: %d file(s), %d flag(s)\n", result.Files, result.Flags)
}

func runShow(args []string) {
	fs := flag.NewFlagSet("show", flag.ExitOnError)
	descriptors := fs.String("descriptors", "", "path to descriptors.pb")
	configDir := fs.String("features", "", "directory of YAML config files")
	fs.Parse(args)

	if *descriptors == "" {
		*descriptors = os.Getenv("PBFLAGS_DESCRIPTORS")
	}
	if *configDir == "" {
		*configDir = os.Getenv("PBFLAGS_FEATURES")
	}

	// Load project config for defaults.
	projCfg, projRoot, projErr := projectconfig.Discover(".")
	if projErr != nil {
		slog.Warn("failed to load .pbflags.yaml", "error", projErr)
	}
	if projCfg.FeaturesPath != "" {
		featDir := projCfg.FeaturesDir(projRoot)
		if *configDir == "" {
			*configDir = featDir
		}
	}

	if *descriptors == "" || *configDir == "" || len(fs.Args()) == 0 {
		slog.Error("usage: pbflags-sync show --descriptors=... --features=... <flag>")
		os.Exit(1)
	}

	descData, err := os.ReadFile(*descriptors)
	if err != nil {
		slog.Error("read descriptors", "error", err)
		os.Exit(1)
	}

	if err := configcli.Show(descData, *configDir, fs.Args()[0], os.Stdout); err != nil {
		slog.Error("show failed", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, dsn, descriptorPath, configDir, sha string) error {
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
		condResult, condErr := defsync.SyncConditions(ctx, conn, configDir, descriptorData, defs, logger, sha)
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
