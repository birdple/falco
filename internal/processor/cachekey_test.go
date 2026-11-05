package processor

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cshum/vipsgen/vips"

	"github.com/birdple/falco/internal/cache"
)

// TestGenerateCacheKey_EveryOutputFieldChangesTheKey walks every field of
// ProcessingParams. A field that changes the output bytes but not the key
// makes two different renders share a cache entry, and whichever is requested
// first is served for both. Fields that do not reach the bytes are listed
// explicitly, so a new field has to be classified to make this test pass.
func TestGenerateCacheKey_EveryOutputFieldChangesTheKey(t *testing.T) {
	notInKey := map[string]string{
		"MaxAge":         "caching directive, applied per response",
		"SMaxAge":        "caching directive, applied per response",
		"WatermarkImage": "the overlay bytes; the key uses WatermarkSource",
	}
	// Fields only written when another one enables them.
	needs := map[string]func(*ProcessingParams){
		"Fit":               func(p *ProcessingParams) { p.Width = 100 },
		"CropX":             func(p *ProcessingParams) { p.CropW, p.CropH = 10, 10 },
		"CropY":             func(p *ProcessingParams) { p.CropW, p.CropH = 10, 10 },
		"TrimThreshold":     func(p *ProcessingParams) { p.TrimEnabled = true },
		"PaddingColor":      func(p *ProcessingParams) { p.PaddingTop = 1 },
		"WatermarkOpacity":  func(p *ProcessingParams) { p.WatermarkSource = "id:logo" },
		"WatermarkPosition": func(p *ProcessingParams) { p.WatermarkSource = "id:logo" },
		"WatermarkScale":    func(p *ProcessingParams) { p.WatermarkSource = "id:logo" },
	}

	typ := reflect.TypeFor[ProcessingParams]()
	for i := range typ.NumField() {
		field := typ.Field(i)
		if _, skip := notInKey[field.Name]; skip {
			continue
		}
		t.Run(field.Name, func(t *testing.T) {
			base := ProcessingParams{}
			if prep, ok := needs[field.Name]; ok {
				prep(&base)
			}
			changed := base
			v := reflect.ValueOf(&changed).Elem().Field(i)
			switch v.Kind() {
			case reflect.Int:
				v.SetInt(7)
			case reflect.Float64:
				v.SetFloat(0.7)
			case reflect.Bool:
				v.SetBool(!v.Bool())
			case reflect.String:
				v.SetString("north")
				if field.Name == "PaddingColor" {
					v.SetString("00FF00")
				}
			default:
				t.Fatalf("unhandled kind %s; classify the field", v.Kind())
			}
			if generateCacheKey("obj", &base) == generateCacheKey("obj", &changed) {
				t.Fatalf("%s changes the output but not the cache key", field.Name)
			}
		})
	}
}

func TestGenerateCacheKey_FloatsAtFullPrecision(t *testing.T) {
	pairs := []struct{ a, b ProcessingParams }{
		{ProcessingParams{Rotate: 45}, ProcessingParams{Rotate: 45.4}},
		{ProcessingParams{Gamma: 1}, ProcessingParams{Gamma: 1.04}},
		{ProcessingParams{Brightness: 0.3}, ProcessingParams{Brightness: 0.4}},
		{ProcessingParams{Blur: 1}, ProcessingParams{Blur: 1.04}},
		{
			ProcessingParams{WatermarkSource: "id:logo", WatermarkOpacity: 0.501},
			ProcessingParams{WatermarkSource: "id:logo", WatermarkOpacity: 0.5},
		},
	}
	for _, p := range pairs {
		if generateCacheKey("obj", &p.a) == generateCacheKey("obj", &p.b) {
			t.Fatalf("%+v and %+v share a cache key", p.a, p.b)
		}
	}
}

// A pad_color crafted to look like the watermark segments must not produce the
// key of a watermarked request: that let an unwatermarked render be planted
// under the watermarked URL.
func TestGenerateCacheKey_PadColorCannotForgeSegments(t *testing.T) {
	forged := &ProcessingParams{PaddingTop: 1, SkipAutoOrient: true, PaddingColor: "FFFFFF_wmid:logo_0.00__0.00"}
	real := &ProcessingParams{PaddingTop: 1, SkipAutoOrient: true, WatermarkSource: "id:logo"}
	if generateCacheKey("obj", forged) == generateCacheKey("obj", real) {
		t.Fatal("a crafted pad_color reproduces a watermarked cache key")
	}
	if strings.Contains(generateCacheKey("obj", forged), "wmid") {
		t.Fatal("pad_color reached the key unnormalised")
	}
}

func TestGenerateCacheKey_ObjectsDoNotShareKeys(t *testing.T) {
	p := &ProcessingParams{Width: 100}
	if generateCacheKey("bucket-a\x00avatar", p) == generateCacheKey("bucket-b\x00avatar", p) {
		t.Fatal("the same storage key in two buckets shares a cache key")
	}
}

func TestNormalizeHexColor(t *testing.T) {
	cases := map[string]struct {
		want string
		ok   bool
	}{
		"ff0000":  {"FF0000", true},
		"#00ff00": {"00FF00", true},
		"":        {"FFFFFF", false},
		"zzzzzz":  {"FFFFFF", false},
		"fff":     {"FFFFFF", false},
		"FF00001": {"FFFFFF", false},
	}
	for in, want := range cases {
		got, ok := NormalizeHexColor(in)
		if got != want.want || ok != want.ok {
			t.Errorf("NormalizeHexColor(%q) = %q, %v; want %q, %v", in, got, ok, want.want, want.ok)
		}
	}
}

func TestInvalidateCache_OnePassAndStaleFillRefused(t *testing.T) {
	p := NewVipsProcessor(10, 85, FormatWebP, 4096, 4096)
	c := cache.NewLRUCache(1<<20, time.Minute)
	defer c.Stop()
	p.SetCache(c)

	keyA := generateCacheKey("ns\x00a", &ProcessingParams{Width: 100})
	keyA2 := generateCacheKey("ns\x00a", &ProcessingParams{Width: 200})
	keyB := generateCacheKey("ns\x00b", &ProcessingParams{Width: 100})
	for _, k := range []string{keyA, keyA2, keyB} {
		_ = c.Set(k, []byte("x"), time.Minute)
	}

	if n := p.InvalidateCache("ns\x00a"); n != 2 {
		t.Fatalf("removed %d entries, want 2", n)
	}
	if _, ok := c.Get(keyB); !ok {
		t.Fatal("an unrelated object was invalidated")
	}

	// A render that started before the invalidation must not put the old
	// bytes back.
	img := encodedPNG(t, 32, 32)
	if _, err := p.Process(context.Background(), bytes.NewReader(img), &ProcessingParams{Width: 100}, keyA); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Get(keyA); ok {
		t.Fatal("a fill racing an invalidation repopulated the cache")
	}
}

func TestProcess_RefusesOversizedInputAndOutput(t *testing.T) {
	p := NewVipsProcessor(10, 85, FormatWebP, 4096, 4096)
	p.SetMaxPixels(64 * 64)

	if _, err := p.Process(context.Background(), bytes.NewReader(encodedPNG(t, 65, 64)), &ProcessingParams{}, ""); !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("oversized input: got %v, want ErrImageTooLarge", err)
	}
	padded := &ProcessingParams{PaddingTop: 10}
	if _, err := p.Process(context.Background(), bytes.NewReader(encodedPNG(t, 64, 60)), padded, ""); !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("oversized output: got %v, want ErrImageTooLarge", err)
	}
	if _, err := p.Process(context.Background(), bytes.NewReader(encodedPNG(t, 64, 64)), &ProcessingParams{}, ""); err != nil {
		t.Fatalf("an image at the limit was refused: %v", err)
	}
}

func TestProcess_RefusesSVG(t *testing.T) {
	p := NewVipsProcessor(10, 85, FormatWebP, 4096, 4096)
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"><rect width="10" height="10"/></svg>`)
	_, err := p.Process(context.Background(), bytes.NewReader(svg), &ProcessingParams{}, "")
	if err == nil {
		t.Fatal("an SVG was decoded")
	}
}

func encodedPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := newTestImage(t, w, h, []float64{10, 20, 30})
	defer img.Close()
	data, err := img.PngsaveBuffer(&vips.PngsaveBufferOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return data
}
