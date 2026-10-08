// Package telemetry wires up OpenTelemetry tracing and metrics.
//
// This package is a near-copy across owl, auk and falco, and the copies are NOT
// identical: auk and falco return early when no OTLP endpoint is configured,
// while owl initialises the exporter regardless. falco used to do the same,
// and with no collector listening every shutdown spent its whole budget
// trying to flush spans to localhost:4317.
//
// Keep that difference in mind before copying changes between them.
package telemetry

import (
	"context"
	"fmt"
	"os"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// Init initializes OpenTelemetry with OTLP gRPC exporters for traces and metrics.
// Returns a shutdown function that must be deferred by the caller.
//
// Without OTEL_EXPORTER_OTLP_ENDPOINT (or the traces-specific variant) it does
// nothing and returns a no-op shutdown: telemetry is off, not pointed at a
// default collector that is not there.
// Non-fatal: if the collector is unreachable, the service continues without telemetry.
func Init(ctx context.Context, serviceName string) (shutdown func(context.Context) error, err error) {
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" && os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") == "" {
		return noop, nil
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceNameKey.String(serviceName),
			semconv.DeploymentEnvironmentKey.String(envOrDefault("OTEL_DEPLOYMENT_ENV", "development")),
		),
		resource.WithProcessRuntimeDescription(),
		resource.WithHost(),
	)
	if err != nil {
		return noop, fmt.Errorf("telemetry: resource: %w", err)
	}

	traceExp, err := otlptracegrpc.New(ctx, otlptracegrpc.WithInsecure())
	if err != nil {
		return noop, fmt.Errorf("telemetry: trace exporter: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExp,
			sdktrace.WithBatchTimeout(5*time.Second),
		),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	metricExp, err := otlpmetricgrpc.New(ctx, otlpmetricgrpc.WithInsecure())
	if err != nil {
		fmt.Fprintf(os.Stderr, "telemetry: metric exporter failed: %v\n", err)
	}

	var mp *metric.MeterProvider
	if metricExp != nil {
		mp = metric.NewMeterProvider(
			metric.WithReader(metric.NewPeriodicReader(metricExp,
				metric.WithInterval(60*time.Second),
			)),
			metric.WithResource(res),
		)
		otel.SetMeterProvider(mp)
	}

	shutdown = func(ctx context.Context) error {
		var errs []error
		if tp != nil {
			if e := tp.Shutdown(ctx); e != nil {
				errs = append(errs, e)
			}
		}
		if mp != nil {
			if e := mp.Shutdown(ctx); e != nil {
				errs = append(errs, e)
			}
		}
		if len(errs) > 0 {
			return fmt.Errorf("telemetry shutdown errors: %v", errs)
		}
		return nil
	}

	return shutdown, nil
}

func noop(_ context.Context) error { return nil }

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
