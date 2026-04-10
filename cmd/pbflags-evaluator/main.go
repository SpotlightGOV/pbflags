// Binary pbflags-evaluator is the read-only flag resolution service.
//
// DB-backed mode (reads flag definitions and state from PostgreSQL):
//
//	pbflags-evaluator --database=postgres://readonly@localhost:5432/flags
//
// Upstream proxy mode (forwards to another evaluator, no database needed):
//
//	pbflags-evaluator --upstream=http://root-evaluator:9201
//
// Exactly one of --database or --upstream is required.
//
// Environment variables override CLI flags:
//
//	PBFLAGS_DATABASE  PostgreSQL connection string (readonly)
//	PBFLAGS_UPSTREAM  Upstream evaluator URL
//	PBFLAGS_LISTEN    Evaluator listen address (default: localhost:9201)
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

	"github.com/SpotlightGOV/pbflags/db"
	"github.com/SpotlightGOV/pbflags/gen/pbflags/v1/pbflagsv1connect"
	"github.com/SpotlightGOV/pbflags/internal/evaluator"
)

var version = "dev"

func main() {
	database := flag.String("database", "", "PostgreSQL connection string (readonly)")
	upstream := flag.String("upstream", "", "Upstream evaluator URL (proxy mode)")
	listen := flag.String("listen", "", "Evaluator listen address (default localhost:9201)")
	defPollInterval := flag.Duration("definition-poll-interval", 0, "How often to poll DB for definition changes (default 60s)")
	configPath := flag.String("config", "", "Path to configuration YAML file")
	flag.Parse()

	setEnvIfFlag("PBFLAGS_DATABASE", *database)
	setEnvIfFlag("PBFLAGS_UPSTREAM", *upstream)
	setEnvIfFlag("PBFLAGS_LISTEN", *listen)

	cfg, err := evaluator.LoadConfig(*configPath)
	if err != nil {
		slog.Error("load config", "error", err)
		os.Exit(1)
	}
	if *defPollInterval > 0 {
		cfg.DefinitionPollInterval = *defPollInterval
	}

	hasDB := cfg.Database != ""
	hasUpstream := cfg.Upstream != ""
	if hasDB == hasUpstream {
		fmt.Fprintln(os.Stderr, "error: exactly one of --database or --upstream is required")
		os.Exit(1)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	if err := run(cfg, logger); err != nil {
		logger.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func setEnvIfFlag(key, value string) {
	if value != "" {
		os.Setenv(key, value)
	}
}

func run(cfg evaluator.Config, logger *slog.Logger) error {
	mode := "upstream"
	if cfg.Database != "" {
		mode = "database"
	}
	logger.Info("starting pbflags evaluator",
		"version", version,
		"mode", mode,
		"listen", cfg.Listen)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	shutdownTracer, err := evaluator.InitTracer(ctx, "pbflags-evaluator", version)
	if err != nil {
		return fmt.Errorf("init tracer: %w", err)
	}
	defer shutdownTracer(context.Background())

	metrics := evaluator.NewMetrics(prometheus.DefaultRegisterer)
	tracker := evaluator.NewHealthTracker(metrics)
	tracer := otel.Tracer("pbflags/evaluator")

	// ── Registry + Fetcher ──────────────────────────────────────────

	var (
		reg         *evaluator.Registry
		pool        *pgxpool.Pool
		fetcher     evaluator.Fetcher
		killFetcher evaluator.KillFetcher
		state       evaluator.StateServer
	)

	if cfg.Database != "" {
		// DB-backed evaluator: check schema, load definitions, poll for changes.
		if err := db.CheckSchemaVersion(ctx, cfg.Database); err != nil {
			return fmt.Errorf("schema check: %w", err)
		}

		pool, err = pgxpool.New(ctx, cfg.Database)
		if err != nil {
			return fmt.Errorf("create database pool: %w", err)
		}
		defer pool.Close()

		if err := pool.Ping(ctx); err != nil {
			return fmt.Errorf("ping database: %w", err)
		}
		logger.Info("database connected")

		defs, err := evaluator.LoadDefinitionsFromDB(ctx, pool)
		if err != nil {
			return fmt.Errorf("load definitions: %w", err)
		}
		reg = evaluator.NewRegistry(evaluator.NewDefaults(defs))
		logger.Info("defaults registry loaded", "flags", len(defs))

		dbFetcher := evaluator.NewDBFetcher(pool, tracker, logger.With("component", "db-fetcher"), metrics, tracer)
		fetcher = dbFetcher
		killFetcher = dbFetcher
		state = dbFetcher
	} else {
		// Upstream proxy: forward all RPCs to upstream evaluator.
		reg = evaluator.NewRegistry(evaluator.NewDefaults(nil))

		otelInt, err := otelconnect.NewInterceptor()
		if err != nil {
			return fmt.Errorf("create otel interceptor: %w", err)
		}
		client := evaluator.NewFlagServerClient(cfg.Upstream, tracker, cfg.Cache.FetchTimeout, metrics,
			connect.WithInterceptors(otelInt))
		fetcher = client
		killFetcher = client
		state = client.StateServer()

		logger.Info("upstream configured", "upstream", cfg.Upstream)
	}

	// ── Cache + Evaluator ───────────────────────────────────────────

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

	eval := evaluator.NewEvaluator(reg, cache, fetcher, logger, metrics, tracer)

	// ── Background goroutines ───────────────────────────────────────

	killPoller := evaluator.NewKillPoller(killFetcher, cache, tracker,
		cfg.Cache.KillTTL, cfg.Cache.FetchTimeout,
		logger.With("component", "kill-poller"), metrics)
	go killPoller.Run(ctx)

	if pool != nil {
		defPoller := evaluator.NewDefinitionPoller(evaluator.DefinitionPollerConfig{
			Pool:         pool,
			Registry:     reg,
			Logger:       logger.With("component", "def-poller"),
			BaseInterval: cfg.DefinitionPollInterval,
		})
		go defPoller.Run(ctx)
	}

	// ── HTTP server ─────────────────────────────────────────────────

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
