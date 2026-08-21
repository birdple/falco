package unit

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/birdple/falco/internal/api/handlers"
	"github.com/birdple/falco/internal/processor"
	"github.com/birdple/falco/internal/storage"
	"github.com/birdple/falco/tests/mocks"
)

// makeDeliveryHandler is a test helper that wires up a Handler with mocks and
// returns a chi router with the /images/* route registered.
func makeDeliveryHandler(t *testing.T, mockStorage *mocks.MockStorageBackend, mockProcessor *mocks.MockImageProcessor) *chi.Mux {
	t.Helper()
	cfg := testConfig()

	h := handlers.NewHandler(cfg, mockStorage, mockProcessor, time.Now())

	r := chi.NewRouter()
	r.Get("/api/v1/images/*", h.HandleDelivery)
	return r
}

// TestHandleDelivery_ExtWebp checks that /images/abc123.webp strips the
// extension and uses "webp" as the format default.
func TestHandleDelivery_ExtWebp(t *testing.T) {
	mockStorage := new(mocks.MockStorageBackend)
	mockProcessor := new(mocks.MockImageProcessor)

	imageData := []byte{0xFF, 0xD8, 0xFF, 0xE0}
	storageKey := "abc123"

	mockStorage.On("Retrieve", mock.Anything, storageKey).
		Return(io.NopCloser(bytes.NewReader(imageData)), &storage.ImageMetadata{
			ID:          storageKey,
			Format:      "jpeg",
			ContentType: "image/jpeg",
			Size:        int64(len(imageData)),
		}, nil)

	mockProcessor.On("GenerateCacheKey", mock.Anything, mock.Anything).Return("ck-webp").Maybe()
	mockProcessor.On("GetFromCache", mock.Anything).Return([]byte(nil), false).Maybe()
	mockProcessor.On("ValidateFormat", mock.Anything).Return(true).Maybe()
	mockProcessor.On("Process", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(&processor.ProcessedImage{
			Data: io.NopCloser(bytes.NewReader(imageData)),
			Metadata: &processor.ImageMetadata{
				Format:      "webp",
				ContentType: "image/webp",
				Size:        int64(len(imageData)),
			},
		}, nil).Maybe()

	router := makeDeliveryHandler(t, mockStorage, mockProcessor)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/images/abc123.webp", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	mockStorage.AssertCalled(t, "Retrieve", mock.Anything, storageKey)
}

// TestHandleDelivery_ExtJpg checks that /images/abc123.jpg maps to "jpeg".
func TestHandleDelivery_ExtJpg(t *testing.T) {
	mockStorage := new(mocks.MockStorageBackend)
	mockProcessor := new(mocks.MockImageProcessor)

	imageData := []byte{0xFF, 0xD8, 0xFF, 0xE0}
	storageKey := "abc123"

	mockStorage.On("Retrieve", mock.Anything, storageKey).
		Return(io.NopCloser(bytes.NewReader(imageData)), &storage.ImageMetadata{
			ID:          storageKey,
			Format:      "jpeg",
			ContentType: "image/jpeg",
			Size:        int64(len(imageData)),
		}, nil)

	mockProcessor.On("GenerateCacheKey", mock.Anything, mock.Anything).Return("ck-jpg").Maybe()
	mockProcessor.On("GetFromCache", mock.Anything).Return([]byte(nil), false).Maybe()
	mockProcessor.On("ValidateFormat", mock.Anything).Return(true).Maybe()
	mockProcessor.On("Process", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(&processor.ProcessedImage{
			Data: io.NopCloser(bytes.NewReader(imageData)),
			Metadata: &processor.ImageMetadata{
				Format:      "jpeg",
				ContentType: "image/jpeg",
				Size:        int64(len(imageData)),
			},
		}, nil).Maybe()

	router := makeDeliveryHandler(t, mockStorage, mockProcessor)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/images/abc123.jpg", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	mockStorage.AssertCalled(t, "Retrieve", mock.Anything, storageKey)
}

// TestHandleDelivery_ExtInvalid checks that /images/abc123.pdf returns 400.
func TestHandleDelivery_ExtInvalid(t *testing.T) {
	mockStorage := new(mocks.MockStorageBackend)
	mockProcessor := new(mocks.MockImageProcessor)

	router := makeDeliveryHandler(t, mockStorage, mockProcessor)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/images/abc123.pdf", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "INVALID_ID")
	mockStorage.AssertNotCalled(t, "Retrieve", mock.Anything, mock.Anything)
}

// TestHandleDelivery_NoExt checks that /images/abc123 (no extension) still
// works exactly as before — no format is inferred from the path.
func TestHandleDelivery_NoExt(t *testing.T) {
	mockStorage := new(mocks.MockStorageBackend)
	mockProcessor := new(mocks.MockImageProcessor)

	imageData := []byte{0xFF, 0xD8, 0xFF, 0xE0}
	storageKey := "abc123"

	mockStorage.On("Retrieve", mock.Anything, storageKey).
		Return(io.NopCloser(bytes.NewReader(imageData)), &storage.ImageMetadata{
			ID:          storageKey,
			Format:      "jpeg",
			ContentType: "image/jpeg",
			Size:        int64(len(imageData)),
		}, nil)

	mockProcessor.On("GenerateCacheKey", mock.Anything, mock.Anything).Return("ck-noext").Maybe()
	mockProcessor.On("GetFromCache", mock.Anything).Return([]byte(nil), false).Maybe()
	mockProcessor.On("ValidateFormat", mock.Anything).Return(true).Maybe()
	mockProcessor.On("Process", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(&processor.ProcessedImage{
			Data: io.NopCloser(bytes.NewReader(imageData)),
			Metadata: &processor.ImageMetadata{
				Format:      "jpeg",
				ContentType: "image/jpeg",
				Size:        int64(len(imageData)),
			},
		}, nil).Maybe()

	router := makeDeliveryHandler(t, mockStorage, mockProcessor)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/images/abc123", nil)

	// Inject chi route context with "*" param to simulate production routing.
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("*", "abc123")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	mockStorage.AssertCalled(t, "Retrieve", mock.Anything, storageKey)
}

// TestHandleDelivery_ExtWithDirectory cubre el caso en que la extensión viene
// sobre un id que además trae directorio: /images/fotos/abc123.webp tiene que
// resolverse contra la clave "fotos/abc123", sin la extensión.
func TestHandleDelivery_ExtWithDirectory(t *testing.T) {
	mockStorage := new(mocks.MockStorageBackend)
	mockProcessor := new(mocks.MockImageProcessor)

	imageData := []byte{0xFF, 0xD8, 0xFF, 0xE0}
	storageKey := "fotos/abc123"

	mockStorage.On("Retrieve", mock.Anything, storageKey).
		Return(io.NopCloser(bytes.NewReader(imageData)), &storage.ImageMetadata{
			ID:          storageKey,
			Format:      "jpeg",
			ContentType: "image/jpeg",
			Size:        int64(len(imageData)),
		}, nil)

	mockProcessor.On("GenerateCacheKey", mock.Anything, mock.Anything).Return("ck-dir").Maybe()
	mockProcessor.On("GetFromCache", mock.Anything).Return([]byte(nil), false).Maybe()
	mockProcessor.On("ValidateFormat", mock.Anything).Return(true).Maybe()
	mockProcessor.On("Process", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(&processor.ProcessedImage{
			Data: io.NopCloser(bytes.NewReader(imageData)),
			Metadata: &processor.ImageMetadata{
				Format:      "webp",
				ContentType: "image/webp",
				Size:        int64(len(imageData)),
			},
		}, nil).Maybe()

	router := makeDeliveryHandler(t, mockStorage, mockProcessor)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/images/fotos/abc123.webp", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	mockStorage.AssertCalled(t, "Retrieve", mock.Anything, storageKey)
}

// TestHandleDelivery_DotInDirectoryOnly cubre el punto que vive en el
// directorio y no en el id: "v1.2/abc123" NO trae extensión, así que no se le
// recorta nada y no se rechaza. Buscar el último punto sobre el path completo
// en vez de sobre el último segmento rompe justo este caso.
func TestHandleDelivery_DotInDirectoryOnly(t *testing.T) {
	mockStorage := new(mocks.MockStorageBackend)
	mockProcessor := new(mocks.MockImageProcessor)

	imageData := []byte{0xFF, 0xD8, 0xFF, 0xE0}
	storageKey := "v1.2/abc123"

	mockStorage.On("Retrieve", mock.Anything, storageKey).
		Return(io.NopCloser(bytes.NewReader(imageData)), &storage.ImageMetadata{
			ID:          storageKey,
			Format:      "jpeg",
			ContentType: "image/jpeg",
			Size:        int64(len(imageData)),
		}, nil)

	mockProcessor.On("GenerateCacheKey", mock.Anything, mock.Anything).Return("ck-dotdir").Maybe()
	mockProcessor.On("GetFromCache", mock.Anything).Return([]byte(nil), false).Maybe()
	mockProcessor.On("ValidateFormat", mock.Anything).Return(true).Maybe()
	mockProcessor.On("Process", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(&processor.ProcessedImage{
			Data: io.NopCloser(bytes.NewReader(imageData)),
			Metadata: &processor.ImageMetadata{
				Format:      "webp",
				ContentType: "image/webp",
				Size:        int64(len(imageData)),
			},
		}, nil).Maybe()

	router := makeDeliveryHandler(t, mockStorage, mockProcessor)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/images/v1.2/abc123", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	mockStorage.AssertCalled(t, "Retrieve", mock.Anything, storageKey)
}
