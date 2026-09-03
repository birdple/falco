// Package middleware holds falco's HTTP middleware: security headers, API key
// and scoped-key authentication, rate limiting, request size limits, real client
// IP resolution and metrics.
//
// The RealIP middleware here replaces chi's, which trusts X-Forwarded-For
// unconditionally. This one only believes it from an allowlisted proxy.
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
			routePattern = r.URL.Path
		}

		// Record metrics
		duration := time.Since(start).Seconds()
		status := strconv.Itoa(wrapped.statusCode)

		m.metrics.HTTPRequestsTotal.WithLabelValues(r.Method, routePattern, status).Inc()
		m.metrics.HTTPRequestDuration.WithLabelValues(r.Method, routePattern).Observe(duration)
		m.metrics.HTTPResponseSize.WithLabelValues(r.Method, routePattern).Observe(float64(wrapped.size))
	})
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
