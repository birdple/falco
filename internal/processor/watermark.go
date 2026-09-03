package processor

import (
	"fmt"

	"github.com/cshum/vipsgen/vips"
)

// Watermark defaults. Both exist because a request that asks for a watermark
// without saying how big or how faint it should be still has to produce a
// sensible image, not a full-size opaque overlay.
const (
	// defaultWatermarkScale is the overlay width as a fraction of the final
	// image width. A fifth is large enough to read on a thumbnail and small
	// enough not to take over a full-size render.
	defaultWatermarkScale = 0.2

	// watermarkMarginRatio insets the overlay from the edge by this fraction of
	// the image width, so a corner watermark does not sit flush against it.
	watermarkMarginRatio = 0.02

	// minWatermarkMargin keeps the inset visible on small renders, where 2% of
	// the width rounds down to nothing.
	minWatermarkMargin = 4
)

// Watermark positions.
const (
	WatermarkTopLeft     = "top-left"
	WatermarkTopRight    = "top-right"
	WatermarkBottomLeft  = "bottom-left"
	WatermarkBottomRight = "bottom-right"
	WatermarkCenter      = "center"
)

// IsValidWatermarkPosition reports whether a position name is one Falco places.
func IsValidWatermarkPosition(position string) bool {
	switch position {
	case WatermarkTopLeft, WatermarkTopRight, WatermarkBottomLeft, WatermarkBottomRight, WatermarkCenter:
		return true
	default:
		return false
	}
}

// applyWatermark composites the resolved overlay onto the image.
//
// It runs last in the pipeline — after resize and after padding — for two
// reasons: the scale is relative to the width the viewer actually gets, and a
// watermark that went through the colour adjustments would come out tinted by
// them, which is never what a logo is for.
//
// The bytes are already in params: the processor does no I/O, so fetching the
// overlay from storage or from the network is the handler's job.
func applyWatermark(img *vips.Image, params *ProcessingParams) error {
	if len(params.WatermarkImage) == 0 {
		return nil
	}

	overlay, err := vips.NewImageFromBuffer(params.WatermarkImage, nil)
	if err != nil {
		return fmt.Errorf("watermark decode failed: %w", err)
	}
	defer overlay.Close()

	if err := resizeWatermark(overlay, img.Width(), params.WatermarkScale); err != nil {
		return err
	}
	if err := fadeWatermark(overlay, params.WatermarkOpacity); err != nil {
		return err
	}

	x, y := watermarkOrigin(params.WatermarkPosition, img.Width(), img.Height(), overlay.Width(), overlay.Height())
	if err := img.Composite2(overlay, vips.BlendModeOver, &vips.Composite2Options{
		X: x,
		Y: y,
		// sRGB rather than the compositing default: the base image is already
		// in sRGB at this point, and letting vips pick a working space here has
		// it convert both images back and forth for no visible gain.
		CompositingSpace: vips.InterpretationSrgb,
	}); err != nil {
		return fmt.Errorf("watermark composite failed: %w", err)
	}
	return nil
}

// resizeWatermark scales the overlay to a fraction of the base image's width,
// keeping its aspect ratio.
func resizeWatermark(overlay *vips.Image, baseWidth int, scale float64) error {
	if scale <= 0 || scale > 1 {
		scale = defaultWatermarkScale
	}

	target := int(float64(baseWidth) * scale)
	if target < 1 {
		target = 1
	}
	// ThumbnailImage only ever shrinks here in practice, but it is also what
	// upscales a small logo onto a large render without going through the
	// resize kernel twice.
	if err := overlay.ThumbnailImage(target, nil); err != nil {
		return fmt.Errorf("watermark resize failed: %w", err)
	}
	return nil
}

// fadeWatermark multiplies the overlay's alpha channel.
//
// An opacity of 0 means "not requested" rather than "invisible": a request that
// went to the trouble of naming a watermark did not mean to make it disappear,
// and a caller who wants no watermark simply omits it.
func fadeWatermark(overlay *vips.Image, opacity float64) error {
	if opacity <= 0 || opacity >= 1 {
		return nil
	}

	// A JPEG logo has no alpha channel to fade, so it gets one first.
	if !overlay.HasAlpha() {
		if err := overlay.Addalpha(); err != nil {
			return fmt.Errorf("watermark alpha failed: %w", err)
		}
	}

	bands := overlay.Bands()
	multipliers := make([]float64, bands)
	offsets := make([]float64, bands)
	for i := range multipliers {
		multipliers[i] = 1
	}
	// Only the last band, which is the alpha: scaling the colour bands would
	// darken the logo instead of fading it.
	multipliers[bands-1] = opacity

	if err := overlay.Linear(multipliers, offsets, nil); err != nil {
		return fmt.Errorf("watermark opacity failed: %w", err)
	}
	return nil
}

// watermarkOrigin returns the top-left corner at which the overlay is placed.
//
// An overlay larger than the image (a scale of 1 against a padded canvas, say)
// would produce a negative origin, which vips reads as a crop of the overlay
// rather than an error. Clamping to zero keeps the visible part anchored at the
// top-left instead of silently cutting the logo's own left edge off.
func watermarkOrigin(position string, baseW, baseH, overlayW, overlayH int) (x, y int) {
	margin := int(float64(baseW) * watermarkMarginRatio)
	if margin < minWatermarkMargin {
		margin = minWatermarkMargin
	}

	switch position {
	case WatermarkTopLeft:
		x, y = margin, margin
	case WatermarkTopRight:
		x, y = baseW-overlayW-margin, margin
	case WatermarkBottomLeft:
		x, y = margin, baseH-overlayH-margin
	case WatermarkCenter:
		x, y = (baseW-overlayW)/2, (baseH-overlayH)/2
	default: // bottom-right is the conventional place for a logo
		x, y = baseW-overlayW-margin, baseH-overlayH-margin
	}

	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	return x, y
}
