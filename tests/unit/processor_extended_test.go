package unit

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/birdple/falco/internal/processor"
)

// Test ResizeMode constants
func TestResizeMode_Constants(t *testing.T) {
	assert.Equal(t, processor.ResizeMode("cover"), processor.ResizeModeCover)
	assert.Equal(t, processor.ResizeMode("contain"), processor.ResizeModeContain)
	assert.Equal(t, processor.ResizeMode("fill"), processor.ResizeModeFill)
}

// Test ImageFormat constants
func TestImageFormat_Constants(t *testing.T) {
	assert.Equal(t, processor.ImageFormat("jpeg"), processor.FormatJPEG)
	assert.Equal(t, processor.ImageFormat("png"), processor.FormatPNG)
	assert.Equal(t, processor.ImageFormat("webp"), processor.FormatWebP)
}

// Test ProcessingParams structure
func TestProcessingParams_Structure(t *testing.T) {
	params := &processor.ProcessingParams{
		Width:   100,
		Height:  100,
		Quality: 80,
		Format:  "webp",
		Fit:     "cover",
	}

	assert.Equal(t, 100, params.Width)
	assert.Equal(t, 100, params.Height)
	assert.Equal(t, 80, params.Quality)
	assert.Equal(t, "webp", params.Format)
	assert.Equal(t, "cover", params.Fit)
}

func TestProcessingParams_AdvancedOptions(t *testing.T) {
	params := &processor.ProcessingParams{
		CropX:      10,
		CropY:      20,
		CropW:      100,
		CropH:      100,
		Rotate:     90.0,
		Flip:       "horizontal",
		Brightness: 10.0,
		Contrast:   5.0,
		Gamma:      1.2,
		Saturation: 1.5,
		Blur:       2.0,
		Sharpen:    1.5,
	}

	assert.Equal(t, 10, params.CropX)
	assert.Equal(t, 20, params.CropY)
	assert.Equal(t, 100, params.CropW)
	assert.Equal(t, 100, params.CropH)
	assert.Equal(t, 90.0, params.Rotate)
	assert.Equal(t, "horizontal", params.Flip)
	assert.Equal(t, 10.0, params.Brightness)
	assert.Equal(t, 5.0, params.Contrast)
	assert.Equal(t, 1.2, params.Gamma)
	assert.Equal(t, 1.5, params.Saturation)
	assert.Equal(t, 2.0, params.Blur)
	assert.Equal(t, 1.5, params.Sharpen)
}

// Test ProcessedImage structure
func TestProcessedImage_Structure(t *testing.T) {
	img := &processor.ProcessedImage{
		CacheKey: "test-key",
		Cached:   true,
	}

	assert.Equal(t, "test-key", img.CacheKey)
	assert.True(t, img.Cached)
}

// Test ImageMetadata structure
func TestImageMetadata_Structure(t *testing.T) {
	meta := &processor.ImageMetadata{
		ID:           "test-id",
		OriginalName: "test.jpg",
		Format:       "jpeg",
		Size:         1024,
		Width:        800,
		Height:       600,
		ContentType:  "image/jpeg",
	}

	assert.Equal(t, "test-id", meta.ID)
	assert.Equal(t, "test.jpg", meta.OriginalName)
	assert.Equal(t, "jpeg", meta.Format)
	assert.Equal(t, int64(1024), meta.Size)
	assert.Equal(t, 800, meta.Width)
	assert.Equal(t, 600, meta.Height)
	assert.Equal(t, "image/jpeg", meta.ContentType)
}
