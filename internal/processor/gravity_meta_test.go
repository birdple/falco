package processor

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"strings"
	"testing"

	"github.com/cshum/vipsgen/vips"
)

// newBandedImage builds an image whose top half is one colour and bottom half
// another, so a crop can be told apart by looking at one pixel.
func newBandedImage(t *testing.T, width, height int, top, bottom color.RGBA) *vips.Image {
	t.Helper()

	src := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(src, image.Rect(0, 0, width, height/2), &image.Uniform{C: top}, image.Point{}, draw.Src)
	draw.Draw(src, image.Rect(0, height/2, width, height), &image.Uniform{C: bottom}, image.Point{}, draw.Src)

	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		t.Fatalf("no se pudo codificar el png de prueba: %v", err)
	}
	img, err := vips.NewImageFromBuffer(buf.Bytes(), nil)
	if err != nil {
		t.Fatalf("no se pudo cargar la imagen de prueba: %v", err)
	}
	return img
}

// TestSmartResizeHonoursCompassGravity is the test the defect needed: the
// parser accepted north/south/east/west and every one of them produced a centre
// crop, because libvips' Crop enum has no way to say "north" and the switch
// fell through to InterestingCentre.
func TestSmartResizeHonoursCompassGravity(t *testing.T) {
	red := color.RGBA{R: 220, A: 255}
	blue := color.RGBA{B: 220, A: 255}

	cases := []struct {
		gravity string
		// wantRed says the crop should come from the red top band.
		wantRed bool
	}{
		{"north", true},
		{"south", false},
		{"northwest", true},
		{"southeast", false},
	}

	for _, tc := range cases {
		t.Run(tc.gravity, func(t *testing.T) {
			// 400x400, red on top, blue underneath. Cropping to a 400x100 strip
			// leaves plenty of room to tell the halves apart.
			img := newBandedImage(t, 400, 400, red, blue)
			defer img.Close()

			params := &ProcessingParams{Width: 400, Height: 100, Gravity: tc.gravity}
			if err := smartResize(img, params); err != nil {
				t.Fatalf("smartResize(%s): %v", tc.gravity, err)
			}

			if img.Width() != 400 || img.Height() != 100 {
				t.Fatalf("crop box: got %dx%d, want 400x100", img.Width(), img.Height())
			}

			px := pixel(t, img, 200, 50)
			gotRed := px[0] > px[2]
			if gotRed != tc.wantRed {
				band := "blue"
				if tc.wantRed {
					band = "red"
				}
				t.Fatalf("gravity=%s cropped the wrong band: pixel is r=%.0f b=%.0f, expected the %s half",
					tc.gravity, px[0], px[2], band)
			}
		})
	}
}

// TestSmartResizeContentAwareStillWorks guards the other family: "smart" and
// "entropy" must keep going through libvips, not through the positional path.
func TestSmartResizeContentAwareStillWorks(t *testing.T) {
	for _, gravity := range []string{"smart", "attention", "entropy"} {
		t.Run(gravity, func(t *testing.T) {
			img := newTestImage(t, 400, 300, []float64{120, 120, 120})
			defer img.Close()

			params := &ProcessingParams{Width: 200, Height: 200, Gravity: gravity}
			if err := smartResize(img, params); err != nil {
				t.Fatalf("smartResize(%s): %v", gravity, err)
			}
			if img.Width() != 200 || img.Height() != 200 {
				t.Fatalf("got %dx%d, want 200x200", img.Width(), img.Height())
			}
		})
	}
}

// TestEncodeImageStripsOrKeepsMetadata covers the second phantom parameter:
// `meta=1` was parsed in delivery and proxy, travelled into ProcessingParams
// and was never read by the encoder, so it could not change anything.
func TestEncodeImageStripsOrKeepsMetadata(t *testing.T) {
	p := &VipsProcessor{webpEffort: 4}
	const field = "exif-ifd0-ImageDescription"

	encode := func(keep vips.Keep) []byte {
		img := newTestImage(t, 64, 64, []float64{200, 100, 50})
		defer img.Close()
		img.SetString(field, "falco metadata probe")

		data, _, err := p.encodeImage(img, FormatJPEG, 90, keep)
		if err != nil {
			t.Fatalf("encodeImage(keep=%v): %v", keep, err)
		}
		return data
	}

	hasField := func(data []byte) bool {
		img, err := vips.NewImageFromBuffer(data, nil)
		if err != nil {
			t.Fatalf("no se pudo releer la imagen codificada: %v", err)
		}
		defer img.Close()
		_, err = img.GetString(field)
		return err == nil
	}

	if hasField(encode(keepMode(&ProcessingParams{StripMetadata: true}))) {
		t.Fatal("stripping is the default and it did not strip: metadata survived the re-encode")
	}
	if !hasField(encode(keepMode(&ProcessingParams{StripMetadata: false}))) {
		t.Fatal("meta=1 asked to keep metadata and it was dropped anyway")
	}
}

// TestGenerateCacheKeySeparatesMetadataVariants: keeping metadata produces
// different bytes, so the two variants must not share a cache entry — otherwise
// whichever was requested first would be served to everyone.
func TestGenerateCacheKeySeparatesMetadataVariants(t *testing.T) {
	stripped := &ProcessingParams{Width: 100, StripMetadata: true}
	kept := &ProcessingParams{Width: 100, StripMetadata: false}

	if generateCacheKey("k", stripped) == generateCacheKey("k", kept) {
		t.Fatal("meta variants collide in the cache: the first one requested would be served to both")
	}

	// Stripping is the default, so its key must be the one that already
	// exists: adding a marker there would cold-start every cache on deploy.
	// Only the opposite case carries the extra "meta" segment.
	if strings.Contains(generateCacheKey("k", stripped), "meta") {
		t.Fatal("the default (stripped) key changed shape; every cached variant would be orphaned")
	}
	if !strings.Contains(generateCacheKey("k", kept), "meta") {
		t.Fatal("the meta variant is not marked in its cache key")
	}
}
