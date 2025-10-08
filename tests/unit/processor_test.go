package unit

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ivangsm/imagine/internal/processor"
)

func TestImageProcessor_Process(t *testing.T) {
	// Create a test image
	testImg := createTestImage(100, 100)

	// Encode to PNG
	var buf bytes.Buffer
	err := png.Encode(&buf, testImg)
	require.NoError(t, err)

	// Create processor
	proc := processor.NewImageProcessor(10, 85, processor.FormatWebP, 1000, 1000)

	// Test basic processing
	params := &processor.ProcessingParams{
		Format: "webp",
	}

	result, err := proc.Process(context.Background(), &buf, params)
	require.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, "webp", result.Metadata.Format)
	assert.True(t, result.Metadata.Size > 0)
}

func TestImageProcessor_ValidateFormat(t *testing.T) {
	proc := processor.NewImageProcessor(10, 85, processor.FormatWebP, 1000, 1000)

	tests := []struct {
		format   string
		expected bool
	}{
		{"jpeg", true},
		{"png", true},
		{"webp", true},
		{"gif", false},
		{"bmp", false},
		{"", false},
	}

	for _, test := range tests {
		t.Run(test.format, func(t *testing.T) {
			result := proc.ValidateFormat(test.format)
			assert.Equal(t, test.expected, result)
		})
	}
}

func TestImageProcessor_SupportedFormats(t *testing.T) {
	proc := processor.NewImageProcessor(10, 85, processor.FormatWebP, 1000, 1000)

	formats := proc.SupportedFormats()
	assert.Contains(t, formats, "jpeg")
	assert.Contains(t, formats, "png")
	assert.Contains(t, formats, "webp")
	assert.Len(t, formats, 3)
}

func TestProcessingParams_Advanced(t *testing.T) {
	params := &processor.ProcessingParams{
		Width:      400,
		Height:     300,
		Quality:    90,
		Format:     "webp",
		CropX:      10,
		CropY:      20,
		CropW:      200,
		CropH:      150,
		Rotate:     90,
		Flip:       "horizontal",
		Brightness: 20,
		Contrast:   30,
		Gamma:      1.2,
		Saturation: 10,
		Blur:       0.5,
		Sharpen:    25,
	}

	// Test parameter values
	assert.Equal(t, 400, params.Width)
	assert.Equal(t, 300, params.Height)
	assert.Equal(t, 90, params.Quality)
	assert.Equal(t, "webp", params.Format)
	assert.Equal(t, 10, params.CropX)
	assert.Equal(t, 20, params.CropY)
	assert.Equal(t, 200, params.CropW)
	assert.Equal(t, 150, params.CropH)
	assert.Equal(t, float64(90), params.Rotate)
	assert.Equal(t, "horizontal", params.Flip)
	assert.Equal(t, float64(20), params.Brightness)
	assert.Equal(t, float64(30), params.Contrast)
	assert.Equal(t, 1.2, params.Gamma)
	assert.Equal(t, float64(10), params.Saturation)
	assert.Equal(t, 0.5, params.Blur)
	assert.Equal(t, float64(25), params.Sharpen)
}

func TestIsValidFormat(t *testing.T) {
	tests := []struct {
		format   string
		expected bool
	}{
		{"jpeg", true},
		{"png", true},
		{"webp", true},
		{"JPEG", false}, // Case sensitive
		{"gif", false},
		{"", false},
	}

	for _, test := range tests {
		t.Run(test.format, func(t *testing.T) {
			result := processor.IsValidFormat(test.format)
			assert.Equal(t, test.expected, result)
		})
	}
}

func TestGetDefaultQuality(t *testing.T) {
	tests := []struct {
		format   processor.ImageFormat
		expected int
	}{
		{processor.FormatJPEG, 85},
		{processor.FormatPNG, 100},
		{processor.FormatWebP, 85},
	}

	for _, test := range tests {
		t.Run(string(test.format), func(t *testing.T) {
			result := processor.GetDefaultQuality(test.format)
			assert.Equal(t, test.expected, result)
		})
	}
}

func TestGetContentType(t *testing.T) {
	tests := []struct {
		format   processor.ImageFormat
		expected string
	}{
		{processor.FormatJPEG, "image/jpeg"},
		{processor.FormatPNG, "image/png"},
		{processor.FormatWebP, "image/webp"},
	}

	for _, test := range tests {
		t.Run(string(test.format), func(t *testing.T) {
			result := processor.GetContentType(test.format)
			assert.Equal(t, test.expected, result)
		})
	}
}

// Helper function to create a test image
func createTestImage(width, height int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, width, height))

	// Fill with a gradient for testing
	for y := range height {
		for x := range width {
			r := uint8((x * 255) / width)
			g := uint8((y * 255) / height)
			b := uint8(128)
			a := uint8(255)
			img.Set(x, y, color.RGBA{r, g, b, a})
		}
	}

	return img
}
