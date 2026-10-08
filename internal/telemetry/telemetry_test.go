package telemetry

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// Without an endpoint, telemetry must be off: no SDK provider installed and a
// shutdown that returns at once. It used to export to localhost:4317 anyway,
// and shutting down with no collector there blocked for the whole budget.
func TestInit_NoEndpointIsOff(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")

	shutdown, err := Init(context.Background(), "falco-test")
	if err != nil {
		t.Fatal(err)
	}
	if _, isSDK := otel.GetTracerProvider().(*sdktrace.TracerProvider); isSDK {
		t.Fatal("an SDK tracer provider was installed with no endpoint configured")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	if err := shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Fatalf("shutdown took %v with telemetry off", time.Since(start))
	}
}
