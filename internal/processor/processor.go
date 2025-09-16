package processor

import (
	"bytes"
	"context"
	"crypto/md5"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"strings"
	"time"

	"github.com/chai2010/webp"
	"github.com/disintegration/imaging"
)

// ImageProcessorImpl implements the ImageProcessor interface
type ImageProcessorImpl struct {
	maxFileSizeMB    int
	defaultQuality   int
	defaultFormat    ImageFormat
	supportedFormats []ImageFormat
	maxDimensions    image.Point
	cache            Cache
}

// NewImageProcessor creates a new image processor
func NewImageProcessor(maxFileSizeMB, defaultQuality int, defaultFormat ImageFormat, maxWidth, maxHeight int) *ImageProcessorImpl {
	return &ImageProcessorImpl{
		maxFileSizeMB:    maxFileSizeMB,
		defaultQuality:   defaultQuality,
		defaultFormat:    defaultFormat,
		supportedFormats: []ImageFormat{FormatJPEG, FormatPNG, FormatWebP},
		maxDimensions:    image.Point{X: maxWidth, Y: maxHeight},
	}
}

// Process processes an image with the given parameters
func (p *ImageProcessorImpl) Process(ctx context.Context, input io.Reader, params *ProcessingParams) (*ProcessedImage, error) {
	// Read the input data first to enable caching
	inputData, err := io.ReadAll(input)
	if err != nil {
		return nil, fmt.Errorf("failed to read input: %w", err)
	}

	// Generate cache key
	cacheKey := p.generateCacheKeyFromData(inputData, params)

	// Check cache first if available
	if p.cache != nil {
		if cachedData, found := p.cache.Get(cacheKey); found {
			// Return cached result
			return &ProcessedImage{
				Data:     io.NopCloser(bytes.NewReader(cachedData)),
				Metadata: &ImageMetadata{CreatedAt: time.Now()},
				CacheKey: cacheKey,
				Cached:   true,
			}, nil
		}
	}

	// Process the image
	reader := bytes.NewReader(inputData)
	srcImg, format, err := p.decodeImage(reader)
	if err != nil {
		return nil, fmt.Errorf("failed to decode image: %w", err)
	}

	// Apply transformations
	processedImg := p.applyTransformations(srcImg, params)

	// Determine output format
	outputFormat := p.determineOutputFormat(params, format)

	// Encode the processed image
	var buf bytes.Buffer
	if err := p.encodeImage(&buf, processedImg, outputFormat, params.Quality); err != nil {
		return nil, fmt.Errorf("failed to encode image: %w", err)
	}

	processedData := buf.Bytes()

	// Store in cache if available
	if p.cache != nil {
		cacheTTL := 24 * time.Hour // Default 24 hours
		p.cache.Set(cacheKey, processedData, cacheTTL)
	}

	// Create processed image result
	result := &ProcessedImage{
		Data: io.NopCloser(bytes.NewReader(processedData)),
		Metadata: &ImageMetadata{
			Format:      string(outputFormat),
			Size:        int64(len(processedData)),
			Width:       processedImg.Bounds().Dx(),
			Height:      processedImg.Bounds().Dy(),
			ContentType: p.getContentTypeFromFormat(string(outputFormat)),
			CreatedAt:   time.Now(),
		},
		CacheKey: cacheKey,
		Cached:   false,
	}

	return result, nil
}

// GetMetadata extracts metadata from an image without processing
func (p *ImageProcessorImpl) GetMetadata(ctx context.Context, input io.Reader) (*ImageMetadata, error) {
	// Read the input to determine format and size
	data, err := io.ReadAll(io.LimitReader(input, int64(p.maxFileSizeMB)*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("failed to read image data: %w", err)
	}

	// Decode image to get dimensions
	reader := bytes.NewReader(data)
	srcImg, format, err := p.decodeImage(reader)
	if err != nil {
		return nil, fmt.Errorf("failed to decode image: %w", err)
	}

	bounds := srcImg.Bounds()

	return &ImageMetadata{
		Format:      format,
		Size:        int64(len(data)),
		Width:       bounds.Dx(),
		Height:      bounds.Dy(),
		ContentType: p.getContentTypeFromFormat(format),
		CreatedAt:   time.Now(),
	}, nil
}

// ValidateFormat validates if the input format is supported
func (p *ImageProcessorImpl) ValidateFormat(format string) bool {
	return IsValidFormat(format)
}

// SupportedFormats returns a list of supported output formats
func (p *ImageProcessorImpl) SupportedFormats() []string {
	formats := make([]string, len(p.supportedFormats))
	for i, format := range p.supportedFormats {
		formats[i] = string(format)
	}
	return formats
}

// GetContentType returns the content type for a format
func (p *ImageProcessorImpl) GetContentType(format string) string {
	return GetContentType(ImageFormat(format))
}

// decodeImage decodes an image from the input reader
func (p *ImageProcessorImpl) decodeImage(input io.Reader) (image.Image, string, error) {
	// Read the image data
	data, err := io.ReadAll(io.LimitReader(input, int64(p.maxFileSizeMB)*1024*1024))
	if err != nil {
		return nil, "", fmt.Errorf("failed to read image data: %w", err)
	}

	// Try to decode as different formats
	reader := bytes.NewReader(data)

	// Try JPEG
	if img, err := jpeg.Decode(reader); err == nil {
		return img, "jpeg", nil
	}
	reader.Seek(0, 0)

	// Try PNG
	if img, err := png.Decode(reader); err == nil {
		return img, "png", nil
	}
	reader.Seek(0, 0)

	// Try WebP
	if img, err := webp.Decode(reader); err == nil {
		return img, "webp", nil
	}

	return nil, "", fmt.Errorf("unsupported image format")
}

// applyTransformations applies the requested transformations to the image
func (p *ImageProcessorImpl) applyTransformations(img image.Image, params *ProcessingParams) image.Image {
	// Start with the original image
	result := img

	// Apply resizing if requested
	if params.Width > 0 || params.Height > 0 {
		result = p.resizeImage(result, params)
	}

	// Ensure dimensions don't exceed maximum
	result = p.enforceMaxDimensions(result)

	return result
}

// resizeImage resizes the image according to the parameters
func (p *ImageProcessorImpl) resizeImage(img image.Image, params *ProcessingParams) image.Image {
	width := params.Width
	height := params.Height

	// If only one dimension is specified, maintain aspect ratio
	bounds := img.Bounds()
	originalWidth := bounds.Dx()
	originalHeight := bounds.Dy()

	if width == 0 {
		// Calculate width based on height and aspect ratio
		width = (originalWidth * height) / originalHeight
	} else if height == 0 {
		// Calculate height based on width and aspect ratio
		height = (originalHeight * width) / originalWidth
	}

	// Apply resizing based on fit mode
	switch params.Fit {
	case "cover":
		return imaging.Fill(img, width, height, imaging.Center, imaging.Lanczos)
	case "contain":
		return imaging.Fit(img, width, height, imaging.Lanczos)
	case "fill":
		return imaging.Resize(img, width, height, imaging.Lanczos)
	default:
		// Default to cover
		return imaging.Fill(img, width, height, imaging.Center, imaging.Lanczos)
	}
}

// enforceMaxDimensions ensures the image doesn't exceed maximum dimensions
func (p *ImageProcessorImpl) enforceMaxDimensions(img image.Image) image.Image {
	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	// Check if resizing is needed
	if width <= p.maxDimensions.X && height <= p.maxDimensions.Y {
		return img
	}

	// Calculate new dimensions maintaining aspect ratio
	if width > p.maxDimensions.X {
		scale := float64(p.maxDimensions.X) / float64(width)
		width = p.maxDimensions.X
		height = int(float64(height) * scale)
	}

	if height > p.maxDimensions.Y {
		scale := float64(p.maxDimensions.Y) / float64(height)
		height = p.maxDimensions.Y
		width = int(float64(width) * scale)
	}

	return imaging.Resize(img, width, height, imaging.Lanczos)
}

// determineOutputFormat determines the output format based on parameters
func (p *ImageProcessorImpl) determineOutputFormat(params *ProcessingParams, inputFormat string) ImageFormat {
	if params.Format != "" && IsValidFormat(params.Format) {
		return ImageFormat(params.Format)
	}

	// Use default format
	return p.defaultFormat
}

// encodeImage encodes the image to the specified format
func (p *ImageProcessorImpl) encodeImage(writer io.Writer, img image.Image, format ImageFormat, quality int) error {
	if quality <= 0 {
		quality = GetDefaultQuality(format)
	}

	// Ensure quality is within valid range
	if quality > 100 {
		quality = 100
	}

	switch format {
	case FormatJPEG:
		return jpeg.Encode(writer, img, &jpeg.Options{Quality: quality})
	case FormatPNG:
		return png.Encode(writer, img)
	case FormatWebP:
		return webp.Encode(writer, img, &webp.Options{Quality: float32(quality)})
	default:
		return fmt.Errorf("unsupported output format: %s", format)
	}
}

// generateCacheKeyFromData generates a cache key from input data and parameters
func (p *ImageProcessorImpl) generateCacheKeyFromData(inputData []byte, params *ProcessingParams) string {
	// Create a hash of the input data for uniqueness
	inputHash := fmt.Sprintf("%x", md5.Sum(inputData))[:16]

	var parts []string
	parts = append(parts, inputHash)

	// Add dimensions
	if params.Width > 0 || params.Height > 0 {
		parts = append(parts, fmt.Sprintf("w%d", params.Width))
		parts = append(parts, fmt.Sprintf("h%d", params.Height))
		if params.Fit != "" {
			parts = append(parts, fmt.Sprintf("f%s", params.Fit))
		}
	}

	// Add quality
	if params.Quality > 0 {
		parts = append(parts, fmt.Sprintf("q%d", params.Quality))
	}

	// Add output format
	if params.Format != "" {
		parts = append(parts, fmt.Sprintf("fmt%s", params.Format))
	}

	return strings.Join(parts, "_")
}

// generateCacheKey generates a cache key for the processed image
func (p *ImageProcessorImpl) generateCacheKey(params *ProcessingParams, originalWidth, originalHeight int, inputFormat string) string {
	var parts []string

	// Add dimensions
	if params.Width > 0 || params.Height > 0 {
		parts = append(parts, fmt.Sprintf("w%d", params.Width))
		parts = append(parts, fmt.Sprintf("h%d", params.Height))
		if params.Fit != "" {
			parts = append(parts, fmt.Sprintf("f%s", params.Fit))
		}
	}

	// Add quality
	if params.Quality > 0 {
		parts = append(parts, fmt.Sprintf("q%d", params.Quality))
	}

	// Add output format
	if params.Format != "" {
		parts = append(parts, fmt.Sprintf("fmt%s", params.Format))
	}

	// Add original dimensions and format for uniqueness
	parts = append(parts, fmt.Sprintf("orig_%dx%d_%s", originalWidth, originalHeight, inputFormat))

	return strings.Join(parts, "_")
}

// SetCache sets the cache for the processor
func (p *ImageProcessorImpl) SetCache(cache Cache) {
	p.cache = cache
}

// GetCacheStats returns cache statistics
func (p *ImageProcessorImpl) GetCacheStats() interface{} {
	if p.cache != nil {
		return p.cache.Stats()
	}
	return nil
}

// getContentTypeFromFormat returns the content type for a format string
func (p *ImageProcessorImpl) getContentTypeFromFormat(format string) string {
	switch format {
	case "jpeg":
		return "image/jpeg"
	case "png":
		return "image/png"
	case "webp":
		return "image/webp"
	default:
		return "application/octet-stream"
	}
}
