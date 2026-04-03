package unit

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/birdple/falco/internal/api/handlers"
	"github.com/birdple/falco/internal/api/types"
	"github.com/birdple/falco/internal/config"
	"github.com/birdple/falco/internal/processor"
	"github.com/birdple/falco/internal/storage"
	"github.com/birdple/falco/tests/mocks"
)

func TestHandleUpload_BinarySuccess(t *testing.T) {
	mockStorage := new(mocks.MockStorageBackend)
	mockProcessor := new(mocks.MockImageProcessor)

	cfg := &config.Config{
		Processing: config.ProcessingConfig{
			MaxFileSizeMB:  10,
			DefaultQuality: 85,
			DefaultFormat:  "webp",
		},
	}
	startTime := time.Now()

	h := handlers.NewHandler(cfg, mockStorage, mockProcessor, startTime)

	imageData := []byte{0xFF, 0xD8, 0xFF, 0xE0} // JPEG header

	req := httptest.NewRequest("POST", "/upload", bytes.NewReader(imageData))
	req.Header.Set("Content-Type", "image/jpeg")

	mockStorage.On("Exists", mock.Anything, mock.Anything).Return(false, nil)

	mockProcessor.On("Process", mock.Anything, mock.Anything, mock.Anything).
		Return(&processor.ProcessedImage{
			Data: io.NopCloser(bytes.NewReader(imageData)),
			Metadata: &processor.ImageMetadata{
				Format:      "jpeg",
				ContentType: "image/jpeg",
				Size:        int64(len(imageData)),
				Width:       100,
				Height:      100,
			},
		}, nil)

	mockStorage.On("Store", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(nil)

	w := httptest.NewRecorder()
	h.HandleUpload(w, req)

	assert.Equal(t, http.StatusCreated, w.Code)
	mockProcessor.AssertExpectations(t)
	mockStorage.AssertExpectations(t)
}

func TestHandleDelivery_Success(t *testing.T) {
	mockStorage := new(mocks.MockStorageBackend)
	mockProcessor := new(mocks.MockImageProcessor)

	cfg := &config.Config{}
	cfg.Processing.MaxDimensions.Width = 4000
	cfg.Processing.MaxDimensions.Height = 4000
	startTime := time.Now()

	h := handlers.NewHandler(cfg, mockStorage, mockProcessor, startTime)

	imageData := []byte{0xFF, 0xD8, 0xFF, 0xE0}
	expectedMetadata := &storage.ImageMetadata{
		ID:          "test-key",
		Format:      "jpeg",
		ContentType: "image/jpeg",
		Size:        int64(len(imageData)),
	}

	mockStorage.On("Retrieve", mock.Anything, "test-key").
		Return(io.NopCloser(bytes.NewReader(imageData)), expectedMetadata, nil)

	mockProcessor.On("Process", mock.Anything, mock.Anything, mock.Anything).
		Return(&processor.ProcessedImage{
			Data: io.NopCloser(bytes.NewReader(imageData)),
			Metadata: &processor.ImageMetadata{
				Format:      "jpeg",
				ContentType: "image/jpeg",
				Size:        int64(len(imageData)),
			},
		}, nil).Maybe()

	mockProcessor.On("ValidateFormat", mock.Anything).Return(true).Maybe()

	req := httptest.NewRequest("GET", "/delivery/test-key", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "test-key")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()
	h.HandleDelivery(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "image/jpeg", w.Header().Get("Content-Type"))
	mockStorage.AssertExpectations(t)
}

func TestHandleList_Success(t *testing.T) {
	mockStorage := new(mocks.MockStorageBackend)
	mockProcessor := new(mocks.MockImageProcessor)

	cfg := &config.Config{}
	startTime := time.Now()

	h := handlers.NewHandler(cfg, mockStorage, mockProcessor, startTime)

	req := httptest.NewRequest("GET", "/list", nil)

	expectedResults := []storage.ListResult{
		{Key: "image1.jpg", Size: 1024},
		{Key: "image2.png", Size: 2048},
	}

	mockStorage.On("List", mock.Anything, "").
		Return(expectedResults, nil)

	w := httptest.NewRecorder()
	h.HandleList(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "image1.jpg")
	assert.Contains(t, w.Body.String(), "image2.png")
	mockStorage.AssertExpectations(t)
}

func TestHandleDelete_Success(t *testing.T) {
	mockStorage := new(mocks.MockStorageBackend)
	mockProcessor := new(mocks.MockImageProcessor)

	cfg := &config.Config{}
	startTime := time.Now()

	h := handlers.NewHandler(cfg, mockStorage, mockProcessor, startTime)

	deleteReq := types.DeleteRequest{
		Keys: []string{"test-key"},
	}
	reqBody, _ := json.Marshal(deleteReq)
	req := httptest.NewRequest("DELETE", "/delete", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")

	mockStorage.On("Delete", mock.Anything, "test-key").
		Return(nil)

	w := httptest.NewRecorder()
	h.HandleDelete(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	mockStorage.AssertExpectations(t)

	var response types.DeleteResponse
	json.Unmarshal(w.Body.Bytes(), &response)
	assert.True(t, response.Success)
	assert.Equal(t, 1, response.Count)
}

func TestHandleHealth_Success(t *testing.T) {
	mockStorage := new(mocks.MockStorageBackend)
	mockProcessor := new(mocks.MockImageProcessor)

	cfg := &config.Config{}
	startTime := time.Now()

	h := handlers.NewHandler(cfg, mockStorage, mockProcessor, startTime)

	req := httptest.NewRequest("GET", "/health", nil)

	mockStorage.On("Health", mock.Anything).Return(nil)
	mockStorage.On("GetStats", mock.Anything).Return(&storage.StorageStats{
		TotalImages: 10,
		TotalSize:   1024000,
	}, nil)

	w := httptest.NewRecorder()
	h.HandleHealth(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "healthy")
	mockStorage.AssertExpectations(t)
}

// Error case tests
func TestHandleUpload_MissingFile(t *testing.T) {
	mockStorage := new(mocks.MockStorageBackend)
	mockProcessor := new(mocks.MockImageProcessor)

	cfg := &config.Config{
		Processing: config.ProcessingConfig{
			MaxFileSizeMB: 10,
		},
	}
	startTime := time.Now()

	h := handlers.NewHandler(cfg, mockStorage, mockProcessor, startTime)

	req := httptest.NewRequest("POST", "/upload", bytes.NewReader([]byte{}))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=boundary")

	w := httptest.NewRecorder()
	h.HandleUpload(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	body := w.Body.String()
	assert.True(t, strings.Contains(body, "MISSING_FILE") || strings.Contains(body, "INVALID_REQUEST"))
}

func TestHandleDelivery_MissingID(t *testing.T) {
	mockStorage := new(mocks.MockStorageBackend)
	mockProcessor := new(mocks.MockImageProcessor)

	cfg := &config.Config{}
	startTime := time.Now()

	h := handlers.NewHandler(cfg, mockStorage, mockProcessor, startTime)

	req := httptest.NewRequest("GET", "/delivery/", nil)

	w := httptest.NewRecorder()
	h.HandleDelivery(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "MISSING_ID")
}

func TestHandleDelete_InvalidJSON(t *testing.T) {
	mockStorage := new(mocks.MockStorageBackend)
	mockProcessor := new(mocks.MockImageProcessor)

	cfg := &config.Config{}
	startTime := time.Now()

	h := handlers.NewHandler(cfg, mockStorage, mockProcessor, startTime)

	req := httptest.NewRequest("DELETE", "/delete", bytes.NewReader([]byte("invalid json")))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.HandleDelete(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "INVALID_JSON")
}
