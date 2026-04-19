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
// Flags can also be supplied via picocli-style @file references:
//
//	pbflags-evaluator @config.flags
//
// Environment variables override CLI flags:
//
//	PBFLAGS_DATABASE            PostgreSQL connection string (readonly)
//	PBFLAGS_UPSTREAM            Upstream evaluator URL
//	PBFLAGS_LISTEN              Evaluator listen address (default: localhost:9201)
//	PBFLAGS_CACHE_KILL_TTL      Kill set cache TTL (default: 30s)
//	PBFLAGS_CACHE_FLAG_TTL      Global flag state cache TTL (default: 10m)
//	PBFLAGS_CACHE_OVERRIDE_TTL  Per-entity override cache TTL (default: 10m)
//	PBFLAGS_LOG_LEVEL           Log level: debug, info, warn, error (default: info)
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
	"github.com/SpotlightGOV/pbflags/internal/flagfile"
)

var version = "dev"

func main() {
	args, err := flagfile.ExpandArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	fs := flag.NewFlagSet("pbflags-evaluator", flag.ExitOnError)
	database := fs.String("database", "", "PostgreSQL connection string (readonly)")
	upstream := fs.String("upstream", "", "Upstream evaluator URL (proxy mode)")
	listen := fs.String("listen", "", "Evaluator listen address (default localhost:9201)")
	killTTL := fs.Duration("cache-kill-ttl", 0, "Kill set cache TTL (default 30s)")
	flagTTL := fs.Duration("cache-flag-ttl", 0, "Global flag state cache TTL (default 10m)")
	overrideTTL := fs.Duration("cache-override-ttl", 0, "Per-entity override cache TTL (default 10m)")
	fs.Parse(args)

	setEnvIfFlag("PBFLAGS_DATABASE", *database)
	setEnvIfFlag("PBFLAGS_UPSTREAM", *upstream)
	setEnvIfFlag("PBFLAGS_LISTEN", *listen)
	setDurationEnvIfFlag("PBFLAGS_CACHE_KILL_TTL", *killTTL)
	setDurationEnvIfFlag("PBFLAGS_CACHE_FLAG_TTL", *flagTTL)
	setDurationEnvIfFlag("PBFLAGS_CACHE_OVERRIDE_TTL", *overrideTTL)

	cfg := evaluator.LoadConfig()

	hasDB := cfg.Database != ""
	hasUpstream := cfg.Upstream != ""
	if hasDB == hasUpstream {
		fmt.Fprintln(os.Stderr, "error: exactly one of --database or --upstream is required")
		os.Exit(1)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))

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

func setDurationEnvIfFlag(key string, d time.Duration) {
	if d > 0 {
		os.Setenv(key, d.String())
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

	// ── Fetcher ────────────────────────────────────────────────────

	var (
		fetcher     evaluator.Fetcher
		killFetcher evaluator.KillFetcher
		state       evaluator.StateServer
	)

	if cfg.Database != "" {
		// DB-backed evaluator: check schema, fetch state from PostgreSQL.
		if err := db.CheckSchemaVersion(ctx, cfg.Database); err != nil {
			return fmt.Errorf("schema check: %w", err)
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

		dbFetcher := evaluator.NewDBFetcher(pool, tracker, logger.With("component", "db-fetcher"), metrics, tracer)
		fetcher = dbFetcher
		killFetcher = dbFetcher
		state = dbFetcher
	} else {
		// Upstream proxy: forward all RPCs to upstream evaluator.
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
		FlagTTL:       cfg.Cache.FlagTTL,
		JitterPercent: cfg.Cache.JitterPercent,
	})
	if err != nil {
		return fmt.Errorf("create cache: %w", err)
	}
	defer cache.Close()

	var evalOpts []evaluator.EvaluatorOption
	if cfg.Cache.FlagTTL > cfg.Cache.KillTTL {
		killPoller := evaluator.NewKillPoller(killFetcher, cache, tracker,
			cfg.Cache.KillTTL, cfg.Cache.FetchTimeout,
			logger.With("component", "kill-poller"), metrics)
		go killPoller.Run(ctx)
	} else {
		logger.Info("kill set poller disabled (flag_ttl <= kill_ttl), using inline kill checks")
		evalOpts = append(evalOpts, evaluator.WithInlineKillCheck())
	}

	eval := evaluator.NewEvaluator(cache, fetcher, logger, metrics, evalOpts...)

	// ── HTTP server ─────────────────────────────────────────────────

	svc := evaluator.NewService(eval, tracker, cache, state)

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
