package processor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/cshum/vipsgen/vips"
)

// bufferPool is a pool of byte buffers to reduce allocations during image encoding.
// Pre-allocated at 2MB to accommodate typical processed image sizes (up to 10MB max input).
var bufferPool = sync.Pool{
	New: func() interface{} {
		// Pre-allocate 2MB buffer - a good balance for images up to 10MB
		// Smaller than max to avoid over-allocation, larger than typical output
		return bytes.NewBuffer(make([]byte, 0, 2*1024*1024))
	},
}

// VipsProcessor implements ImageProcessor using libvips
type VipsProcessor struct {
	maxFileSizeMB    int
	defaultQuality   int
	defaultFormat    ImageFormat
	supportedFormats []ImageFormat
	maxDimensions    struct{ width, height int }
	cache            Cache
}

// NewVipsProcessor creates a new vips-based image processor
func NewVipsProcessor(maxFileSizeMB, defaultQuality int, defaultFormat ImageFormat, maxWidth, maxHeight int) *VipsProcessor {
	return &VipsProcessor{
		maxFileSizeMB:    maxFileSizeMB,
		defaultQuality:   defaultQuality,
		defaultFormat:    defaultFormat,
		supportedFormats: []ImageFormat{FormatJPEG, FormatPNG, FormatWebP, FormatHEIC, FormatAVIF},
		maxDimensions:    struct{ width, height int }{width: maxWidth, height: maxHeight},
	}
}

// Process processes an image with the given parameters
func (p *VipsProcessor) Process(ctx context.Context, input io.Reader, params *ProcessingParams) (*ProcessedImage, error) {
	// Read input data
	inputData, err := io.ReadAll(input)
	if err != nil {
		return nil, fmt.Errorf("failed to read input: %w", err)
	}

	// Generate cache key
	cacheKey := p.generateCacheKey(inputData, params)

	// Check cache
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

	// Load image from buffer
	source := vips.NewSource(io.NopCloser(bytes.NewReader(inputData)))
	defer source.Close()

	img, err := vips.NewImageFromSource(source, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to load image: %w", err)
	}
	defer img.Close()

	// Detect format
	format := p.detectFormat(inputData)

	// Apply transformations
	if err := p.applyTransformations(img, params); err != nil {
		return nil, fmt.Errorf("failed to apply transformations: %w", err)
	}

	// Determine output format
	outputFormat := p.determineOutputFormat(params, format)

	// Encode image
	processedData, err := p.encodeImage(img, outputFormat, params.Quality)
	if err != nil {
		return nil, fmt.Errorf("failed to encode image: %w", err)
	}

	// Cache result
	if p.cache != nil {
		p.cache.Set(cacheKey, processedData, 24*time.Hour)
	}

	return &ProcessedImage{
		Data: io.NopCloser(bytes.NewReader(processedData)),
		Metadata: &ImageMetadata{
			Format:      string(outputFormat),
			Size:        int64(len(processedData)),
			Width:       img.Width(),
			Height:      img.Height(),
			ContentType: GetContentType(outputFormat),
			CreatedAt:   time.Now(),
		},
		CacheKey: cacheKey,
		Cached:   false,
	}, nil
}

// applyTransformations applies all transformations to the image
func (p *VipsProcessor) applyTransformations(img *vips.Image, params *ProcessingParams) error {
	// Crop
	if params.CropW > 0 && params.CropH > 0 {
		if err := img.ExtractArea(params.CropX, params.CropY, params.CropW, params.CropH); err != nil {
			return fmt.Errorf("crop failed: %w", err)
		}
	}

	// Flip
	if params.Flip != "" {
		switch params.Flip {
		case "horizontal":
			if err := img.Flip(vips.DirectionHorizontal); err != nil {
				return fmt.Errorf("flip horizontal failed: %w", err)
			}
		case "vertical":
			if err := img.Flip(vips.DirectionVertical); err != nil {
				return fmt.Errorf("flip vertical failed: %w", err)
			}
		}
	}

	// Rotate
	if params.Rotate != 0 {
		if err := img.Rotate(params.Rotate, nil); err != nil {
			return fmt.Errorf("rotate failed: %w", err)
		}
	}

	// Resize with upscaling protection
	if params.Width > 0 || params.Height > 0 {
		// Prevent upscaling attacks - limit requested dimensions to original or max dimensions
		originalWidth := img.Width()
		originalHeight := img.Height()

		requestedWidth := params.Width
		requestedHeight := params.Height

		// Calculate missing dimension maintaining aspect ratio
		if requestedWidth == 0 && requestedHeight > 0 {
			requestedWidth = (originalWidth * requestedHeight) / originalHeight
		} else if requestedHeight == 0 && requestedWidth > 0 {
			requestedHeight = (originalHeight * requestedWidth) / originalWidth
		}

		// SECURITY: Prevent upscaling beyond original size or max dimensions
		// This prevents DoS attacks requesting massive upscaling (e.g., 100x100 -> 10000x10000)
		maxAllowedWidth := originalWidth
		maxAllowedHeight := originalHeight

		// Allow upscaling only up to maxDimensions if specified
		if p.maxDimensions.width > 0 && p.maxDimensions.width > originalWidth {
			maxAllowedWidth = p.maxDimensions.width
		}
		if p.maxDimensions.height > 0 && p.maxDimensions.height > originalHeight {
			maxAllowedHeight = p.maxDimensions.height
		}

		// Clamp requested dimensions to allowed maximum
		if requestedWidth > maxAllowedWidth {
			// Proportionally reduce both dimensions
			scale := float64(maxAllowedWidth) / float64(requestedWidth)
			requestedWidth = maxAllowedWidth
			requestedHeight = int(float64(requestedHeight) * scale)
		}
		if requestedHeight > maxAllowedHeight {
			// Proportionally reduce both dimensions
			scale := float64(maxAllowedHeight) / float64(requestedHeight)
			requestedHeight = maxAllowedHeight
			requestedWidth = int(float64(requestedWidth) * scale)
		}

		// Update params with safe dimensions
		safeParams := *params
		safeParams.Width = requestedWidth
		safeParams.Height = requestedHeight

		if err := p.resizeImage(img, &safeParams); err != nil {
			return fmt.Errorf("resize failed: %w", err)
		}
	}

	// Apply max dimensions as final safeguard (should rarely trigger after above protection)
	if img.Width() > p.maxDimensions.width || img.Height() > p.maxDimensions.height {
		scale := 1.0
		if img.Width() > p.maxDimensions.width {
			scale = float64(p.maxDimensions.width) / float64(img.Width())
		}
		if img.Height() > p.maxDimensions.height {
			heightScale := float64(p.maxDimensions.height) / float64(img.Height())
			if heightScale < scale {
				scale = heightScale
			}
		}
		if err := img.Resize(scale, nil); err != nil {
			return fmt.Errorf("max dimensions resize failed: %w", err)
		}
	}

	// Brightness
	if params.Brightness != 0 {
		multiplier := 1.0 + (params.Brightness / 100.0)
		if err := img.Linear([]float64{multiplier, multiplier, multiplier}, []float64{0, 0, 0}, nil); err != nil {
			return fmt.Errorf("brightness failed: %w", err)
		}
	}

	// Contrast
	if params.Contrast != 0 {
		multiplier := 1.0 + (params.Contrast / 100.0)
		offset := 128.0 * (1.0 - multiplier)
		if err := img.Linear([]float64{multiplier, multiplier, multiplier}, []float64{offset, offset, offset}, nil); err != nil {
			return fmt.Errorf("contrast failed: %w", err)
		}
	}

	// Gamma
	if params.Gamma != 0 && params.Gamma != 1.0 {
		if err := img.Gamma(&vips.GammaOptions{Exponent: params.Gamma}); err != nil {
			return fmt.Errorf("gamma failed: %w", err)
		}
	}

	// Blur
	if params.Blur > 0 {
		sigma := params.Blur / 2.0
		if err := img.Gaussblur(sigma, nil); err != nil {
			return fmt.Errorf("blur failed: %w", err)
		}
	}

	// Sharpen
	if params.Sharpen > 0 {
		if err := img.Sharpen(nil); err != nil {
			return fmt.Errorf("sharpen failed: %w", err)
		}
	}

	return nil
}

// resizeImage handles different resize modes
func (p *VipsProcessor) resizeImage(img *vips.Image, params *ProcessingParams) error {
	w := params.Width
	h := params.Height

	// Calculate missing dimension
	if w == 0 && h > 0 {
		w = (img.Width() * h) / img.Height()
	} else if h == 0 && w > 0 {
		h = (img.Height() * w) / img.Width()
	}

	switch params.Fit {
	case "cover":
		return img.ThumbnailImage(w, &vips.ThumbnailImageOptions{
			Height: h,
			Crop:   vips.InterestingCentre,
			Size:   vips.SizeBoth,
		})
	case "contain":
		return img.ThumbnailImage(w, &vips.ThumbnailImageOptions{
			Height: h,
			Size:   vips.SizeDown,
		})
	case "fill":
		scaleX := float64(w) / float64(img.Width())
		scaleY := float64(h) / float64(img.Height())
		return img.Resize(scaleX, &vips.ResizeOptions{Vscale: scaleY})
	default:
		return img.ThumbnailImage(w, &vips.ThumbnailImageOptions{
			Height: h,
			Crop:   vips.InterestingCentre,
			Size:   vips.SizeBoth,
		})
	}
}

// encodeImage encodes the image to the specified format
func (p *VipsProcessor) encodeImage(img *vips.Image, format ImageFormat, quality int) ([]byte, error) {
	if quality <= 0 {
		quality = GetDefaultQuality(format)
	}
	if quality > 100 {
		quality = 100
	}

	// Get buffer from pool
	buf := bufferPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufferPool.Put(buf)

	target := vips.NewTarget(nopWriteCloser{buf})
	defer target.Close()

	var err error
	switch format {
	case FormatJPEG:
		// JPEG with progressive encoding and optimization
		err = img.JpegsaveTarget(target, &vips.JpegsaveTargetOptions{
			Q:                  quality,
			Interlace:          true,          // Progressive JPEG for better web loading
			OptimizeCoding:     true,          // Optimize Huffman tables
			TrellisQuant:       quality >= 80, // Better compression for high quality
			OvershootDeringing: quality >= 80, // Reduce compression artifacts
			OptimizeScans:      true,          // Optimize progressive scan order
		})
	case FormatPNG:
		// PNG compression level (0-9)
		// Map quality (0-100) to compression effort (9=best compression, 0=fastest)
		compression := 6 // Default balanced
		if quality < 100 {
			compression = 9 - (quality * 9 / 100)
			if compression < 0 {
				compression = 0
			}
			if compression > 9 {
				compression = 9
			}
		}
		err = img.PngsaveTarget(target, &vips.PngsaveTargetOptions{
			Compression: compression,
			Interlace:   true, // Interlaced PNG for progressive loading
			// Filter option to try all PNG filters for best compression
			// Note: Using default filter (adaptive) which works well for most cases
		})
	case FormatWebP:
		// WebP with advanced compression options
		err = img.WebpsaveTarget(target, &vips.WebpsaveTargetOptions{
			Q:              quality,
			Lossless:       quality == 100,                 // Lossless if quality is 100
			NearLossless:   quality >= 95 && quality < 100, // Near-lossless for very high quality
			Effort:         6,                              // Compression effort (0-6, higher=better compression)
			SmartSubsample: quality >= 80,                  // Better chroma subsampling for high quality
			MinSize:        true,                           // Enable extra optimizations for smaller file size
			Mixed:          quality >= 80,                  // Allow mixed lossy/lossless encoding
		})
	case FormatHEIC:
		// HEIC with optimization
		err = img.HeifsaveTarget(target, &vips.HeifsaveTargetOptions{
			Q:        quality,
			Lossless: quality == 100, // Lossless if quality is 100
			Effort:   8,              // Encoding effort (0-9, higher=slower but better)
			// Note: Chroma subsampling is handled automatically by libvips
		})
	case FormatAVIF:
		// AVIF with optimization (uses HEIF encoder with AV1 codec)
		err = img.HeifsaveTarget(target, &vips.HeifsaveTargetOptions{
			Q:        quality,
			Lossless: quality == 100, // Lossless if quality is 100
			Effort:   8,              // Higher effort for better compression
			// Note: AVIF support depends on libvips being compiled with HEIF/AV1 support
		})
	default:
		return nil, fmt.Errorf("unsupported format: %s", format)
	}

	if err != nil {
		return nil, err
	}

	// Copy buffer data before returning to pool
	result := make([]byte, buf.Len())
	copy(result, buf.Bytes())
	return result, nil
}

// GetMetadata extracts metadata from an image
func (p *VipsProcessor) GetMetadata(ctx context.Context, input io.Reader) (*ImageMetadata, error) {
	data, err := io.ReadAll(io.LimitReader(input, int64(p.maxFileSizeMB)*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("failed to read image data: %w", err)
	}

	source := vips.NewSource(io.NopCloser(bytes.NewReader(data)))
	defer source.Close()

	img, err := vips.NewImageFromSource(source, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to load image: %w", err)
	}
	defer img.Close()

	format := p.detectFormat(data)

	return &ImageMetadata{
		Format:      format,
		Size:        int64(len(data)),
		Width:       img.Width(),
		Height:      img.Height(),
		ContentType: GetContentType(ImageFormat(format)),
		CreatedAt:   time.Now(),
	}, nil
}

// ValidateFormat validates if a format is supported
func (p *VipsProcessor) ValidateFormat(format string) bool {
	return IsValidFormat(format)
}

// SupportedFormats returns the list of supported formats
func (p *VipsProcessor) SupportedFormats() []string {
	formats := make([]string, len(p.supportedFormats))
	for i, format := range p.supportedFormats {
		formats[i] = string(format)
	}
	return formats
}

// GetContentType returns the content type for a format
func (p *VipsProcessor) GetContentType(format string) string {
	return GetContentType(ImageFormat(format))
}

// SetCache sets the cache
func (p *VipsProcessor) SetCache(cache Cache) {
	p.cache = cache
}

// GetCacheStats returns cache statistics
func (p *VipsProcessor) GetCacheStats() interface{} {
	if p.cache != nil {
		return p.cache.Stats()
	}
	return nil
}

// detectFormat detects the image format from data
func (p *VipsProcessor) detectFormat(data []byte) string {
	if len(data) < 12 {
		return "unknown"
	}

	// JPEG
	if data[0] == 0xFF && data[1] == 0xD8 {
		return "jpeg"
	}

	// PNG
	if data[0] == 0x89 && data[1] == 0x50 && data[2] == 0x4E && data[3] == 0x47 {
		return "png"
	}

	// WebP
	if data[8] == 0x57 && data[9] == 0x45 && data[10] == 0x42 && data[11] == 0x50 {
		return "webp"
	}

	// HEIC/HEIF
	if data[4] == 0x66 && data[5] == 0x74 && data[6] == 0x79 && data[7] == 0x70 {
		ftype := string(data[8:12])
		if ftype == "heic" || ftype == "heix" || ftype == "hevc" || ftype == "hevx" || ftype == "mif1" {
			return "heic"
		}
		if ftype == "avif" || ftype == "avis" {
			return "avif"
		}
	}

	return "unknown"
}

// determineOutputFormat determines the output format
func (p *VipsProcessor) determineOutputFormat(params *ProcessingParams, inputFormat string) ImageFormat {
	if params.Format != "" && IsValidFormat(params.Format) {
		return ImageFormat(params.Format)
	}
	return p.defaultFormat
}

// generateCacheKey generates a cache key including ALL transformation parameters.
// Uses SHA-256 (truncated to 32 chars / 128 bits) for better collision resistance than MD5.
func (p *VipsProcessor) generateCacheKey(inputData []byte, params *ProcessingParams) string {
	// Use SHA-256 for better collision resistance (128 bits vs 64 bits with MD5)
	inputHash := fmt.Sprintf("%x", sha256.Sum256(inputData))[:32]

	var parts []string
	parts = append(parts, inputHash)

	// Size parameters
	if params.Width > 0 || params.Height > 0 {
		parts = append(parts, fmt.Sprintf("w%d_h%d", params.Width, params.Height))
		if params.Fit != "" {
			parts = append(parts, fmt.Sprintf("f%s", params.Fit))
		}
	}

	// Quality and format
	if params.Quality > 0 {
		parts = append(parts, fmt.Sprintf("q%d", params.Quality))
	}
	if params.Format != "" {
		parts = append(parts, fmt.Sprintf("fmt%s", params.Format))
	}

	// Crop parameters
	if params.CropW > 0 || params.CropH > 0 {
		parts = append(parts, fmt.Sprintf("crop%d_%d_%d_%d", params.CropX, params.CropY, params.CropW, params.CropH))
	}

	// Rotation and flip
	if params.Rotate != 0 {
		parts = append(parts, fmt.Sprintf("rot%.0f", params.Rotate))
	}
	if params.Flip != "" {
		parts = append(parts, fmt.Sprintf("flip%s", params.Flip))
	}

	// Color adjustments
	if params.Brightness != 0 {
		parts = append(parts, fmt.Sprintf("br%.0f", params.Brightness))
	}
	if params.Contrast != 0 {
		parts = append(parts, fmt.Sprintf("con%.0f", params.Contrast))
	}
	if params.Gamma != 0 {
		parts = append(parts, fmt.Sprintf("gam%.1f", params.Gamma))
	}
	if params.Saturation != 0 {
		parts = append(parts, fmt.Sprintf("sat%.0f", params.Saturation))
	}

	// Effects
	if params.Blur > 0 {
		parts = append(parts, fmt.Sprintf("blur%.1f", params.Blur))
	}
	if params.Sharpen > 0 {
		parts = append(parts, fmt.Sprintf("sharp%.1f", params.Sharpen))
	}

	return strings.Join(parts, "_")
}

// nopWriteCloser wraps a writer to add a no-op Close method
type nopWriteCloser struct {
	io.Writer
}

func (nopWriteCloser) Close() error {
	return nil
}
