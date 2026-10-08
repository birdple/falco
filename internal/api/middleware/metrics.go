// Package middleware holds falco's HTTP middleware: security headers, API key
// and scoped-key authentication, rate limiting, request size limits, real client
// IP resolution and metrics.
//
// RealIP is falco's own, not chi's: chi's trusts X-Forwarded-For
// unconditionally; this one only from an allowlisted proxy.
package middleware

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/birdple/falco/internal/pkg/metrics"
)

// MetricsMiddleware provides HTTP metrics collection using Prometheus
type MetricsMiddleware struct {
	metrics *metrics.Metrics
}

// NewMetricsMiddleware creates a new metrics middleware
func NewMetricsMiddleware(m *metrics.Metrics) *MetricsMiddleware {
	return &MetricsMiddleware{
		metrics: m,
	}
}

// Handler returns the metrics middleware handler
func (m *MetricsMiddleware) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Wrap response writer to capture status code and size
		wrapped := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}

		next.ServeHTTP(wrapped, r)

		// Get route pattern for cleaner metrics (not the actual path with IDs)
		routePattern := chi.RouteContext(r.Context()).RoutePattern()
		if routePattern == "" {
			// NOT r.URL.Path: every distinct path a scanner probes would
			// become its own time series, and Prometheus keeps them all.
			routePattern = unmatchedRouteLabel
		}
		method := methodLabel(r.Method)

		// Record metrics
		duration := time.Since(start).Seconds()
		status := strconv.Itoa(wrapped.statusCode)

		m.metrics.HTTPRequestsTotal.WithLabelValues(method, routePattern, status).Inc()
		m.metrics.HTTPRequestDuration.WithLabelValues(method, routePattern).Observe(duration)
		m.metrics.HTTPResponseSize.WithLabelValues(method, routePattern).Observe(float64(wrapped.size))
	})
}

// unmatchedRouteLabel is the path label of a request no route matched.
const unmatchedRouteLabel = "unmatched"

// methodLabel bounds the method label to the standard methods. The method is
// client-chosen text, so passing it through let anyone mint a new series per
// request with a made-up verb.
func methodLabel(method string) string {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch,
		http.MethodDelete, http.MethodConnect, http.MethodOptions, http.MethodTrace:
		return method
	default:
		return "OTHER"
	}
}

// responseWriter wraps http.ResponseWriter to capture status code and response size
type responseWriter struct {
	http.ResponseWriter
	statusCode int
	size       int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	size, err := rw.ResponseWriter.Write(b)
	rw.size += size
	return size, err
}

// Flush implements the http.Flusher interface
func (rw *responseWriter) Flush() {
	if f, ok := rw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
