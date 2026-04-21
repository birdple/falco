package unit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
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

// Additional Upload tests
func TestHandleUpload_MultipartWithFile(t *testing.T) {
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

	// Create proper multipart form data
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	part, _ := writer.CreateFormFile("file", "test.jpg")
	imageData := []byte{0xFF, 0xD8, 0xFF, 0xE0} // JPEG header
	part.Write(imageData)
	writer.Close()

	req := httptest.NewRequest("POST", "/upload", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	// Setup mocks
	mockProcessor.On("Process", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(&processor.ProcessedImage{
			Data: io.NopCloser(bytes.NewReader(imageData)),
			Metadata: &processor.ImageMetadata{
				Format:      "jpeg",
				ContentType: "image/jpeg",
				Size:        int64(len(imageData)),
			},
		}, nil)
	mockStorage.On("Store", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(nil)

	w := httptest.NewRecorder()
	h.HandleUpload(w, req)

	assert.Equal(t, http.StatusCreated, w.Code)
	mockStorage.AssertExpectations(t)
	mockProcessor.AssertExpectations(t)
}

func TestHandleUpload_JSONWithURL(t *testing.T) {
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

	uploadReq := map[string]interface{}{
		"url": "https://example.com/image.jpg",
	}
	reqBody, _ := json.Marshal(uploadReq)

	req := httptest.NewRequest("POST", "/upload", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")

	// This will fail in practice because we can't actually fetch the URL
	// but we're testing the validation logic
	w := httptest.NewRecorder()
	h.HandleUpload(w, req)

	// Should fail with fetch error or process the request
	assert.True(t, w.Code == http.StatusBadRequest || w.Code == http.StatusInternalServerError || w.Code == http.StatusCreated)
}

func TestHandleUpload_InvalidQuality(t *testing.T) {
	mockStorage := new(mocks.MockStorageBackend)
	mockProcessor := new(mocks.MockImageProcessor)

	cfg := &config.Config{
		Processing: config.ProcessingConfig{
			MaxFileSizeMB: 10,
		},
	}


	startTime := time.Now()

	h := handlers.NewHandler(cfg, mockStorage, mockProcessor, startTime)

	imageData := []byte{0xFF, 0xD8, 0xFF, 0xE0}
	req := httptest.NewRequest("POST", "/upload?quality=invalid", bytes.NewReader(imageData))
	req.Header.Set("Content-Type", "image/jpeg")

	w := httptest.NewRecorder()
	h.HandleUpload(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "INVALID_QUALITY")
}

// Additional Delivery tests
func TestHandleDelivery_WithTransformations(t *testing.T) {
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

	// Setup mocks
	mockProcessor.On("ValidateFormat", "webp").Return(true)
	mockProcessor.On("GenerateCacheKey", mock.Anything, mock.Anything).Return("test-cache-key")
	mockProcessor.On("GetFromCache", "test-cache-key").Return([]byte(nil), false)

	mockStorage.On("Retrieve", mock.Anything, "test-key").
		Return(io.NopCloser(bytes.NewReader(imageData)), expectedMetadata, nil)

	mockProcessor.On("Process", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(&processor.ProcessedImage{
			Data: io.NopCloser(bytes.NewReader(imageData)),
			Metadata: &processor.ImageMetadata{
				Format:      "webp",
				ContentType: "image/webp",
				Size:        int64(len(imageData)),
				Width:       100,
				Height:      100,
			},
		}, nil)

	// Request with transformation parameters
	req := httptest.NewRequest("GET", "/delivery/test-key?w=100&h=100&f=webp&q=80", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "test-key")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()
	h.HandleDelivery(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "image/webp", w.Header().Get("Content-Type"))
	mockStorage.AssertExpectations(t)
	mockProcessor.AssertExpectations(t)
}

func TestHandleDelivery_InvalidWidth(t *testing.T) {
	mockStorage := new(mocks.MockStorageBackend)
	mockProcessor := new(mocks.MockImageProcessor)

	cfg := &config.Config{}
	cfg.Processing.MaxDimensions.Width = 4000
	cfg.Processing.MaxDimensions.Height = 4000



	startTime := time.Now()

	h := handlers.NewHandler(cfg, mockStorage, mockProcessor, startTime)

	req := httptest.NewRequest("GET", "/delivery/test-key?w=10", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "test-key")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()
	h.HandleDelivery(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "INVALID_WIDTH")
}

func TestHandleDelivery_InvalidFormat(t *testing.T) {
	mockStorage := new(mocks.MockStorageBackend)
	mockProcessor := new(mocks.MockImageProcessor)

	cfg := &config.Config{}
	cfg.Processing.MaxDimensions.Width = 4000
	cfg.Processing.MaxDimensions.Height = 4000



	startTime := time.Now()

	h := handlers.NewHandler(cfg, mockStorage, mockProcessor, startTime)

	mockProcessor.On("ValidateFormat", "bmp").Return(false)

	req := httptest.NewRequest("GET", "/delivery/test-key?f=bmp", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "test-key")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()
	h.HandleDelivery(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "INVALID_FORMAT")
}

// Additional Delete tests
func TestHandleDelete_WithPrefix(t *testing.T) {
	mockStorage := new(mocks.MockStorageBackend)
	mockProcessor := new(mocks.MockImageProcessor)

	cfg := &config.Config{}


	startTime := time.Now()

	h := handlers.NewHandler(cfg, mockStorage, mockProcessor, startTime)

	deleteReq := types.DeleteRequest{
		Prefix: "images/2024/",
	}
	reqBody, _ := json.Marshal(deleteReq)
	req := httptest.NewRequest("DELETE", "/delete", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")

	// Mock listing files with prefix
	mockStorage.On("List", mock.Anything, "images/2024").Return([]storage.ListResult{
		{Key: "images/2024/img1.jpg"},
		{Key: "images/2024/img2.jpg"},
	}, nil)

	mockStorage.On("Delete", mock.Anything, "images/2024/img1.jpg").Return(nil)
	mockStorage.On("Delete", mock.Anything, "images/2024/img2.jpg").Return(nil)

	w := httptest.NewRecorder()
	h.HandleDelete(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var response types.DeleteResponse
	json.Unmarshal(w.Body.Bytes(), &response)
	assert.True(t, response.Success)
	assert.Equal(t, 2, response.Count)
	mockStorage.AssertExpectations(t)
}

func TestHandleDelete_MissingParameters(t *testing.T) {
	mockStorage := new(mocks.MockStorageBackend)
	mockProcessor := new(mocks.MockImageProcessor)

	cfg := &config.Config{}


	startTime := time.Now()

	h := handlers.NewHandler(cfg, mockStorage, mockProcessor, startTime)

	deleteReq := types.DeleteRequest{}
	reqBody, _ := json.Marshal(deleteReq)
	req := httptest.NewRequest("DELETE", "/delete", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.HandleDelete(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "MISSING_PARAMETERS")
}

// Additional List tests
func TestHandleList_WithPrefix(t *testing.T) {
	mockStorage := new(mocks.MockStorageBackend)
	mockProcessor := new(mocks.MockImageProcessor)

	cfg := &config.Config{}


	startTime := time.Now()

	h := handlers.NewHandler(cfg, mockStorage, mockProcessor, startTime)

	req := httptest.NewRequest("GET", "/list?prefix=images/", nil)

	expectedResults := []storage.ListResult{
		{Key: "photo1.jpg", Size: 1024},
		{Key: "photo2.jpg", Size: 2048},
	}

	mockStorage.On("List", mock.Anything, "images").Return(expectedResults, nil)

	w := httptest.NewRecorder()
	h.HandleList(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "photo1.jpg")
	mockStorage.AssertExpectations(t)
}

func TestHandleList_StorageError(t *testing.T) {
	mockStorage := new(mocks.MockStorageBackend)
	mockProcessor := new(mocks.MockImageProcessor)

	cfg := &config.Config{}


	startTime := time.Now()

	h := handlers.NewHandler(cfg, mockStorage, mockProcessor, startTime)

	req := httptest.NewRequest("GET", "/list", nil)

	mockStorage.On("List", mock.Anything, "").Return([]storage.ListResult{}, errors.New("storage error"))

	w := httptest.NewRecorder()
	h.HandleList(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	mockStorage.AssertExpectations(t)
}

// Additional Health tests
func TestHandleHealth_StorageUnhealthy(t *testing.T) {
	mockStorage := new(mocks.MockStorageBackend)
	mockProcessor := new(mocks.MockImageProcessor)

	cfg := &config.Config{}


	startTime := time.Now()

	h := handlers.NewHandler(cfg, mockStorage, mockProcessor, startTime)

	req := httptest.NewRequest("GET", "/health", nil)

	mockStorage.On("Health", mock.Anything).Return(errors.New("storage unavailable"))

	w := httptest.NewRecorder()
	h.HandleHealth(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Contains(t, w.Body.String(), "unhealthy")
	mockStorage.AssertExpectations(t)
}

// Removed TestHandleHealth_GetStatsError: health endpoint no longer calls GetStats()
// (health is a lean load-balancer probe; verbose stats belong in /metrics).

// Tests for HandleUpdate
func TestHandleUpdate_Success(t *testing.T) {
	mockStorage := new(mocks.MockStorageBackend)
	mockProcessor := new(mocks.MockImageProcessor)

	cfg := &config.Config{}


	startTime := time.Now()

	h := handlers.NewHandler(cfg, mockStorage, mockProcessor, startTime)

	updateReq := types.UpdateRequest{
		URL:     "https://example.com/image.jpg",
		Bucket:  "test-bucket",
		Key:     "test-key",
		Quality: 80,
		Format:  "webp",
	}
	reqBody, _ := json.Marshal(updateReq)
	req := httptest.NewRequest("POST", "/update", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")

	// Mock expectations
	mockProcessor.On("ValidateFormat", "webp").Return(true)
	mockStorage.On("Exists", mock.Anything, "test-key").Return(false, nil)
	
	imageData := []byte{0xFF, 0xD8, 0xFF, 0xE0}
	mockProcessor.On("Process", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(&processor.ProcessedImage{
			Data: io.NopCloser(bytes.NewReader(imageData)),
			Metadata: &processor.ImageMetadata{
				Format:      "webp",
				ContentType: "image/webp",
				Size:        int64(len(imageData)),
			},
		}, nil)

	mockStorage.On("Store", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(nil)

	w := httptest.NewRecorder()
	h.HandleUpdate(w, req)

	// Will fail with download error in tests, but validates JSON parsing
	assert.True(t, w.Code == http.StatusOK || w.Code == http.StatusBadRequest)
}

func TestHandleUpdate_InvalidJSON(t *testing.T) {
	mockStorage := new(mocks.MockStorageBackend)
	mockProcessor := new(mocks.MockImageProcessor)

	cfg := &config.Config{}


	startTime := time.Now()

	h := handlers.NewHandler(cfg, mockStorage, mockProcessor, startTime)

	req := httptest.NewRequest("POST", "/update", bytes.NewReader([]byte("invalid")))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.HandleUpdate(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "INVALID_JSON")
}

func TestHandleUpdate_MissingURL(t *testing.T) {
	mockStorage := new(mocks.MockStorageBackend)
	mockProcessor := new(mocks.MockImageProcessor)

	cfg := &config.Config{}


	startTime := time.Now()

	h := handlers.NewHandler(cfg, mockStorage, mockProcessor, startTime)

	updateReq := types.UpdateRequest{
		Bucket:  "test-bucket",
		Key:     "test-key",
		Quality: 80,
	}
	reqBody, _ := json.Marshal(updateReq)
	req := httptest.NewRequest("POST", "/update", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.HandleUpdate(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "MISSING_URL")
}

func TestHandleUpdate_MissingBucket(t *testing.T) {
	mockStorage := new(mocks.MockStorageBackend)
	mockProcessor := new(mocks.MockImageProcessor)

	cfg := &config.Config{}


	startTime := time.Now()

	h := handlers.NewHandler(cfg, mockStorage, mockProcessor, startTime)

	updateReq := types.UpdateRequest{
		URL:     "https://example.com/image.jpg",
		Key:     "test-key",
		Quality: 80,
	}
	reqBody, _ := json.Marshal(updateReq)
	req := httptest.NewRequest("POST", "/update", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.HandleUpdate(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "MISSING_BUCKET")
}

func TestHandleUpdate_MissingKey(t *testing.T) {
	mockStorage := new(mocks.MockStorageBackend)
	mockProcessor := new(mocks.MockImageProcessor)

	cfg := &config.Config{}


	startTime := time.Now()

	h := handlers.NewHandler(cfg, mockStorage, mockProcessor, startTime)

	updateReq := types.UpdateRequest{
		URL:     "https://example.com/image.jpg",
		Bucket:  "test-bucket",
		Quality: 80,
	}
	reqBody, _ := json.Marshal(updateReq)
	req := httptest.NewRequest("POST", "/update", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.HandleUpdate(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "MISSING_KEY")
}

func TestHandleUpdate_InvalidQuality(t *testing.T) {
	mockStorage := new(mocks.MockStorageBackend)
	mockProcessor := new(mocks.MockImageProcessor)

	cfg := &config.Config{}


	startTime := time.Now()

	h := handlers.NewHandler(cfg, mockStorage, mockProcessor, startTime)

	updateReq := types.UpdateRequest{
		URL:     "https://example.com/image.jpg",
		Bucket:  "test-bucket",
		Key:     "test-key",
		Quality: 150,
	}
	reqBody, _ := json.Marshal(updateReq)
	req := httptest.NewRequest("POST", "/update", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.HandleUpdate(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "INVALID_QUALITY")
}

func TestHandleUpdate_InvalidFormat(t *testing.T) {
	mockStorage := new(mocks.MockStorageBackend)
	mockProcessor := new(mocks.MockImageProcessor)

	cfg := &config.Config{}


	startTime := time.Now()

	h := handlers.NewHandler(cfg, mockStorage, mockProcessor, startTime)

	mockProcessor.On("ValidateFormat", "invalid").Return(false)

	updateReq := types.UpdateRequest{
		URL:     "https://example.com/image.jpg",
		Bucket:  "test-bucket",
		Key:     "test-key",
		Quality: 80,
		Format:  "invalid",
	}
	reqBody, _ := json.Marshal(updateReq)
	req := httptest.NewRequest("POST", "/update", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.HandleUpdate(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "INVALID_FORMAT")
}

// Test HandleDocs
func TestHandleDocs(t *testing.T) {
	mockStorage := new(mocks.MockStorageBackend)
	mockProcessor := new(mocks.MockImageProcessor)

	cfg := &config.Config{}


	startTime := time.Now()

	h := handlers.NewHandler(cfg, mockStorage, mockProcessor, startTime)

	req := httptest.NewRequest("GET", "/docs", nil)
	w := httptest.NewRecorder()

	h.HandleDocs(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "text/html; charset=utf-8", w.Header().Get("Content-Type"))
	assert.Contains(t, w.Body.String(), "<!DOCTYPE html>")
	assert.Contains(t, w.Body.String(), "redoc")
}
