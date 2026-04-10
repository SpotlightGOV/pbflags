package evaluator

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DefinitionPoller polls the database for flag definition changes and swaps
// the registry when new definitions are detected. It uses exponential backoff
// on consecutive failures (2x, 4x, 8x capped) and resets on success.
type DefinitionPoller struct {
	pool         *pgxpool.Pool
	registry     *Registry
	logger       *slog.Logger
	baseInterval time.Duration
	jitterPct    float64 // 0.0–1.0 (e.g. 0.2 = ±20%)

	mu           sync.Mutex
	lastLoadTime time.Time
	failures     int

	// reloadFunc can be overridden for testing. Defaults to loadAndSwap.
	reloadFunc func(ctx context.Context) error
}

// DefinitionPollerConfig holds configuration for the definition poller.
type DefinitionPollerConfig struct {
	Pool         *pgxpool.Pool
	Registry     *Registry
	Logger       *slog.Logger
	BaseInterval time.Duration // Default: 60s
	JitterPct    float64       // Default: 0.2 (±20%)
}

// NewDefinitionPoller creates a poller that watches for definition changes.
func NewDefinitionPoller(cfg DefinitionPollerConfig) *DefinitionPoller {
	if cfg.BaseInterval <= 0 {
		cfg.BaseInterval = 60 * time.Second
	}
	if cfg.JitterPct <= 0 {
		cfg.JitterPct = 0.2
	}
	p := &DefinitionPoller{
		pool:         cfg.Pool,
		registry:     cfg.Registry,
		logger:       cfg.Logger,
		baseInterval: cfg.BaseInterval,
		jitterPct:    cfg.JitterPct,
	}
	p.reloadFunc = p.loadAndSwap
	return p
}

// Run polls for definition changes until ctx is cancelled.
func (p *DefinitionPoller) Run(ctx context.Context) {
	p.logger.Info("definition poller started", "interval", p.baseInterval)

	for {
		interval := p.nextInterval()
		timer := time.NewTimer(interval)

		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			if err := p.poll(ctx); err != nil {
				p.mu.Lock()
				p.failures++
				p.mu.Unlock()
				p.logger.Error("definition poll failed", "error", err, "consecutive_failures", p.consecutiveFailures())
			}
		}
	}
}

// TriggerReload forces an immediate definition reload from DB.
// Returns an error if the reload fails.
func (p *DefinitionPoller) TriggerReload(ctx context.Context) error {
	return p.reloadFunc(ctx)
}

func (p *DefinitionPoller) poll(ctx context.Context) error {
	var maxUpdatedAt time.Time
	err := p.pool.QueryRow(ctx, `
		SELECT GREATEST(
		  (SELECT COALESCE(MAX(updated_at), '1970-01-01') FROM feature_flags.flags),
		  (SELECT COALESCE(MAX(updated_at), '1970-01-01') FROM feature_flags.features)
		)`).Scan(&maxUpdatedAt)
	if err != nil {
		return err
	}

	p.mu.Lock()
	needsReload := maxUpdatedAt.After(p.lastLoadTime)
	p.mu.Unlock()

	if !needsReload {
		return nil
	}

	return p.reloadFunc(ctx)
}

func (p *DefinitionPoller) loadAndSwap(ctx context.Context) error {
	defs, err := LoadDefinitionsFromDB(ctx, p.pool)
	if err != nil {
		return err
	}

	old := p.registry.Load()
	next := NewDefaults(defs)
	p.registry.Swap(next)

	p.mu.Lock()
	p.lastLoadTime = time.Now()
	p.failures = 0
	p.mu.Unlock()

	added, removed := diffFlagIDs(old, next)
	p.logger.Info("definitions reloaded from DB",
		"total_flags", next.Len(),
		"added", len(added),
		"removed", len(removed))

	return nil
}

func (p *DefinitionPoller) nextInterval() time.Duration {
	p.mu.Lock()
	failures := p.failures
	p.mu.Unlock()

	interval := p.baseInterval

	// Exponential backoff: 2x, 4x, 8x capped.
	for i := 0; i < failures && i < 3; i++ {
		interval *= 2
	}

	// Apply jitter: ±jitterPct.
	jitter := float64(interval) * p.jitterPct
	interval += time.Duration(rand.Float64()*2*jitter - jitter)

	return interval
}

func (p *DefinitionPoller) consecutiveFailures() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.failures
}
