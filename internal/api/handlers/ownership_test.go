package handlers

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apimw "github.com/birdple/falco/internal/api/middleware"
)

func uploadAs(t *testing.T, h *Handler, scope *apimw.APIScope, owner, query string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/upload"+query, bytes.NewReader(body))
	req.Header.Set("Content-Type", "image/jpeg")
	if owner != "" {
		req.Header.Set("X-Owner-Id", owner)
	}
	req = req.WithContext(apimw.WithScope(req.Context(), scope))
	w := httptest.NewRecorder()
	h.HandleUpload(w, req)
	return w
}

func TestUpload_ScopedCallerCannotTakeOverAnotherOwnersObject(t *testing.T) {
	h := scopeHandler(t)
	h.config.Processing.MaxFileSizeMB = 10
	img := jpegWithEXIF(t)
	scoped := onlyBucket("main")

	require.Equal(t, http.StatusCreated, uploadAs(t, h, scoped, "alice", "?id=avatar&format=jpeg", img).Code)

	// Bob may not replace Alice's object under a chosen id...
	w := uploadAs(t, h, scoped, "bob", "?id=avatar&format=jpeg", img)
	assert.Equal(t, http.StatusConflict, w.Code, w.Body.String())

	// ...Alice may.
	assert.Equal(t, http.StatusCreated, uploadAs(t, h, scoped, "alice", "?id=avatar&format=jpeg", img).Code)

	// With a content-hash id Bob gets the existing object back, and Alice
	// stays its owner.
	require.Equal(t, http.StatusCreated, uploadAs(t, h, scoped, "alice", "?format=jpeg", img).Code)
	require.Equal(t, http.StatusCreated, uploadAs(t, h, scoped, "bob", "?format=jpeg", img).Code)

	backend, err := h.storageRegistry.Get("main")
	require.NoError(t, err)
	meta, err := backend.Stat(t.Context(), "avatar")
	require.NoError(t, err)
	assert.Equal(t, "alice", meta.OwnerID)
}

func TestMeta_OwnerIDOnlyForAdminOrOwner(t *testing.T) {
	h := scopeHandler(t)
	h.config.Processing.MaxFileSizeMB = 10
	require.Equal(t, http.StatusCreated, uploadAs(t, h, onlyBucket("main"), "alice", "?id=pic&format=jpeg", jpegWithEXIF(t)).Code)

	router := chi.NewRouter()
	router.Get("/api/v1/meta/*", h.HandleObjectMeta)
	get := func(scope *apimw.APIScope, owner string) string {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/meta/pic", nil)
		if owner != "" {
			req.Header.Set("X-Owner-Id", owner)
		}
		req = req.WithContext(apimw.WithScope(req.Context(), scope))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		return w.Body.String()
	}

	assert.Contains(t, get(&apimw.APIScope{IsAdmin: true}, ""), `"alice"`)
	assert.Contains(t, get(onlyBucket("main"), "alice"), `"alice"`)
	assert.NotContains(t, get(onlyBucket("main"), "bob"), `"alice"`)
	assert.NotContains(t, get(onlyBucket("main"), ""), `"alice"`)
}
