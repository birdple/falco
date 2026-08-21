package httputil

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteJSON(t *testing.T) {
	w := httptest.NewRecorder()
	data := map[string]string{"key": "value"}

	err := WriteJSON(w, http.StatusOK, data)
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

	var result map[string]string
	err = json.Unmarshal(w.Body.Bytes(), &result)
	require.NoError(t, err)
	assert.Equal(t, "value", result["key"])
}

func TestWriteJSON_DifferentStatusCodes(t *testing.T) {
	codes := []int{http.StatusOK, http.StatusCreated, http.StatusBadRequest, http.StatusInternalServerError}
	for _, code := range codes {
		w := httptest.NewRecorder()
		err := WriteJSON(w, code, map[string]string{})
		require.NoError(t, err)
		assert.Equal(t, code, w.Code)
	}
}

func TestWriteSuccess(t *testing.T) {
	w := httptest.NewRecorder()
	err := WriteSuccess(w, http.StatusOK, map[string]string{"id": "abc123"})
	require.NoError(t, err)

	var result JSONResponse
	err = json.Unmarshal(w.Body.Bytes(), &result)
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Nil(t, result.Error)
	assert.NotNil(t, result.Data)
}

func BenchmarkWriteJSON(b *testing.B) {
	data := map[string]any{
		"id":     "abc123",
		"format": "webp",
		"width":  1920,
		"height": 1080,
	}
	b.ResetTimer()
	for range b.N {
		w := httptest.NewRecorder()
		WriteJSON(w, http.StatusOK, data)
	}
}

func BenchmarkWriteSuccess(b *testing.B) {
	data := map[string]string{"id": "abc123", "url": "/api/v1/images/abc123"}
	b.ResetTimer()
	for range b.N {
		w := httptest.NewRecorder()
		WriteSuccess(w, http.StatusOK, data)
	}
}

func BenchmarkWriteError(b *testing.B) {
	b.ResetTimer()
	for range b.N {
		w := httptest.NewRecorder()
		WriteError(w, http.StatusBadRequest, "INVALID_FORMAT", "unsupported image format")
	}
}

func TestWriteError(t *testing.T) {
	w := httptest.NewRecorder()
	err := WriteError(w, http.StatusBadRequest, "INVALID_FORMAT", "unsupported image format")
	require.NoError(t, err)

	assert.Equal(t, http.StatusBadRequest, w.Code)

	var result JSONResponse
	err = json.Unmarshal(w.Body.Bytes(), &result)
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.NotNil(t, result.Error)
	assert.Equal(t, "INVALID_FORMAT", result.Error.Code)
	assert.Equal(t, "unsupported image format", result.Error.Message)
}
