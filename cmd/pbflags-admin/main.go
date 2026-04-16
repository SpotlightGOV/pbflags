// Binary pbflags-admin is the flag management control plane.
//
// It provides the admin API, web UI, and a local evaluator interface.
// Requires a PostgreSQL database with R/W access.
//
// Normal mode (requires external "pbflags sync" for migrations and definition sync):
//
//	pbflags-admin --database=postgres://user:pass@localhost:5432/flags
//
// Standalone mode (single instance — runs migrations, syncs definitions, and
// evaluates flags in one process):
//
//	pbflags-admin --standalone --descriptors=descriptors.pb \
//	  --database=postgres://user:pass@localhost:5432/flags
//
// Standalone mode with a pre-compiled bundle (no proto/CEL tooling needed):
//
//	pbflags-admin --standalone --bundle=bundle.pb \
//	  --database=postgres://user:pass@localhost:5432/flags
//
// Flags can also be supplied via picocli-style @file references:
//
//	pbflags-admin @config.flags
//
// Environment variables override CLI flags:
//
//	PBFLAGS_DATABASE            PostgreSQL connection string
//	PBFLAGS_ADMIN               Admin listen address (default: :9200)
//	PBFLAGS_LISTEN              Evaluator listen address (default: :9201)
//	PBFLAGS_DESCRIPTORS         Path to descriptors.pb (standalone only)
//	PBFLAGS_BUNDLE              Path to compiled bundle.pb (standalone only; mutually exclusive with PBFLAGS_DESCRIPTORS)
//	PBFLAGS_ENV_NAME            Environment label shown in admin UI
//	PBFLAGS_ENV_COLOR           Accent color for admin UI environment banner
//	PBFLAGS_CACHE_KILL_TTL      Kill set cache TTL (default: 30s)
//	PBFLAGS_CACHE_FLAG_TTL      Global flag state cache TTL (default: 10m)
//	PBFLAGS_CACHE_OVERRIDE_TTL  Per-entity override cache TTL (default: 10m)
//	PBFLAGS_AUTH_STRATEGY       Authentication strategy: none, shared-secret, trusted-header (default: none)
//	PBFLAGS_AUTH_TOKEN           Shared-secret Bearer token (required if strategy=shared-secret)
//	PBFLAGS_AUTH_HEADER          Header name for trusted-header strategy (default: X-Forwarded-User)
//	PBFLAGS_ALLOW_RUNTIME_OVERRIDES  Allow state-changing admin RPCs and UI controls (default: true; set =false for read-only admin)
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"connectrpc.com/connect"
	"connectrpc.com/otelconnect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/SpotlightGOV/pbflags/db"
	"github.com/SpotlightGOV/pbflags/gen/pbflags/v1/pbflagsv1connect"
	"github.com/SpotlightGOV/pbflags/internal/admin"
	adminweb "github.com/SpotlightGOV/pbflags/internal/admin/web"
	"github.com/SpotlightGOV/pbflags/internal/authn"
	"github.com/SpotlightGOV/pbflags/internal/evaluator"
	"github.com/SpotlightGOV/pbflags/internal/flagfile"
	"github.com/SpotlightGOV/pbflags/internal/projectconfig"
	defsync "github.com/SpotlightGOV/pbflags/internal/sync"
)

var version = "dev"

func main() {
	args, err := flagfile.ExpandArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	fs := flag.NewFlagSet("pbflags-admin", flag.ExitOnError)
	database := fs.String("database", "", "PostgreSQL connection string")
	listen := fs.String("listen", "", "Admin listen address (default :9200)")
	evaluatorListen := fs.String("evaluator-listen", "", "Evaluator listen address (default :9201, empty to disable)")
	standalone := fs.Bool("standalone", false, "Run all roles in one process (admin + evaluator + sync + migrations)")
	descriptors := fs.String("descriptors", "", "Path to descriptors.pb (requires --standalone)")
	bundle := fs.String("bundle", "", "Path to compiled bundle.pb (requires --standalone; mutually exclusive with --descriptors)")
	configDir := fs.String("features", "", "Directory of YAML flag config files (standalone; syncs conditions on startup)")
	killTTL := fs.Duration("cache-kill-ttl", 0, "Kill set cache TTL (default 30s)")
	flagTTL := fs.Duration("cache-flag-ttl", 0, "Global flag state cache TTL (default 10m)")
	_ = fs.Duration("cache-override-ttl", 0, "Deprecated: overrides removed") // kept for flag-file compat
	envName := fs.String("env-name", "", "Environment label shown in admin UI")
	envColor := fs.String("env-color", "", "Accent color for admin UI environment banner (hex)")
	devAssets := fs.String("dev-assets", "", "Read admin UI assets from disk for live reload (dev only)")
	allowRuntimeOverrides := fs.Bool("allow-runtime-overrides", true, "Allow state-changing admin RPCs and UI controls (condition overrides, sync lock, flag/launch kill, launch ramp adjustments). Default on. Set =false for read-only admin. Also via PBFLAGS_ALLOW_RUNTIME_OVERRIDES.")
	fs.Parse(args)

	setEnvIfFlag("PBFLAGS_DATABASE", *database)
	setEnvIfFlag("PBFLAGS_ADMIN", *listen)
	setEnvIfFlag("PBFLAGS_LISTEN", *evaluatorListen)
	setEnvIfFlag("PBFLAGS_DESCRIPTORS", *descriptors)
	setEnvIfFlag("PBFLAGS_BUNDLE", *bundle)
	setEnvIfFlag("PBFLAGS_ENV_NAME", *envName)
	setEnvIfFlag("PBFLAGS_ENV_COLOR", *envColor)
	setDurationEnvIfFlag("PBFLAGS_CACHE_KILL_TTL", *killTTL)
	setDurationEnvIfFlag("PBFLAGS_CACHE_FLAG_TTL", *flagTTL)
	// PBFLAGS_CACHE_OVERRIDE_TTL removed — overrides no longer exist.

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

	cfg := evaluator.LoadConfig()
	// Admin listen defaults to :9200.
	if cfg.Admin == "" {
		cfg.Admin = ":9200"
	}
	// Evaluator listen defaults to :9201.
	if cfg.Listen == "" {
		cfg.Listen = ":9201"
	}

	// Validation.
	if cfg.Database == "" {
		fmt.Fprintln(os.Stderr, "error: --database or PBFLAGS_DATABASE is required")
		os.Exit(1)
	}
	if !*standalone && cfg.Descriptors != "" {
		fmt.Fprintln(os.Stderr, "error: --descriptors requires --standalone")
		os.Exit(1)
	}
	if !*standalone && cfg.Bundle != "" {
		fmt.Fprintln(os.Stderr, "error: --bundle requires --standalone")
		os.Exit(1)
	}
	if cfg.Descriptors != "" && cfg.Bundle != "" {
		fmt.Fprintln(os.Stderr, "error: --descriptors and --bundle are mutually exclusive")
		os.Exit(1)
	}
	if *standalone && cfg.Descriptors == "" && cfg.Bundle == "" {
		fmt.Fprintln(os.Stderr, "error: --descriptors or --bundle is required with --standalone")
		os.Exit(1)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	authCfg := authn.LoadConfig()
	auth, err := authn.NewAuthenticator(authCfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if authCfg.Strategy != "" && authCfg.Strategy != "none" {
		logger.Info("admin auth enabled", "strategy", authCfg.Strategy)
	}

	// Env var fills in when the flag wasn't explicitly passed; explicit
	// --allow-runtime-overrides= wins over env. Accepts standard boolean
	// strings ("true"/"false"/"1"/"0"/"yes"/"no"/"on"/"off"). Anything
	// else is ignored — silent fallback to the flag default keeps a
	// typo'd env var from quietly flipping policy.
	flagWasSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "allow-runtime-overrides" {
			flagWasSet = true
		}
	})
	if !flagWasSet {
		if v := os.Getenv("PBFLAGS_ALLOW_RUNTIME_OVERRIDES"); v != "" {
			if b, err := strconv.ParseBool(v); err == nil {
				*allowRuntimeOverrides = b
			} else {
				logger.Warn("ignoring unparseable PBFLAGS_ALLOW_RUNTIME_OVERRIDES", "value", v)
			}
		}
	}
	if !*allowRuntimeOverrides {
		logger.Info("runtime overrides disabled — admin is read-only (no overrides, kills, ramps, or sync lock)")
	}

	if err := run(cfg, *standalone, *configDir, *devAssets, *allowRuntimeOverrides, auth, logger); err != nil {
		if held, ok := defsync.IsLockHeld(err); ok {
			fmt.Fprintf(os.Stderr,
				"\nSync is LOCKED.\n  holder: %s\n  reason: %s\n  since:  %s\n\nUnlock with: pb unlock\n\n",
				held.Info.Actor, held.Info.Reason, held.Info.CreatedAt.Format("2006-01-02 15:04:05 MST"))
			os.Exit(2)
		}
		logger.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func setEnvIfFlag(key, value string) {
	if value != "" {
		os.Setenv(key, value)
	}
}

// envBool returns true when the named env var holds a truthy value
// ("1", "true", "yes" — case-insensitive). Empty/unset → false.
func envBool(key string) bool {
	switch strings.ToLower(os.Getenv(key)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func setDurationEnvIfFlag(key string, d time.Duration) {
	if d > 0 {
		os.Setenv(key, d.String())
	}
}

func run(cfg evaluator.Config, standalone bool, configDir, devAssetsDir string, allowRuntimeOverrides bool, auth authn.Authenticator, logger *slog.Logger) error {
	mode := "normal"
	if standalone {
		mode = "standalone"
	}
	logger.Info("starting pbflags admin",
		"version", version,
		"mode", mode,
		"admin_listen", cfg.Admin,
		"evaluator_listen", cfg.Listen)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	shutdownTracer, err := evaluator.InitTracer(ctx, "pbflags-admin", version)
	if err != nil {
		return fmt.Errorf("init tracer: %w", err)
	}
	defer shutdownTracer(context.Background())

	metrics := evaluator.NewMetrics(prometheus.DefaultRegisterer)
	tracker := evaluator.NewHealthTracker(metrics)
	tracer := otel.Tracer("pbflags/evaluator")

	// ── Database setup ──────────────────────────────────────────────

	if standalone {
		// Standalone: run migrations (DDL required).
		logger.Info("running database migrations")
		if err := db.Migrate(ctx, cfg.Database); err != nil {
			return fmt.Errorf("migration failed: %w", err)
		}
		logger.Info("migrations complete")
	} else {
		// Normal: verify schema is already migrated (no DDL needed).
		if err := db.CheckSchemaVersion(ctx, cfg.Database); err != nil {
			return fmt.Errorf("schema check: %w", err)
		}
	}

	pool, err := pgxpool.New(ctx, cfg.Database)
	if err != nil {
		return fmt.Errorf("create database pool: %w", err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}
	logger.Info("database connected")

	// ── Standalone lease check ──────────────────────────────────────

	instanceID := instanceIdentifier()
	if standalone {
		checkStandaloneLease(ctx, pool, instanceID, logger)
		go runLeaseHeartbeat(ctx, pool, instanceID)
	}

	// ── Definitions ─────────────────────────────────────────────────

	if standalone && cfg.Bundle != "" {
		// Bundle mode: load pre-compiled bundle to DB.
		bundleData, err := os.ReadFile(cfg.Bundle)
		if err != nil {
			return fmt.Errorf("read bundle: %w", err)
		}
		syncConn, err := pgx.Connect(ctx, cfg.Database)
		if err != nil {
			return fmt.Errorf("connect for sync: %w", err)
		}
		result, err := defsync.LoadBundle(ctx, syncConn, bundleData, "")
		syncConn.Close(ctx)
		if err != nil {
			return fmt.Errorf("load bundle: %w", err)
		}
		logger.Info("bundle loaded",
			"features", result.Features,
			"flags_upserted", result.FlagsUpserted,
			"flags_archived", result.FlagsArchived,
			"conditions_updated", result.ConditionsUpdated)
	} else if standalone {
		// Descriptor mode: parse descriptors and sync to DB.
		defs, err := evaluator.ParseDescriptorFile(cfg.Descriptors)
		if err != nil {
			return fmt.Errorf("parse descriptors: %w", err)
		}

		syncConn, err := pgx.Connect(ctx, cfg.Database)
		if err != nil {
			return fmt.Errorf("connect for sync: %w", err)
		}
		result, err := defsync.SyncDefinitions(ctx, syncConn, defs, logger)
		if err != nil {
			syncConn.Close(ctx)
			return fmt.Errorf("sync definitions: %w", err)
		}
		logger.Info("definitions synced",
			"features", result.Features,
			"flags_upserted", result.FlagsUpserted,
			"flags_archived", result.FlagsArchived)

		if configDir != "" {
			descriptorData, readErr := os.ReadFile(cfg.Descriptors)
			if readErr != nil {
				syncConn.Close(ctx)
				return fmt.Errorf("read descriptors for conditions: %w", readErr)
			}
			condResult, condErr := defsync.SyncConditions(ctx, syncConn, configDir, descriptorData, defs, logger, "")
			if condErr != nil {
				syncConn.Close(ctx)
				return fmt.Errorf("sync conditions: %w", condErr)
			}
			for _, w := range condResult.Warnings {
				logger.Warn(w)
			}
			logger.Info("conditions synced", "flags_updated", condResult.FlagsUpdated)
		}
		syncConn.Close(ctx)
	}

	// ── Cache + Evaluator ───────────────────────────────────────────

	cache, err := evaluator.NewCacheStore(evaluator.CacheStoreConfig{
		FlagTTL:       cfg.Cache.FlagTTL,
		JitterPercent: cfg.Cache.JitterPercent,
	})
	if err != nil {
		return fmt.Errorf("create cache: %w", err)
	}
	defer cache.Close()

	dbFetcher := evaluator.NewDBFetcher(pool, tracker,
		logger.With("component", "db-fetcher"), metrics, tracer)

	var evalOpts []evaluator.EvaluatorOption
	if cfg.Cache.FlagTTL > cfg.Cache.KillTTL {
		killPoller := evaluator.NewKillPoller(dbFetcher, cache, tracker,
			cfg.Cache.KillTTL, cfg.Cache.FetchTimeout,
			logger.With("component", "kill-poller"), metrics)
		go killPoller.Run(ctx)
	} else {
		logger.Info("kill set poller disabled (flag_ttl <= kill_ttl), using inline kill checks")
		evalOpts = append(evalOpts, evaluator.WithInlineKillCheck())
	}

	eval := evaluator.NewEvaluator(cache, dbFetcher, logger, metrics, evalOpts...)

	if standalone {
		sighupCh := make(chan os.Signal, 1)
		signal.Notify(sighupCh, syscall.SIGHUP)
		reloadLogger := logger.With("component", "reload")

		if cfg.Bundle != "" {
			// Bundle mode: watch bundle file for changes → LoadBundle.
			bundleWatcher := evaluator.NewFileWatcher(cfg.Bundle,
				30*time.Second, sighupCh, reloadLogger,
				func(_ context.Context, trigger string) error {
					data, err := os.ReadFile(cfg.Bundle)
					if err != nil {
						return fmt.Errorf("read bundle: %w", err)
					}
					syncConn, err := pgx.Connect(ctx, cfg.Database)
					if err != nil {
						return fmt.Errorf("connect for sync: %w", err)
					}
					defer syncConn.Close(ctx)

					result, err := defsync.LoadBundle(ctx, syncConn, data, "")
					if err != nil {
						return fmt.Errorf("load bundle: %w", err)
					}
					reloadLogger.Info("bundle reloaded",
						"trigger", trigger,
						"features", result.Features,
						"flags_upserted", result.FlagsUpserted,
						"flags_archived", result.FlagsArchived,
						"conditions_updated", result.ConditionsUpdated)
					return nil
				})
			go bundleWatcher.Run(ctx)
		} else {
			// Descriptor mode: watch descriptors file → SyncDefinitions + SyncConditions.
			watcher := evaluator.NewDescriptorWatcher(cfg.Descriptors,
				30*time.Second, sighupCh, reloadLogger)
			watcher.SetSyncAndReload(func(syncCtx context.Context, parsedDefs []evaluator.FlagDef) error {
				syncConn, err := pgx.Connect(syncCtx, cfg.Database)
				if err != nil {
					return fmt.Errorf("connect for sync: %w", err)
				}
				defer syncConn.Close(syncCtx)

				if _, err := defsync.SyncDefinitions(syncCtx, syncConn, parsedDefs, logger); err != nil {
					return fmt.Errorf("sync definitions: %w", err)
				}

				// Also re-sync conditions if a config directory is configured.
				if configDir != "" {
					descriptorData, err := os.ReadFile(cfg.Descriptors)
					if err != nil {
						return fmt.Errorf("read descriptors for conditions: %w", err)
					}
					if _, err := defsync.SyncConditions(syncCtx, syncConn, configDir, descriptorData, parsedDefs, logger, ""); err != nil {
						return fmt.Errorf("sync conditions: %w", err)
					}
				}
				return nil
			})
			go watcher.Run(ctx)
		}
	}

	// ── Admin server ────────────────────────────────────────────────

	adminLogger := logger.With("component", "admin")
	store := admin.NewStore(pool, adminLogger)
	var adminOpts []admin.AdminServiceOption
	if allowRuntimeOverrides {
		adminOpts = append(adminOpts, admin.WithAllowRuntimeOverrides())
	}
	adminService := admin.NewAdminService(store, adminLogger, adminOpts...)

	// Inner mux holds all authenticated admin routes.
	adminMux := http.NewServeMux()
	adminPath, adminHandler := pbflagsv1connect.NewFlagAdminServiceHandler(adminService)
	adminMux.Handle(adminPath, adminHandler)

	webHandler, err := adminweb.NewHandler(store, adminLogger, adminweb.EnvConfig{
		Name:                  cfg.EnvName,
		Color:                 cfg.EnvColor,
		Version:               version,
		DevAssetsDir:          devAssetsDir,
		AllowRuntimeOverrides: allowRuntimeOverrides,
	})
	if err != nil {
		return fmt.Errorf("create web handler: %w", err)
	}
	webHandler.Register(adminMux)

	// Outer mux: healthz is unauthenticated, everything else goes
	// through the auth middleware.
	outerMux := http.NewServeMux()
	outerMux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := pool.Ping(r.Context()); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprintf(w, "unhealthy: %v", err)
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})
	outerMux.Handle("/", authn.Middleware(auth, adminMux))

	adminServer := &http.Server{
		Addr:    cfg.Admin,
		Handler: h2c.NewHandler(outerMux, &http2.Server{}),
	}

	go func() {
		adminLogger.Info("admin server listening", "addr", cfg.Admin)
		if err := adminServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			adminLogger.Error("admin server error", "error", err)
		}
	}()

	// ── Evaluator server ────────────────────────────────────────────

	svc := evaluator.NewService(eval, tracker, cache, dbFetcher)

	serverOtelInt, err := otelconnect.NewInterceptor()
	if err != nil {
		return fmt.Errorf("create server otel interceptor: %w", err)
	}

	evalMux := http.NewServeMux()
	evalPath, evalHandler := pbflagsv1connect.NewFlagEvaluatorServiceHandler(svc,
		connect.WithInterceptors(serverOtelInt))
	evalMux.Handle(evalPath, evalHandler)
	evalMux.Handle("/metrics", promhttp.Handler())

	evalMux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		status := tracker.Status()
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "status=%s", status.String())
	})

	evalServer := &http.Server{
		Addr:    cfg.Listen,
		Handler: h2c.NewHandler(evalMux, &http2.Server{}),
	}

	go func() {
		logger.Info("evaluator server listening", "addr", cfg.Listen)
		if err := evalServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("evaluator server error", "error", err)
		}
	}()

	// ── Shutdown ────────────────────────────────────────────────────

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	sig := <-sigCh
	logger.Info("shutting down", "signal", sig.String())

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	adminServer.Shutdown(shutdownCtx)
	evalServer.Shutdown(shutdownCtx)
	cancel()
	return nil
}

// ── Standalone lease ────────────────────────────────────────────────

func instanceIdentifier() string {
	hostname, _ := os.Hostname()
	return fmt.Sprintf("%s/pid-%d", hostname, os.Getpid())
}

func checkStandaloneLease(ctx context.Context, pool *pgxpool.Pool, instanceID string, logger *slog.Logger) {
	var existingInstance string
	var heartbeat time.Time
	err := pool.QueryRow(ctx, `
		SELECT instance_id, heartbeat_at FROM feature_flags.standalone_lease
		WHERE id = 'singleton' AND heartbeat_at > now() - interval '2 minutes'
	`).Scan(&existingInstance, &heartbeat)

	if err == nil && existingInstance != instanceID {
		logger.Warn("STANDALONE CONFLICT: another standalone instance is active",
			"other_instance", existingInstance,
			"last_heartbeat", heartbeat)
		logger.Warn("Running multiple standalone instances risks split-brain definition conflicts. " +
			"If you are certain the other instance is gone, this warning will clear within 2 minutes.")
	}

	// Upsert our lease.
	_, err = pool.Exec(ctx, `
		INSERT INTO feature_flags.standalone_lease (id, instance_id, started_at, heartbeat_at)
		VALUES ('singleton', $1, now(), now())
		ON CONFLICT (id) DO UPDATE SET
			instance_id = EXCLUDED.instance_id,
			started_at = EXCLUDED.started_at,
			heartbeat_at = EXCLUDED.heartbeat_at
	`, instanceID)
	if err != nil {
		logger.Warn("failed to acquire standalone lease", "error", err)
	}
}

func runLeaseHeartbeat(ctx context.Context, pool *pgxpool.Pool, instanceID string) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Clear lease on shutdown.
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
			pool.Exec(cleanupCtx, `
				DELETE FROM feature_flags.standalone_lease WHERE instance_id = $1
			`, instanceID)
			cleanupCancel()
			return
		case <-ticker.C:
			pool.Exec(ctx, `
				UPDATE feature_flags.standalone_lease
				SET heartbeat_at = now()
				WHERE instance_id = $1
			`, instanceID)
		}
	}
}
