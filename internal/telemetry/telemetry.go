// Package telemetry wires up OpenTelemetry tracing and metrics.
//
// This package is a near-copy across falco, owl and auk. The copies are NOT
// identical: each one imports its own internal/version, and the router-side
// middlewares that tag the HTTP route live in each service's HTTP layer. The
// initialisation below is meant to stay in sync.
//
// Without OTEL_EXPORTER_OTLP_ENDPOINT (or the traces-specific variant) nothing
// is initialised: otherwise the SDK falls back to localhost:4317 and retries
// forever when no collector is listening — falco used to do exactly that, and
// every shutdown spent its whole budget trying to flush spans to localhost.
//
// Init never takes the service down; what it does do is break the silence — a
// missing endpoint is announced once, and export failures go through
// otel.SetErrorHandler instead of dying in the batcher's buffer.
package telemetry

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/birdple/falco/internal/version"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	otellogglobal "go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// serviceNamespace groups the monorepo's services under one namespace.
// OTEL_RESOURCE_ATTRIBUTES can override (or blank) it per deployment.
const serviceNamespace = "birdple"

// Init initializes OpenTelemetry with OTLP gRPC exporters for traces and
// metrics. Returns a shutdown function that must be deferred by the caller.
//
// Without an endpoint it does nothing and returns a no-op shutdown: telemetry
// is off, not pointed at a default collector that is not there. A collector
// that is unreachable is not fatal either; the caller logs the error.
func Init(ctx context.Context, serviceName string) (shutdown func(context.Context) error, err error) {
	// Installed even when telemetry is off: it is global, and it is the only
	// way an exporter failure (401, 413, collector down) leaves a trace.
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		fmt.Fprintf(os.Stderr, "telemetry: %v\n", err)
	}))

	if !endpointConfigured() {
		fmt.Fprintf(os.Stderr, "telemetry: OTEL_EXPORTER_OTLP_ENDPOINT not set, telemetry is off for %s\n", serviceName)
		return noop, nil
	}

	res, err := buildResource(ctx, serviceName)
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
		sdktrace.WithSampler(samplerFromEnv()),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	// The metrics exporter is only built when there is a general or a
	// metrics-specific endpoint. With only OTEL_EXPORTER_OTLP_TRACES_ENDPOINT
	// set, otlpmetricgrpc would fall back to localhost:4317 and retry forever —
	// exactly what this package exists to prevent.
	var mp *metric.MeterProvider
	var logProvider *sdklog.LoggerProvider
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" || os.Getenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT") != "" {
		metricExp, err := otlpmetricgrpc.New(ctx, otlpmetricgrpc.WithInsecure())
		if err != nil {
			fmt.Fprintf(os.Stderr, "telemetry: metric exporter failed: %v\n", err)
		} else {
			mp = metric.NewMeterProvider(
				metric.WithReader(metric.NewPeriodicReader(metricExp,
					metric.WithInterval(60*time.Second),
				)),
				metric.WithResource(res),
			)
			otel.SetMeterProvider(mp)
		}
	}

	// Logs por OTLP: mismo resource y mismo collector que trazas/métricas. El
	// writer de Zerolog usa el provider global en el momento de escribir, así
	// que no importa que el logger se haya creado antes.
	logExp, logErr := otlploggrpc.New(ctx, otlploggrpc.WithInsecure())
	if logErr != nil {
		fmt.Fprintf(os.Stderr, "telemetry: log exporter failed: %v\n", logErr)
	} else {
		logProvider = sdklog.NewLoggerProvider(
			sdklog.WithProcessor(sdklog.NewBatchProcessor(logExp)),
			sdklog.WithResource(res),
		)
		otellogglobal.SetLoggerProvider(logProvider)
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
		if logProvider != nil {
			if e := logProvider.Shutdown(ctx); e != nil {
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

// endpointConfigured reports whether there is anywhere to export to. The SDK
// accepts both variables (general and per-signal); either one is enough.
func endpointConfigured() bool {
	return os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" ||
		os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") != ""
}

// buildResource builds the service identity.
//
// OTEL_RESOURCE_ATTRIBUTES wins over what is set here: it is the standard way
// to adjust attributes per deployment, and the compose file puts
// deployment.environment there. OTEL_DEPLOYMENT_ENV stays as the historical
// fallback for deployments that already define it, and "development" for the
// rest.
func buildResource(ctx context.Context, serviceName string) (*resource.Resource, error) {
	attrs := []attribute.KeyValue{
		semconv.ServiceNameKey.String(serviceName),
		semconv.ServiceNamespaceKey.String(serviceNamespace),
		semconv.ServiceVersionKey.String(version.Version),
		semconv.DeploymentEnvironmentKey.String(envOrDefault("OTEL_DEPLOYMENT_ENV", "development")),
	}
	// Without an instance id a backend merges replicas into one entity, and
	// there is no way to say "this replica is the one failing". The hostname
	// is enough while each replica runs in its own container or host.
	if host, err := os.Hostname(); err == nil && host != "" {
		attrs = append(attrs, semconv.ServiceInstanceIDKey.String(host))
	}

	base, err := resource.New(ctx,
		resource.WithAttributes(attrs...),
		resource.WithTelemetrySDK(),
		resource.WithProcessRuntimeDescription(),
		resource.WithHost(),
	)
	if err != nil {
		return nil, err
	}

	env, envErr := resource.New(ctx, resource.WithFromEnv())
	if envErr != nil {
		// A malformed OTEL_RESOURCE_ATTRIBUTES must not leave the service with
		// no identity: warn and keep the attributes above.
		fmt.Fprintf(os.Stderr, "telemetry: OTEL_RESOURCE_ATTRIBUTES ignored: %v\n", envErr)
		return base, nil
	}
	return resource.Merge(base, env)
}

// samplerFromEnv translates OTEL_TRACES_SAMPLER and OTEL_TRACES_SAMPLER_ARG,
// the same values as the OTel spec. An unknown value is reported and falls back
// to the default (parentbased_always_on) instead of failing startup.
func samplerFromEnv() sdktrace.Sampler {
	name := strings.ToLower(strings.TrimSpace(envOrDefault("OTEL_TRACES_SAMPLER", "parentbased_always_on")))
	switch name {
	case "always_on":
		return sdktrace.AlwaysSample()
	case "always_off":
		return sdktrace.NeverSample()
	case "traceidratio":
		return sdktrace.TraceIDRatioBased(ratioFromEnv())
	case "parentbased_always_on":
		return sdktrace.ParentBased(sdktrace.AlwaysSample())
	case "parentbased_always_off":
		return sdktrace.ParentBased(sdktrace.NeverSample())
	case "parentbased_traceidratio":
		return sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratioFromEnv()))
	default:
		fmt.Fprintf(os.Stderr, "telemetry: unknown OTEL_TRACES_SAMPLER %q, using parentbased_always_on\n", name)
		return sdktrace.ParentBased(sdktrace.AlwaysSample())
	}
}

// ratioFromEnv reads OTEL_TRACES_SAMPLER_ARG. The spec wants a ratio in [0,1];
// out of range or unreadable is reported and becomes 1.0 (sample everything),
// which is the mistake that does not lose data.
func ratioFromEnv() float64 {
	raw := os.Getenv("OTEL_TRACES_SAMPLER_ARG")
	if raw == "" {
		return 1.0
	}
	ratio, err := strconv.ParseFloat(raw, 64)
	if err != nil || ratio < 0 || ratio > 1 {
		fmt.Fprintf(os.Stderr, "telemetry: invalid OTEL_TRACES_SAMPLER_ARG %q, using 1.0\n", raw)
		return 1.0
	}
	return ratio
}

func noop(_ context.Context) error { return nil }

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
