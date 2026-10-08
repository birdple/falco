package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/birdple/falco/internal/pkg/metrics"
)

// requestLabelValues returns every value of the given label across the
// falco_http_requests_total series.
func requestLabelValues(t *testing.T, label string) map[string]bool {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)
	values := make(map[string]bool)
	for _, f := range families {
		if f.GetName() != "falco_http_requests_total" {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == label {
					values[l.GetValue()] = true
				}
			}
		}
	}
	return values
}

// Unmatched paths and made-up methods are client-chosen text. Used verbatim as
// labels, every probe for /wp-admin/<random> or every request with a junk verb
// minted a new Prometheus series, and those are never freed.
func TestMetricsMiddleware_BoundsLabelCardinality(t *testing.T) {
	r := chi.NewRouter()
	r.Use(NewMetricsMiddleware(metrics.Default()).Handler)
	r.Get("/images/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/images/abc", nil),
		httptest.NewRequest(http.MethodGet, "/wp-admin/probe-1234", nil),
		httptest.NewRequest("BREW", "/images/abc", nil),
	} {
		r.ServeHTTP(httptest.NewRecorder(), req)
	}

	paths := requestLabelValues(t, "path")
	assert.True(t, paths["/images/{id}"], "matched routes keep their pattern")
	assert.True(t, paths[unmatchedRouteLabel], "unmatched routes share one label")
	assert.False(t, paths["/wp-admin/probe-1234"], "the raw path never becomes a label")

	methods := requestLabelValues(t, "method")
	assert.True(t, methods["GET"])
	assert.True(t, methods["OTHER"], "a non-standard method is folded into OTHER")
	assert.False(t, methods["BREW"])
}

func TestMethodLabel(t *testing.T) {
	for _, m := range []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "CONNECT", "OPTIONS", "TRACE"} {
		assert.Equal(t, m, methodLabel(m))
	}
	assert.Equal(t, "OTHER", methodLabel("get"))
	assert.Equal(t, "OTHER", methodLabel("PROPFIND"))
}
