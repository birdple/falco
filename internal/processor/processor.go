package processor

import (
	"bytes"
	"context"
	"crypto/md5"
	"fmt"
	"image"
	"io"
	"strings"
	"time"
)

// ImageProcessorImpl implements the ImageProcessor interface
type ImageProcessorImpl struct {
	maxFileSizeMB    int
	defaultQuality   int
	defaultFormat    ImageFormat
	supportedFormats []ImageFormat
	maxDimensions    image.Point
	cache            Cache
	decoder          ImageDecoder
	encoder          ImageEncoder
}

// NewImageProcessor creates a new image processor
func NewImageProcessor(maxFileSizeMB, defaultQuality int, defaultFormat ImageFormat, maxWidth, maxHeight int) ImageProcessor {
	// Use VipsProcessor which supports JPEG, PNG, WebP, HEIC, and AVIF
	return NewVipsProcessor(maxFileSizeMB, defaultQuality, defaultFormat, maxWidth, maxHeight)
}

// Process processes an image with the given parameters
func (p *ImageProcessorImpl) Process(ctx context.Context, input io.Reader, params *ProcessingParams) (*ProcessedImage, error) {
	// Read the input data for caching
	inputData, err := io.ReadAll(input)
	if err != nil {
		return nil, fmt.Errorf("failed to read input: %w", err)
	}

	// Generate cache key
	cacheKey := p.generateCacheKeyFromData(inputData, params)

	// Check cache first
	if p.cache != nil {
		if cachedData, found := p.cache.Get(cacheKey); found {
			return &ProcessedImage{
				Data:     io.NopCloser(bytes.NewReader(cachedData)),
				Metadata: &ImageMetadata{CreatedAt: time.Now()},
				CacheKey: cacheKey,
				Cached:   true,
			}, nil
		}
	}

	// Decode image
	srcImg, format, err := p.decoder.Decode(inputData)
	if err != nil {
		return nil, fmt.Errorf("failed to decode image: %w", err)
	}

	// Build transformation pipeline
	pipeline := p.buildPipeline(params)

	// Apply transformations
	processedImg := pipeline.Execute(srcImg)

	// Determine output format
	outputFormat := p.determineOutputFormat(params, format)

	// Encode image
	processedData, err := p.encoder.Encode(processedImg, outputFormat, params.Quality)
	if err != nil {
		return nil, fmt.Errorf("failed to encode image: %w", err)
	}

	// Cache result
	if p.cache != nil {
		cacheTTL := 24 * time.Hour
		p.cache.Set(cacheKey, processedData, cacheTTL)
	}

	// Create result
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

// buildPipeline constructs a transformation pipeline based on processing parameters
func (p *ImageProcessorImpl) buildPipeline(params *ProcessingParams) *Pipeline {
	pipeline := NewPipeline()

	// Add transformations in order
	pipeline.
		AddIf(params.CropW > 0 && params.CropH > 0,
			CropTransform(params.CropX, params.CropY, params.CropW, params.CropH)).
		AddIf(params.Flip != "",
			FlipTransform(params.Flip)).
		AddIf(params.Rotate != 0,
			RotateTransform(params.Rotate)).
		AddIf(params.Brightness != 0,
			BrightnessTransform(params.Brightness)).
		AddIf(params.Contrast != 0,
			ContrastTransform(params.Contrast)).
		AddIf(params.Gamma != 0,
			GammaTransform(params.Gamma)).
		AddIf(params.Saturation != 0,
			SaturationTransform(params.Saturation)).
		AddIf(params.Blur > 0,
			BlurTransform(params.Blur)).
		AddIf(params.Sharpen > 0,
			SharpenTransform(params.Sharpen)).
		AddIf(params.Width > 0 || params.Height > 0,
			ResizeTransform(params.Width, params.Height, params.Fit)).
		Add(MaxDimensionsTransform(p.maxDimensions.X, p.maxDimensions.Y))

	return pipeline
}

// GetMetadata extracts metadata from an image without processing
func (p *ImageProcessorImpl) GetMetadata(ctx context.Context, input io.Reader) (*ImageMetadata, error) {
	// Read the input
	data, err := io.ReadAll(io.LimitReader(input, int64(p.maxFileSizeMB)*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("failed to read image data: %w", err)
	}

	// Decode image
	srcImg, format, err := p.decoder.Decode(data)
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

// determineOutputFormat determines the output format based on parameters
func (p *ImageProcessorImpl) determineOutputFormat(params *ProcessingParams, inputFormat string) ImageFormat {
	if params.Format != "" && IsValidFormat(params.Format) {
		return ImageFormat(params.Format)
	}
	return p.defaultFormat
}

// generateCacheKeyFromData generates a cache key from input data and parameters
func (p *ImageProcessorImpl) generateCacheKeyFromData(inputData []byte, params *ProcessingParams) string {
	inputHash := fmt.Sprintf("%x", md5.Sum(inputData))[:16]

	var parts []string
	parts = append(parts, inputHash)

	if params.Width > 0 || params.Height > 0 {
		parts = append(parts, fmt.Sprintf("w%d", params.Width))
		parts = append(parts, fmt.Sprintf("h%d", params.Height))
		if params.Fit != "" {
			parts = append(parts, fmt.Sprintf("f%s", params.Fit))
		}
	}

	if params.Quality > 0 {
		parts = append(parts, fmt.Sprintf("q%d", params.Quality))
	}

	if params.Format != "" {
		parts = append(parts, fmt.Sprintf("fmt%s", params.Format))
	}

	return strings.Join(parts, "_")
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
