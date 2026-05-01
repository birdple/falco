package telemetry

import (
	"net/http"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// WrapHandler wraps an http.Handler with OpenTelemetry instrumentation.
func WrapHandler(handler http.Handler, serverName string) http.Handler {
	return otelhttp.NewHandler(handler, serverName)
}

// WrapTransport wraps an http.RoundTripper with OpenTelemetry instrumentation
// for outgoing HTTP requests. Pass nil to wrap http.DefaultTransport.
func WrapTransport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return otelhttp.NewTransport(base)
}
