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

	"github.com/birdple/falco/internal/pkg/logger"
	"github.com/birdple/falco/internal/pkg/metrics"
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
	sem              chan struct{} // semaphore limiting concurrent processing
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

// SetMaxConcurrency sets the maximum number of concurrent processing operations.
// Must be called before processing starts. A value of 0 means unlimited.
func (p *VipsProcessor) SetMaxConcurrency(n int) {
	if n > 0 {
		p.sem = make(chan struct{}, n)
	}
}

// Process processes an image with the given parameters.
// cacheKey should be obtained from GenerateCacheKey; pass "" to skip caching.
// The caller is expected to check GetFromCache before calling Process on the
// delivery path — this method no longer reads from cache, only writes.
func (p *VipsProcessor) Process(ctx context.Context, input io.Reader, params *ProcessingParams, cacheKey string) (*ProcessedImage, error) {
	// Read input data
	inputData, err := io.ReadAll(input)
	if err != nil {
		return nil, fmt.Errorf("failed to read input: %w", err)
	}

	// Capture input size for metrics before any potential release
	inputSize := len(inputData)

	// Acquire processing slot (limits concurrent CPU-intensive operations)
	if p.sem != nil {
		select {
		case p.sem <- struct{}{}:
			defer func() { <-p.sem }()
		case <-ctx.Done():
			return nil, ctx.Err()
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

	// Detect format before releasing input buffer
	format := p.detectFormat(inputData)

	// Release input buffer to reduce memory pressure during processing
	inputData = nil

	// Apply transformations
	if err := p.applyTransformations(img, params); err != nil {
		return nil, fmt.Errorf("failed to apply transformations: %w", err)
	}

	// Determine output format
	outputFormat := p.determineOutputFormat(params, format)

	// Encode image (actualFormat may differ from outputFormat on fallback, e.g. AVIF→WebP)
	processedData, actualFormat, err := p.encodeImage(img, outputFormat, params.Quality)
	if err != nil {
		return nil, fmt.Errorf("failed to encode image: %w", err)
	}
	outputFormat = actualFormat

	// Track processing size metrics
	m := metrics.Default()
	m.ImageProcessingSize.WithLabelValues("input").Observe(float64(inputSize))
	m.ImageProcessingSize.WithLabelValues("output").Observe(float64(len(processedData)))

	// Cache result under the caller-provided key (skip if empty)
	if p.cache != nil && cacheKey != "" {
		p.cache.Set(cacheKey, processedData, 24*time.Hour)
		m.CacheSize.Set(float64(p.cache.Size()))
		m.CacheItemCount.Set(float64(p.cache.Len()))
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
	// Auto-orient from EXIF (should happen early, before resize)
	if params.AutoOrient {
		_ = img.Autorot(nil) // Non-fatal: some formats don't have EXIF orientation
	}

	// Trim (remove uniform color borders, before resize)
	if params.TrimEnabled {
		threshold := params.TrimThreshold
		if threshold == 0 {
			threshold = 10
		}
		left, top, width, height, err := img.FindTrim(&vips.FindTrimOptions{Threshold: threshold})
		if err == nil && width > 0 && height > 0 {
			if err := img.ExtractArea(left, top, width, height); err != nil {
				return fmt.Errorf("trim extract failed: %w", err)
			}
		}
	}

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

	// Gravity-aware resize (smart crop)
	if (params.Width > 0 || params.Height > 0) && params.Gravity != "" {
		interesting := vips.InterestingCentre
		switch params.Gravity {
		case "smart", "attention":
			interesting = vips.InterestingAttention
		case "entropy":
			interesting = vips.InterestingEntropy
		}
		w, h := params.Width, params.Height
		if w == 0 {
			w = img.Width()
		}
		if h == 0 {
			h = img.Height()
		}
		if err := img.ThumbnailImage(w, &vips.ThumbnailImageOptions{
			Height: h,
			Crop:   interesting,
			Size:   vips.SizeBoth,
		}); err != nil {
			return fmt.Errorf("smart resize failed: %w", err)
		}
	} else if params.Width > 0 || params.Height > 0 {
		// Resize with upscaling protection
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

	// Padding
	if params.PaddingTop > 0 || params.PaddingRight > 0 || params.PaddingBottom > 0 || params.PaddingLeft > 0 {
		bg := parseHexColor(params.PaddingColor)
		newWidth := img.Width() + params.PaddingLeft + params.PaddingRight
		newHeight := img.Height() + params.PaddingTop + params.PaddingBottom
		if err := img.Embed(params.PaddingLeft, params.PaddingTop, newWidth, newHeight, &vips.EmbedOptions{
			Extend:     vips.ExtendBackground,
			Background: bg,
		}); err != nil {
			return fmt.Errorf("padding failed: %w", err)
		}
	}

	return nil
}

// parseHexColor parses a hex color string (e.g. "FF0000") into RGB float64 slice
func parseHexColor(hex string) []float64 {
	if hex == "" {
		return []float64{255, 255, 255} // default white
	}
	// Remove leading # if present
	if len(hex) > 0 && hex[0] == '#' {
		hex = hex[1:]
	}
	if len(hex) != 6 {
		return []float64{255, 255, 255}
	}
	r := hexToByte(hex[0:2])
	g := hexToByte(hex[2:4])
	b := hexToByte(hex[4:6])
	return []float64{float64(r), float64(g), float64(b)}
}

func hexToByte(s string) byte {
	var val byte
	for _, c := range s {
		val <<= 4
		switch {
		case c >= '0' && c <= '9':
			val |= byte(c - '0')
		case c >= 'a' && c <= 'f':
			val |= byte(c-'a') + 10
		case c >= 'A' && c <= 'F':
			val |= byte(c-'A') + 10
		}
	}
	return val
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

// encodeImage encodes the image to the specified format.
// Returns the encoded bytes and the actual format used (may differ from requested if fallback occurred).
func (p *VipsProcessor) encodeImage(img *vips.Image, format ImageFormat, quality int) ([]byte, ImageFormat, error) {
	if quality <= 0 {
		quality = GetDefaultQuality(format)
	}
	if quality > 100 {
		quality = 100
	}

	// Get buffer from pool
	buf := bufferPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer func() {
		// Discard buffers that grew beyond 8MB to prevent memory bloat
		if buf.Cap() <= 8*1024*1024 {
			bufferPool.Put(buf)
		}
	}()

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
		// AVIF requires HeifCompressionAv1 to produce actual AV1-encoded AVIF
		err = img.HeifsaveTarget(target, &vips.HeifsaveTargetOptions{
			Q:           quality,
			Lossless:    quality == 100,
			Effort:      6,
			Compression: vips.HeifCompressionAv1,
		})
		if err != nil {
			// Log the fallback so clients can detect AV1 support issues
			logger.Warn().Err(err).Msg("AVIF encoding failed, falling back to WebP")
			buf.Reset()
			format = FormatWebP // Update format so Content-Type is correct
			err = img.WebpsaveTarget(target, &vips.WebpsaveTargetOptions{
				Q: quality,
			})
		}
	default:
		return nil, format, fmt.Errorf("unsupported format: %s", format)
	}

	if err != nil {
		return nil, format, err
	}

	// Copy buffer data before returning to pool
	return bytes.Clone(buf.Bytes()), format, nil
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

	// GIF: 47 49 46 38
	if data[0] == 0x47 && data[1] == 0x49 && data[2] == 0x46 {
		return "gif"
	}

	// TIFF: 49 49 (little-endian) or 4D 4D (big-endian)
	if (data[0] == 0x49 && data[1] == 0x49) || (data[0] == 0x4D && data[1] == 0x4D) {
		return "tiff"
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

	// SVG: starts with <svg or <?xml
	if len(data) > 5 && (string(data[:4]) == "<svg" || string(data[:5]) == "<?xml") {
		return "svg"
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

// GenerateCacheKey builds a deterministic cache key from a storage key and
// processing parameters. The storage key (bucket:dir/filename) uniquely
// identifies the original image without requiring a round-trip to the storage
// backend, so the cache can be checked before any I/O.
func (p *VipsProcessor) GenerateCacheKey(storageKey string, params *ProcessingParams) string {
	return generateCacheKey(storageKey, params)
}

// InvalidateCacheForKey drops every cached variant of a storage key.
//
// Todas las variantes de un mismo objeto comparten el prefijo
// `sha256(storageKey)[:32]` que arma `generateCacheKey`, así que basta con
// barrer las claves por ese prefijo. Es O(n) sobre las claves de la cache, pero
// sólo corre en borrado y en update, que son raros comparados con las lecturas.
func (p *VipsProcessor) InvalidateCacheForKey(storageKey string) int {
	if p.cache == nil {
		return 0
	}

	prefix := fmt.Sprintf("%x", sha256.Sum256([]byte(storageKey)))[:32]

	removed := 0
	for _, key := range p.cache.Keys() {
		if strings.HasPrefix(key, prefix) {
			p.cache.Delete(key)
			removed++
		}
	}

	return removed
}

// GetFromCache returns cached processed image bytes for the given key.
func (p *VipsProcessor) GetFromCache(key string) ([]byte, bool) {
	if p.cache == nil {
		return nil, false
	}
	data, found := p.cache.Get(key)
	if found {
		metrics.Default().CacheHits.Inc()
	} else {
		metrics.Default().CacheMisses.Inc()
	}
	return data, found
}

// generateCacheKey builds a cache key from storageKey + all transformation
// parameters. The storageKey is hashed (SHA-256, truncated) to keep key
// length bounded while avoiding collisions.
func generateCacheKey(storageKey string, params *ProcessingParams) string {
	keyHash := fmt.Sprintf("%x", sha256.Sum256([]byte(storageKey)))[:32]

	var parts []string
	parts = append(parts, keyHash)

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
	if params.Flop {
		parts = append(parts, "flop")
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
	if params.Hue != 0 {
		parts = append(parts, fmt.Sprintf("hue%d", params.Hue))
	}

	// Effects
	if params.Blur > 0 {
		parts = append(parts, fmt.Sprintf("blur%.1f", params.Blur))
	}
	if params.Sharpen > 0 {
		parts = append(parts, fmt.Sprintf("sharp%.1f", params.Sharpen))
	}

	// Extended parameters
	if params.Gravity != "" {
		parts = append(parts, fmt.Sprintf("grav%s", params.Gravity))
	}
	if params.TrimEnabled {
		parts = append(parts, fmt.Sprintf("trim%.0f", params.TrimThreshold))
	}
	if params.PaddingTop > 0 || params.PaddingRight > 0 || params.PaddingBottom > 0 || params.PaddingLeft > 0 {
		parts = append(parts, fmt.Sprintf("pad%d_%d_%d_%d_%s", params.PaddingTop, params.PaddingRight, params.PaddingBottom, params.PaddingLeft, params.PaddingColor))
	}
	if params.AutoOrient {
		parts = append(parts, "orient")
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
