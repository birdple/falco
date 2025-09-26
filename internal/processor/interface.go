package processor

import (
	"context"
	"io"
	"time"
)

// ProcessingParams holds parameters for image processing
type ProcessingParams struct {
	Width   int    `json:"width,omitempty"`
	Height  int    `json:"height,omitempty"`
	Quality int    `json:"quality,omitempty"`
	Format  string `json:"format,omitempty"`
	Fit     string `json:"fit,omitempty"` // "cover", "contain", "fill"

	// Advanced transformations
	CropX  int     `json:"crop_x,omitempty"` // Crop start X position
	CropY  int     `json:"crop_y,omitempty"` // Crop start Y position
	CropW  int     `json:"crop_w,omitempty"` // Crop width
	CropH  int     `json:"crop_h,omitempty"` // Crop height
	Rotate float64 `json:"rotate,omitempty"` // Rotation angle in degrees
	Flip   string  `json:"flip,omitempty"`   // "horizontal", "vertical"
	Flop   bool    `json:"flop,omitempty"`   // Mirror horizontally

	// Filters and effects
	Brightness float64 `json:"brightness,omitempty"` // -100 to 100
	Contrast   float64 `json:"contrast,omitempty"`   // -100 to 100
	Gamma      float64 `json:"gamma,omitempty"`      // 0.0 to 3.0
	Saturation float64 `json:"saturation,omitempty"` // -100 to 500
	Hue        int     `json:"hue,omitempty"`        // -180 to 180
	Blur       float64 `json:"blur,omitempty"`       // 0.0 to 100.0
	Sharpen    float64 `json:"sharpen,omitempty"`    // 0.0 to 100.0

	// Watermark
	WatermarkURL      string  `json:"watermark_url,omitempty"`
	WatermarkOpacity  float64 `json:"watermark_opacity,omitempty"`  // 0.0 to 1.0
	WatermarkPosition string  `json:"watermark_position,omitempty"` // "top-left", "top-right", "bottom-left", "bottom-right", "center"
}

// ProcessedImage holds the result of image processing
type ProcessedImage struct {
	Data     io.ReadCloser
	Metadata *ImageMetadata
	CacheKey string
	Cached   bool // Indicates if this result came from cache
}

// ImageMetadata holds metadata about processed images
type ImageMetadata struct {
	ID           string    `json:"id"`
	OriginalName string    `json:"original_name"`
	Format       string    `json:"format"`
	Size         int64     `json:"size"`
	Width        int       `json:"width"`
	Height       int       `json:"height"`
	ContentType  string    `json:"content_type"`
	CreatedAt    time.Time `json:"created_at"`
}

// Cache defines the interface for caching processed images
type Cache interface {
	Get(key string) ([]byte, bool)
	Set(key string, value []byte, ttl time.Duration) error
	Delete(key string)
	Clear()
	Stats() interface{}
	Contains(key string) bool
	Keys() []string
	Size() int64
	MaxSize() int64
	Len() int
}

// ImageProcessor defines the interface for image processing
type ImageProcessor interface {
	// Process processes an image with the given parameters
	Process(ctx context.Context, input io.Reader, params *ProcessingParams) (*ProcessedImage, error)

	// GetMetadata extracts metadata from an image without processing
	GetMetadata(ctx context.Context, input io.Reader) (*ImageMetadata, error)

	// ValidateFormat validates if the input format is supported
	ValidateFormat(format string) bool

	// SupportedFormats returns a list of supported output formats
	SupportedFormats() []string

	// GetContentType returns the content type for a format
	GetContentType(format string) string

	// SetCache sets the cache for the processor
	SetCache(cache Cache)

	// GetCacheStats returns cache statistics
	GetCacheStats() interface{}
}

// ResizeMode represents different image resizing modes
type ResizeMode string

const (
	ResizeModeCover   ResizeMode = "cover"   // Maintain aspect ratio, crop if necessary
	ResizeModeContain ResizeMode = "contain" // Maintain aspect ratio, fit within dimensions
	ResizeModeFill    ResizeMode = "fill"    // Stretch to fill dimensions
)

// ImageFormat represents supported image formats
type ImageFormat string

const (
	FormatJPEG ImageFormat = "jpeg"
	FormatPNG  ImageFormat = "png"
	FormatWebP ImageFormat = "webp"
)

// IsValidFormat checks if a format string is valid
func IsValidFormat(format string) bool {
	switch ImageFormat(format) {
	case FormatJPEG, FormatPNG, FormatWebP:
		return true
	default:
		return false
	}
}

// GetDefaultQuality returns the default quality for a format
func GetDefaultQuality(format ImageFormat) int {
	switch format {
	case FormatJPEG:
		return 85
	case FormatPNG:
		return 100 // PNG is lossless
	case FormatWebP:
		return 85 // WebP quality (0-100)
	default:
		return 85
	}
}

// GetContentType returns the MIME content type for a format
func GetContentType(format ImageFormat) string {
	switch format {
	case FormatJPEG:
		return "image/jpeg"
	case FormatPNG:
		return "image/png"
	default:
		return "application/octet-stream"
	}
}
