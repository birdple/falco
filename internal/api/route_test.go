package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// serveWithSpan runs a chi router with routeTag and an already-started span in
// the request context, which is what otelhttp gives the router in production.
func serveWithSpan(t *testing.T, target string) []sdktrace.ReadOnlySpan {
	t.Helper()

	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))

	r := chi.NewRouter()
	r.Use(routeTag)
	r.Get("/api/v1/images/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	ctx, span := tp.Tracer("test").Start(context.Background(), "GET")
	req := httptest.NewRequest(http.MethodGet, target, nil).WithContext(ctx)
	r.ServeHTTP(httptest.NewRecorder(), req)
	span.End()

	return recorder.Ended()
}

// The chi span used to come out as "GET" with no http.route: otelhttp sits
// outside the router and chi copies the request. The middleware has to write
// both onto the span otelhttp opened.
func TestRouteTag_NamesTheSpanAfterTheRoute(t *testing.T) {
	spans := serveWithSpan(t, "/api/v1/images/abc123")

	require.Len(t, spans, 1)
	assert.Equal(t, "GET /api/v1/images/{id}", spans[0].Name())
	assert.Contains(t, spans[0].Attributes(), semconv.HTTPRoute("/api/v1/images/{id}"))
}

// A 404 has no route. Tagging it would fold every unknown path into a real
// endpoint in the metrics.
func TestRouteTag_LeavesUnmatchedRequestsAlone(t *testing.T) {
	spans := serveWithSpan(t, "/nope")

	require.Len(t, spans, 1)
	assert.Equal(t, "GET", spans[0].Name())
	assert.NotContains(t, spans[0].Attributes(), attribute.String("http.route", "/nope"))
}
