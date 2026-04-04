// Binary pbflags-server is the feature flag evaluation service. It reads proto
// descriptors at startup, builds a defaults registry, and serves the
// FlagEvaluatorService Connect API. Flag state comes from either direct
// database access (root mode) or an upstream evaluator (proxy mode).
//
// Root mode:
//
//	pbflags-server --database=postgres://... --descriptors=descriptors.pb
//
// Proxy mode:
//
//	pbflags-server --server=http://root-evaluator:9201 --descriptors=descriptors.pb
//
// Combined mode (root + embedded admin):
//
//	pbflags-server --database=postgres://... --descriptors=descriptors.pb --admin=:9200
//
// Environment variables override config file values:
//
//	PBFLAGS_DESCRIPTORS  Path to descriptors.pb
//	PBFLAGS_SERVER       Upstream evaluator URL (proxy mode)
//	PBFLAGS_LISTEN       Evaluator listen address
//	PBFLAGS_ADMIN        Admin listen address (enables combined mode)
//	PBFLAGS_DATABASE     PostgreSQL connection string (root mode)
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
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/SpotlightGOV/pbflags/gen/pbflags/v1/pbflagsv1connect"
	"github.com/SpotlightGOV/pbflags/internal/admin"
	"github.com/SpotlightGOV/pbflags/internal/evaluator"
)

var version = "dev"

func main() {
	configPath := flag.String("config", "", "Path to configuration YAML file")
	adminAddr := flag.String("admin", "", "Admin server listen address (enables combined mode)")
	databaseURL := flag.String("database", "", "PostgreSQL connection string (root mode)")
	descriptors := flag.String("descriptors", "", "Path to descriptors.pb")
	listen := flag.String("listen", "", "Evaluator listen address")
	serverURL := flag.String("server", "", "Upstream evaluator URL (proxy mode)")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	setEnvIfFlag("PBFLAGS_ADMIN", *adminAddr)
	setEnvIfFlag("PBFLAGS_DATABASE", *databaseURL)
	setEnvIfFlag("PBFLAGS_DESCRIPTORS", *descriptors)
	setEnvIfFlag("PBFLAGS_LISTEN", *listen)
	setEnvIfFlag("PBFLAGS_SERVER", *serverURL)

	if err := run(*configPath, logger); err != nil {
		logger.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func setEnvIfFlag(key, value string) {
	if value != "" {
		os.Setenv(key, value)
	}
}

func run(configPath string, logger *slog.Logger) error {
	cfg, err := evaluator.LoadConfig(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	rootMode := cfg.Database != ""

	logger.Info("starting pbflags server",
		"version", version,
		"descriptors", cfg.Descriptors,
		"server", cfg.Server,
		"listen", cfg.Listen,
		"admin", cfg.Admin,
		"mode", modeString(rootMode))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	shutdownTracer, err := evaluator.InitTracer(ctx, "pbflags-server", version)
	if err != nil {
		return fmt.Errorf("init tracer: %w", err)
	}
	defer shutdownTracer(context.Background())

	defs, err := evaluator.ParseDescriptorFile(cfg.Descriptors)
	if err != nil {
		return fmt.Errorf("parse descriptors: %w", err)
	}

	defaults := evaluator.NewDefaults(defs)
	reg := evaluator.NewRegistry(defaults)
	logger.Info("defaults registry loaded", "flags", defaults.Len())

	metrics := evaluator.NewMetrics(prometheus.DefaultRegisterer)
	tracker := evaluator.NewHealthTracker(metrics)
	tracer := otel.Tracer("pbflags/evaluator")

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

	var (
		fetcher     evaluator.Fetcher
		killFetcher evaluator.KillFetcher
		state       evaluator.StateServer
	)

	if rootMode {
		pool, err := pgxpool.New(ctx, cfg.Database)
		if err != nil {
			return fmt.Errorf("create database pool: %w", err)
		}
		defer pool.Close()

		if err := pool.Ping(ctx); err != nil {
			return fmt.Errorf("ping database: %w", err)
		}
		logger.Info("database connected (root mode)")

		dbFetcher := evaluator.NewDBFetcher(pool, tracker, logger.With("component", "db-fetcher"), metrics, tracer)
		fetcher = dbFetcher
		killFetcher = dbFetcher
		state = dbFetcher

		if cfg.Admin != "" {
			if err := startAdmin(ctx, cfg, pool, defs, logger); err != nil {
				return fmt.Errorf("start admin: %w", err)
			}
		}
	} else {
		otelInt, err := otelconnect.NewInterceptor()
		if err != nil {
			return fmt.Errorf("create otel interceptor: %w", err)
		}
		client := evaluator.NewFlagServerClient(cfg.Server, tracker, cfg.Cache.FetchTimeout, metrics,
			connect.WithInterceptors(otelInt))
		fetcher = client
		killFetcher = client
		state = client.StateServer()
	}

	eval := evaluator.NewEvaluator(reg, cache, fetcher, logger, metrics, tracer)

	sighupCh := make(chan os.Signal, 1)
	signal.Notify(sighupCh, syscall.SIGHUP)

	poller := evaluator.NewKillPoller(killFetcher, cache, tracker,
		cfg.Cache.KillTTL, cfg.Cache.FetchTimeout,
		logger.With("component", "kill-poller"), metrics)
	go poller.Run(ctx)

	watcher := evaluator.NewDescriptorWatcher(cfg.Descriptors, reg,
		30*time.Second, sighupCh,
		logger.With("component", "reload"))
	go watcher.Run(ctx)

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

	server := &http.Server{
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
		server.Shutdown(shutdownCtx)
		cancel()
	}()

	logger.Info("serving", "address", cfg.Listen)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}

func modeString(root bool) string {
	if root {
		return "root"
	}
	return "proxy"
}

func startAdmin(ctx context.Context, cfg evaluator.Config, pool *pgxpool.Pool, defs []evaluator.FlagDef, logger *slog.Logger) error {
	adminLogger := logger.With("component", "admin")

	store := admin.NewStore(pool, adminLogger, defs)
	adminService := admin.NewAdminService(store, adminLogger)

	mux := http.NewServeMux()
	adminPath, adminHandler := pbflagsv1connect.NewFlagAdminServiceHandler(adminService)
	mux.Handle(adminPath, adminHandler)

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
