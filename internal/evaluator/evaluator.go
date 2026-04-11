package evaluator

import (
	"context"
	"log/slog"
	"golang.org/x/sync/singleflight"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
)

// Fetcher fetches flag state from the remote flag server on demand.
type Fetcher interface {
	FetchFlagState(ctx context.Context, flagID string) (*CachedFlagState, error)
	FetchOverrides(ctx context.Context, entityID string, flagIDs []string) ([]*CachedOverride, error)
}

// Evaluator resolves flag values using the full precedence chain:
// kill set → per-entity override → global state → stale cache → compiled default.
// Note: per-entity kills are not supported; overrides can only be ENABLED or DEFAULT.
type Evaluator struct {
	cache           *CacheStore
	fetcher         Fetcher
	logger          *slog.Logger
	metrics         *Metrics
	tracer          trace.Tracer
	inlineKillCheck bool          // when true, fetch flag state before overrides to check kills
	fetchTimeout    time.Duration // timeout for background refresh fetches
	flagGroup       singleflight.Group
	overrideGroup   singleflight.Group
}

// EvaluatorOption configures optional Evaluator behavior.
type EvaluatorOption func(*Evaluator)

// WithInlineKillCheck enables inline kill checking. Use this when the kill
// set poller is not running (e.g. flagTTL <= killTTL) so kills are still
// checked before overrides by fetching each flag's state eagerly.
func WithInlineKillCheck() EvaluatorOption {
	return func(e *Evaluator) { e.inlineKillCheck = true }
}

// WithFetchTimeout sets the timeout for background refresh fetches.
// Defaults to 500ms if not set.
func WithFetchTimeout(d time.Duration) EvaluatorOption {
	return func(e *Evaluator) { e.fetchTimeout = d }
}

// NewEvaluator creates an Evaluator.
func NewEvaluator(cache *CacheStore, fetcher Fetcher, logger *slog.Logger, m *Metrics, tracer trace.Tracer, opts ...EvaluatorOption) *Evaluator {
	e := &Evaluator{
		cache:        cache,
		fetcher:      fetcher,
		logger:       logger,
		metrics:      m,
		tracer:       tracer,
		fetchTimeout: 500 * time.Millisecond,
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// Evaluate resolves a single flag for an optional entity.
func (e *Evaluator) Evaluate(ctx context.Context, flagID, entityID string) (value *pbflagsv1.FlagValue, source pbflagsv1.EvaluationSource) {
	ctx, span := e.tracer.Start(ctx, "Evaluator.Evaluate",
		trace.WithAttributes(
			attribute.String("flag_id", flagID),
			attribute.String("entity_id", entityID),
		))
	defer func() {
		span.SetAttributes(attribute.String("source", sourceLabel(source)))
		span.End()
		e.metrics.EvaluationsTotal.WithLabelValues(sourceLabel(source), "ok").Inc()
	}()

	// 1. Kill set check — highest priority (populated by poller).
	ks := e.cache.GetKillSet()
	if ks.IsKilled(flagID) {
		e.metrics.CacheHitsTotal.WithLabelValues("kill_set").Inc()
		return nil, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_KILLED
	}

	// 2. Inline kill check: when the kill set poller is not running,
	//    fetch state now and check killed before overrides.
	var prefetched *CachedFlagState
	if e.inlineKillCheck {
		fetched, err := e.fetcher.FetchFlagState(ctx, flagID)
		if err == nil && fetched != nil {
			e.cache.SetFlagState(fetched)
			if fetched.State == pbflagsv1.State_STATE_KILLED {
				return nil, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_KILLED
			}
			prefetched = fetched
		}
	}

	// 3. Per-entity override (if applicable).
	if entityID != "" {
		if val, src, ok := e.resolveOverride(ctx, flagID, entityID); ok {
			return val, src
		}
	}

	// 4. Global state.
	if prefetched != nil {
		return e.evalFlagState(prefetched)
	}
	return e.resolveGlobal(ctx, flagID)
}

func (e *Evaluator) resolveOverride(
	ctx context.Context,
	flagID, entityID string,
) (*pbflagsv1.FlagValue, pbflagsv1.EvaluationSource, bool) {
	override := e.cache.GetOverride(flagID, entityID)
	if override != nil {
		e.metrics.CacheHitsTotal.WithLabelValues("overrides").Inc()
		trace.SpanFromContext(ctx).SetAttributes(attribute.Bool("cache_hit", true))
	} else {
		e.metrics.CacheMissesTotal.WithLabelValues("overrides").Inc()

		// Stale-while-revalidate: if we have a stale value, return it
		// immediately and refresh in the background.
		if stale := e.cache.GetStaleOverride(flagID, entityID); stale != nil {
			e.backgroundRefreshOverride(entityID, flagID)
			return e.evalOverride(stale, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_STALE)
		}

		// Cold start: no stale value, must block on fetch.
		fetched, err := e.fetcher.FetchOverrides(ctx, entityID, []string{flagID})
		if err != nil {
			e.logger.Debug("override fetch failed", "flag_id", flagID, "entity_id", entityID, "error", err)
			return nil, 0, false
		}
		for _, o := range fetched {
			e.cache.SetOverride(o)
			if o.FlagID == flagID {
				override = o
			}
		}
	}

	if override == nil {
		return nil, 0, false
	}

	return e.evalOverride(override, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_OVERRIDE)
}

func (e *Evaluator) evalOverride(
	override *CachedOverride,
	freshSource pbflagsv1.EvaluationSource,
) (*pbflagsv1.FlagValue, pbflagsv1.EvaluationSource, bool) {
	switch override.State {
	case pbflagsv1.State_STATE_KILLED, pbflagsv1.State_STATE_DEFAULT:
		return nil, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT, true
	case pbflagsv1.State_STATE_ENABLED:
		if override.Value != nil {
			return override.Value, freshSource, true
		}
		return nil, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT, true
	}
	return nil, 0, false
}

func (e *Evaluator) resolveGlobal(
	ctx context.Context,
	flagID string,
) (*pbflagsv1.FlagValue, pbflagsv1.EvaluationSource) {
	state := e.cache.GetFlagState(flagID)

	if state != nil {
		e.metrics.CacheHitsTotal.WithLabelValues("flags").Inc()
		trace.SpanFromContext(ctx).SetAttributes(attribute.Bool("cache_hit", true))
		return e.evalFlagState(state)
	}

	e.metrics.CacheMissesTotal.WithLabelValues("flags").Inc()

	// Stale-while-revalidate: if we have a stale value, return it
	// immediately and refresh in the background.
	if stale := e.cache.GetStaleFlagState(flagID); stale != nil {
		e.backgroundRefreshFlag(flagID)
		return e.evalFlagStateWithSource(stale, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_STALE)
	}

	// Cold start: no stale value, must block on fetch.
	fetched, err := e.fetcher.FetchFlagState(ctx, flagID)
	if err != nil {
		e.logger.Debug("flag state fetch failed", "flag_id", flagID, "error", err)
		return nil, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT
	}
	if fetched != nil {
		e.cache.SetFlagState(fetched)
	}
	return e.evalFlagState(fetched)
}

// evalFlagState evaluates an already-fetched flag state using GLOBAL as the
// source for fresh values. Shared by resolveGlobal and the inline kill-check
// prefetch path.
func (e *Evaluator) evalFlagState(state *CachedFlagState) (*pbflagsv1.FlagValue, pbflagsv1.EvaluationSource) {
	return e.evalFlagStateWithSource(state, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_GLOBAL)
}

// evalFlagStateWithSource evaluates flag state with a caller-specified source
// label for enabled values (e.g. GLOBAL for fresh, STALE for background refresh).
func (e *Evaluator) evalFlagStateWithSource(state *CachedFlagState, freshSource pbflagsv1.EvaluationSource) (*pbflagsv1.FlagValue, pbflagsv1.EvaluationSource) {
	if state == nil {
		return nil, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT
	}

	if state.Archived {
		if state.Value != nil {
			return state.Value, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_ARCHIVED
		}
		return nil, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT
	}

	switch state.State {
	case pbflagsv1.State_STATE_DEFAULT, pbflagsv1.State_STATE_KILLED:
		return nil, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT
	case pbflagsv1.State_STATE_ENABLED:
		if state.Value != nil {
			return state.Value, freshSource
		}
	}

	return nil, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT
}

// backgroundRefreshFlag triggers a singleflight-guarded background fetch for
// a flag's global state. The result is written to the cache for the next caller.
func (e *Evaluator) backgroundRefreshFlag(flagID string) {
	go func() {
		_, _, _ = e.flagGroup.Do(flagID, func() (any, error) {
			ctx, cancel := context.WithTimeout(context.Background(), e.fetchTimeout)
			defer cancel()

			fetched, err := e.fetcher.FetchFlagState(ctx, flagID)
			if err != nil {
				e.logger.Debug("background flag refresh failed", "flag_id", flagID, "error", err)
				e.metrics.BackgroundRefreshes.WithLabelValues("flags", "error").Inc()
				return nil, err
			}
			if fetched != nil {
				e.cache.SetFlagState(fetched)
			}
			e.metrics.BackgroundRefreshes.WithLabelValues("flags", "ok").Inc()
			return nil, nil
		})
	}()
}

// backgroundRefreshOverride triggers a singleflight-guarded background fetch
// for a per-entity override. The result is written to the cache for the next caller.
func (e *Evaluator) backgroundRefreshOverride(entityID, flagID string) {
	key := flagID + ":" + entityID
	go func() {
		_, _, _ = e.overrideGroup.Do(key, func() (any, error) {
			ctx, cancel := context.WithTimeout(context.Background(), e.fetchTimeout)
			defer cancel()

			fetched, err := e.fetcher.FetchOverrides(ctx, entityID, []string{flagID})
			if err != nil {
				e.logger.Debug("background override refresh failed", "flag_id", flagID, "entity_id", entityID, "error", err)
				e.metrics.BackgroundRefreshes.WithLabelValues("overrides", "error").Inc()
				return nil, err
			}
			for _, o := range fetched {
				e.cache.SetOverride(o)
			}
			e.metrics.BackgroundRefreshes.WithLabelValues("overrides", "ok").Inc()
			return nil, nil
		})
	}()
}
