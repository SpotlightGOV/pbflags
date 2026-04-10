// Binary pbflags-server is the feature flag evaluation service.
//
// Monolithic mode (single instance, auto-migrates, syncs descriptors to DB):
//
//	pbflags-server --descriptors=descriptors.pb --database=postgres://... --admin=:8080
//
// Distributed mode (multi-instance, reads definitions from DB only):
//
//	pbflags-server --distributed --database=postgres://...
//
// Proxy mode (forwards to upstream root evaluator):
//
//	pbflags-server --upstream=http://root-evaluator:9201
//
// WARNING: Do not run multiple instances without --distributed. Monolithic mode
// is single-instance only. Running multiple monolithic instances causes split-brain
// definition conflicts. Use pbflags-sync in CI/CD and --distributed for production
// multi-instance deployments.
//
// Environment variables override config file values:
//
//	PBFLAGS_DESCRIPTORS  Path to descriptors.pb
//	PBFLAGS_UPSTREAM     Upstream evaluator URL (proxy mode)
//	PBFLAGS_LISTEN       Evaluator listen address
//	PBFLAGS_ADMIN        Admin listen address (enables combined mode)
//	PBFLAGS_DATABASE     PostgreSQL connection string
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
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
	"github.com/SpotlightGOV/pbflags/internal/evaluator"
	defsync "github.com/SpotlightGOV/pbflags/internal/sync"
)

var version = "dev"

func main() {
	configPath := flag.String("config", "", "Path to configuration YAML file")
	adminAddr := flag.String("admin", "", "Admin server listen address (enables combined mode)")
	databaseURL := flag.String("database", "", "PostgreSQL connection string (root mode)")
	descriptors := flag.String("descriptors", "", "Path to descriptors.pb")
	listen := flag.String("listen", "", "Evaluator listen address")
	upstreamURL := flag.String("upstream", "", "Upstream evaluator URL (proxy mode)")
	upgrade := flag.Bool("upgrade", false, "Run database migrations and exit (legacy; monolithic mode auto-migrates)")
	exitAfterUpgrade := flag.Bool("exit-after-upgrade", false, "Exit after running migrations (requires --upgrade)")
	distributed := flag.Bool("distributed", false, "Distributed mode: definitions loaded from DB only (use pbflags-sync for deploy)")
	defPollInterval := flag.Duration("definition-poll-interval", 0, "How often to poll DB for definition changes (default 60s)")
	envName := flag.String("env-name", "", "Environment label shown in admin UI (e.g. production, staging, dev)")
	envColor := flag.String("env-color", "", "Accent color for admin UI environment banner (hex, e.g. #f87171)")
	devAssets := flag.String("dev-assets", "", "Read admin UI assets from disk for live reload (dev only)")
	flag.Parse()

	setEnvIfFlag("PBFLAGS_ENV_NAME", *envName)
	setEnvIfFlag("PBFLAGS_ENV_COLOR", *envColor)

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	setEnvIfFlag("PBFLAGS_ADMIN", *adminAddr)
	setEnvIfFlag("PBFLAGS_DATABASE", *databaseURL)
	setEnvIfFlag("PBFLAGS_DESCRIPTORS", *descriptors)
	setEnvIfFlag("PBFLAGS_LISTEN", *listen)
	setEnvIfFlag("PBFLAGS_UPSTREAM", *upstreamURL)

	// Legacy --upgrade: run migrations and optionally exit.
	if *upgrade {
		dsn := *databaseURL
		if dsn == "" {
			dsn = os.Getenv("PBFLAGS_DATABASE")
		}
		if dsn == "" {
			fmt.Fprintln(os.Stderr, "error: --database or PBFLAGS_DATABASE required for --upgrade")
			os.Exit(1)
		}
		logger.Info("running database migrations")
		if err := db.Migrate(context.Background(), dsn); err != nil {
			logger.Error("migration failed", "error", err)
			os.Exit(1)
		}
		logger.Info("migrations complete")
		if *exitAfterUpgrade {
			return
		}
	}

	// Determine server mode.
	//   --distributed                     → distributed (DB-only, no descriptors)
	//   --upstream                         → proxy
	//   --descriptors + --database         → monolithic (auto-migrates, syncs, loads from DB)
	//   --descriptors only                 → classic (legacy, no DB definitions)
	resolveUpstream := *upstreamURL != "" || os.Getenv("PBFLAGS_UPSTREAM") != ""
	resolveDescriptors := *descriptors != "" || os.Getenv("PBFLAGS_DESCRIPTORS") != ""
	resolveDatabase := *databaseURL != "" || os.Getenv("PBFLAGS_DATABASE") != ""

	var mode evaluator.ServerMode
	switch {
	case *distributed:
		mode = evaluator.ModeDistributed
	case resolveUpstream:
		mode = evaluator.ModeProxy
	case resolveDescriptors && resolveDatabase:
		mode = evaluator.ModeMonolithic
	default:
		mode = evaluator.ModeClassic
	}

	if err := run(*configPath, logger, *devAssets, mode, *defPollInterval); err != nil {
		logger.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func setEnvIfFlag(key, value string) {
	if value != "" {
		os.Setenv(key, value)
	}
}

func run(configPath string, logger *slog.Logger, devAssetsDir string, mode evaluator.ServerMode, defPollInterval time.Duration) error {
	cfg, err := evaluator.LoadConfigWithMode(configPath, mode, defPollInterval)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	logger.Info("starting pbflags server",
		"version", version,
		"mode", modeName(cfg.Mode),
		"listen", cfg.Listen)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	shutdownTracer, err := evaluator.InitTracer(ctx, "pbflags-server", version)
	if err != nil {
		return fmt.Errorf("init tracer: %w", err)
	}
	defer shutdownTracer(context.Background())

	metrics := evaluator.NewMetrics(prometheus.DefaultRegisterer)
	tracker := evaluator.NewHealthTracker(metrics)
	tracer := otel.Tracer("pbflags/evaluator")

	// ── Build the defaults registry ──────────────────────────────────
	//
	// Monolithic:  migrate → sync → load from DB → registry
	// Distributed: schema check → load from DB → registry (no migrations)
	// Classic:     parse descriptors → registry
	// Proxy:       parse descriptors → registry

	var (
		reg  *evaluator.Registry
		pool *pgxpool.Pool
		defs []evaluator.FlagDef
	)

	switch cfg.Mode {
	case evaluator.ModeMonolithic:
		// 1. Always run migrations in monolithic mode.
		logger.Info("running database migrations")
		if err := db.Migrate(ctx, cfg.Database); err != nil {
			return fmt.Errorf("migration failed: %w", err)
		}
		logger.Info("migrations complete")

		// 2. Connect pool.
		pool, err = pgxpool.New(ctx, cfg.Database)
		if err != nil {
			return fmt.Errorf("create database pool: %w", err)
		}
		defer pool.Close()

		// 4. Parse descriptors and sync to DB.
		defs, err = evaluator.ParseDescriptorFile(cfg.Descriptors)
		if err != nil {
			return fmt.Errorf("parse descriptors: %w", err)
		}

		syncConn, err := pgx.Connect(ctx, cfg.Database)
		if err != nil {
			return fmt.Errorf("connect for sync: %w", err)
		}
		result, err := defsync.SyncDefinitions(ctx, syncConn, defs, logger)
		syncConn.Close(ctx)
		if err != nil {
			return fmt.Errorf("sync definitions: %w", err)
		}
		logger.Info("definitions synced",
			"features", result.Features,
			"flags_upserted", result.FlagsUpserted,
			"flags_archived", result.FlagsArchived)

		// 5. Load definitions from DB → build registry.
		defs, err = evaluator.LoadDefinitionsFromDB(ctx, pool)
		if err != nil {
			return fmt.Errorf("load definitions from DB: %w", err)
		}
		reg = evaluator.NewRegistry(evaluator.NewDefaults(defs))
		logger.Info("defaults registry loaded from DB", "flags", len(defs))

	case evaluator.ModeDistributed:
		// 1. Schema version check.
		if err := db.CheckSchemaVersion(ctx, cfg.Database); err != nil {
			return fmt.Errorf("schema check: %w", err)
		}

		// 2. Connect pool.
		pool, err = pgxpool.New(ctx, cfg.Database)
		if err != nil {
			return fmt.Errorf("create database pool: %w", err)
		}
		defer pool.Close()

		// 3. Load definitions from DB → build registry.
		defs, err = evaluator.LoadDefinitionsFromDB(ctx, pool)
		if err != nil {
			return fmt.Errorf("load definitions from DB: %w", err)
		}
		reg = evaluator.NewRegistry(evaluator.NewDefaults(defs))
		logger.Info("defaults registry loaded from DB", "flags", len(defs))

	default:
		// Classic and proxy: parse descriptors → build registry.
		defs, err = evaluator.ParseDescriptorFile(cfg.Descriptors)
		if err != nil {
			return fmt.Errorf("parse descriptors: %w", err)
		}
		reg = evaluator.NewRegistry(evaluator.NewDefaults(defs))
		logger.Info("defaults registry loaded", "flags", len(defs))

		if cfg.Database != "" {
			pool, err = pgxpool.New(ctx, cfg.Database)
			if err != nil {
				return fmt.Errorf("create database pool: %w", err)
			}
			defer pool.Close()
		}
	}

	// ── Cache ────────────────────────────────────────────────────────

	cache, err := evaluator.NewCacheStore(evaluator.CacheStoreConfig{
		FlagTTL:         cfg.Cache.FlagTTL,
		OverrideTTL:     cfg.Cache.OverrideTTL,
		OverrideMaxSize: cfg.Cache.OverrideMaxSize,
		JitterPercent:   cfg.Cache.JitterPercent,
	})
	if err != nil {
		return fmt.Errorf("create cache: %w", err)
	}
	defer cache.Close()

	// ── Fetcher / state (root vs proxy) ──────────────────────────────

	var (
		fetcher     evaluator.Fetcher
		killFetcher evaluator.KillFetcher
		state       evaluator.StateServer
	)

	if pool != nil {
		if err := pool.Ping(ctx); err != nil {
			return fmt.Errorf("ping database: %w", err)
		}
		logger.Info("database connected", "mode", modeName(cfg.Mode))

		dbFetcher := evaluator.NewDBFetcher(pool, tracker, logger.With("component", "db-fetcher"), metrics, tracer)
		fetcher = dbFetcher
		killFetcher = dbFetcher
		state = dbFetcher
	} else {
		otelInt, err := otelconnect.NewInterceptor()
		if err != nil {
			return fmt.Errorf("create otel interceptor: %w", err)
		}
		client := evaluator.NewFlagServerClient(cfg.Upstream, tracker, cfg.Cache.FetchTimeout, metrics,
			connect.WithInterceptors(otelInt))
		fetcher = client
		killFetcher = client
		state = client.StateServer()
	}

	// ── Evaluator ────────────────────────────────────────────────────

	eval := evaluator.NewEvaluator(reg, cache, fetcher, logger, metrics, tracer)

	// ── Background goroutines ────────────────────────────────────────

	sighupCh := make(chan os.Signal, 1)
	signal.Notify(sighupCh, syscall.SIGHUP)

	killPoller := evaluator.NewKillPoller(killFetcher, cache, tracker,
		cfg.Cache.KillTTL, cfg.Cache.FetchTimeout,
		logger.With("component", "kill-poller"), metrics)
	go killPoller.Run(ctx)

	// Definition reload: DB poller for monolithic/distributed, fsnotify for classic/proxy.
	var defPoller *evaluator.DefinitionPoller
	switch cfg.Mode {
	case evaluator.ModeMonolithic, evaluator.ModeDistributed:
		defPoller = evaluator.NewDefinitionPoller(evaluator.DefinitionPollerConfig{
			Pool:         pool,
			Registry:     reg,
			Logger:       logger.With("component", "def-poller"),
			BaseInterval: cfg.DefinitionPollInterval,
		})
		go defPoller.Run(ctx)

		if cfg.Mode == evaluator.ModeMonolithic {
			// Monolithic also watches the descriptor file; on change it
			// syncs to DB then reloads from DB before swapping.
			watcher := evaluator.NewDescriptorWatcher(cfg.Descriptors, reg,
				30*time.Second, sighupCh,
				logger.With("component", "reload"))
			watcher.SetSyncAndReload(func(syncCtx context.Context, parsedDefs []evaluator.FlagDef) ([]evaluator.FlagDef, error) {
				syncConn, err := pgx.Connect(syncCtx, cfg.Database)
				if err != nil {
					return nil, fmt.Errorf("connect for sync: %w", err)
				}
				defer syncConn.Close(syncCtx)

				if _, err := defsync.SyncDefinitions(syncCtx, syncConn, parsedDefs, logger); err != nil {
					return nil, fmt.Errorf("sync definitions: %w", err)
				}
				return evaluator.LoadDefinitionsFromDB(syncCtx, pool)
			})
			go watcher.Run(ctx)
		}
	default:
		watcher := evaluator.NewDescriptorWatcher(cfg.Descriptors, reg,
			30*time.Second, sighupCh,
			logger.With("component", "reload"))
		go watcher.Run(ctx)
	}

	// ── Admin server (after poller, so reload endpoint can use it) ───

	if cfg.Admin != "" && pool != nil {
		if err := startAdmin(ctx, cfg, pool, reg, defPoller, defs, logger, devAssetsDir); err != nil {
			return fmt.Errorf("start admin: %w", err)
		}
	}

	// ── HTTP server ──────────────────────────────────────────────────

	svc := evaluator.NewService(eval, reg, tracker, cache, state)

	prometheus.MustRegister(prometheus.NewGaugeFunc(
		prometheus.GaugeOpts{
			Name: "pbflags_override_cache_size",
			Help: "Approximate entries in the override LRU cache.",
		},
		func() float64 { return float64(cache.OverrideCacheSize()) },
	))

	serverOtelInt, err := otelconnect.NewInterceptor()
	if err != nil {
		return fmt.Errorf("create server otel interceptor: %w", err)
	}

	mux := http.NewServeMux()
	evalPath, evalHandler := pbflagsv1connect.NewFlagEvaluatorServiceHandler(svc,
		connect.WithInterceptors(serverOtelInt))
	mux.Handle(evalPath, evalHandler)
	mux.Handle("/metrics", promhttp.Handler())

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		status := tracker.Status()
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "status=%s", status.String())
	})

	httpServer := &http.Server{
		Addr:    cfg.Listen,
		Handler: h2c.NewHandler(mux, &http2.Server{}),
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigCh
		logger.Info("shutting down", "signal", sig.String())
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		httpServer.Shutdown(shutdownCtx)
		cancel()
	}()

	logger.Info("serving", "address", cfg.Listen)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}

func modeName(m evaluator.ServerMode) string {
	switch m {
	case evaluator.ModeMonolithic:
		return "monolithic"
	case evaluator.ModeDistributed:
		return "distributed"
	case evaluator.ModeProxy:
		return "proxy"
	default:
		return "classic"
	}
}

func startAdmin(ctx context.Context, cfg evaluator.Config, pool *pgxpool.Pool, reg *evaluator.Registry, defPoller *evaluator.DefinitionPoller, defs []evaluator.FlagDef, logger *slog.Logger, devAssetsDir string) error {
	adminLogger := logger.With("component", "admin")

	store := admin.NewStore(pool, adminLogger, defs)
	if reg != nil {
		store.SetRegistry(reg)
	}
	adminService := admin.NewAdminService(store, adminLogger)

	mux := http.NewServeMux()
	adminPath, adminHandler := pbflagsv1connect.NewFlagAdminServiceHandler(adminService)
	mux.Handle(adminPath, adminHandler)

	webHandler, err := adminweb.NewHandler(store, adminLogger, adminweb.EnvConfig{
		Name:         cfg.EnvName,
		Color:        cfg.EnvColor,
		Version:      version,
		DevAssetsDir: devAssetsDir,
	})
	if err != nil {
		return fmt.Errorf("create web handler: %w", err)
	}
	webHandler.Register(mux)

	// Reload endpoint: triggers an immediate definition reload from DB.
	if defPoller != nil {
		mux.HandleFunc("POST /admin/reload-definitions", func(w http.ResponseWriter, r *http.Request) {
			if err := defPoller.TriggerReload(r.Context()); err != nil {
				adminLogger.Error("reload-definitions failed", "error", err)
				http.Error(w, fmt.Sprintf("reload failed: %v", err), http.StatusInternalServerError)
				return
			}
			adminLogger.Info("definitions reloaded via admin endpoint")
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, "ok")
		})
	}

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := pool.Ping(r.Context()); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprintf(w, "unhealthy: %v", err)
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})

	server := &http.Server{
		Addr:    cfg.Admin,
		Handler: h2c.NewHandler(mux, &http2.Server{}),
	}

	go func() {
		adminLogger.Info("admin server listening", "addr", cfg.Admin)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			adminLogger.Error("admin server error", "error", err)
		}
	}()

	go func() {
		<-ctx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		server.Shutdown(shutdownCtx)
	}()

	return nil
}
