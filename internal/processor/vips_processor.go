package processor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/birdple/falco/internal/cache"
	"github.com/birdple/falco/internal/pkg/logger"
	"github.com/birdple/falco/internal/pkg/metrics"
	"github.com/cshum/vipsgen/vips"
)

// bufferPool is a pool of byte buffers to reduce allocations during image encoding.
// Pre-allocated at 2MB to accommodate typical processed image sizes (up to 10MB max input).
var bufferPool = sync.Pool{
	New: func() any {
		// Pre-allocate 2MB buffer - a good balance for images up to 10MB
		// Smaller than max to avoid over-allocation, larger than typical output
		return bytes.NewBuffer(make([]byte, 0, 2*1024*1024))
	},
}

// defaultWebPEffort is libwebp's encode effort (0-6, higher = slower/smaller)
// used when SetWebPEffort is never called. 4 trades ~5% larger output for a
// >2x faster encode versus libwebp's own max (6) — see SetWebPEffort.
const defaultWebPEffort = 4

// defaultCacheTTL is used when SetCacheTTL is never called or is called with
// a non-positive value. See SetCacheTTL for why this exists as a real,
// config-driven setting instead of the hardcoded 24h it replaced.
const defaultCacheTTL = 24 * time.Hour

// VipsProcessor implements ImageProcessor using libvips
type VipsProcessor struct {
	maxFileSizeMB    int
	defaultQuality   int
	defaultFormat    ImageFormat
	supportedFormats []ImageFormat
	maxDimensions    struct{ width, height int }
	cache            Cache
	sem              chan struct{} // semaphore limiting concurrent processing
	webpEffort       int           // libwebp encode effort (0-6); see SetWebPEffort
	cacheTTL         time.Duration // per-entry LRU TTL; see SetCacheTTL
}

// NewVipsProcessor creates a new vips-based image processor
func NewVipsProcessor(maxFileSizeMB, defaultQuality int, defaultFormat ImageFormat, maxWidth, maxHeight int) *VipsProcessor {
	return &VipsProcessor{
		maxFileSizeMB:    maxFileSizeMB,
		defaultQuality:   defaultQuality,
		defaultFormat:    defaultFormat,
		supportedFormats: []ImageFormat{FormatJPEG, FormatPNG, FormatWebP, FormatHEIC, FormatAVIF},
		maxDimensions:    struct{ width, height int }{width: maxWidth, height: maxHeight},
		webpEffort:       defaultWebPEffort,
		cacheTTL:         defaultCacheTTL,
	}
}

// SetMaxConcurrency sets the maximum number of concurrent processing operations.
// Must be called before processing starts. A value of 0 means unlimited.
func (p *VipsProcessor) SetMaxConcurrency(n int) {
	if n > 0 {
		p.sem = make(chan struct{}, n)
	}
}

// SetWebPEffort sets libwebp's encode effort (0-6, higher = slower/smaller
// output). Measured locally on real BGG box art with libvips 8.18.5: effort
// 6 (libvips' own default) takes ~2.3x longer than effort 4 for a ~5% size
// gain. A value <0 is ignored (keeps defaultWebPEffort); 0 leaves the field
// unset only if explicitly passed as such by a future caller — in practice
// config always supplies a positive value.
func (p *VipsProcessor) SetWebPEffort(effort int) {
	if effort >= 0 {
		p.webpEffort = effort
	}
}

// SetCacheTTL sets how long a processed entry stays in the LRU cache before
// expiring, read from CACHE_TTL_HOURS. Previously this value was loaded from
// config but only ever fed to NewShardedCache's cleanupInterval parameter —
// the goroutine sweep frequency, not a per-entry expiry — while the actual
// TTL used in Process() was hardcoded at 24h regardless of config. So raising
// CACHE_TTL_HOURS did nothing; this setter is what makes it real.
func (p *VipsProcessor) SetCacheTTL(ttl time.Duration) {
	if ttl > 0 {
		p.cacheTTL = ttl
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

	m := metrics.Default()

	// Acquire processing slot (limits concurrent CPU-intensive operations).
	// Measured on its own label ("semaphore_wait") separately from the work
	// itself below ("transform"): under a burst of cold images, most of the
	// latency a client observes is queueing here, not libvips work — and
	// without this split the two are indistinguishable from the outside.
	if p.sem != nil {
		waitStart := time.Now()
		select {
		case p.sem <- struct{}{}:
			m.ImageProcessingDuration.WithLabelValues("semaphore_wait").Observe(time.Since(waitStart).Seconds())
			defer func() { <-p.sem }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	processStart := time.Now()

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

	// "transform" spans decode + apply-transformations + encode only — the
	// semaphore wait above is excluded and recorded separately.
	m.ImageProcessingDuration.WithLabelValues("transform").Observe(time.Since(processStart).Seconds())

	// Track processing size metrics
	m.ImageProcessingSize.WithLabelValues("input").Observe(float64(inputSize))
	m.ImageProcessingSize.WithLabelValues("output").Observe(float64(len(processedData)))

	// Cache result under the caller-provided key (skip if empty)
	if p.cache != nil && cacheKey != "" {
		_ = p.cache.Set(cacheKey, processedData, p.cacheTTL)
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
// applyTransformations runs the transformation pipeline over an image.
//
// The order is load-bearing, not incidental:
//
//  1. orientation and geometry (auto-orient, trim, crop, flip, rotate) come
//     first, so everything after works on the image as the viewer will see it;
//  2. resizing comes next, so the expensive colour work runs on the smallest
//     pixel count;
//  3. colour adjustments;
//  4. padding, so the padding is not itself scaled or colour-shifted;
//  5. the watermark last of all — its scale is relative to the width the
//     viewer actually gets, and a logo that went through the colour
//     adjustments would come out tinted by them.
func (p *VipsProcessor) applyTransformations(img *vips.Image, params *ProcessingParams) error {
	if err := applyGeometry(img, params); err != nil {
		return err
	}
	if err := p.applyResize(img, params); err != nil {
		return err
	}
	if err := applyColorAdjustments(img, params); err != nil {
		return err
	}
	if err := applyPadding(img, params); err != nil {
		return err
	}
	return applyWatermark(img, params)
}

// defaultTrimThreshold is the channel distance used when trimming is requested
// without one. Low enough to catch JPEG-noisy white borders, tight enough not
// to eat into a light-coloured subject.
const defaultTrimThreshold = 10

// applyGeometry runs the operations that change what part of the image is kept
// and which way up it is.
func applyGeometry(img *vips.Image, params *ProcessingParams) error {
	if params.AutoOrient {
		// Non-fatal: plenty of formats carry no EXIF orientation at all.
		_ = img.Autorot(nil)
	}

	if params.TrimEnabled {
		threshold := params.TrimThreshold
		if threshold == 0 {
			threshold = defaultTrimThreshold
		}
		// A trim that finds nothing is not an error: the image simply has no
		// uniform border to remove.
		left, top, width, height, err := img.FindTrim(&vips.FindTrimOptions{Threshold: threshold})
		if err == nil && width > 0 && height > 0 {
			if err := img.ExtractArea(left, top, width, height); err != nil {
				return fmt.Errorf("trim extract failed: %w", err)
			}
		}
	}

	if params.CropW > 0 && params.CropH > 0 {
		if err := img.ExtractArea(params.CropX, params.CropY, params.CropW, params.CropH); err != nil {
			return fmt.Errorf("crop failed: %w", err)
		}
	}

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

	if params.Rotate != 0 {
		if err := rotateImage(img, params.Rotate); err != nil {
			return err
		}
	}
	return nil
}

// rotateImage turns the image, using the exact rotation for right angles.
//
// vips_rotate interpolates, which for a quarter turn means resampling every
// pixel and coming out a pixel short (a 400x300 rotated 90 degrees measured
// 300x399). vips_rot is a transpose: lossless, faster, and exactly the size the
// caller expects. Right angles are also the overwhelmingly common request.
func rotateImage(img *vips.Image, degrees float64) error {
	switch normalizeAngle(degrees) {
	case 0:
		return nil
	case 90:
		return wrapRotateErr(img.Rot(vips.AngleD90))
	case 180:
		return wrapRotateErr(img.Rot(vips.AngleD180))
	case 270:
		return wrapRotateErr(img.Rot(vips.AngleD270))
	}

	return wrapRotateErr(img.Rotate(degrees, nil))
}

// normalizeAngle folds an angle into [0, 360) so that -90 and 270 take the same
// exact-rotation branch instead of only one of them doing so.
func normalizeAngle(degrees float64) float64 {
	normalized := math.Mod(degrees, 360)
	if normalized < 0 {
		normalized += 360
	}
	return normalized
}

func wrapRotateErr(err error) error {
	if err != nil {
		return fmt.Errorf("rotate failed: %w", err)
	}
	return nil
}

// applyResize scales the image, then enforces the configured ceiling.
func (p *VipsProcessor) applyResize(img *vips.Image, params *ProcessingParams) error {
	switch {
	case (params.Width > 0 || params.Height > 0) && params.Gravity != "":
		if err := smartResize(img, params); err != nil {
			return err
		}
	case params.Width > 0 || params.Height > 0:
		safeParams := *params
		safeParams.Width, safeParams.Height = p.safeResizeDimensions(img.Width(), img.Height(), params)
		if err := p.resizeImage(img, &safeParams); err != nil {
			return fmt.Errorf("resize failed: %w", err)
		}
	}

	return p.enforceMaxDimensions(img)
}

// smartResize crops to the requested box using libvips' content-aware
// strategies, so the interesting part of the image survives the crop.
func smartResize(img *vips.Image, params *ProcessingParams) error {
	interesting := vips.InterestingCentre
	switch params.Gravity {
	case "smart", "attention":
		interesting = vips.InterestingAttention
	case "entropy":
		interesting = vips.InterestingEntropy
	}

	width, height := params.Width, params.Height
	if width == 0 {
		width = img.Width()
	}
	if height == 0 {
		height = img.Height()
	}

	if err := img.ThumbnailImage(width, &vips.ThumbnailImageOptions{
		Height: height,
		Crop:   interesting,
		Size:   vips.SizeBoth,
	}); err != nil {
		return fmt.Errorf("smart resize failed: %w", err)
	}
	return nil
}

// safeResizeDimensions works out what the image may actually be resized to,
// filling in a missing dimension from the aspect ratio and refusing to upscale
// where upscaling would be pointless.
//
// The distinction that matters is between a BOX and a CAP:
//
//   - both dimensions given is an exact box (a 128x128 avatar slot), and
//     upscaling a smaller source to fill it is the whole point;
//   - one dimension given is a cap, with the other derived to preserve aspect
//     ratio. Upscaling past the source's native resolution there just produces
//     a bigger, blurrier file for no visual gain — measured: a 150x150 source
//     capped at w=600 came back 600x600 and 48% heavier before this was fixed.
//
// It is also what stops an upscaling DoS: a request for 100x100 → 10000x10000
// gets clamped back to the source's own size.
func (p *VipsProcessor) safeResizeDimensions(originalWidth, originalHeight int, params *ProcessingParams) (width, height int) {
	width, height = params.Width, params.Height
	explicitBox := width > 0 && height > 0

	switch {
	case width == 0 && height > 0:
		width = (originalWidth * height) / originalHeight
	case height == 0 && width > 0:
		height = (originalHeight * width) / originalWidth
	}

	maxWidth, maxHeight := originalWidth, originalHeight
	if explicitBox {
		if p.maxDimensions.width > 0 && p.maxDimensions.width > originalWidth {
			maxWidth = p.maxDimensions.width
		}
		if p.maxDimensions.height > 0 && p.maxDimensions.height > originalHeight {
			maxHeight = p.maxDimensions.height
		}
	}

	// Clamping scales BOTH dimensions so the aspect ratio survives.
	if width > maxWidth {
		scale := float64(maxWidth) / float64(width)
		width = maxWidth
		height = int(float64(height) * scale)
	}
	if height > maxHeight {
		scale := float64(maxHeight) / float64(height)
		height = maxHeight
		width = int(float64(width) * scale)
	}
	return width, height
}

// enforceMaxDimensions is the final safeguard against an oversized result. It
// should rarely fire — safeResizeDimensions already clamps the common paths —
// but it also covers images that arrive oversized and are never resized.
func (p *VipsProcessor) enforceMaxDimensions(img *vips.Image) error {
	if img.Width() <= p.maxDimensions.width && img.Height() <= p.maxDimensions.height {
		return nil
	}

	scale := 1.0
	if img.Width() > p.maxDimensions.width {
		scale = float64(p.maxDimensions.width) / float64(img.Width())
	}
	if img.Height() > p.maxDimensions.height {
		if heightScale := float64(p.maxDimensions.height) / float64(img.Height()); heightScale < scale {
			scale = heightScale
		}
	}

	if err := img.Resize(scale, nil); err != nil {
		return fmt.Errorf("max dimensions resize failed: %w", err)
	}
	return nil
}

// applyColorAdjustments runs the tone and detail operations.
//
// Brightness and contrast are expressed as percentages around 0, so they map
// onto a linear multiplier; contrast pivots around mid-grey (128) so that
// raising it darkens shadows and lifts highlights instead of just brightening
// everything.
func applyColorAdjustments(img *vips.Image, params *ProcessingParams) error {
	if params.Brightness != 0 {
		multiplier := 1.0 + (params.Brightness / 100.0)
		if err := img.Linear([]float64{multiplier, multiplier, multiplier}, []float64{0, 0, 0}, nil); err != nil {
			return fmt.Errorf("brightness failed: %w", err)
		}
	}

	if params.Contrast != 0 {
		multiplier := 1.0 + (params.Contrast / 100.0)
		offset := 128.0 * (1.0 - multiplier)
		if err := img.Linear([]float64{multiplier, multiplier, multiplier}, []float64{offset, offset, offset}, nil); err != nil {
			return fmt.Errorf("contrast failed: %w", err)
		}
	}

	// Gamma 1.0 is the identity, so it is treated as "not requested".
	if params.Gamma != 0 && params.Gamma != 1.0 {
		if err := img.Gamma(&vips.GammaOptions{Exponent: params.Gamma}); err != nil {
			return fmt.Errorf("gamma failed: %w", err)
		}
	}

	if err := applySaturationAndHue(img, params); err != nil {
		return err
	}

	if params.Blur > 0 {
		if err := img.Gaussblur(params.Blur/2.0, nil); err != nil {
			return fmt.Errorf("blur failed: %w", err)
		}
	}

	if params.Sharpen > 0 {
		// The parameter is a 0-100 dial, not a sigma. libvips' own default
		// sigma is 0.5, which is barely visible; 3.0 is where the halo starts
		// to show on a photograph. Passing nil here — which is what this used
		// to do — ignored the requested amount entirely and always sharpened
		// by the default.
		opts := vips.DefaultSharpenOptions()
		opts.Sigma = minSharpenSigma + (params.Sharpen/100.0)*(maxSharpenSigma-minSharpenSigma)
		if err := img.Sharpen(opts); err != nil {
			return fmt.Errorf("sharpen failed: %w", err)
		}
	}
	return nil
}

// Sharpen maps the 0-100 request onto a libvips sigma in this range.
const (
	minSharpenSigma = 0.5
	maxSharpenSigma = 3.0
)

// applySaturationAndHue rotates hue and scales chroma.
//
// Both are done in LCh, where chroma and hue are their own bands, so each is a
// single multiply-and-add rather than a matrix over RGB. The image is converted
// back to the space it arrived in: leaving it in LCh would change what the
// encoder writes out.
//
// vips_colourspace carries extra bands through, so an image with an alpha
// channel keeps it — which is why the coefficient arrays are sized from the
// band count *after* the conversion, not before.
func applySaturationAndHue(img *vips.Image, params *ProcessingParams) error {
	if params.Saturation == 0 && params.Hue == 0 {
		return nil
	}

	original := img.Interpretation()
	if err := img.Colourspace(vips.InterpretationLch, nil); err != nil {
		return fmt.Errorf("saturation colourspace failed: %w", err)
	}

	bands := img.Bands()
	if bands < 3 {
		// Not something LCh conversion produces, but bailing out beats
		// indexing past the end of the coefficient arrays.
		return fmt.Errorf("unexpected band count after LCh conversion: %d", bands)
	}

	multipliers := make([]float64, bands)
	offsets := make([]float64, bands)
	for i := range multipliers {
		multipliers[i] = 1
	}

	if params.Saturation != 0 {
		// -100 drains the colour completely, 0 is the identity, and the
		// documented ceiling of 500 is a six-fold boost.
		multipliers[1] = 1.0 + (params.Saturation / 100.0)
		if multipliers[1] < 0 {
			multipliers[1] = 0
		}
	}
	if params.Hue != 0 {
		// The h band is in degrees, and the conversion back is trigonometric,
		// so a value that lands outside 0-360 wraps on its own.
		offsets[2] = float64(params.Hue)
	}

	if err := img.Linear(multipliers, offsets, nil); err != nil {
		return fmt.Errorf("saturation/hue failed: %w", err)
	}

	// Back to where it came from — but not every interpretation has a route
	// from LCh. An image that libvips tagged "multiband" (a TIFF with an
	// unusual band layout, say) fails here, and the transformation itself has
	// already succeeded by this point, so falling back to sRGB serves the image
	// rather than turning a saturation request into a 422. sRGB is also what
	// every encoder downstream wants.
	if err := img.Colourspace(original, nil); err != nil {
		if srgbErr := img.Colourspace(vips.InterpretationSrgb, nil); srgbErr != nil {
			return fmt.Errorf("saturation colourspace restore failed: %w", srgbErr)
		}
	}
	return nil
}

// applyPadding grows the canvas around the image, filling the new area with the
// requested background colour.
func applyPadding(img *vips.Image, params *ProcessingParams) error {
	if params.PaddingTop == 0 && params.PaddingRight == 0 &&
		params.PaddingBottom == 0 && params.PaddingLeft == 0 {
		return nil
	}

	newWidth := img.Width() + params.PaddingLeft + params.PaddingRight
	newHeight := img.Height() + params.PaddingTop + params.PaddingBottom

	if err := img.Embed(params.PaddingLeft, params.PaddingTop, newWidth, newHeight, &vips.EmbedOptions{
		Extend:     vips.ExtendBackground,
		Background: parseHexColor(params.PaddingColor),
	}); err != nil {
		return fmt.Errorf("padding failed: %w", err)
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

	// explicitBox is true only when the caller gave both dimensions — a
	// genuine "crop to this exact box" request (e.g. a 128x128 avatar slot),
	// where filling the box (and upscaling a smaller source if needed) is
	// the point. When only one dimension is given, the other is derived
	// below to preserve aspect ratio — that's a resize CAP, not a box, and
	// must never upscale. See coverSize.
	explicitBox := w > 0 && h > 0

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
			Size:   coverSize(explicitBox),
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
			Size:   coverSize(explicitBox),
		})
	}
}

// coverSize picks SizeBoth for an explicit w+h box (upscaling a smaller
// source to fill it is the caller's intent) and SizeDown otherwise — when
// only one dimension was requested and the other derived proportionally,
// there is no box to fill, only a cap, so upscaling would just waste CPU
// and bytes for no visual gain (measured: a 150x150 source requested at
// w=600 with no explicit height came back 600x600 and 48% heavier).
//
// the request onto the matching vips.Size, which is all it does.
//
//nolint:revive // the bool is this function's input datum: it maps a fact about
func coverSize(explicitBox bool) vips.Size {
	if explicitBox {
		return vips.SizeBoth
	}
	return vips.SizeDown
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
			compression = min(max(9-(quality*9/100), 0), 9)
		}
		err = img.PngsaveTarget(target, &vips.PngsaveTargetOptions{
			Compression: compression,
			Interlace:   true, // Interlaced PNG for progressive loading
			// Filter option to try all PNG filters for best compression
			// Note: Using default filter (adaptive) which works well for most cases
		})
	case FormatWebP:
		// WebP with advanced compression options.
		// Effort is configurable (see SetWebPEffort) instead of hardcoded at
		// libwebp's max — measured ~2.3x slower than effort 4 for ~5% smaller
		// output. MinSize is intentionally omitted: measured zero byte
		// savings on real BGG box art, just extra CPU.
		err = img.WebpsaveTarget(target, &vips.WebpsaveTargetOptions{
			Q:              quality,
			Lossless:       quality == 100,                 // Lossless if quality is 100
			NearLossless:   quality >= 95 && quality < 100, // Near-lossless for very high quality
			Effort:         p.webpEffort,
			SmartSubsample: quality >= 80, // Better chroma subsampling for high quality
			Mixed:          quality >= 80, // Allow mixed lossy/lossless encoding
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

// GetCacheStats returns cache statistics.
//
// With no cache configured it returns Backend "none" with the measurable fields
// set to statUnmeasured: a zeroed CacheStats would read as "an empty cache",
// which is not the same thing as "there is no cache".
func (p *VipsProcessor) GetCacheStats() cache.CacheStats {
	if p.cache != nil {
		return p.cache.Stats()
	}
	return cache.NoCacheStats()
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
// Every variant of the same object shares the `sha256(storageKey)[:32]` prefix
// that generateCacheKey builds, so sweeping the keys by that prefix is enough.
// It is O(n) over the cache's keys, but it only runs on delete and update, which
// are rare next to reads.
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
			parts = append(parts, "f"+params.Fit)
		}
	}

	// Quality and format
	if params.Quality > 0 {
		parts = append(parts, fmt.Sprintf("q%d", params.Quality))
	}
	if params.Format != "" {
		parts = append(parts, "fmt"+params.Format)
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
		parts = append(parts, "flip"+params.Flip)
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
		parts = append(parts, "grav"+params.Gravity)
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

	// The watermark keys on its source, never on its bytes: two requests for
	// different overlays must not collide, and hashing the overlay on every
	// request to find that out would cost more than the composite does.
	if params.WatermarkSource != "" {
		parts = append(parts, fmt.Sprintf("wm%s_%.2f_%s_%.2f",
			params.WatermarkSource, params.WatermarkOpacity,
			params.WatermarkPosition, params.WatermarkScale))
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
