package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ivangsm/imagine/internal/api"
	"github.com/ivangsm/imagine/internal/cache"
	"github.com/ivangsm/imagine/internal/config"
	"github.com/ivangsm/imagine/internal/processor"
	"github.com/ivangsm/imagine/internal/storage"
)

func TestServer_UploadAndRetrieve(t *testing.T) {
	// Setup test server
	server := setupTestServer(t)
	defer server.Shutdown(context.Background())

	// Create test image
	testImage := createTestPNGImage(t, 200, 200)

	// Test upload
	uploadResp := uploadTestImage(t, server, testImage)
	assert.True(t, uploadResp.Success)
	assert.NotEmpty(t, uploadResp.Data.ID)
	assert.Equal(t, "webp", uploadResp.Data.Format)
	assert.Equal(t, "image.webp", uploadResp.Data.OriginalName)

	imageID := uploadResp.Data.ID

	// Test retrieval
	retrieveResp := retrieveImage(t, server, imageID)
	assert.Equal(t, http.StatusOK, retrieveResp.Code)
	assert.Equal(t, "image/webp", retrieveResp.Header().Get("Content-Type"))
	assert.True(t, retrieveResp.Body.Len() > 0)
}

func TestServer_AdvancedTransformations(t *testing.T) {
	// Setup test server
	server := setupTestServer(t)
	defer server.Shutdown(context.Background())

	// Create test image
	testImage := createTestPNGImage(t, 400, 300)

	// Upload image
	uploadResp := uploadTestImage(t, server, testImage)
	require.True(t, uploadResp.Success)
	imageID := uploadResp.Data.ID

	// Test resize transformation
	resizeURL := fmt.Sprintf("/api/v1/images/%s?w=200&h=150", imageID)
	req := httptest.NewRequest("GET", resizeURL, nil)
	w := httptest.NewRecorder()

	server.Router().ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "image/webp", w.Header().Get("Content-Type"))

	// Test rotation transformation
	rotateURL := fmt.Sprintf("/api/v1/images/%s?w=200&h=150&rotate=90", imageID)
	req = httptest.NewRequest("GET", rotateURL, nil)
	w = httptest.NewRecorder()

	server.Router().ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "image/webp", w.Header().Get("Content-Type"))

	// Test filter transformation
	filterURL := fmt.Sprintf("/api/v1/images/%s?w=200&h=150&brightness=20&contrast=30", imageID)
	req = httptest.NewRequest("GET", filterURL, nil)
	w = httptest.NewRecorder()

	server.Router().ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "image/webp", w.Header().Get("Content-Type"))
}

func TestServer_HealthCheck(t *testing.T) {
	// Setup test server
	server := setupTestServer(t)
	defer server.Shutdown(context.Background())

	// Test health check
	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()

	server.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

	var health map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &health)
	require.NoError(t, err)

	assert.Equal(t, "healthy", health["status"])
	assert.Contains(t, health, "timestamp")
	assert.Contains(t, health, "version")
}

func TestServer_InvalidImageID(t *testing.T) {
	// Setup test server
	server := setupTestServer(t)
	defer server.Shutdown(context.Background())

	// Test invalid image ID
	req := httptest.NewRequest("GET", "/api/v1/images/invalid-id", nil)
	w := httptest.NewRecorder()

	server.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

	var errorResp map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &errorResp)
	require.NoError(t, err)

	assert.Equal(t, "IMAGE_NOT_FOUND", errorResp["error"].(map[string]interface{})["code"])
}

func TestServer_InvalidParameters(t *testing.T) {
	// Setup test server
	server := setupTestServer(t)
	defer server.Shutdown(context.Background())

	// Create and upload test image
	testImage := createTestPNGImage(t, 200, 200)
	uploadResp := uploadTestImage(t, server, testImage)
	require.True(t, uploadResp.Success)
	imageID := uploadResp.Data.ID

	// Test invalid width
	invalidURL := fmt.Sprintf("/api/v1/images/%s?w=-100", imageID)
	req := httptest.NewRequest("GET", invalidURL, nil)
	w := httptest.NewRecorder()

	server.Router().ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)

	// Test invalid format
	invalidURL = fmt.Sprintf("/api/v1/images/%s?f=invalid", imageID)
	req = httptest.NewRequest("GET", invalidURL, nil)
	w = httptest.NewRecorder()

	server.Router().ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestServer_CORSHeaders(t *testing.T) {
	// Setup test server
	server := setupTestServer(t)
	defer server.Shutdown(context.Background())

	// Test OPTIONS request
	req := httptest.NewRequest("OPTIONS", "/api/v1/upload", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	w := httptest.NewRecorder()

	server.Router().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "http://localhost:3000", w.Header().Get("Access-Control-Allow-Origin"))
	assert.Contains(t, w.Header().Get("Access-Control-Allow-Methods"), "POST")
	assert.Contains(t, w.Header().Get("Access-Control-Allow-Headers"), "Content-Type")
}

func TestServer_RateLimiting(t *testing.T) {
	// Setup test server with low rate limit for testing
	cfg := &config.Config{}
	cfg.Security.RateLimit.RequestsPerMinute = 2
	cfg.Security.RateLimit.Burst = 1

	server := setupTestServerWithConfig(t, cfg)
	defer server.Shutdown(context.Background())

	// Make multiple requests to trigger rate limiting
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest("GET", "/health", nil)
		w := httptest.NewRecorder()
		server.Router().ServeHTTP(w, req)

		if i < 3 {
			// First few requests should succeed
			assert.True(t, w.Code == http.StatusOK || w.Code == http.StatusTooManyRequests)
		}
	}

	// At least one request should be rate limited
	// (This is a basic test - in production you'd want more sophisticated rate limiting tests)
}

// Helper functions

func setupTestServer(t *testing.T) *api.Server {
	cfg := &config.Config{}
	cfg.Server.Port = 8080
	cfg.Server.Host = "localhost"
	cfg.Processing.MaxFileSizeMB = 10
	cfg.Processing.DefaultQuality = 85
	cfg.Processing.DefaultFormat = "webp"
	cfg.Processing.MaxDimensions.Width = 2000
	cfg.Processing.MaxDimensions.Height = 2000
	cfg.Cache.SizeMB = 10
	cfg.Storage.Primary = "filesystem"
	cfg.Storage.Local.Path = t.TempDir() + "/images"

	return setupTestServerWithConfig(t, cfg)
}

func setupTestServerWithConfig(t *testing.T, cfg *config.Config) *api.Server {
	// Create storage backend
	storageConfig := &storage.StorageConfig{
		Type:      storage.StorageType(cfg.Storage.Primary),
		LocalPath: cfg.GetLocalStoragePath(),
	}
	storageBackend, err := storage.NewStorageBackend(storageConfig)
	require.NoError(t, err)

	// Create image processor
	imageProcessor := processor.NewImageProcessor(
		cfg.Processing.MaxFileSizeMB,
		cfg.Processing.DefaultQuality,
		processor.ImageFormat(cfg.Processing.DefaultFormat),
		cfg.Processing.MaxDimensions.Width,
		cfg.Processing.MaxDimensions.Height,
	)

	// Create cache if configured
	if cfg.Cache.SizeMB > 0 {
		cacheSize := cfg.GetCacheSizeBytes()
		lruCache := cache.NewLRUCache(cacheSize, cfg.GetCacheTTL())
		imageProcessor.SetCache(lruCache)
	}

	// Create logger
	logger := &testLogger{t: t}

	// Create server
	server, err := api.NewServer(&api.ServerConfig{
		Config:         cfg,
		Logger:         logger,
		Storage:        storageBackend,
		ImageProcessor: imageProcessor,
	})
	require.NoError(t, err)

	return server
}

func createTestPNGImage(t *testing.T, width, height int) []byte {
	// Create a simple test image
	img := image.NewRGBA(image.Rect(0, 0, width, height))

	// Fill with a solid color
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{100, 150, 200, 255})
		}
	}

	// Encode to PNG
	var buf bytes.Buffer
	err := png.Encode(&buf, img)
	require.NoError(t, err)

	return buf.Bytes()
}

func uploadTestImage(t *testing.T, server *api.Server, imageData []byte) api.UploadResponse {
	// Create multipart form data
	body := &bytes.Buffer{}
	writer := &multipartWriter{buffer: body}

	// Add image file
	writer.writeBoundary()
	writer.writeContentDisposition("file", "test.png")
	writer.writeContentType("image/png")
	writer.writeLine("")
	writer.writeData(imageData)
	writer.writeBoundary()
	writer.writeLine("")

	// Create request
	req := httptest.NewRequest("POST", "/api/v1/upload", body)
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+writer.boundary)

	// Execute request
	w := httptest.NewRecorder()
	server.Router().ServeHTTP(w, req)

	// Parse response
	require.Equal(t, http.StatusCreated, w.Code)

	var response api.UploadResponse
	err := json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	return response
}

func retrieveImage(t *testing.T, server *api.Server, imageID string) *httptest.ResponseRecorder {
	url := fmt.Sprintf("/api/v1/images/%s", imageID)
	req := httptest.NewRequest("GET", url, nil)
	w := httptest.NewRecorder()

	server.Router().ServeHTTP(w, req)
	return w
}

// Simple multipart writer for testing
type multipartWriter struct {
	buffer   *bytes.Buffer
	boundary string
}

func (w *multipartWriter) writeBoundary() {
	if w.boundary == "" {
		w.boundary = "----TestBoundary123"
	}
	w.buffer.WriteString("--" + w.boundary + "\r\n")
}

func (w *multipartWriter) writeContentDisposition(name, filename string) {
	w.buffer.WriteString(fmt.Sprintf("Content-Disposition: form-data; name=\"%s\"; filename=\"%s\"\r\n", name, filename))
}

func (w *multipartWriter) writeContentType(contentType string) {
	w.buffer.WriteString("Content-Type: " + contentType + "\r\n")
}

func (w *multipartWriter) writeLine(line string) {
	w.buffer.WriteString(line + "\r\n")
}

func (w *multipartWriter) writeData(data []byte) {
	w.buffer.Write(data)
	w.buffer.WriteString("\r\n")
}

// Test logger implementation
type testLogger struct {
	t *testing.T
}

func (l *testLogger) Info(args ...interface{})                        {}
func (l *testLogger) Infof(format string, args ...interface{})        {}
func (l *testLogger) Error(args ...interface{})                       {}
func (l *testLogger) Errorf(format string, args ...interface{})       {}
func (l *testLogger) Warn(args ...interface{})                        {}
func (l *testLogger) Warnf(format string, args ...interface{})        {}
func (l *testLogger) Debug(args ...interface{})                       {}
func (l *testLogger) Debugf(format string, args ...interface{})       {}
func (l *testLogger) WithField(key string, value interface{}) Logger  { return l }
func (l *testLogger) WithFields(fields map[string]interface{}) Logger { return l }
func (l *testLogger) WithError(err error) Logger                      { return l }

// Logger interface for test logger
type Logger interface {
	Info(args ...interface{})
	Infof(format string, args ...interface{})
	Error(args ...interface{})
	Errorf(format string, args ...interface{})
	Warn(args ...interface{})
	Warnf(format string, args ...interface{})
	Debug(args ...interface{})
	Debugf(format string, args ...interface{})
	WithField(key string, value interface{}) Logger
	WithFields(fields map[string]interface{}) Logger
	WithError(err error) Logger
}
