package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The proxy is authorised like delivery: with HMAC_REQUIRED an unsigned or
// badly signed URL is refused before anything is fetched.
func TestHandleProxy_RequiresSignature(t *testing.T) {
	h := scopeHandler(t)
	h.config.Security.HMACRequired = true
	h.config.Security.APIKeyRequired = true
	t.Setenv("HMAC_REQUIRE_EXPIRY", "true")

	router := chi.NewRouter()
	router.Get("/api/v1/proxy/*", h.HandleProxy)

	const path = "/api/v1/proxy/x.webp?url=https://not-allowlisted.example/a.jpg"

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), "INVALID_SIGNATURE")

	// Signed, it gets past authorisation and hits the next gate: the host
	// allowlist.
	sw := signRequest(t, h, nil, `{"path":"`+path+`","expires_in":60}`)
	require.Equal(t, http.StatusOK, sw.Code, sw.Body.String())
	var signed SignURLResponse
	require.NoError(t, json.Unmarshal(sw.Body.Bytes(), &signed))

	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, signed.SignedURL, nil))
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.True(t, strings.Contains(w.Body.String(), "HOST_NOT_ALLOWED"), w.Body.String())
}
