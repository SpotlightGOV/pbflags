package evaluator

import (
	"context"
	"log/slog"
	"time"
)

// KillFetcher fetches the current kill set. Implemented by both
// FlagServerClient (proxy mode) and DBFetcher (root mode).
type KillFetcher interface {
	GetKilledFlags(ctx context.Context) (*KillSet, error)
}

// KillPoller runs a background loop that polls GetKilledFlags at regular intervals.
// On failure, the last known kill set is preserved indefinitely.
type KillPoller struct {
	fetcher KillFetcher
	cache   *CacheStore
	tracker *HealthTracker
	baseTTL time.Duration
	timeout time.Duration
	logger  *slog.Logger
}

// NewKillPoller creates a kill set poller.
func NewKillPoller(
	fetcher KillFetcher,
	cache *CacheStore,
	tracker *HealthTracker,
	baseTTL, timeout time.Duration,
	logger *slog.Logger,
) *KillPoller {
	return &KillPoller{
		fetcher: fetcher,
		cache:   cache,
		tracker: tracker,
		baseTTL: baseTTL,
		timeout: timeout,
		logger:  logger,
	}
}

// Run starts the polling loop. Blocks until ctx is cancelled.
func (p *KillPoller) Run(ctx context.Context) {
	p.poll(ctx)

	for {
		interval := p.baseTTL * time.Duration(p.tracker.BackoffMultiplier())
		timer := time.NewTimer(interval)

		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			p.poll(ctx)
		}
	}
}

func (p *KillPoller) poll(ctx context.Context) {
	fetchCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	ks, err := p.fetcher.GetKilledFlags(fetchCtx)
	if err != nil {
		p.logger.Warn("kill set poll failed, preserving last known kills",
			"error", err,
			"consecutive_failures", p.tracker.ConsecutiveFailures())
		return
	}

	p.cache.SetKillSet(ks)
	p.logger.Debug("kill set updated",
		"killed_flags", len(ks.FlagIDs),
		"killed_overrides", len(ks.KilledOverrides))
}
