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
	"github.com/sirupsen/logrus"
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
	// Create mocks
	mockStorage := new(mocks.MockStorageBackend)
	mockProcessor := new(mocks.MockImageProcessor)

	// Create config with required fields
	cfg := &config.Config{
		Processing: config.ProcessingConfig{
			MaxFileSizeMB:  10,
			DefaultQuality: 85,
			DefaultFormat:  "webp",
		},
	}
	logger := logrus.New()
	logger.SetOutput(io.Discard) // Disable logs during tests
	startTime := time.Now()

	// Create handler
	h := handlers.NewHandler(cfg, logger, mockStorage, mockProcessor, startTime)

	// Create test image data
	imageData := []byte{0xFF, 0xD8, 0xFF, 0xE0} // JPEG header

	req := httptest.NewRequest("POST", "/upload", bytes.NewReader(imageData))
	req.Header.Set("Content-Type", "image/jpeg")

	// Setup expectations
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

	// Execute
	w := httptest.NewRecorder()
	h.HandleUpload(w, req)

	// Assert
	assert.Equal(t, http.StatusCreated, w.Code) // Upload devuelve 201 Created
	mockProcessor.AssertExpectations(t)
	mockStorage.AssertExpectations(t)
}

func TestHandleDelivery_Success(t *testing.T) {
	// Create mocks
	mockStorage := new(mocks.MockStorageBackend)
	mockProcessor := new(mocks.MockImageProcessor)

	// Create config
	cfg := &config.Config{}
	cfg.Processing.MaxDimensions.Width = 4000
	cfg.Processing.MaxDimensions.Height = 4000
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	startTime := time.Now()

	// Create handler
	h := handlers.NewHandler(cfg, logger, mockStorage, mockProcessor, startTime)

	// Create test image data
	imageData := []byte{0xFF, 0xD8, 0xFF, 0xE0}
	expectedMetadata := &storage.ImageMetadata{
		ID:          "test-key",
		Format:      "jpeg",
		ContentType: "image/jpeg",
		Size:        int64(len(imageData)),
	}

	// Setup mock expectations
	mockStorage.On("Retrieve", mock.Anything, "test-key").
		Return(io.NopCloser(bytes.NewReader(imageData)), expectedMetadata, nil)

	// El delivery puede transformar la imagen si hay parámetros, necesitamos mockear Process
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

	// Create request with chi context for URL params
	req := httptest.NewRequest("GET", "/delivery/test-key", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "test-key")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	// Execute
	w := httptest.NewRecorder()
	h.HandleDelivery(w, req)

	// Assert
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "image/jpeg", w.Header().Get("Content-Type"))
	mockStorage.AssertExpectations(t)
}

func TestHandleList_Success(t *testing.T) {
	// Create mocks
	mockStorage := new(mocks.MockStorageBackend)
	mockProcessor := new(mocks.MockImageProcessor)

	// Create config
	cfg := &config.Config{}
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	startTime := time.Now()

	// Create handler
	h := handlers.NewHandler(cfg, logger, mockStorage, mockProcessor, startTime)

	// Create request
	req := httptest.NewRequest("GET", "/list", nil)

	// Setup expectations
	expectedResults := []storage.ListResult{
		{Key: "image1.jpg", Size: 1024},
		{Key: "image2.png", Size: 2048},
	}

	mockStorage.On("List", mock.Anything, "").
		Return(expectedResults, nil)

	// Execute
	w := httptest.NewRecorder()
	h.HandleList(w, req)

	// Assert
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "image1.jpg")
	assert.Contains(t, w.Body.String(), "image2.png")
	mockStorage.AssertExpectations(t)
}

func TestHandleDelete_Success(t *testing.T) {
	// Create mocks
	mockStorage := new(mocks.MockStorageBackend)
	mockProcessor := new(mocks.MockImageProcessor)

	// Create config
	cfg := &config.Config{}
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	startTime := time.Now()

	// Create handler
	h := handlers.NewHandler(cfg, logger, mockStorage, mockProcessor, startTime)

	// Create delete request
	deleteReq := types.DeleteRequest{
		Keys: []string{"test-key"},
	}
	reqBody, _ := json.Marshal(deleteReq)
	req := httptest.NewRequest("DELETE", "/delete", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")

	// Setup expectations
	mockStorage.On("Delete", mock.Anything, "test-key").
		Return(nil)

	// Execute
	w := httptest.NewRecorder()
	h.HandleDelete(w, req)

	// Assert
	assert.Equal(t, http.StatusOK, w.Code)
	mockStorage.AssertExpectations(t)

	// Verify response
	var response types.DeleteResponse
	json.Unmarshal(w.Body.Bytes(), &response)
	assert.True(t, response.Success)
	assert.Equal(t, 1, response.Count)
}

func TestHandleHealth_Success(t *testing.T) {
	// Create mocks
	mockStorage := new(mocks.MockStorageBackend)
	mockProcessor := new(mocks.MockImageProcessor)

	// Create config
	cfg := &config.Config{}
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	startTime := time.Now()

	// Create handler
	h := handlers.NewHandler(cfg, logger, mockStorage, mockProcessor, startTime)

	// Create request
	req := httptest.NewRequest("GET", "/health", nil)

	// Setup expectations
	mockStorage.On("Health", mock.Anything).Return(nil)
	mockStorage.On("GetStats", mock.Anything).Return(&storage.StorageStats{
		TotalImages: 10,
		TotalSize:   1024000,
	}, nil)

	// Execute
	w := httptest.NewRecorder()
	h.HandleHealth(w, req)

	// Assert
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
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	startTime := time.Now()

	h := handlers.NewHandler(cfg, logger, mockStorage, mockProcessor, startTime)

	req := httptest.NewRequest("POST", "/upload", bytes.NewReader([]byte{}))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=boundary")

	w := httptest.NewRecorder()
	h.HandleUpload(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	// El handler puede devolver INVALID_REQUEST o MISSING_FILE dependiendo del multipart parsing
	body := w.Body.String()
	assert.True(t, strings.Contains(body, "MISSING_FILE") || strings.Contains(body, "INVALID_REQUEST"))
}

func TestHandleDelivery_MissingID(t *testing.T) {
	mockStorage := new(mocks.MockStorageBackend)
	mockProcessor := new(mocks.MockImageProcessor)

	cfg := &config.Config{}
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	startTime := time.Now()

	h := handlers.NewHandler(cfg, logger, mockStorage, mockProcessor, startTime)

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
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	startTime := time.Now()

	h := handlers.NewHandler(cfg, logger, mockStorage, mockProcessor, startTime)

	req := httptest.NewRequest("DELETE", "/delete", bytes.NewReader([]byte("invalid json")))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.HandleDelete(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "INVALID_JSON")
}
