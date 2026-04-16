// pbflags is the unified CLI for the pbflags feature flag system.
// It provides subcommands for syncing definitions, validating configs,
// compiling config bundles, and inspecting flag state.
//
// Usage:
//
//	pbflags sync       Sync definitions and conditions to the database
//	pbflags validate   Validate YAML config files against proto descriptors
//	pbflags show       Show resolved config for a specific flag
//	pbflags export     Export DB state as YAML config files
//	pbflags compile    Compile YAML configs into a binary bundle
//	pbflags load       Load a compiled bundle into the database
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SpotlightGOV/pbflags/db"
	"github.com/SpotlightGOV/pbflags/internal/configcli"
	"github.com/SpotlightGOV/pbflags/internal/configexport"
	"github.com/SpotlightGOV/pbflags/internal/evaluator"
	"github.com/SpotlightGOV/pbflags/internal/flagfile"
	"github.com/SpotlightGOV/pbflags/internal/projectconfig"
	defsync "github.com/SpotlightGOV/pbflags/internal/sync"
)

const usage = `pb — feature flag CLI

Usage:
  pb <command> [flags]

Config commands:
  init       Initialize a new pbflags project
  sync       Sync definitions and conditions to the database
  validate   Validate YAML config files against proto descriptors
  show       Show resolved config for a specific flag
  export     Export DB state as YAML config files
  compile    Compile YAML configs into a binary bundle
  load       Load a compiled bundle into the database
  format     Format YAML config files into canonical form
  lint       Detect breaking changes in proto definitions
  migrate    Run database migrations

Admin commands:
  flag       Flag operations (list, get, kill, unkill)
  launch     Launch lifecycle (list, get, ramp, status, kill, unkill)
  audit      View audit log
  lock       Acquire the global sync lock (--reason required, --status to view)
  unlock     Release the global sync lock
  condition  Per-condition value overrides (override, clear, list)

Auth commands:
  auth login   Save API credentials
  auth status  Show current identity
  auth logout  Remove stored credentials

Run "pb <command> -h" for command-specific help.
`

func main() {
	args, err := flagfile.ExpandArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(1)
	}

	switch args[0] {
	case "init":
		runInit(args[1:])
	case "sync":
		runSync(args[1:])
	case "validate":
		runValidate(args[1:])
	case "show":
		runShow(args[1:])
	case "export":
		runExport(args[1:])
	case "compile":
		runCompile(args[1:])
	case "load":
		runLoad(args[1:])
	case "format":
		runFormat(args[1:])
	case "lint":
		runLint(args[1:])
	case "migrate":
		runMigrate(args[1:])
	case "flag":
		runFlag(args[1:])
	case "launch":
		runLaunch(args[1:])
	case "audit":
		runAudit(args[1:])
	case "lock":
		runLock(args[1:])
	case "unlock":
		runUnlock(args[1:])
	case "condition":
		runCondition(args[1:])
	case "auth":
		runAuth(args[1:])
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "pb: unknown command %q\n\n", args[0])
		fmt.Fprint(os.Stderr, usage)
		os.Exit(1)
	}
}

// resolveProjectConfig loads .pbflags.yaml and applies defaults for
// descriptors and features paths. Nil pointers are safely skipped.
func resolveProjectConfig(descriptors, configDir *string) {
	projCfg, projRoot, projErr := projectconfig.Discover(".")
	if projErr != nil {
		slog.Debug("no .pbflags.yaml found", "error", projErr)
		return
	}
	if configDir != nil && *configDir == "" && projCfg.FeaturesPath != "" {
		*configDir = projCfg.FeaturesDir(projRoot)
	}
	if descriptors != nil && *descriptors == "" && projCfg.DescriptorsPath != "" {
		*descriptors = projCfg.DescriptorsFile(projRoot)
	}
}

// resolveEnv fills in flag values from environment variables if not set.
func resolveEnv(database, descriptors, configDir, sha *string) {
	if database != nil && *database == "" {
		*database = os.Getenv("PBFLAGS_DATABASE")
	}
	if descriptors != nil && *descriptors == "" {
		*descriptors = os.Getenv("PBFLAGS_DESCRIPTORS")
	}
	if configDir != nil && *configDir == "" {
		*configDir = os.Getenv("PBFLAGS_FEATURES")
	}
	if sha != nil && *sha == "" {
		*sha = os.Getenv("PBFLAGS_SHA")
	}
}

// --- sync ---

func runSync(args []string) {
	fs := flag.NewFlagSet("pbsync", flag.ExitOnError)
	database := fs.String("database", "", "PostgreSQL connection string (or PBFLAGS_DATABASE)")
	descriptors := fs.String("descriptors", "", "path to descriptors.pb (or PBFLAGS_DESCRIPTORS)")
	configDir := fs.String("features", "", "directory of YAML flag config files (or PBFLAGS_FEATURES)")
	sha := fs.String("sha", "", "Git commit SHA to record on synced features (or PBFLAGS_SHA)")
	fs.Parse(args)

	resolveEnv(database, descriptors, configDir, sha)
	resolveProjectConfig(descriptors, configDir)

	if *database == "" {
		slog.Error("--database flag or PBFLAGS_DATABASE env var is required")
		os.Exit(1)
	}
	if *descriptors == "" {
		slog.Error("--descriptors flag or PBFLAGS_DESCRIPTORS env var is required")
		os.Exit(1)
	}

	if err := doSync(context.Background(), *database, *descriptors, *configDir, *sha); err != nil {
		slog.Error("sync failed", "error", err)
		os.Exit(1)
	}
}

func doSync(ctx context.Context, dsn, descriptorPath, configDir, sha string) error {
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

// --- validate ---

func runValidate(args []string) {
	fs := flag.NewFlagSet("pbvalidate", flag.ExitOnError)
	descriptors := fs.String("descriptors", "", "path to descriptors.pb")
	configDir := fs.String("features", "", "directory of YAML config files")
	fs.Parse(args)

	resolveEnv(nil, descriptors, configDir, nil)
	resolveProjectConfig(descriptors, configDir)

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

// --- show ---

func runShow(args []string) {
	fs := flag.NewFlagSet("pbshow", flag.ExitOnError)
	descriptors := fs.String("descriptors", "", "path to descriptors.pb")
	configDir := fs.String("features", "", "directory of YAML config files")
	fs.Parse(args)

	resolveEnv(nil, descriptors, configDir, nil)
	resolveProjectConfig(descriptors, configDir)

	if *descriptors == "" || *configDir == "" || len(fs.Args()) == 0 {
		slog.Error("usage: pb show --descriptors=... --features=... <flag>")
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

// --- export ---

func runExport(args []string) {
	fs := flag.NewFlagSet("pbexport", flag.ExitOnError)
	database := fs.String("database", "", "PostgreSQL connection string")
	entityDim := fs.String("entity-dimension", "", "context dimension for per-entity override conditions (e.g., user_id)")
	outputDir := fs.String("output", "", "directory to write YAML files (default: stdout)")
	fs.Parse(args)

	resolveEnv(database, nil, nil, nil)

	if *database == "" {
		slog.Error("--database or PBFLAGS_DATABASE is required for export")
		os.Exit(1)
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, *database)
	if err != nil {
		slog.Error("connect to database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	configs, err := configexport.Export(ctx, pool, configexport.Options{
		EntityDimension: *entityDim,
	})
	if err != nil {
		slog.Error("export failed", "error", err)
		os.Exit(1)
	}

	if *outputDir != "" {
		os.MkdirAll(*outputDir, 0o755)
		for _, c := range configs {
			path := filepath.Join(*outputDir, c.FeatureID+".yaml")
			if err := os.WriteFile(path, c.YAML, 0o644); err != nil {
				slog.Error("write file", "path", path, "error", err)
				os.Exit(1)
			}
			fmt.Printf("wrote %s\n", path)
		}
	} else {
		for _, c := range configs {
			fmt.Printf("# feature: %s\n", c.FeatureID)
			os.Stdout.Write(c.YAML)
			fmt.Println("---")
		}
	}
}

// --- compile ---

func runCompile(args []string) {
	fs := flag.NewFlagSet("pbcompile", flag.ExitOnError)
	descriptors := fs.String("descriptors", "", "path to descriptors.pb")
	configDir := fs.String("features", "", "directory of YAML flag config files")
	output := fs.String("output", "bundle.pb", "output path for compiled bundle")
	fs.Parse(args)

	resolveEnv(nil, descriptors, configDir, nil)
	resolveProjectConfig(descriptors, configDir)

	if *descriptors == "" {
		slog.Error("--descriptors is required for compile")
		os.Exit(1)
	}
	if *configDir == "" {
		slog.Error("--features is required for compile")
		os.Exit(1)
	}

	descData, err := os.ReadFile(*descriptors)
	if err != nil {
		slog.Error("read descriptors", "error", err)
		os.Exit(1)
	}

	bundle, err := defsync.Compile(descData, *configDir)
	if err != nil {
		slog.Error("compile failed", "error", err)
		os.Exit(1)
	}

	if err := os.WriteFile(*output, bundle, 0o644); err != nil {
		slog.Error("write bundle", "error", err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s (%d bytes)\n", *output, len(bundle))
}

// --- load ---

func runLoad(args []string) {
	fs := flag.NewFlagSet("pbload", flag.ExitOnError)
	database := fs.String("database", "", "PostgreSQL connection string (or PBFLAGS_DATABASE)")
	bundlePath := fs.String("bundle", "", "path to compiled bundle")
	sha := fs.String("sha", "", "Git commit SHA to record on synced features")
	fs.Parse(args)

	resolveEnv(database, nil, nil, sha)

	if *database == "" {
		slog.Error("--database or PBFLAGS_DATABASE is required for load")
		os.Exit(1)
	}
	if *bundlePath == "" {
		slog.Error("--bundle is required for load")
		os.Exit(1)
	}

	bundleData, err := os.ReadFile(*bundlePath)
	if err != nil {
		slog.Error("read bundle", "error", err)
		os.Exit(1)
	}

	slog.Info("running database migrations")
	ctx := context.Background()
	if err := db.Migrate(ctx, *database); err != nil {
		slog.Error("migration failed", "error", err)
		os.Exit(1)
	}

	conn, err := pgx.Connect(ctx, *database)
	if err != nil {
		slog.Error("connect to database", "error", err)
		os.Exit(1)
	}
	defer conn.Close(ctx)

	result, err := defsync.LoadBundle(ctx, conn, bundleData, *sha)
	if err != nil {
		slog.Error("load failed", "error", err)
		os.Exit(1)
	}

	slog.Info("load complete",
		"features", result.Features,
		"flags_upserted", result.FlagsUpserted,
		"flags_archived", result.FlagsArchived,
		"conditions_updated", result.ConditionsUpdated,
	)
}
