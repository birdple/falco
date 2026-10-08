package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/birdple/falco/internal/config"
)

// newPprofTestServer builds a minimal Server. No storage or processor is needed:
// only the /debug/pprof/ routes are exercised here.
func newPprofTestServer(t *testing.T, enablePprof, apiKeyRequired bool) *Server {
	t.Helper()

	cfg := &config.Config{}
	cfg.Server.Port = 8080
	cfg.Server.Host = "127.0.0.1"
	cfg.Development.EnablePprof = enablePprof
	cfg.Security.APIKeyRequired = apiKeyRequired
	cfg.Security.APIKey = "test-secret"

	return NewServer(&ServerConfig{Config: cfg})
}

// TestPprof_DisabledByDefault: without ENABLE_PPROF the routes do not exist.
// That is the half of the flag that matters — for a long time it did nothing.
func TestPprof_DisabledByDefault(t *testing.T) {
	s := newPprofTestServer(t, false, false)

	for _, path := range []string{"/debug/pprof/", "/debug/pprof/heap", "/debug/pprof/goroutineleak"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		s.Router().ServeHTTP(w, req)
		assert.Equal(t, http.StatusNotFound, w.Code, "path %s", path)
	}
}

// TestPprof_EnabledServesProfiles: with the flag on the routes answer, and in
// particular goroutineleak, the profile new in Go 1.27.
func TestPprof_EnabledServesProfiles(t *testing.T) {
	s := newPprofTestServer(t, true, true)

	tests := []struct {
		name string
		path string
	}{
		{"index", "/debug/pprof/"},
		{"heap", "/debug/pprof/heap?debug=1"},
		{"goroutine", "/debug/pprof/goroutine?debug=1"},
		{"goroutineleak", "/debug/pprof/goroutineleak?debug=1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			req.Header.Set("X-API-Key", "test-secret")
			w := httptest.NewRecorder()
			s.Router().ServeHTTP(w, req)

			require.Equal(t, http.StatusOK, w.Code)
			assert.NotEmpty(t, w.Body.String())
		})
	}
}

// TestPprof_RequiresAPIKey: a profile exposes code paths and internal process
// state, so it sits behind the same key as /metrics.
func TestPprof_RequiresAPIKey(t *testing.T) {
	s := newPprofTestServer(t, true, true)

	req := httptest.NewRequest(http.MethodGet, "/debug/pprof/goroutineleak?debug=1", nil)
	w := httptest.NewRecorder()
	s.Router().ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code)

	req = httptest.NewRequest(http.MethodGet, "/debug/pprof/goroutineleak?debug=1", nil)
	req.Header.Set("X-API-Key", "test-secret")
	w = httptest.NewRecorder()
	s.Router().ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}

// TestPprof_NotMountedWithoutAPIKey: with no key to put them behind, the
// profiles are not mounted at all rather than served in the open.
func TestPprof_NotMountedWithoutAPIKey(t *testing.T) {
	s := newPprofTestServer(t, true, false)

	req := httptest.NewRequest(http.MethodGet, "/debug/pprof/heap?debug=1", nil)
	w := httptest.NewRecorder()
	s.Router().ServeHTTP(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)
}
