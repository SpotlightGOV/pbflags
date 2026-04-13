package evaluator

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
)

func TestEvaluate_CreatesSpanWithAttributes(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	defer tp.Shutdown(context.Background())

	tracer := tp.Tracer("pbflags/evaluator")
	cache := newTestCache(t)
	fetcher := &stubFetcher{
		flagState: &CachedFlagState{FlagID: "f/1", State: pbflagsv1.State_STATE_DEFAULT},
	}
	eval := NewEvaluator(cache, fetcher, slog.Default(), NewNoopMetrics(), tracer)

	val, src := eval.Evaluate(context.Background(), "f/1", "entity-42")
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT, src)
	require.Nil(t, val)

	spans := exporter.GetSpans()
	require.Len(t, spans, 1, "expected one span")
	assert.Equal(t, "Evaluator.Evaluate", spans[0].Name)

	attrs := make(map[string]string)
	for _, a := range spans[0].Attributes {
		attrs[string(a.Key)] = a.Value.Emit()
	}
	assert.Equal(t, "f/1", attrs["flag_id"])
	assert.Equal(t, "entity-42", attrs["entity_id"])
	assert.Equal(t, "default", attrs["source"])
}

func TestEvaluate_KilledFlagSpanHasKilledSource(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	defer tp.Shutdown(context.Background())

	tracer := tp.Tracer("pbflags/evaluator")
	cache := newTestCache(t)
	cache.SetKillSet(&KillSet{
		FlagIDs: map[string]struct{}{"f/1": {}},
	})
	fetcher := &stubFetcher{}
	eval := NewEvaluator(cache, fetcher, slog.Default(), NewNoopMetrics(), tracer)

	_, src := eval.Evaluate(context.Background(), "f/1", "")
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_KILLED, src)

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)

	attrs := make(map[string]string)
	for _, a := range spans[0].Attributes {
		attrs[string(a.Key)] = a.Value.Emit()
	}
	assert.Equal(t, "killed", attrs["source"])
}

func TestEvaluate_CacheHitSetsAttribute(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	defer tp.Shutdown(context.Background())

	tracer := tp.Tracer("pbflags/evaluator")
	cache := newTestCache(t)
	fetcher := &stubFetcher{
		flagState: &CachedFlagState{FlagID: "f/1", State: pbflagsv1.State_STATE_DEFAULT},
	}
	eval := NewEvaluator(cache, fetcher, slog.Default(), NewNoopMetrics(), tracer)

	// First call populates cache.
	eval.Evaluate(context.Background(), "f/1", "")
	cache.WaitAll()
	exporter.Reset()

	// Second call should hit cache.
	eval.Evaluate(context.Background(), "f/1", "")

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)

	attrs := make(map[string]string)
	for _, a := range spans[0].Attributes {
		attrs[string(a.Key)] = a.Value.Emit()
	}
	assert.Equal(t, "true", attrs["cache_hit"])
}
