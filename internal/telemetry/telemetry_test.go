package telemetry

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"

	"github.com/birdple/falco/internal/version"
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

// OTEL_TRACES_SAMPLER is what keeps 100% of spans from reaching the collector.
// The default has to stay parentbased_always_on, and a bad value must not take
// the service down.
func TestSamplerFromEnv(t *testing.T) {
	cases := []struct {
		name    string
		sampler string
		arg     string
		want    string
	}{
		{"default", "", "", "ParentBased{"},
		{"always_on", "always_on", "", "AlwaysOnSampler"},
		{"always_off", "always_off", "", "AlwaysOffSampler"},
		{"traceidratio", "traceidratio", "0.25", "TraceIDRatioBased{0.25}"},
		{"parentbased_traceidratio", "parentbased_traceidratio", "0.1", "TraceIDRatioBased{0.1}"},
		{"parentbased_always_off", "parentbased_always_off", "", "AlwaysOffSampler"},
		// An unknown sampler falls back to the default instead of failing.
		{"unknown", "nonsense", "", "ParentBased{"},
		// An out-of-range ratio samples everything: the mistake that loses no data.
		{"bad_ratio", "parentbased_traceidratio", "7", "TraceIDRatioBased{1}"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("OTEL_TRACES_SAMPLER", tc.sampler)
			t.Setenv("OTEL_TRACES_SAMPLER_ARG", tc.arg)

			if got := samplerFromEnv().Description(); !strings.Contains(got, tc.want) {
				t.Errorf("sampler description = %q, want it to contain %q", got, tc.want)
			}
		})
	}
}

// The resource is the only place service.version, service.instance.id and the
// environment can come from; without them a backend cannot tell deploys apart
// or separate replicas.
func TestBuildResource_CarriesServiceIdentity(t *testing.T) {
	t.Setenv("OTEL_DEPLOYMENT_ENV", "staging")
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "deployment.environment=production,team.owner=birdple")

	res, err := buildResource(context.Background(), "falco-test")
	if err != nil {
		t.Fatal(err)
	}

	attrs := make(map[attribute.Key]string, len(res.Attributes()))
	for _, kv := range res.Attributes() {
		attrs[kv.Key] = kv.Value.AsString()
	}

	if got := attrs[semconv.ServiceNameKey]; got != "falco-test" {
		t.Errorf("service.name = %q, want falco-test", got)
	}
	if got := attrs[semconv.ServiceVersionKey]; got != version.Version {
		t.Errorf("service.version = %q, want %q", got, version.Version)
	}
	if got := attrs[semconv.ServiceNamespaceKey]; got != serviceNamespace {
		t.Errorf("service.namespace = %q, want %q", got, serviceNamespace)
	}
	if attrs[semconv.ServiceInstanceIDKey] == "" {
		t.Error("service.instance.id is empty")
	}
	// OTEL_RESOURCE_ATTRIBUTES wins over the OTEL_DEPLOYMENT_ENV fallback: it is
	// what the compose file and Dokploy actually set.
	if got := attrs[semconv.DeploymentEnvironmentKey]; got != "production" {
		t.Errorf("deployment.environment = %q, want production", got)
	}
	if got := attrs["team.owner"]; got != "birdple" {
		t.Errorf("team.owner = %q, want birdple", got)
	}
}
