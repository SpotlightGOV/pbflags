package evaluator

import (
	"context"
	"log/slog"
	"time"

	"golang.org/x/sync/singleflight"

	"google.golang.org/protobuf/proto"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
)

// Fetcher fetches flag state from the remote flag server on demand.
type Fetcher interface {
	FetchFlagState(ctx context.Context, flagID string) (*CachedFlagState, error)
}

// Evaluator resolves flag values using the precedence chain:
// kill set → conditions (CEL) → global state → stale cache → compiled default.
//
// Per-flag tracing is intentionally omitted — the otelconnect RPC interceptor
// creates a parent span for each request, and per-evaluation spans add ~160ns
// of overhead on the kill-set fast path (97% of the actual work cost). Use
// Prometheus counters for per-flag observability instead.
type Evaluator struct {
	cache           *CacheStore
	condCache       *ConditionCache // nil when condition caching is not configured
	fetcher         Fetcher
	condEval        *ConditionEvaluator // nil when conditions are not configured
	logger          *slog.Logger
	metrics         *Metrics
	inlineKillCheck bool          // when true, fetch flag state eagerly to check kills
	fetchTimeout    time.Duration // timeout for background refresh fetches
	flagGroup       singleflight.Group
}

// EvaluatorOption configures optional Evaluator behavior.
type EvaluatorOption func(*Evaluator)

// WithInlineKillCheck enables inline kill checking. Use this when the kill
// set poller is not running (e.g. flagTTL <= killTTL) so kills are still
// checked by fetching each flag's state eagerly.
func WithInlineKillCheck() EvaluatorOption {
	return func(e *Evaluator) { e.inlineKillCheck = true }
}

// WithFetchTimeout sets the timeout for background refresh fetches.
// Defaults to 500ms if not set.
func WithFetchTimeout(d time.Duration) EvaluatorOption {
	return func(e *Evaluator) { e.fetchTimeout = d }
}

// WithConditionEvaluator enables CEL condition evaluation.
func WithConditionEvaluator(ce *ConditionEvaluator) EvaluatorOption {
	return func(e *Evaluator) { e.condEval = ce }
}

// WithConditionCache sets the cache for condition evaluation results.
func WithConditionCache(cc *ConditionCache) EvaluatorOption {
	return func(e *Evaluator) { e.condCache = cc }
}

// setFlagState writes to the cache store and invalidates any condition
// cache entries for the flag (bumps version so old keys become unreachable).
func (e *Evaluator) setFlagState(state *CachedFlagState) {
	e.cache.SetFlagState(state)
	if e.condCache != nil && len(state.Conditions) > 0 {
		e.condCache.InvalidateFlag(state.FlagID)
	}
}

// NewEvaluator creates an Evaluator.
func NewEvaluator(cache *CacheStore, fetcher Fetcher, logger *slog.Logger, m *Metrics, opts ...EvaluatorOption) *Evaluator {
	e := &Evaluator{
		cache:        cache,
		fetcher:      fetcher,
		logger:       logger,
		metrics:      m,
		fetchTimeout: 500 * time.Millisecond,
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// Evaluate resolves a single flag for an optional entity.
func (e *Evaluator) Evaluate(ctx context.Context, flagID, entityID string) (value *pbflagsv1.FlagValue, source pbflagsv1.EvaluationSource) {
	// 1. Kill set check — highest priority (populated by poller).
	ks := e.cache.GetKillSet()
	if ks.IsKilled(flagID) {
		e.metrics.cacheHitKillSet.Inc()
		e.metrics.evalKilled.Inc()
		return nil, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_KILLED
	}

	// 2. Inline kill check: when the kill set poller is not running,
	//    fetch state now and check killed eagerly.
	var prefetched *CachedFlagState
	if e.inlineKillCheck {
		fetched, err := e.fetcher.FetchFlagState(ctx, flagID)
		if err == nil && fetched != nil {
			e.setFlagState(fetched)
			if fetched.State == pbflagsv1.State_STATE_KILLED {
				e.metrics.evalKilled.Inc()
				return nil, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_KILLED
			}
			prefetched = fetched
		}
	}

	// 3. Global state — conditions and default handle the actual value.
	if prefetched != nil {
		val, src := e.evalFlagState(prefetched)
		e.metrics.incEval(src)
		return val, src
	}
	val, src := e.resolveGlobal(ctx, flagID)
	e.metrics.incEval(src)
	return val, src
}

// EvaluateWithContext resolves a flag using the v1 evaluation precedence:
// kill set → conditions (CEL) → static config value → compiled default.
// evalCtx is the deserialized EvaluationContext proto (may be nil).
func (e *Evaluator) EvaluateWithContext(ctx context.Context, flagID string, evalCtx proto.Message) (value *pbflagsv1.FlagValue, source pbflagsv1.EvaluationSource) {
	// 1. Kill set check.
	if e.cache.GetKillSet().IsKilled(flagID) {
		e.metrics.cacheHitKillSet.Inc()
		e.metrics.evalKilled.Inc()
		return nil, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_KILLED
	}

	// 2. Resolve flag state (cache → stale → fetch) — reuses singleflight.
	state, val, src := e.resolveGlobalWithState(ctx, flagID)

	// If killed or archived, return as-is (no condition evaluation).
	if src == pbflagsv1.EvaluationSource_EVALUATION_SOURCE_KILLED ||
		src == pbflagsv1.EvaluationSource_EVALUATION_SOURCE_ARCHIVED {
		e.metrics.incEval(src)
		return val, src
	}

	// 3. Conditions (with inline launch overrides) — check cache, then evaluate CEL chain.
	if state != nil && len(state.Conditions) > 0 && e.condEval != nil && evalCtx != nil {
		var version uint64
		if e.condCache != nil {
			version = e.condCache.FlagVersion(flagID)
		}
		// Skip the condition cache when live overrides are present. Overrides
		// can change the resolved value without bumping the flag version,
		// and conflating two callers (one before, one after an override edit)
		// would serve stale results until the next flag refresh.
		useCondCache := e.condCache != nil && len(state.ConditionOverrides) == 0
		cacheKey := BuildCacheKey(flagID, version, state.DimMeta, evalCtx, state.Launches...)

		if useCondCache {
			if cached, noMatch, ok := e.condCache.Get(cacheKey); ok {
				e.metrics.cacheHitConditions.Inc()
				if noMatch {
					// Fall through to static-default override / default below.
					return e.applyStaticDefaultOverride(state, val, src)
				}
				e.metrics.evalCached.Inc()
				return cached, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_CACHED
			}
			e.metrics.cacheMissConditions.Inc()
		}

		result := e.condEval.EvaluateConditionsWithOverrides(flagID, state.Conditions, evalCtx, state.ConditionOverrides, state.Launches...)

		if result.Value != nil {
			if useCondCache {
				e.condCache.Set(cacheKey, result.Value)
			}
			if result.LaunchHit {
				e.metrics.evalLaunch.Inc()
				return result.Value, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_LAUNCH
			}
			if result.OverrideHit {
				e.metrics.evalConditionOverride.Inc()
				return result.Value, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_CONDITION_OVERRIDE
			}
			e.metrics.evalCondition.Inc()
			return result.Value, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_CONDITION
		}
		// No condition matched — cache the no-match to avoid re-evaluation.
		if useCondCache {
			e.condCache.SetNoMatch(cacheKey)
		}
	}

	// 4. Static-default override (live), then whatever resolveGlobal determined.
	return e.applyStaticDefaultOverride(state, val, src)
}

// applyStaticDefaultOverride returns the live static-default override (when
// set) in preference to the compiled default. When no override is set, the
// caller-provided (val, src) pair is returned and the eval counter is
// incremented with that source.
func (e *Evaluator) applyStaticDefaultOverride(
	state *CachedFlagState,
	val *pbflagsv1.FlagValue,
	src pbflagsv1.EvaluationSource,
) (*pbflagsv1.FlagValue, pbflagsv1.EvaluationSource) {
	if state != nil && state.StaticDefaultOverride != nil {
		e.metrics.evalConditionOverride.Inc()
		return state.StaticDefaultOverride, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_CONDITION_OVERRIDE
	}
	e.metrics.incEval(src)
	return val, src
}

func (e *Evaluator) resolveGlobal(
	ctx context.Context,
	flagID string,
) (*pbflagsv1.FlagValue, pbflagsv1.EvaluationSource) {
	_, val, src := e.resolveGlobalWithState(ctx, flagID)
	return val, src
}

// resolveGlobalWithState resolves global flag state via cache → stale → fetch,
// returning the CachedFlagState alongside the evaluated (value, source).
func (e *Evaluator) resolveGlobalWithState(
	ctx context.Context,
	flagID string,
) (*CachedFlagState, *pbflagsv1.FlagValue, pbflagsv1.EvaluationSource) {
	state := e.cache.GetFlagState(flagID)
	if state != nil {
		e.metrics.cacheHitFlags.Inc()
		val, src := e.evalFlagState(state)
		return state, val, src
	}

	e.metrics.cacheMissFlags.Inc()

	if stale := e.cache.GetStaleFlagState(flagID); stale != nil {
		e.backgroundRefreshFlag(flagID)
		val, src := e.evalFlagStateWithSource(stale, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_STALE)
		return stale, val, src
	}

	fetched, err := e.fetcher.FetchFlagState(ctx, flagID)
	if err != nil {
		e.logger.Debug("flag state fetch failed", "flag_id", flagID, "error", err)
		return nil, nil, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT
	}
	if fetched != nil {
		e.setFlagState(fetched)
	}
	val, src := e.evalFlagState(fetched)
	return fetched, val, src
}

// evalFlagState evaluates an already-fetched flag state using GLOBAL as the
// source for fresh values. Shared by resolveGlobal and the inline kill-check
// prefetch path.
func (e *Evaluator) evalFlagState(state *CachedFlagState) (*pbflagsv1.FlagValue, pbflagsv1.EvaluationSource) {
	return e.evalFlagStateWithSource(state, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_GLOBAL)
}

// evalFlagStateWithSource evaluates flag state with a caller-specified source
// label (e.g. GLOBAL for fresh, STALE for background refresh).
// The global state no longer carries a value — conditions and compiled defaults
// handle actual values. This method checks killed and archived status only.
func (e *Evaluator) evalFlagStateWithSource(state *CachedFlagState, _ pbflagsv1.EvaluationSource) (*pbflagsv1.FlagValue, pbflagsv1.EvaluationSource) {
	if state == nil {
		return nil, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT
	}
	if state.Archived {
		return nil, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT
	}
	if state.State == pbflagsv1.State_STATE_KILLED {
		return nil, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT
	}
	// No global value — conditions and default handle everything.
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
				e.metrics.bgRefreshErr.Inc()
				return nil, err
			}
			if fetched != nil {
				e.setFlagState(fetched)
			}
			e.metrics.bgRefreshOK.Inc()
			return nil, nil
		})
	}()
}
