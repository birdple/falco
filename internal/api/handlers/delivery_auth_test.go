package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func deliveryRouter(h *Handler) *chi.Mux {
	r := chi.NewRouter()
	r.Get("/api/v1/images/*", h.HandleDelivery)
	return r
}

func TestHandleDelivery_SignatureGate(t *testing.T) {
	h := scopeHandler(t)
	h.config.Security.HMACRequired = true
	h.config.Security.APIKeyRequired = true
	t.Setenv("HMAC_REQUIRE_EXPIRY", "true")
	h.config.Processing.MaxDimensions.Width = 4096
	h.config.Processing.MaxDimensions.Height = 4096
	router := deliveryRouter(h)

	serve := func(url string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, url, nil))
		return w
	}

	// Unsigned and badly signed URLs are refused.
	w := serve("/api/v1/images/missing?w=100")
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), "INVALID_SIGNATURE")
	assert.Equal(t, http.StatusForbidden, serve("/api/v1/images/missing?w=100&sig=AAAA").Code)

	// A valid signature gets through to storage (and the object is missing).
	sw := signRequest(t, h, nil, `{"path":"/api/v1/images/missing?w=100","expires_in":60}`)
	require.Equal(t, http.StatusOK, sw.Code)
	var signed SignURLResponse
	require.NoError(t, json.Unmarshal(sw.Body.Bytes(), &signed))
	w = serve(signed.SignedURL)
	assert.Equal(t, http.StatusNotFound, w.Code, w.Body.String())

	// Changing a parameter after signing breaks the signature.
	assert.Equal(t, http.StatusForbidden, serve(signed.SignedURL+"&h=50").Code)

	// Without an expiry policy the route fails closed instead of guessing.
	t.Setenv("HMAC_REQUIRE_EXPIRY", "")
	w = serve(signed.SignedURL)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Contains(t, w.Body.String(), "CONFIG_ERROR")
}
