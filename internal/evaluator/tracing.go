package evaluator

import (
	"context"
	"os"
	"strconv"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace/noop"
)

// InitTracer sets up the OpenTelemetry TracerProvider.
//
// When OTEL_EXPORTER_OTLP_ENDPOINT is set, spans are exported via gRPC to
// the configured collector. When unset, a noop provider is registered so all
// tracing calls are zero-cost no-ops.
//
// Sampling is configured via PBFLAGS_OTEL_SAMPLE_RATIO (float, 0.0–1.0,
// default 0.01 = 1%). The sampler is ParentBased: incoming requests that
// are already sampled by an upstream are always traced; root spans created
// by this service are sampled at the configured ratio.
//
// Returns a shutdown function that flushes buffered spans.
func InitTracer(ctx context.Context, serviceName, version string) (shutdown func(context.Context) error, err error) {
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" {
		otel.SetTracerProvider(noop.NewTracerProvider())
		return func(context.Context) error { return nil }, nil
	}

	exp, err := otlptracegrpc.New(ctx)
	if err != nil {
		return nil, err
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(version),
		),
	)
	if err != nil {
		return nil, err
	}

	ratio := 0.01 // default: 1% of root spans
	if v := os.Getenv("PBFLAGS_OTEL_SAMPLE_RATIO"); v != "" {
		if parsed, parseErr := strconv.ParseFloat(v, 64); parseErr == nil && parsed >= 0 && parsed <= 1 {
			ratio = parsed
		}
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio))),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return tp.Shutdown, nil
}
