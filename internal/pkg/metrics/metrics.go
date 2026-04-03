// Package metrics provides Prometheus metrics for the Imagine image processing service.
package metrics

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Metrics holds all application metrics
type Metrics struct {
	// HTTP metrics
	HTTPRequestsTotal   *prometheus.CounterVec
	HTTPRequestDuration *prometheus.HistogramVec
	HTTPResponseSize    *prometheus.HistogramVec

	// Processing metrics
	ImageProcessingTotal    *prometheus.CounterVec
	ImageProcessingDuration *prometheus.HistogramVec
	ImageProcessingSize     *prometheus.HistogramVec

	// Cache metrics
	CacheHits      prometheus.Counter
	CacheMisses    prometheus.Counter
	CacheSize      prometheus.Gauge
	CacheItemCount prometheus.Gauge
	CacheEvictions prometheus.Counter

	// Storage metrics
	StorageOperationsTotal    *prometheus.CounterVec
	StorageOperationDuration  *prometheus.HistogramVec
	StorageCircuitBreakerOpen prometheus.Gauge
}

// namespace is the Prometheus namespace for all metrics
const namespace = "falco"

// Singleton pattern to prevent duplicate metric registration
var (
	defaultMetrics *Metrics
	metricsOnce    sync.Once
)

// Default returns the singleton metrics instance
func Default() *Metrics {
	metricsOnce.Do(func() {
		defaultMetrics = create()
	})
	return defaultMetrics
}

// New returns the singleton metrics instance (alias for Default for compatibility)
func New() *Metrics {
	return Default()
}

// create actually creates the metrics (internal use only)
func create() *Metrics {
	return &Metrics{
		// HTTP metrics
		HTTPRequestsTotal: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "http_requests_total",
				Help:      "Total number of HTTP requests by method, path, and status code",
			},
			[]string{"method", "path", "status"},
		),
		HTTPRequestDuration: promauto.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Name:      "http_request_duration_seconds",
				Help:      "Duration of HTTP requests in seconds",
				Buckets:   prometheus.DefBuckets,
			},
			[]string{"method", "path"},
		),
		HTTPResponseSize: promauto.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Name:      "http_response_size_bytes",
				Help:      "Size of HTTP responses in bytes",
				Buckets:   prometheus.ExponentialBuckets(100, 10, 8), // 100B to 10GB
			},
			[]string{"method", "path"},
		),

		// Processing metrics
		ImageProcessingTotal: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "image_processing_total",
				Help:      "Total number of image processing operations by format and status",
			},
			[]string{"input_format", "output_format", "status"},
		),
		ImageProcessingDuration: promauto.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Name:      "image_processing_duration_seconds",
				Help:      "Duration of image processing operations in seconds",
				Buckets:   []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
			},
			[]string{"operation"},
		),
		ImageProcessingSize: promauto.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Name:      "image_size_bytes",
				Help:      "Size of processed images in bytes",
				Buckets:   prometheus.ExponentialBuckets(1024, 4, 10), // 1KB to ~1GB
			},
			[]string{"type"}, // "input" or "output"
		),

		// Cache metrics
		CacheHits: promauto.NewCounter(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "cache_hits_total",
				Help:      "Total number of cache hits",
			},
		),
		CacheMisses: promauto.NewCounter(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "cache_misses_total",
				Help:      "Total number of cache misses",
			},
		),
		CacheSize: promauto.NewGauge(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Name:      "cache_size_bytes",
				Help:      "Current cache size in bytes",
			},
		),
		CacheItemCount: promauto.NewGauge(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Name:      "cache_item_count",
				Help:      "Current number of items in the cache",
			},
		),
		CacheEvictions: promauto.NewCounter(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "cache_evictions_total",
				Help:      "Total number of cache evictions",
			},
		),

		// Storage metrics
		StorageOperationsTotal: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "storage_operations_total",
				Help:      "Total number of storage operations by operation type and status",
			},
			[]string{"operation", "backend", "status"},
		),
		StorageOperationDuration: promauto.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Name:      "storage_operation_duration_seconds",
				Help:      "Duration of storage operations in seconds",
				Buckets:   prometheus.DefBuckets,
			},
			[]string{"operation", "backend"},
		),
		StorageCircuitBreakerOpen: promauto.NewGauge(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Name:      "storage_circuit_breaker_open",
				Help:      "Whether the storage circuit breaker is open (1) or closed (0)",
			},
		),
	}
}
