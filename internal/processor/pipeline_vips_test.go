package processor

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"testing"

	"github.com/cshum/vipsgen/vips"
)

// These tests run the real libvips pipeline and look at pixels. Everything
// else in the package is checked without libvips, which is how a
// transformation can be "wired up" as a struct field that no stage applies.
func TestMain(m *testing.M) {
	// Same configuration as cmd/server: VectorEnabled matters because Startup
	// with a nil config silently disables libvips' SIMD paths.
	vips.Startup(&vips.Config{ConcurrencyLevel: 1, VectorEnabled: true})
	code := m.Run()
	vips.Shutdown()
	os.Exit(code)
}

// newTestImage builds a uniform image of the given colour by encoding a PNG and
// decoding it back.
//
// The round-trip is the point. vips_black produces an image tagged "multiband",
// which no decoded photograph ever is, and colourspace conversions behave
// differently on it — so a test built that way would exercise paths production
// never takes. Loading from a buffer is exactly what the delivery route does.
func newTestImage(t *testing.T, width, height int, rgb []float64) *vips.Image {
	t.Helper()

	src := image.NewRGBA(image.Rect(0, 0, width, height))
	fill := color.RGBA{R: uint8(rgb[0]), G: uint8(rgb[1]), B: uint8(rgb[2]), A: 255}
	draw.Draw(src, src.Bounds(), &image.Uniform{C: fill}, image.Point{}, draw.Src)

	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		t.Fatalf("could not encode the test png: %v", err)
	}

	img, err := vips.NewImageFromBuffer(buf.Bytes(), nil)
	if err != nil {
		t.Fatalf("could not load the test image: %v", err)
	}
	return img
}

func pixel(t *testing.T, img *vips.Image, x, y int) []float64 {
	t.Helper()
	values, err := img.Getpoint(x, y, nil)
	if err != nil {
		t.Fatalf("getpoint(%d,%d) failed: %v", x, y, err)
	}
	return values
}

// pngBytes encodes an image so it can be fed back in as watermark input, which
// is the form the handler delivers it in.
func pngBytes(t *testing.T, img *vips.Image) []byte {
	t.Helper()
	data, err := img.PngsaveBuffer(nil)
	if err != nil {
		t.Fatalf("could not encode the png: %v", err)
	}
	return data
}

// TestApplyWatermarkCompositesPixels asserts that the watermark is actually
// drawn, not just that it enters the cache key.
func TestApplyWatermarkCompositesPixels(t *testing.T) {
	base := newTestImage(t, 400, 300, []float64{120, 120, 120})
	defer base.Close()
	overlay := newTestImage(t, 120, 120, []float64{255, 255, 255})
	defer overlay.Close()

	params := &ProcessingParams{WatermarkImage: pngBytes(t, overlay)}
	if err := applyWatermark(base, params); err != nil {
		t.Fatalf("applyWatermark failed: %v", err)
	}

	// Default position and scale: an 80px overlay inset 8px from the bottom
	// right of a 400x300 image.
	corner := pixel(t, base, 360, 260)
	if corner[0] < 250 {
		t.Errorf("bottom-right corner is %v, expected the white overlay", corner)
	}

	// And nowhere else: a watermark that covered the image would also pass an
	// assertion that only looked at the corner.
	elsewhere := pixel(t, base, 20, 20)
	if elsewhere[0] != 120 {
		t.Errorf("top-left corner is %v, expected the background untouched", elsewhere)
	}
}

// TestApplyWatermarkPosition checks that the position parameter moves it.
func TestApplyWatermarkPosition(t *testing.T) {
	base := newTestImage(t, 400, 300, []float64{120, 120, 120})
	defer base.Close()
	overlay := newTestImage(t, 120, 120, []float64{255, 255, 255})
	defer overlay.Close()

	params := &ProcessingParams{
		WatermarkImage:    pngBytes(t, overlay),
		WatermarkPosition: WatermarkTopLeft,
	}
	if err := applyWatermark(base, params); err != nil {
		t.Fatalf("applyWatermark failed: %v", err)
	}

	if corner := pixel(t, base, 40, 40); corner[0] < 250 {
		t.Errorf("top-left corner is %v, expected the overlay", corner)
	}
	if corner := pixel(t, base, 360, 260); corner[0] != 120 {
		t.Errorf("bottom-right corner is %v, expected the background untouched", corner)
	}
}

// TestApplyWatermarkOpacity: half opacity over a flat background is a
// predictable midpoint, so the blend is checkable rather than merely "changed".
func TestApplyWatermarkOpacity(t *testing.T) {
	base := newTestImage(t, 400, 300, []float64{120, 120, 120})
	defer base.Close()
	overlay := newTestImage(t, 120, 120, []float64{255, 255, 255})
	defer overlay.Close()

	params := &ProcessingParams{
		WatermarkImage:   pngBytes(t, overlay),
		WatermarkOpacity: 0.5,
	}
	if err := applyWatermark(base, params); err != nil {
		t.Fatalf("applyWatermark failed: %v", err)
	}

	got := pixel(t, base, 360, 260)[0]
	if got < 180 || got > 195 {
		t.Errorf("50%% blend gave %v, expected about 187", got)
	}
}

// TestApplyWatermarkNoSourceIsNoOp: without bytes there is nothing to draw, and
// the image must come out untouched rather than erroring.
func TestApplyWatermarkNoSourceIsNoOp(t *testing.T) {
	base := newTestImage(t, 100, 100, []float64{120, 120, 120})
	defer base.Close()

	if err := applyWatermark(base, &ProcessingParams{}); err != nil {
		t.Fatalf("applyWatermark without overlay failed: %v", err)
	}
	if got := pixel(t, base, 50, 50); got[0] != 120 {
		t.Errorf("image changed without overlay: %v", got)
	}
}

// TestSaturationDrainsColour: -100 is greyscale, which is checkable exactly —
// the three channels have to converge.
func TestSaturationDrainsColour(t *testing.T) {
	img := newTestImage(t, 50, 50, []float64{200, 40, 40})
	defer img.Close()

	if err := applySaturationAndHue(img, &ProcessingParams{Saturation: -100}); err != nil {
		t.Fatalf("applySaturationAndHue failed: %v", err)
	}

	got := pixel(t, img, 25, 25)
	if len(got) < 3 {
		t.Fatalf("expected 3 bands, got %d", len(got))
	}
	// Grey means the channels agree; the rounding of the LCh round-trip leaves
	// a unit or two of slack.
	if diff := got[0] - got[1]; diff > 2 || diff < -2 {
		t.Errorf("saturation -100 gave %v, expected grey", got)
	}
	if diff := got[1] - got[2]; diff > 2 || diff < -2 {
		t.Errorf("saturation -100 gave %v, expected grey", got)
	}
}

// TestHueRotatesColour: a red rotated 120 degrees lands on green, which is the
// cheapest way to prove the rotation is applied to the h band and not to
// chroma.
func TestHueRotatesColour(t *testing.T) {
	img := newTestImage(t, 50, 50, []float64{200, 40, 40})
	defer img.Close()

	if err := applySaturationAndHue(img, &ProcessingParams{Hue: 120}); err != nil {
		t.Fatalf("applySaturationAndHue failed: %v", err)
	}

	got := pixel(t, img, 25, 25)
	if got[1] <= got[0] {
		t.Errorf("hue +120 on red gave %v, expected green to dominate", got)
	}
}

// TestSaturationAndHueNoOp: with neither requested the image must not make the
// LCh round-trip at all, which would shift the colour by a rounding error for
// no reason.
func TestSaturationAndHueNoOp(t *testing.T) {
	img := newTestImage(t, 50, 50, []float64{200, 40, 40})
	defer img.Close()

	if err := applySaturationAndHue(img, &ProcessingParams{}); err != nil {
		t.Fatalf("applySaturationAndHue failed: %v", err)
	}

	got := pixel(t, img, 25, 25)
	if got[0] != 200 || got[1] != 40 || got[2] != 40 {
		t.Errorf("image changed with nothing requested: %v", got)
	}
}

// TestRightAngleRotationIsExact guards the switch to vips_rot: the
// interpolating path turned a 400x300 into a 300x399.
func TestRightAngleRotationIsExact(t *testing.T) {
	for _, degrees := range []float64{90, 270, -90} {
		img := newTestImage(t, 400, 300, []float64{120, 120, 120})

		if err := rotateImage(img, degrees); err != nil {
			img.Close()
			t.Fatalf("rotateImage(%v) failed: %v", degrees, err)
		}
		if img.Width() != 300 || img.Height() != 400 {
			t.Errorf("rotating %v gave %dx%d, expected 300x400", degrees, img.Width(), img.Height())
		}
		img.Close()
	}
}

// TestSharpenUsesTheRequestedAmount: two different amounts must produce two
// different images; with a nil sigma libvips always uses its default and the
// parameter is a boolean wearing a range.
func TestSharpenUsesTheRequestedAmount(t *testing.T) {
	// A flat image cannot show sharpening, so this one has an edge in it.
	edge := newTestImage(t, 100, 100, []float64{128, 128, 128})
	defer edge.Close()
	white := newTestImage(t, 40, 40, []float64{255, 255, 255})
	defer white.Close()
	if err := edge.Insert(white, 30, 30, nil); err != nil {
		t.Fatalf("insert failed: %v", err)
	}
	if err := edge.Gaussblur(2, nil); err != nil {
		t.Fatalf("blur failed: %v", err)
	}

	sharpenedAt := func(amount float64) float64 {
		img, err := edge.Copy(nil)
		if err != nil {
			t.Fatalf("copy failed: %v", err)
		}
		defer img.Close()
		if err := applyColorAdjustments(img, &ProcessingParams{Sharpen: amount}); err != nil {
			t.Fatalf("applyColorAdjustments failed: %v", err)
		}
		// Sampled on the dark side of the halo, a few pixels out from the
		// edge: the edge itself is a fixed point of the unsharp mask and reads
		// the same at every sigma.
		return pixel(t, img, 26, 45)[0]
	}

	low, high := sharpenedAt(10), sharpenedAt(100)
	if low == high {
		t.Errorf("sharpen=10 and sharpen=100 gave the same pixel (%v): the value is being ignored", low)
	}
	// More sharpening digs the dark side of the halo deeper, so the stronger
	// request has to come out darker rather than merely different.
	if high >= low {
		t.Errorf("sharpen=100 gave %v and sharpen=10 gave %v: expected a stronger halo", high, low)
	}
}

// TestSaturationSurvivesAnUntaggedImage: an image libvips tagged "multiband"
// has no route back from LCh. The transformation has already been applied by
// then, so it falls back to sRGB instead of answering 422 over a colour tag.
func TestSaturationSurvivesAnUntaggedImage(t *testing.T) {
	img, err := vips.NewBlack(50, 50, &vips.BlackOptions{Bands: 3})
	if err != nil {
		t.Fatalf("could not create the image: %v", err)
	}
	defer img.Close()
	if err := img.Linear([]float64{0, 0, 0}, []float64{200, 40, 40}, nil); err != nil {
		t.Fatalf("linear failed: %v", err)
	}
	if img.Interpretation() != vips.InterpretationMultiband {
		t.Fatalf("the starting image should be multiband, it is %v", img.Interpretation())
	}

	if err := applySaturationAndHue(img, &ProcessingParams{Saturation: -100}); err != nil {
		t.Fatalf("applySaturationAndHue failed on an untagged image: %v", err)
	}
	if img.Interpretation() != vips.InterpretationSrgb {
		t.Errorf("expected the fallback to sRGB, got %v", img.Interpretation())
	}
}
