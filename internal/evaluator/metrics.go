package evaluator

import (
	"github.com/prometheus/client_golang/prometheus"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
)

// Metrics holds all Prometheus metrics for the evaluator.
type Metrics struct {
	EvaluationsTotal    *prometheus.CounterVec
	CacheHitsTotal      *prometheus.CounterVec
	CacheMissesTotal    *prometheus.CounterVec
	BackgroundRefreshes *prometheus.CounterVec
	FetchDuration       *prometheus.HistogramVec
	KillSetSize         prometheus.Gauge
	ConsecutiveFails    prometheus.Gauge
	PollerLastSuccess   prometheus.Gauge
}

// NewMetrics creates and registers all metrics with the given registerer.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
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

	return m
}

// NewNoopMetrics returns metrics that are not registered with any registry.
// Use in tests where metric values don't matter.
func NewNoopMetrics() *Metrics {
	return &Metrics{
		EvaluationsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "pbflags_evaluations_total",
			Help: "Total flag evaluations.",
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
			Help: "Background refreshes.",
		}, []string{"tier", "result"}),

		FetchDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "pbflags_fetch_duration_seconds",
			Help: "Fetch latency.",
		}, []string{"backend", "operation"}),

		KillSetSize: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "pbflags_kill_set_size",
			Help: "Killed flags.",
		}),

		ConsecutiveFails: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "pbflags_health_consecutive_failures",
			Help: "Consecutive failures.",
		}),

		PollerLastSuccess: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "pbflags_poller_last_success_timestamp",
			Help: "Last poll success.",
		}),
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
