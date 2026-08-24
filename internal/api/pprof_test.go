package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/birdple/falco/internal/config"
)

// newPprofTestServer arma un Server mínimo. No hace falta storage ni processor:
// las rutas que se ejercitan aquí son sólo las de /debug/pprof/.
func newPprofTestServer(t *testing.T, enablePprof, apiKeyRequired bool) *Server {
	t.Helper()

	cfg := &config.Config{}
	cfg.Server.Port = 8080
	cfg.Server.Host = "127.0.0.1"
	cfg.Development.EnablePprof = enablePprof
	cfg.Security.APIKeyRequired = apiKeyRequired
	cfg.Security.APIKey = "secreto-de-prueba"

	return NewServer(&ServerConfig{Config: cfg})
}

// TestPprof_DisabledByDefault: sin ENABLE_PPROF las rutas no existen. Es la
// mitad que importa del flag — durante mucho tiempo no hizo absolutamente nada.
func TestPprof_DisabledByDefault(t *testing.T) {
	s := newPprofTestServer(t, false, false)

	for _, path := range []string{"/debug/pprof/", "/debug/pprof/heap", "/debug/pprof/goroutineleak"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		s.Router().ServeHTTP(w, req)
		assert.Equal(t, http.StatusNotFound, w.Code, "path %s", path)
	}
}

// TestPprof_EnabledServesProfiles: con el flag prendido las rutas responden, y
// en particular goroutineleak, el perfil nuevo de Go 1.27.
func TestPprof_EnabledServesProfiles(t *testing.T) {
	s := newPprofTestServer(t, true, false)

	tests := []struct {
		name string
		path string
	}{
		{"índice", "/debug/pprof/"},
		{"heap", "/debug/pprof/heap?debug=1"},
		{"goroutine", "/debug/pprof/goroutine?debug=1"},
		{"goroutineleak", "/debug/pprof/goroutineleak?debug=1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			w := httptest.NewRecorder()
			s.Router().ServeHTTP(w, req)

			require.Equal(t, http.StatusOK, w.Code)
			assert.NotEmpty(t, w.Body.String())
		})
	}
}

// TestPprof_RequiresAPIKey: un perfil expone rutas de código y estado interno
// del proceso, así que va detrás de la misma llave que /metrics.
func TestPprof_RequiresAPIKey(t *testing.T) {
	s := newPprofTestServer(t, true, true)

	req := httptest.NewRequest(http.MethodGet, "/debug/pprof/goroutineleak?debug=1", nil)
	w := httptest.NewRecorder()
	s.Router().ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code)

	req = httptest.NewRequest(http.MethodGet, "/debug/pprof/goroutineleak?debug=1", nil)
	req.Header.Set("X-API-Key", "secreto-de-prueba")
	w = httptest.NewRecorder()
	s.Router().ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}
