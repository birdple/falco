package processor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
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
// used when SetWebPEffort is never called. libwebp's max (6) encodes much
// slower for a marginal size gain.
const defaultWebPEffort = 4

// defaultCacheTTL is used when SetCacheTTL is never called or is called with
// a non-positive value.
const defaultCacheTTL = 24 * time.Hour

// defaultMaxPixels bounds both the decoded input and the produced output, in
// pixels. libvips puts no ceiling on JPEG or PNG dimensions, so without one a
// 2 MB PNG declaring 30000×30000 decodes to gigabytes, and a request padding
// an image by tens of thousands of pixels builds a canvas just as large.
// 100 MP covers every camera short of the 200 MP phone modes.
const defaultMaxPixels = 100_000_000

// ErrImageTooLarge is returned when an input or an output exceeds the pixel
// ceiling.
var ErrImageTooLarge = errors.New("image exceeds the pixel limit")

// ErrUnsupportedInput is returned when libvips recognises the input but it is
// not one of the raster formats falco serves.
var ErrUnsupportedInput = errors.New("unsupported input format")

// allowedLoaders are the input formats Process decodes. Everything else that
// libvips could open — SVG, PDF, the matrix/CSV/raw loaders, its own .v
// format — is refused: they are either script-capable, a parser surface falco
// has no use for, or a way to declare huge images in a few bytes.
var allowedLoaders = map[vips.ImageType]bool{
	vips.ImageTypeJpeg: true,
	vips.ImageTypePng:  true,
	vips.ImageTypeWebp: true,
	vips.ImageTypeGif:  true,
	vips.ImageTypeHeif: true,
	vips.ImageTypeAvif: true,
	vips.ImageTypeTiff: true,
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
	webpEffort       int           // libwebp encode effort (0-6); see SetWebPEffort
	cacheTTL         time.Duration // per-entry LRU TTL; see SetCacheTTL
	maxPixels        int64         // input and output pixel ceiling; see SetMaxPixels

	// invalidatedAt records recent invalidations by object prefix; see
	// staleFillWindow.
	invalidatedMu sync.Mutex
	invalidatedAt map[string]time.Time
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
		maxPixels:        defaultMaxPixels,
	}
}

// SetMaxPixels sets the pixel ceiling for decoded inputs and produced outputs.
// A non-positive value keeps defaultMaxPixels.
func (p *VipsProcessor) SetMaxPixels(n int64) {
	if n > 0 {
		p.maxPixels = n
	}
}

// checkPixels refuses an image whose raster would exceed the ceiling. Width and
// height come from the header, so this costs nothing before the decode.
func (p *VipsProcessor) checkPixels(img *vips.Image, what string) error {
	if px := int64(img.Width()) * int64(img.Height()); px > p.maxPixels {
		return fmt.Errorf("%w: %s is %dx%d", ErrImageTooLarge, what, img.Width(), img.Height())
	}
	return nil
}

// SetMaxConcurrency sets the maximum number of concurrent processing operations.
// Must be called before processing starts. A value of 0 means unlimited.
func (p *VipsProcessor) SetMaxConcurrency(n int) {
	if n > 0 {
		p.sem = make(chan struct{}, n)
	}
}

// SetWebPEffort sets libwebp's encode effort (0-6, higher = slower/smaller
// output). A negative value is ignored and keeps defaultWebPEffort.
func (p *VipsProcessor) SetWebPEffort(effort int) {
	if effort >= 0 {
		p.webpEffort = effort
	}
}

// SetCacheTTL sets how long a processed entry stays in the LRU cache, read
// from CACHE_TTL_HOURS. It is the per-entry TTL; NewShardedCache's sweep
// frequency is a different knob.
func (p *VipsProcessor) SetCacheTTL(ttl time.Duration) {
	if ttl > 0 {
		p.cacheTTL = ttl
	}
}

// Process processes an image with the given parameters. cacheKey comes from
// GenerateCacheKey; "" skips caching. It only writes to the cache, never
// reads: the caller checks GetFromCache first.
func (p *VipsProcessor) Process(ctx context.Context, input io.Reader, params *ProcessingParams, cacheKey string) (*ProcessedImage, error) {
	// Read input data
	inputData, err := io.ReadAll(input)
	if err != nil {
		return nil, fmt.Errorf("failed to read input: %w", err)
	}

	// Capture input size for metrics before any potential release
	inputSize := len(inputData)

	m := metrics.Default()

	// Processing slot (bounds concurrent operations). Measured under its own
	// label ("semaphore_wait"), apart from "transform": from the outside,
	// queueing and libvips work are indistinguishable without that split.
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

	if !allowedLoaders[img.Format()] {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedInput, img.Format())
	}
	if err := p.checkPixels(img, "input"); err != nil {
		return nil, err
	}

	// Detect format before releasing input buffer
	format := p.detectFormat(inputData)

	// Apply transformations
	if err := p.applyTransformations(img, params); err != nil {
		return nil, fmt.Errorf("failed to apply transformations: %w", err)
	}
	// Padding grows the canvas after every resize limit has been applied, so
	// the output is checked on its own.
	if err := p.checkPixels(img, "output"); err != nil {
		return nil, err
	}

	// Determine output format
	outputFormat := p.determineOutputFormat(params, format)

	// Encode image (actualFormat may differ from outputFormat on fallback, e.g. AVIF→WebP)
	processedData, actualFormat, err := p.encodeImage(img, outputFormat, params.Quality, keepMode(params))
	if err != nil {
		return nil, fmt.Errorf("failed to encode image: %w", err)
	}
	// A fallback encode is not cached: the key names the requested format, and
	// a hit describes itself from that key, so a cached fallback would be WebP
	// bytes served as image/avif.
	if actualFormat != outputFormat {
		cacheKey = ""
	}
	outputFormat = actualFormat

	// "transform" spans decode + apply-transformations + encode only — the
	// semaphore wait above is excluded and recorded separately.
	m.ImageProcessingDuration.WithLabelValues("transform").Observe(time.Since(processStart).Seconds())

	// Track processing size metrics
	m.ImageProcessingSize.WithLabelValues("input").Observe(float64(inputSize))
	m.ImageProcessingSize.WithLabelValues("output").Observe(float64(len(processedData)))

	// Cache result under the caller-provided key (skip if empty, and skip a
	// fill that may be racing an invalidation of the same object).
	if p.cache != nil && cacheKey != "" && !p.recentlyInvalidated(cacheKey) {
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
	if !params.SkipAutoOrient {
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
// vips_rotate interpolates: for a quarter turn it resamples every pixel and
// comes out a pixel short. vips_rot is a transpose: lossless, faster and
// exactly the expected size. Right angles are also the common request.
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

// smartResize crops to the requested box according to the requested gravity.
//
// There are two families here and they need different machinery:
//
//   - content-aware ("smart"/"attention", "entropy") is libvips' own job:
//     ThumbnailImage picks the region.
//   - a compass point ("north", "southeast", …) is a fixed position, which
//     libvips' Crop enum cannot express — it only offers low/centre/high on
//     both axes at once. So the image is scaled to cover the box and then the
//     region is extracted by hand.
func smartResize(img *vips.Image, params *ProcessingParams) error {
	width, height := params.Width, params.Height
	if width == 0 {
		width = img.Width()
	}
	if height == 0 {
		height = img.Height()
	}

	if anchor, ok := gravityAnchor(params.Gravity); ok {
		return positionalCrop(img, width, height, anchor)
	}

	interesting := vips.InterestingCentre
	switch params.Gravity {
	case "smart", "attention":
		interesting = vips.InterestingAttention
	case "entropy":
		interesting = vips.InterestingEntropy
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

// anchor is a gravity expressed as the fraction of the leftover pixels that
// goes above and to the left of the crop: 0 keeps the near edge, 0.5 centres,
// 1 keeps the far edge.
type anchor struct{ x, y float64 }

// gravityAnchor maps a compass point to its anchor. The second return says
// whether the value was a compass point at all — "smart" and "entropy" are
// not, and are handled by libvips instead.
func gravityAnchor(gravity string) (anchor, bool) {
	switch gravity {
	case "center", "centre":
		return anchor{0.5, 0.5}, true
	case "north":
		return anchor{0.5, 0}, true
	case "south":
		return anchor{0.5, 1}, true
	case "east":
		return anchor{1, 0.5}, true
	case "west":
		return anchor{0, 0.5}, true
	case "northeast":
		return anchor{1, 0}, true
	case "northwest":
		return anchor{0, 0}, true
	case "southeast":
		return anchor{1, 1}, true
	case "southwest":
		return anchor{0, 1}, true
	}
	return anchor{}, false
}

// positionalCrop scales the image to cover width×height and extracts that box
// at the given anchor.
func positionalCrop(img *vips.Image, width, height int, at anchor) error {
	srcW, srcH := img.Width(), img.Height()
	if srcW <= 0 || srcH <= 0 {
		return errors.New("positional crop: source has no dimensions")
	}

	// Cover: the larger of the two ratios, so neither axis falls short of the
	// box and there is something to crop away on the other one.
	scale := max(float64(width)/float64(srcW), float64(height)/float64(srcH))
	if err := img.Resize(scale, &vips.ResizeOptions{Kernel: vips.KernelLanczos3}); err != nil {
		return fmt.Errorf("positional crop: resize failed: %w", err)
	}

	// Rounding can leave the scaled image a pixel short of the box; extracting
	// past the edge is an error in libvips, so the box is clamped instead.
	cropW := min(width, img.Width())
	cropH := min(height, img.Height())

	left := int(float64(img.Width()-cropW) * at.x)
	top := int(float64(img.Height()-cropH) * at.y)

	if err := img.ExtractArea(left, top, cropW, cropH); err != nil {
		return fmt.Errorf("positional crop: extract failed: %w", err)
	}
	return nil
}

// safeResizeDimensions works out what the image may actually be resized to,
// filling in a missing dimension from the aspect ratio and refusing to upscale
// where upscaling would be pointless. The distinction is between a box and a
// cap:
//
//   - both dimensions given is an exact box (a 128x128 avatar slot), and
//     upscaling a smaller source to fill it is the whole point;
//   - one dimension given is a cap, with the other derived to preserve aspect
//     ratio. Upscaling past the source's native resolution there just produces
//     a bigger, blurrier file.
//
// It is also what stops an upscaling DoS: 100x100 → 10000x10000 gets clamped
// back to the source's own size.
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
		// The parameter is a 0-100 dial, not a sigma. libvips' default sigma
		// (0.5) is barely visible; 3.0 is where the halo starts to show on a
		// photograph. With nil opts libvips ignores the requested amount.
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

// parseHexColor parses a hex colour ("FF0000" or "#FF0000") into RGB bands.
// Anything that is not six hex digits is the default white.
func parseHexColor(raw string) []float64 {
	c, _ := NormalizeHexColor(raw)
	rgb, err := hex.DecodeString(c)
	if err != nil || len(rgb) != 3 {
		return []float64{255, 255, 255}
	}
	return []float64{float64(rgb[0]), float64(rgb[1]), float64(rgb[2])}
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

// coverSize picks SizeBoth for an explicit w+h box (filling it, upscaling a
// smaller source if needed, is the caller's intent) and SizeDown otherwise:
// with one dimension there is no box to fill, only a cap, and upscaling wastes
// CPU and bytes for no visual gain.
//
//nolint:revive // the bool is the input datum: it maps a fact about the request onto a vips.Size
func coverSize(explicitBox bool) vips.Size {
	if explicitBox {
		return vips.SizeBoth
	}
	return vips.SizeDown
}

// encodeImage encodes the image to the specified format.
// Returns the encoded bytes and the actual format used (may differ from requested if fallback occurred).
func (p *VipsProcessor) encodeImage(img *vips.Image, format ImageFormat, quality int, keep vips.Keep) ([]byte, ImageFormat, error) {
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
			Keep:               keep,
		})
	case FormatPNG:
		// PNG compression level (0-9)
		// Map quality (0-100) to compression effort (9=best compression, 0=fastest)
		compression := 6 // Default balanced
		if quality < 100 {
			compression = min(max(9-(quality*9/100), 0), 9)
		}
		// Not interlaced: libvips' interlaced PNG writer holds the whole
		// image in memory, and Adam7 makes the file larger for a progressive
		// display browsers barely use.
		err = img.PngsaveTarget(target, &vips.PngsaveTargetOptions{
			Compression: compression,
			Keep:        keep,
		})
	case FormatWebP:
		// WebP. Effort is configurable (SetWebPEffort) rather than libwebp's
		// max, which encodes much slower for a marginal gain. MinSize is
		// omitted: on real box art it saves no bytes and costs CPU.
		err = img.WebpsaveTarget(target, &vips.WebpsaveTargetOptions{
			Q:              quality,
			Lossless:       quality == 100,                 // Lossless if quality is 100
			NearLossless:   quality >= 95 && quality < 100, // Near-lossless for very high quality
			Effort:         p.webpEffort,
			SmartSubsample: quality >= 80, // Better chroma subsampling for high quality
			Mixed:          quality >= 80, // Allow mixed lossy/lossless encoding
			Keep:           keep,
		})
	case FormatHEIC:
		// HEIC with optimization
		err = img.HeifsaveTarget(target, &vips.HeifsaveTargetOptions{
			Q:        quality,
			Lossless: quality == 100, // Lossless if quality is 100
			Effort:   8,              // Encoding effort (0-9, higher=slower but better)
			Keep:     keep,
			// Note: Chroma subsampling is handled automatically by libvips
		})
	case FormatAVIF:
		// AVIF requires HeifCompressionAv1 to produce actual AV1-encoded AVIF
		err = img.HeifsaveTarget(target, &vips.HeifsaveTargetOptions{
			Q:           quality,
			Lossless:    quality == 100,
			Effort:      6,
			Compression: vips.HeifCompressionAv1,
			Keep:        keep,
		})
		if err != nil {
			// Log the fallback so clients can detect AV1 support issues
			logger.Warn().Err(err).Msg("AVIF encoding failed, falling back to WebP")
			buf.Reset()
			format = FormatWebP // Update format so Content-Type is correct
			err = img.WebpsaveTarget(target, &vips.WebpsaveTargetOptions{
				Q:    quality,
				Keep: keep,
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

// keepMode says which metadata survives the re-encode. vips' zero value is an
// empty bitfield that keeps nothing, so it has to be set explicitly for
// `?meta=1` to have any effect. Stripping stays the default: a CDN origin
// should not hand out the camera GPS coordinates baked into an upload.
func keepMode(params *ProcessingParams) vips.Keep {
	if params.KeepMetadata {
		return vips.KeepAll
	}
	return vips.KeepNone
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

// GenerateCacheKey builds a deterministic cache key from an object key and
// processing parameters. The object key names the original — the caller
// includes the backend it lives in, not just its storage key — so the cache
// can be checked before any I/O.
func (p *VipsProcessor) GenerateCacheKey(objectKey string, params *ProcessingParams) string {
	return generateCacheKey(objectKey, params)
}

// staleFillWindow is how long after an invalidation a cache fill for the same
// object is refused. A request that read the original just before a delete or
// update finishes its encode after the invalidation ran; without this it would
// put the old bytes back for a whole TTL. It covers the longest a fetch plus a
// process can take on the delivery path.
const staleFillWindow = time.Minute

// InvalidateCache drops every cached variant of the given objects and returns
// how many entries were removed.
//
// Every variant of an object shares the prefix generateCacheKey derives from
// its object key, so one pass over the cache's keys handles any number of
// objects — a prefix delete of thousands of keys costs one walk, not one per
// key (on Redis, one SCAN rather than thousands).
func (p *VipsProcessor) InvalidateCache(objectKeys ...string) int {
	if len(objectKeys) == 0 {
		return 0
	}
	prefixes := make(map[string]bool, len(objectKeys))
	for _, k := range objectKeys {
		prefixes[objectKeyPrefix(k)] = true
	}
	p.markInvalidated(prefixes)

	if p.cache == nil {
		return 0
	}
	removed := 0
	for _, key := range p.cache.Keys() {
		if len(key) >= objectPrefixLen && prefixes[key[:objectPrefixLen]] {
			p.cache.Delete(key)
			removed++
		}
	}
	return removed
}

// markInvalidated records when each prefix was last invalidated, pruning
// records that have aged out of the window.
func (p *VipsProcessor) markInvalidated(prefixes map[string]bool) {
	now := time.Now()
	p.invalidatedMu.Lock()
	defer p.invalidatedMu.Unlock()
	if p.invalidatedAt == nil {
		p.invalidatedAt = make(map[string]time.Time)
	}
	for prefix, at := range p.invalidatedAt {
		if now.Sub(at) > staleFillWindow {
			delete(p.invalidatedAt, prefix)
		}
	}
	for prefix := range prefixes {
		p.invalidatedAt[prefix] = now
	}
}

// recentlyInvalidated reports whether a cache key's object was invalidated
// within staleFillWindow.
func (p *VipsProcessor) recentlyInvalidated(cacheKey string) bool {
	if len(cacheKey) < objectPrefixLen {
		return false
	}
	p.invalidatedMu.Lock()
	defer p.invalidatedMu.Unlock()
	at, ok := p.invalidatedAt[cacheKey[:objectPrefixLen]]
	return ok && time.Since(at) <= staleFillWindow
}

// PurgeCache drops every cached variant and returns how many entries were
// removed.
//
// The count comes from walking the keys rather than from the cache's own
// Clear(), which reports nothing: an operator pressing "purge" has to be able
// to tell an empty cache from a purge that did not run.
func (p *VipsProcessor) PurgeCache() int {
	if p.cache == nil {
		return 0
	}
	keys := p.cache.Keys()
	for _, key := range keys {
		p.cache.Delete(key)
	}
	return len(keys)
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

// objectPrefixLen is the length of the object part of every cache key.
const objectPrefixLen = 32

// objectKeyPrefix is the part of a cache key that names the original: a
// truncated SHA-256 of the object key, so the key length stays bounded and the
// object key can carry any characters.
func objectKeyPrefix(objectKey string) string {
	sum := sha256.Sum256([]byte(objectKey))
	return hex.EncodeToString(sum[:])[:objectPrefixLen]
}

// generateCacheKey builds a cache key from an object key plus every parameter
// that changes the output bytes.
//
// Two different requests must never share a key — the first one rendered would
// be served for both — so the encoding is unambiguous:
//
//   - floats are written at full precision (rotate=45.4 and rotate=45 used to
//     share "rot45");
//   - enum-like fields are written only when they are plain tokens, and hashed
//     otherwise, so no value can contain the "_" separator;
//   - free-form fields (the padding colour, the watermark source) are
//     normalised or hashed, so a crafted pad_color could no longer forge the
//     segments of a watermarked key and plant an unwatermarked image under it.
func generateCacheKey(objectKey string, params *ProcessingParams) string {
	parts := []string{objectKeyPrefix(objectKey)}

	// Size parameters
	if params.Width > 0 || params.Height > 0 {
		parts = append(parts, fmt.Sprintf("w%d_h%d", params.Width, params.Height))
		if params.Fit != "" {
			parts = append(parts, "f"+keyToken(params.Fit))
		}
	}

	// Quality and format
	if params.Quality > 0 {
		parts = append(parts, fmt.Sprintf("q%d", params.Quality))
	}
	if params.Format != "" {
		parts = append(parts, "fmt"+keyToken(params.Format))
	}

	// Crop parameters
	if params.CropW > 0 || params.CropH > 0 {
		parts = append(parts, fmt.Sprintf("crop%d_%d_%d_%d", params.CropX, params.CropY, params.CropW, params.CropH))
	}

	// Rotation and flip
	if params.Rotate != 0 {
		parts = append(parts, "rot"+keyFloat(params.Rotate))
	}
	if params.Flip != "" {
		parts = append(parts, "flip"+keyToken(params.Flip))
	}

	// Color adjustments
	if params.Brightness != 0 {
		parts = append(parts, "br"+keyFloat(params.Brightness))
	}
	if params.Contrast != 0 {
		parts = append(parts, "con"+keyFloat(params.Contrast))
	}
	if params.Gamma != 0 {
		parts = append(parts, "gam"+keyFloat(params.Gamma))
	}
	if params.Saturation != 0 {
		parts = append(parts, "sat"+keyFloat(params.Saturation))
	}
	if params.Hue != 0 {
		parts = append(parts, fmt.Sprintf("hue%d", params.Hue))
	}

	// Effects
	if params.Blur > 0 {
		parts = append(parts, "blur"+keyFloat(params.Blur))
	}
	if params.Sharpen > 0 {
		parts = append(parts, "sharp"+keyFloat(params.Sharpen))
	}

	// Extended parameters
	if params.Gravity != "" {
		parts = append(parts, "grav"+keyToken(params.Gravity))
	}
	if params.TrimEnabled {
		parts = append(parts, "trim"+keyFloat(params.TrimThreshold))
	}
	if params.PaddingTop > 0 || params.PaddingRight > 0 || params.PaddingBottom > 0 || params.PaddingLeft > 0 {
		color, _ := NormalizeHexColor(params.PaddingColor)
		parts = append(parts, fmt.Sprintf("pad%d_%d_%d_%d_%s",
			params.PaddingTop, params.PaddingRight, params.PaddingBottom, params.PaddingLeft, color))
	}
	if !params.SkipAutoOrient {
		parts = append(parts, "orient")
	}
	// Keeping metadata produces different bytes, so it has to be part of the
	// key. Stripping is the default, so only the opposite is recorded.
	if params.KeepMetadata {
		parts = append(parts, "meta")
	}

	// The watermark keys on its source, never on its bytes: two requests for
	// different overlays must not collide, and hashing the overlay on every
	// request to find that out would cost more than the composite does. The
	// source itself is hashed because it is free-form text.
	if params.WatermarkSource != "" {
		sum := sha256.Sum256([]byte(params.WatermarkSource))
		parts = append(parts, "wm"+hex.EncodeToString(sum[:8]),
			keyFloat(params.WatermarkOpacity), keyToken(params.WatermarkPosition), keyFloat(params.WatermarkScale))
	}

	return strings.Join(parts, "_")
}

// keyFloat formats a float for a cache key at full precision.
func keyFloat(v float64) string {
	return strconv.FormatFloat(v, 'g', -1, 64)
}

// keyToken returns s when it is a plain lowercase token, and a short hash of it
// otherwise. Callers validate these fields against fixed sets, so the hash only
// matters if a value slips past them — and then it still cannot inject "_".
func keyToken(s string) string {
	for _, c := range s {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			sum := sha256.Sum256([]byte(s))
			return "x" + hex.EncodeToString(sum[:6])
		}
	}
	return s
}

// NormalizeHexColor validates a six-digit hex colour, with or without a
// leading "#", and returns it upper-cased without the "#". An empty or invalid
// value returns the default white and false.
func NormalizeHexColor(raw string) (string, bool) {
	c := strings.TrimPrefix(raw, "#")
	if len(c) != 6 {
		return defaultPadColor, false
	}
	for _, r := range c {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return defaultPadColor, false
		}
	}
	return strings.ToUpper(c), true
}

// defaultPadColor is the padding colour when none (or an invalid one) is given.
const defaultPadColor = "FFFFFF"

// nopWriteCloser wraps a writer to add a no-op Close method
type nopWriteCloser struct {
	io.Writer
}

func (nopWriteCloser) Close() error {
	return nil
}
