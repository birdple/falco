package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/birdple/falco/internal/config"
)

func scopedConfig() *config.Config {
	cfg := &config.Config{}
	cfg.Storage.Buckets = map[string]config.BucketConfig{
		"a": {Type: "filesystem", Path: "/a", Keys: []config.BucketKeyConfig{{Name: "key-a", Key: "secret-a"}}},
		"b": {Type: "filesystem", Path: "/b"},
	}
	return cfg
}

func TestScopedAPIKeyAuth(t *testing.T) {
	auth := NewScopedAPIKeyAuth("admin-secret", scopedConfig())
	require.True(t, auth.HasScopedKeys())

	var seen *APIScope
	h := auth.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = GetScope(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	call := func(header, value string) int {
		seen = nil
		req := httptest.NewRequest(http.MethodGet, "/api/v1/list", nil)
		if header != "" {
			req.Header.Set(header, value)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code
	}

	assert.Equal(t, http.StatusOK, call("X-API-Key", "admin-secret"))
	require.NotNil(t, seen)
	assert.True(t, seen.IsAdmin)

	assert.Equal(t, http.StatusOK, call("Authorization", "Bearer secret-a"))
	require.NotNil(t, seen)
	assert.False(t, seen.IsAdmin)
	assert.Equal(t, "key-a", seen.KeyName)
	assert.True(t, seen.CanAccessBucket("a"))
	assert.False(t, seen.CanAccessBucket("b"))

	assert.Equal(t, http.StatusUnauthorized, call("X-API-Key", "nope"))
	assert.Nil(t, seen)
	assert.Equal(t, http.StatusUnauthorized, call("", ""))
}

func TestAPIScope_CanAccessBucket(t *testing.T) {
	var nilScope *APIScope
	assert.True(t, nilScope.CanAccessBucket("x"), "no scope means auth is off")
	assert.True(t, (&APIScope{IsAdmin: true}).CanAccessBucket("x"))
	assert.False(t, (&APIScope{}).CanAccessBucket("x"), "an empty bucket set grants nothing")
	assert.True(t, (&APIScope{Buckets: map[string]bool{"x": true}}).CanAccessBucket("x"))
}
