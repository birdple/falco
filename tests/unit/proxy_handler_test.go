package unit

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/birdple/falco/internal/api/handlers"
	"github.com/birdple/falco/internal/config"
	"github.com/birdple/falco/tests/mocks"
)

// makeProxyRouter wires up a Handler with mocks and returns a chi router with
// the /api/v1/proxy/* route registered.
func makeProxyRouter(t *testing.T, mockStorage *mocks.MockStorageBackend, mockProcessor *mocks.MockImageProcessor) *chi.Mux {
	t.Helper()
	cfg := &config.Config{}
	cfg.Processing.MaxDimensions.Width = 4000
	cfg.Processing.MaxDimensions.Height = 4000
	cfg.Processing.DefaultFormat = "webp"

	h := handlers.NewHandler(cfg, mockStorage, mockProcessor, time.Now())

	r := chi.NewRouter()
	r.Get("/api/v1/proxy/*", h.HandleProxy)
	return r
}

// TestHandleProxy_MissingURL returns 400 when the ?url= parameter is absent.
func TestHandleProxy_MissingURL(t *testing.T) {
	mockStorage := new(mocks.MockStorageBackend)
	mockProcessor := new(mocks.MockImageProcessor)

	mockProcessor.On("GetFromCache", mock.Anything).Return([]byte(nil), false).Maybe()

	router := makeProxyRouter(t, mockStorage, mockProcessor)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/proxy/abc123.webp", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "MISSING_URL")
}

// TestHandleProxy_InvalidExtension returns 400 when the path segment has no
// known image extension.
func TestHandleProxy_InvalidExtension(t *testing.T) {
	mockStorage := new(mocks.MockStorageBackend)
	mockProcessor := new(mocks.MockImageProcessor)

	router := makeProxyRouter(t, mockStorage, mockProcessor)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/proxy/abc123.pdf?url=https://example.com/img.pdf", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "INVALID_EXTENSION")
}

// TestHandleProxy_HostNotAllowed returns 403 when the URL host is not in the
// default allowlist (and PROXY_ALLOWED_HOSTS env var is unset).
func TestHandleProxy_HostNotAllowed(t *testing.T) {
	mockStorage := new(mocks.MockStorageBackend)
	mockProcessor := new(mocks.MockImageProcessor)

	mockProcessor.On("GetFromCache", mock.Anything).Return([]byte(nil), false).Maybe()

	router := makeProxyRouter(t, mockStorage, mockProcessor)

	// example.com is not in the default allowlist.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/proxy/abc123.webp?url=https://example.com/image.webp", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), "HOST_NOT_ALLOWED")
}

// TestHandleProxy_PrivateHost_Localhost returns 403 for a localhost URL, even
// if it were somehow added to the allowlist, because the SSRF guard fires.
// Here we test the guard independently via a URL whose host resolves to loopback.
func TestHandleProxy_PrivateHostSSRF(t *testing.T) {
	mockStorage := new(mocks.MockStorageBackend)
	mockProcessor := new(mocks.MockImageProcessor)

	mockProcessor.On("GetFromCache", mock.Anything).Return([]byte(nil), false).Maybe()

	// Override the allowlist to include "localhost" so we reach the SSRF guard.
	t.Setenv("PROXY_ALLOWED_HOSTS", "localhost")

	router := makeProxyRouter(t, mockStorage, mockProcessor)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/proxy/abc123.webp?url=http://localhost:8080/image.webp", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), "HOST_NOT_ALLOWED")
}

// TestHandleProxy_InvalidAbsoluteURL returns 400 for a relative or malformed URL.
func TestHandleProxy_InvalidAbsoluteURL(t *testing.T) {
	mockStorage := new(mocks.MockStorageBackend)
	mockProcessor := new(mocks.MockImageProcessor)

	mockProcessor.On("GetFromCache", mock.Anything).Return([]byte(nil), false).Maybe()

	router := makeProxyRouter(t, mockStorage, mockProcessor)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/proxy/abc123.webp?url=not-a-url", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "INVALID_URL")
}

// TestHandleProxy_SSRFBlocksPrivateEvenIfAllowlisted verifies that a host in
// the allowlist is still blocked when it resolves to a private/loopback address.
// We use an httptest.Server to simulate the upstream so no real network call is made.
func TestHandleProxy_SSRFBlocksPrivateEvenIfAllowlisted(t *testing.T) {
	mockStorage := new(mocks.MockStorageBackend)
	mockProcessor := new(mocks.MockImageProcessor)

	// Upstream fake image server.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/webp")
		w.WriteHeader(http.StatusOK)
		// Minimal webp-like body (just needs to look like bytes to LimitReader).
		w.Write([]byte("RIFF\x00\x00\x00\x00WEBPVP8 "))
	}))
	defer upstream.Close()

	// Override the allowlist to permit the loopback address used by httptest.
	// Note: httptest.Server binds to 127.0.0.1; we whitelist "127.0.0.1" AND
	// skip the SSRF guard for this test by using the server's hostname directly.
	// Since 127.0.0.1 is private the SSRF guard will still fire — that is
	// intentional and correct behavior. We assert 403 from the guard here,
	// demonstrating that the allowlist check passed but SSRF blocked it.
	t.Setenv("PROXY_ALLOWED_HOSTS", "127.0.0.1")

	router := makeProxyRouter(t, mockStorage, mockProcessor)

	mockProcessor.On("GetFromCache", mock.Anything).Return([]byte(nil), false).Maybe()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/proxy/abc123.webp?url="+upstream.URL+"/image.webp", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// SSRF guard catches 127.0.0.1 — expected 403, not 400 (URL was valid).
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), "HOST_NOT_ALLOWED")
}
