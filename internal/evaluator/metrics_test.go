package evaluator

import (
	"context"
	"log/slog"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
)

func TestNewMetrics_RegistersAllFamilies(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)
	require.NotNil(t, m)

	// Observe at least once so counters/histograms appear in Gather.
	m.EvaluationsTotal.WithLabelValues("global", "ok").Inc()
	m.CacheHitsTotal.WithLabelValues("flags").Inc()
	m.CacheMissesTotal.WithLabelValues("overrides").Inc()
	m.FetchDuration.WithLabelValues("db", "flag_state").Observe(0.001)
	m.KillSetSize.Set(0)
	m.ConsecutiveFails.Set(0)
	m.PollerLastSuccess.SetToCurrentTime()

	families, err := reg.Gather()
	require.NoError(t, err)

	names := make(map[string]bool, len(families))
	for _, f := range families {
		names[f.GetName()] = true
	}

	for _, want := range []string{
		"pbflags_evaluations_total",
		"pbflags_cache_hits_total",
		"pbflags_cache_misses_total",
		"pbflags_fetch_duration_seconds",
		"pbflags_kill_set_size",
		"pbflags_health_consecutive_failures",
		"pbflags_poller_last_success_timestamp",
	} {
		assert.True(t, names[want], "expected metric family %q", want)
	}
}

func TestNewNoopMetrics_DoesNotPanic(t *testing.T) {
	m := NewNoopMetrics()
	require.NotNil(t, m)
	// Exercise all counters/gauges to ensure they are non-nil and functional.
	m.EvaluationsTotal.WithLabelValues("global", "ok").Inc()
	m.CacheHitsTotal.WithLabelValues("flags").Inc()
	m.CacheMissesTotal.WithLabelValues("overrides").Inc()
	m.KillSetSize.Set(3)
	m.ConsecutiveFails.Set(0)
	m.PollerLastSuccess.SetToCurrentTime()
}

func TestEvaluate_IncrementsEvaluationCounter(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)

	cache := newTestCache(t)
	fetcher := &stubFetcher{
		flagState: &CachedFlagState{
			FlagID: "f/1",
			State:  pbflagsv1.State_STATE_DEFAULT,
		},
	}
	eval := NewEvaluator(cache, fetcher, slog.Default(), m)

	eval.Evaluate(context.Background(), "f/1", "")

	families, err := reg.Gather()
	require.NoError(t, err)

	for _, f := range families {
		if f.GetName() == "pbflags_evaluations_total" {
			for _, metric := range f.GetMetric() {
				labels := make(map[string]string)
				for _, lp := range metric.GetLabel() {
					labels[lp.GetName()] = lp.GetValue()
				}
				if labels["source"] == "default" && labels["status"] == "ok" {
					assert.Equal(t, float64(1), metric.GetCounter().GetValue(), "evaluation count")
					return
				}
			}
		}
	}
	t.Fatal("pbflags_evaluations_total{source=default,status=ok} not found in gathered metrics")
}

func TestEvaluate_IncrementsCacheHitOnKillSet(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)

	cache := newTestCache(t)
	cache.SetKillSet(&KillSet{
		FlagIDs: map[string]struct{}{"f/1": {}},
	})

	fetcher := &stubFetcher{}
	eval := NewEvaluator(cache, fetcher, slog.Default(), m)

	_, src := eval.Evaluate(context.Background(), "f/1", "")
	assert.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_KILLED, src)

	families, err := reg.Gather()
	require.NoError(t, err)

	for _, f := range families {
		if f.GetName() == "pbflags_cache_hits_total" {
			for _, metric := range f.GetMetric() {
				for _, lp := range metric.GetLabel() {
					if lp.GetName() == "tier" && lp.GetValue() == "kill_set" {
						assert.Equal(t, float64(1), metric.GetCounter().GetValue())
						return
					}
				}
			}
		}
	}
	t.Fatal("pbflags_cache_hits_total{tier=kill_set} not found")
}

func TestHealthTracker_UpdatesConsecutiveFailuresGauge(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)
	ht := NewHealthTracker(m)

	ht.RecordFailure()
	ht.RecordFailure()

	families, err := reg.Gather()
	require.NoError(t, err)

	for _, f := range families {
		if f.GetName() == "pbflags_health_consecutive_failures" {
			require.NotEmpty(t, f.GetMetric())
			assert.Equal(t, float64(2), f.GetMetric()[0].GetGauge().GetValue())

			ht.RecordSuccess()
			families2, _ := reg.Gather()
			for _, f2 := range families2 {
				if f2.GetName() == "pbflags_health_consecutive_failures" {
					assert.Equal(t, float64(0), f2.GetMetric()[0].GetGauge().GetValue())
					return
				}
			}
		}
	}
	t.Fatal("pbflags_health_consecutive_failures not found")
}

func TestSourceLabel(t *testing.T) {
	tests := []struct {
		source pbflagsv1.EvaluationSource
		want   string
	}{
		{pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT, "default"},
		{pbflagsv1.EvaluationSource_EVALUATION_SOURCE_GLOBAL, "global"},
		{pbflagsv1.EvaluationSource_EVALUATION_SOURCE_OVERRIDE, "override"},
		{pbflagsv1.EvaluationSource_EVALUATION_SOURCE_KILLED, "killed"},
		{pbflagsv1.EvaluationSource_EVALUATION_SOURCE_CACHED, "cached"},
		{pbflagsv1.EvaluationSource_EVALUATION_SOURCE_ARCHIVED, "archived"},
		{pbflagsv1.EvaluationSource_EVALUATION_SOURCE_UNSPECIFIED, "unknown"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, sourceLabel(tt.source), "sourceLabel(%v)", tt.source)
	}
}
