package handlers

import (
	"bytes"
	"context"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apimw "github.com/birdple/falco/internal/api/middleware"
	"github.com/birdple/falco/internal/storage"
)

func TestUpload_OverLimitIs413(t *testing.T) {
	h := scopeHandler(t)
	h.config.Processing.MaxFileSizeMB = 1
	oversized := bytes.Repeat([]byte{0xff}, 1024*1024+1)
	admin := &apimw.APIScope{IsAdmin: true}

	t.Run("raw body", func(t *testing.T) {
		w := uploadAs(t, h, admin, "", "", oversized)
		assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code, w.Body.String())
		assert.Contains(t, w.Body.String(), "REQUEST_TOO_LARGE")
	})

	t.Run("multipart", func(t *testing.T) {
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		part, err := mw.CreateFormFile("file", "big.jpg")
		require.NoError(t, err)
		_, err = part.Write(oversized)
		require.NoError(t, err)
		require.NoError(t, mw.Close())

		req := httptest.NewRequest(http.MethodPost, "/api/v1/upload", &body)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req = req.WithContext(apimw.WithScope(req.Context(), admin))
		w := httptest.NewRecorder()
		h.HandleUpload(w, req)
		assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code, w.Body.String())
	})
}

// failingStat is a backend that cannot answer Stat.
type failingStat struct {
	storage.StorageBackend
	err error
}

func (f failingStat) Stat(context.Context, string) (*storage.ImageMetadata, error) {
	return nil, f.err
}

func TestUpdate_StorageFailureIsNotAForbidden(t *testing.T) {
	h := scopeHandler(t)
	h.config.Processing.MaxFileSizeMB = 10
	fs, err := storage.NewFilesystemStorage(t.TempDir())
	require.NoError(t, err)

	update := func(bucket string) *httptest.ResponseRecorder {
		body := `{"url":"https://example.com/a.jpg","bucket":"` + bucket + `","key":"k","quality":80}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/update", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Owner-Id", "alice")
		req = req.WithContext(apimw.WithScope(req.Context(), onlyBucket(bucket)))
		w := httptest.NewRecorder()
		h.HandleUpdate(w, req)
		return w
	}

	h.storageRegistry.Register("down", failingStat{fs, errors.New("connection refused")})
	w := update("down")
	assert.Equal(t, http.StatusInternalServerError, w.Code, w.Body.String())

	h.storageRegistry.Register("breaker", failingStat{fs, storage.ErrStorageUnavailable})
	w = update("breaker")
	assert.Equal(t, http.StatusServiceUnavailable, w.Code, w.Body.String())

	// A real refusal is still a 403.
	require.Equal(t, http.StatusCreated, uploadAs(t, h, onlyBucket("main"), "bob", "?id=k&format=jpeg", jpegWithEXIF(t)).Code)
	w = update("main")
	assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
}

func TestDeliverRaw_HonoursRequestedTTL(t *testing.T) {
	h := scopeHandler(t)
	h.config.Processing.MaxFileSizeMB = 10
	h.config.Cache.DefaultMaxAge = 31536000
	h.config.Cache.DefaultSMaxAge = 31536000
	require.Equal(t, http.StatusCreated, uploadAs(t, h, &apimw.APIScope{IsAdmin: true}, "", "?id=pic&format=jpeg", jpegWithEXIF(t)).Code)

	router := deliveryRouter(h)
	serve := func(url string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, url, nil))
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		return w
	}

	assert.Equal(t, "public, max-age=31536000, s-maxage=31536000", serve("/api/v1/images/pic").Header().Get("Cache-Control"))
	assert.Equal(t, "public, max-age=60, s-maxage=120", serve("/api/v1/images/pic?maxage=60&smaxage=120").Header().Get("Cache-Control"))
}
