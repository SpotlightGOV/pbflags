package evaluator

import (
	"context"
	"log/slog"

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
	inlineKillCheck bool // when true, fetch flag state before overrides to check kills
}

// EvaluatorOption configures optional Evaluator behavior.
type EvaluatorOption func(*Evaluator)

// WithInlineKillCheck enables inline kill checking. Use this when the kill
// set poller is not running (e.g. flagTTL <= killTTL) so kills are still
// checked before overrides by fetching each flag's state eagerly.
func WithInlineKillCheck() EvaluatorOption {
	return func(e *Evaluator) { e.inlineKillCheck = true }
}

// NewEvaluator creates an Evaluator.
func NewEvaluator(cache *CacheStore, fetcher Fetcher, logger *slog.Logger, m *Metrics, tracer trace.Tracer, opts ...EvaluatorOption) *Evaluator {
	e := &Evaluator{
		cache:   cache,
		fetcher: fetcher,
		logger:  logger,
		metrics: m,
		tracer:  tracer,
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
		fetched, err := e.fetcher.FetchOverrides(ctx, entityID, []string{flagID})
		if err != nil {
			e.logger.Debug("override fetch failed", "flag_id", flagID, "entity_id", entityID, "error", err)
			// Fall back to stale cache if available.
			if stale := e.cache.GetStaleOverride(flagID, entityID); stale != nil && stale.Value != nil {
				return stale.Value, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_CACHED, true
			}
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

	switch override.State {
	case pbflagsv1.State_STATE_KILLED, pbflagsv1.State_STATE_DEFAULT:
		return nil, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT, true
	case pbflagsv1.State_STATE_ENABLED:
		if override.Value != nil {
			return override.Value, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_OVERRIDE, true
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
	} else {
		e.metrics.CacheMissesTotal.WithLabelValues("flags").Inc()
		fetched, err := e.fetcher.FetchFlagState(ctx, flagID)
		if err != nil {
			e.logger.Debug("flag state fetch failed", "flag_id", flagID, "error", err)
			if stale := e.cache.GetStaleFlagState(flagID); stale != nil && stale.Value != nil {
				return stale.Value, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_CACHED
			}
			return nil, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT
		}
		state = fetched
		if state != nil {
			e.cache.SetFlagState(state)
		}
	}

	return e.evalFlagState(state)
}

// evalFlagState evaluates an already-fetched flag state. Shared by
// resolveGlobal and the inline kill-check prefetch path.
func (e *Evaluator) evalFlagState(state *CachedFlagState) (*pbflagsv1.FlagValue, pbflagsv1.EvaluationSource) {
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
			return state.Value, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_GLOBAL
		}
	}

	return nil, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT
}
