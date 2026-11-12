package telemetry

import (
	"context"
	"net/http"
	"strings"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// WrapHandler wraps an http.Handler with OpenTelemetry instrumentation.
//
// It also opens the path for http.route to reach the server metric: otelhttp
// builds the metric attributes from the request it keeps, and chi copies the
// request while routing, so the pattern never reaches it. The router middleware
// publishes the route into the Labeler — carried in the context and read after
// the handler runs — which is the only way the metric gets http.route. Without
// it, http.server.request.duration cannot be faceted per endpoint.
func WrapHandler(handler http.Handler, serverName string) http.Handler {
	instrumented := otelhttp.NewHandler(handler, serverName)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// One Labeler per request: its attributes are read when the metric is
		// recorded, by which time the router knows which route matched.
		ctx := otelhttp.ContextWithLabeler(r.Context(), &otelhttp.Labeler{})
		instrumented.ServeHTTP(w, r.WithContext(ctx))
	})
}

// TagRoute sets the route name and http.route attribute on the request's span
// and publishes the route for otelhttp's metric.
//
// Call it from the router middleware after routing: before there is no route,
// and an empty route is not tagged so 404s keep their generic span name instead
// of inventing an endpoint. The span and the Labeler are touched on the
// request's own goroutine, between the handler and the span's end.
func TagRoute(ctx context.Context, method, pattern string) {
	route := routeFromPattern(pattern)
	if route == "" {
		return
	}

	if labeler, ok := otelhttp.LabelerFromContext(ctx); ok {
		labeler.Add(semconv.HTTPRoute(route))
	}

	span := trace.SpanFromContext(ctx)
	if !span.IsRecording() {
		return
	}
	span.SetAttributes(semconv.HTTPRoute(route))
	span.SetName(method + " " + route)
}

// routeFromPattern keeps the part that starts at "/", dropping any method (and
// host) prefix a router may have put in front of the path. Both the http.route
// attribute and the span name want the path alone.
func routeFromPattern(pattern string) string {
	if i := strings.IndexByte(pattern, '/'); i >= 0 {
		return pattern[i:]
	}
	return ""
}

// WrapTransport wraps an http.RoundTripper with OpenTelemetry instrumentation
// for outgoing HTTP requests. Pass nil to wrap http.DefaultTransport.
func WrapTransport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return otelhttp.NewTransport(base)
}
