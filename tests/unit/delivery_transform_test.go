package unit

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/birdple/falco/internal/api/handlers"
	"github.com/birdple/falco/internal/processor"
	"github.com/birdple/falco/internal/storage"
	"github.com/birdple/falco/tests/mocks"
)

// transformRouter wires a delivery handler whose processor records the
// parameters it was handed, which is what most of these tests assert on: the
// point is not that the request returned 200 but that the transformation
// actually reached the processor.
func transformRouter(t *testing.T, captured *processor.ProcessingParams) (*chi.Mux, *mocks.MockStorageBackend) {
	t.Helper()

	imageData := []byte{0xFF, 0xD8, 0xFF, 0xE0}
	mockStorage := new(mocks.MockStorageBackend)
	mockProcessor := new(mocks.MockImageProcessor)

	// A fresh reader per call: the watermark path retrieves a second object,
	// and handing both calls the same already-drained reader would make the
	// overlay silently empty.
	mockStorage.On("Retrieve", mock.Anything, mock.Anything).
		Return(func(_ context.Context, _ string) io.ReadCloser {
			return io.NopCloser(bytes.NewReader(imageData))
		},
			&storage.ImageMetadata{ID: "img", Format: "jpeg", ContentType: "image/jpeg", Size: int64(len(imageData))},
			nil).Maybe()

	mockProcessor.On("GenerateCacheKey", mock.Anything, mock.Anything).Return("ck").Maybe()
	mockProcessor.On("GetFromCache", mock.Anything).Return([]byte(nil), false).Maybe()
	mockProcessor.On("ValidateFormat", mock.Anything).Return(true).Maybe()
	mockProcessor.On("Process", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			if p, ok := args.Get(2).(*processor.ProcessingParams); ok && captured != nil {
				*captured = *p
			}
		}).
		Return(&processor.ProcessedImage{
			Data: io.NopCloser(bytes.NewReader(imageData)),
			Metadata: &processor.ImageMetadata{
				Format: "webp", ContentType: "image/webp", Size: int64(len(imageData)),
			},
		}, nil).Maybe()

	h := handlers.NewHandler(testConfig(), mockStorage, mockProcessor, time.Now())
	r := chi.NewRouter()
	r.Get("/api/v1/images/*", h.HandleDelivery)
	return r, mockStorage
}

func get(t *testing.T, router *chi.Mux, target string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil))
	return w
}

// errorCode pulls the machine-readable code out of an error body. Tests assert
// on it rather than on the message, which is the contract clients are told to
// rely on.
func errorCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	return body.Error.Code
}

// TestDelivery_TransformParamsReachProcessor is the test that would have caught
// the original defect: the parameters were documented, the pipeline implemented
// them, and nothing carried them from the query string to the processor.
func TestDelivery_TransformParamsReachProcessor(t *testing.T) {
	var got processor.ProcessingParams
	router, _ := transformRouter(t, &got)

	w := get(t, router, "/api/v1/images/img?w=200&rotate=90&flip=vertical"+
		"&crop_x=5&crop_y=6&crop_w=100&crop_h=80"+
		"&brightness=20&contrast=-10&gamma=2.2&saturation=150&hue=-45&blur=3&sharpen=40")

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, 200, got.Width)
	assert.InDelta(t, 90, got.Rotate, 0.001)
	assert.Equal(t, "vertical", got.Flip)
	assert.Equal(t, 5, got.CropX)
	assert.Equal(t, 6, got.CropY)
	assert.Equal(t, 100, got.CropW)
	assert.Equal(t, 80, got.CropH)
	assert.InDelta(t, 20, got.Brightness, 0.001)
	assert.InDelta(t, -10, got.Contrast, 0.001)
	assert.InDelta(t, 2.2, got.Gamma, 0.001)
	assert.InDelta(t, 150, got.Saturation, 0.001)
	assert.Equal(t, -45, got.Hue)
	assert.InDelta(t, 3, got.Blur, 0.001)
	assert.InDelta(t, 40, got.Sharpen, 0.001)
}

// TestDelivery_GeometryRejections covers the parameters that must fail rather
// than fall back: serving a different image than the one asked for is worse
// than an error.
func TestDelivery_GeometryRejections(t *testing.T) {
	cases := []struct {
		name  string
		query string
		code  string
	}{
		{"crop sin tamaño", "crop_x=10", "INVALID_CROP"},
		{"crop sin alto", "crop_w=100", "INVALID_CROP"},
		{"crop de ancho cero", "crop_w=0&crop_h=10", "INVALID_CROP"},
		{"crop de origen negativo", "crop_x=-1&crop_w=10&crop_h=10", "INVALID_CROP"},
		{"crop no numérico", "crop_w=abc&crop_h=10", "INVALID_CROP"},
		{"rotación fuera de rango", "rotate=400", "INVALID_ROTATE"},
		{"rotación no numérica", "rotate=abc", "INVALID_ROTATE"},
		{"flip desconocido", "flip=diagonal", "INVALID_FLIP"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			router, _ := transformRouter(t, nil)
			w := get(t, router, "/api/v1/images/img?"+tc.query)
			assert.Equal(t, http.StatusBadRequest, w.Code)
			assert.Equal(t, tc.code, errorCode(t, w))
		})
	}
}

// TestDelivery_CosmeticParamsFallBack covers the other half of the contract: a
// malformed cosmetic value leaves its default in place and still serves the
// image.
func TestDelivery_CosmeticParamsFallBack(t *testing.T) {
	cases := []struct {
		name  string
		query string
	}{
		{"brillo fuera de rango", "brightness=9999"},
		{"saturación no numérica", "saturation=abc"},
		{"hue fuera de rango", "hue=999"},
		{"gamma negativa", "gamma=-1"},
		{"desenfoque fuera de rango", "blur=1000"},
		{"opacidad fuera de rango", "wm_opacity=5"},
		{"posición desconocida", "wm_position=diagonal"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got processor.ProcessingParams
			router, _ := transformRouter(t, &got)
			w := get(t, router, "/api/v1/images/img?w=100&"+tc.query)

			require.Equal(t, http.StatusOK, w.Code)
			assert.Zero(t, got.Brightness)
			assert.Zero(t, got.Saturation)
			assert.Zero(t, got.Hue)
			assert.Zero(t, got.Gamma)
			assert.Zero(t, got.Blur)
			assert.Zero(t, got.WatermarkOpacity)
			assert.Empty(t, got.WatermarkPosition)
		})
	}
}

// TestDelivery_WatermarkFromStorage checks the whole chain for an overlay that
// lives in Falco's own storage: parsed into a source, read through the same
// backend as the image, and handed to the processor as bytes.
func TestDelivery_WatermarkFromStorage(t *testing.T) {
	var got processor.ProcessingParams
	router, mockStorage := transformRouter(t, &got)

	w := get(t, router, "/api/v1/images/img?w=200&wm=logo-a&wm_position=center&wm_opacity=0.4&wm_scale=0.3")

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "id:logo-a", got.WatermarkSource)
	assert.Equal(t, "center", got.WatermarkPosition)
	assert.InDelta(t, 0.4, got.WatermarkOpacity, 0.001)
	assert.InDelta(t, 0.3, got.WatermarkScale, 0.001)
	// The overlay bytes have to reach the processor: the source alone would
	// give a cache key for a watermark that never gets composited.
	assert.NotEmpty(t, got.WatermarkImage)
	mockStorage.AssertCalled(t, "Retrieve", mock.Anything, "logo-a")
}

// TestDelivery_WatermarkRejections: every one of these must fail loudly. An
// image served without the watermark it was asked for is indistinguishable
// from one that worked.
func TestDelivery_WatermarkRejections(t *testing.T) {
	cases := []struct {
		name   string
		query  string
		status int
		code   string
	}{
		{"ambas fuentes", "wm=logo-b&wm_url=https://example.com/l.png", http.StatusBadRequest, "INVALID_WATERMARK"},
		{"id con traversal", "wm=../../etc/passwd", http.StatusBadRequest, "INVALID_WATERMARK"},
		{"url externa sin allowlist", "wm_url=https%3A%2F%2Fexample.com%2Fl.png", http.StatusForbidden, "WATERMARK_HOST_NOT_ALLOWED"},
		{"url relativa", "wm_url=%2Flocal%2Fl.png", http.StatusForbidden, "WATERMARK_HOST_NOT_ALLOWED"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			router, _ := transformRouter(t, nil)
			w := get(t, router, "/api/v1/images/img?w=100&"+tc.query)
			assert.Equal(t, tc.status, w.Code)
			assert.Equal(t, tc.code, errorCode(t, w))
		})
	}
}

// TestDelivery_WatermarkCountsAsTransformation: without the watermark in
// wantsTransformation, a request carrying only wm= would take the raw streaming
// path and be served with no overlay at all.
func TestDelivery_WatermarkCountsAsTransformation(t *testing.T) {
	var got processor.ProcessingParams
	router, _ := transformRouter(t, &got)

	w := get(t, router, "/api/v1/images/img?wm=logo-c")

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "id:logo-c", got.WatermarkSource)
}
