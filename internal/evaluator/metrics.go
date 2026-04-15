package evaluator

import (
	"github.com/prometheus/client_golang/prometheus"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
)

// Metrics holds all Prometheus metrics for the evaluator.
//
// Hot-path counters are pre-curried at init time to avoid per-call
// WithLabelValues map lookups. The *Vec originals are retained for
// registration and Gather but are not used on the evaluation path.
type Metrics struct {
	// Vectors (for registration / gather).
	EvaluationsTotal    *prometheus.CounterVec
	CacheHitsTotal      *prometheus.CounterVec
	CacheMissesTotal    *prometheus.CounterVec
	BackgroundRefreshes *prometheus.CounterVec
	FetchDuration       *prometheus.HistogramVec
	KillSetSize         prometheus.Gauge
	ConsecutiveFails    prometheus.Gauge
	PollerLastSuccess   prometheus.Gauge

	// Pre-curried hot-path counters — zero map lookups per call.
	evalDefault   prometheus.Counter
	evalKilled    prometheus.Counter
	evalCondition prometheus.Counter
	evalLaunch    prometheus.Counter
	evalCached    prometheus.Counter
	evalStale     prometheus.Counter

	cacheHitKillSet    prometheus.Counter
	cacheHitFlags      prometheus.Counter
	cacheHitConditions prometheus.Counter

	cacheMissFlags      prometheus.Counter
	cacheMissConditions prometheus.Counter

	bgRefreshOK  prometheus.Counter
	bgRefreshErr prometheus.Counter
}

// NewMetrics creates and registers all metrics with the given registerer.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := newMetricsVecs()

	reg.MustRegister(
		m.EvaluationsTotal,
		m.CacheHitsTotal,
		m.CacheMissesTotal,
		m.BackgroundRefreshes,
		m.FetchDuration,
		m.KillSetSize,
		m.ConsecutiveFails,
		m.PollerLastSuccess,
	)

	m.curryCounters()
	return m
}

// NewNoopMetrics returns metrics that are not registered with any registry.
// Use in tests where metric values don't matter.
func NewNoopMetrics() *Metrics {
	m := newMetricsVecs()
	m.curryCounters()
	return m
}

func newMetricsVecs() *Metrics {
	return &Metrics{
		EvaluationsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "pbflags_evaluations_total",
			Help: "Total flag evaluations by source and status.",
		}, []string{"source", "status"}),

		CacheHitsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "pbflags_cache_hits_total",
			Help: "Cache hits by tier.",
		}, []string{"tier"}),

		CacheMissesTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "pbflags_cache_misses_total",
			Help: "Cache misses by tier.",
		}, []string{"tier"}),

		BackgroundRefreshes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "pbflags_background_refreshes_total",
			Help: "Background cache refreshes by tier and result.",
		}, []string{"tier", "result"}),

		FetchDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "pbflags_fetch_duration_seconds",
			Help:    "Fetch latency for DB and upstream calls.",
			Buckets: []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1},
		}, []string{"backend", "operation"}),

		KillSetSize: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "pbflags_kill_set_size",
			Help: "Number of currently killed flags.",
		}),

		ConsecutiveFails: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "pbflags_health_consecutive_failures",
			Help: "Consecutive upstream fetch failures.",
		}),

		PollerLastSuccess: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "pbflags_poller_last_success_timestamp",
			Help: "Unix timestamp of the last successful kill-set poll.",
		}),
	}
}

// curryCounters pre-resolves all hot-path label combinations to avoid
// per-call map lookups in WithLabelValues.
func (m *Metrics) curryCounters() {
	m.evalDefault = m.EvaluationsTotal.WithLabelValues("default", "ok")
	m.evalKilled = m.EvaluationsTotal.WithLabelValues("killed", "ok")
	m.evalCondition = m.EvaluationsTotal.WithLabelValues("condition", "ok")
	m.evalLaunch = m.EvaluationsTotal.WithLabelValues("launch", "ok")
	m.evalCached = m.EvaluationsTotal.WithLabelValues("cached", "ok")
	m.evalStale = m.EvaluationsTotal.WithLabelValues("stale", "ok")

	m.cacheHitKillSet = m.CacheHitsTotal.WithLabelValues("kill_set")
	m.cacheHitFlags = m.CacheHitsTotal.WithLabelValues("flags")
	m.cacheHitConditions = m.CacheHitsTotal.WithLabelValues("conditions")

	m.cacheMissFlags = m.CacheMissesTotal.WithLabelValues("flags")
	m.cacheMissConditions = m.CacheMissesTotal.WithLabelValues("conditions")

	m.bgRefreshOK = m.BackgroundRefreshes.WithLabelValues("flags", "ok")
	m.bgRefreshErr = m.BackgroundRefreshes.WithLabelValues("flags", "error")
}

// incEval increments the pre-curried evaluation counter for the given source.
func (m *Metrics) incEval(source pbflagsv1.EvaluationSource) {
	switch source {
	case pbflagsv1.EvaluationSource_EVALUATION_SOURCE_KILLED:
		m.evalKilled.Inc()
	case pbflagsv1.EvaluationSource_EVALUATION_SOURCE_CONDITION:
		m.evalCondition.Inc()
	case pbflagsv1.EvaluationSource_EVALUATION_SOURCE_LAUNCH:
		m.evalLaunch.Inc()
	case pbflagsv1.EvaluationSource_EVALUATION_SOURCE_CACHED:
		m.evalCached.Inc()
	case pbflagsv1.EvaluationSource_EVALUATION_SOURCE_STALE:
		m.evalStale.Inc()
	default:
		m.evalDefault.Inc()
	}
}

// sourceLabel converts an EvaluationSource enum to a short label string.
func sourceLabel(source pbflagsv1.EvaluationSource) string {
	switch source {
	case pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT:
		return "default"
	case pbflagsv1.EvaluationSource_EVALUATION_SOURCE_GLOBAL:
		return "global"
	case pbflagsv1.EvaluationSource_EVALUATION_SOURCE_OVERRIDE:
		return "override"
	case pbflagsv1.EvaluationSource_EVALUATION_SOURCE_KILLED:
		return "killed"
	case pbflagsv1.EvaluationSource_EVALUATION_SOURCE_CACHED:
		return "cached"
	case pbflagsv1.EvaluationSource_EVALUATION_SOURCE_ARCHIVED:
		return "archived"
	case pbflagsv1.EvaluationSource_EVALUATION_SOURCE_STALE:
		return "stale"
	case pbflagsv1.EvaluationSource_EVALUATION_SOURCE_CONDITION:
		return "condition"
	case pbflagsv1.EvaluationSource_EVALUATION_SOURCE_LAUNCH:
		return "launch"
	default:
		return "unknown"
	}
}
