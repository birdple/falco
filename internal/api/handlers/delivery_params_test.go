package handlers

import (
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/birdple/falco/internal/config"
	"github.com/birdple/falco/internal/processor"
)

func paramsHandler(t *testing.T) *Handler {
	t.Helper()
	cfg := &config.Config{}
	cfg.Processing.MaxDimensions.Width = 4096
	cfg.Processing.MaxDimensions.Height = 4096
	return NewHandler(cfg, nil, processor.NewVipsProcessor(10, 85, processor.FormatWebP, 4096, 4096), time.Now())
}

func parse(t *testing.T, h *Handler, raw, ext string) (*processor.ProcessingParams, *paramError) {
	t.Helper()
	q, err := url.ParseQuery(raw)
	require.NoError(t, err)
	return h.parseDeliveryParams(q, ext)
}

// Geometry and encoding parameters reject a malformed value with a 400.
func TestParseDeliveryParams_Rejects(t *testing.T) {
	h := paramsHandler(t)
	cases := map[string]string{
		"w=abc":                  "INVALID_WIDTH",
		"w=8":                    "INVALID_WIDTH",
		"width=5000":             "INVALID_WIDTH",
		"h=-1":                   "INVALID_HEIGHT",
		"q=0":                    "INVALID_QUALITY",
		"quality=101":            "INVALID_QUALITY",
		"f=bmp":                  "INVALID_FORMAT",
		"format=svg":             "INVALID_FORMAT",
		"fit=stretch":            "INVALID_FIT",
		"crop_x=1":               "INVALID_CROP",
		"crop_w=0&crop_h=1":      "INVALID_CROP",
		"rotate=361":             "INVALID_ROTATE",
		"rotate=NaN":             "INVALID_ROTATE",
		"rotate=Inf":             "INVALID_ROTATE",
		"flip=diagonal":          "INVALID_FLIP",
		"wm=a&wm_url=http://x/y": "INVALID_WATERMARK",
		"wm=../x":                "INVALID_WATERMARK",
	}
	for raw, code := range cases {
		t.Run(raw, func(t *testing.T) {
			_, perr := parse(t, h, raw, "")
			require.NotNil(t, perr, "accepted %q", raw)
			assert.Equal(t, code, perr.code)
		})
	}
}

// Cosmetic parameters fall back to their default when malformed; NaN and Inf
// count as malformed, since they slip through every range comparison.
func TestParseDeliveryParams_CosmeticFallbacks(t *testing.T) {
	h := paramsHandler(t)
	p, perr := parse(t, h,
		"brightness=NaN&contrast=Inf&gamma=-Inf&saturation=NaN&hue=NaN&blur=NaN&sharpen=Inf"+
			"&wm_opacity=NaN&wm_scale=NaN&trim=1&trim_threshold=NaN&maxage=-5&gravity=sideways",
		"")
	require.Nil(t, perr)
	assert.Zero(t, p.Brightness)
	assert.Zero(t, p.Contrast)
	assert.Zero(t, p.Gamma)
	assert.Zero(t, p.Saturation)
	assert.Zero(t, p.Hue)
	assert.Zero(t, p.Blur)
	assert.Zero(t, p.Sharpen)
	assert.Zero(t, p.WatermarkOpacity)
	assert.Zero(t, p.WatermarkScale)
	assert.Zero(t, p.TrimThreshold)
	assert.Zero(t, p.MaxAge)
	assert.Empty(t, p.Gravity)
}

func TestParseDeliveryParams_Padding(t *testing.T) {
	h := paramsHandler(t)

	p, perr := parse(t, h, "pad_top=30000&pad_left=4096&pad_right=12&pad_color=%2300ff00", "")
	require.Nil(t, perr)
	assert.Zero(t, p.PaddingTop, "padding beyond the max dimension must be ignored")
	assert.Equal(t, 4096, p.PaddingLeft)
	assert.Equal(t, 12, p.PaddingRight)
	assert.Equal(t, "00FF00", p.PaddingColor, "colour is normalised")

	p, perr = parse(t, h, "pad_top=1&pad_color=FFFFFF_wmid:logo_0.00__0.00", "")
	require.Nil(t, perr)
	assert.Equal(t, "FFFFFF", p.PaddingColor, "an invalid colour falls back to white")
}

func TestParseDeliveryParams_AliasesAndDefaults(t *testing.T) {
	h := paramsHandler(t)

	p, perr := parse(t, h, "width=200&height=100&quality=70&format=jpeg&fit=cover", "")
	require.Nil(t, perr)
	assert.Equal(t, 200, p.Width)
	assert.Equal(t, 100, p.Height)
	assert.Equal(t, 70, p.Quality)
	assert.Equal(t, "jpeg", p.Format)
	assert.Equal(t, FitCover, p.Fit)

	p, perr = parse(t, h, "", "webp")
	require.Nil(t, perr)
	assert.Equal(t, "webp", p.Format, "the path extension is the format default")
	assert.False(t, p.SkipAutoOrient, "auto-orient is on by default")
	assert.False(t, p.KeepMetadata, "metadata is stripped by default")

	p, perr = parse(t, h, "f=png&orient=0&meta=1&maxage=60&smaxage=600&gravity=north&rotate=45.5", "webp")
	require.Nil(t, perr)
	assert.Equal(t, "png", p.Format, "?f= wins over the extension")
	assert.True(t, p.SkipAutoOrient)
	assert.True(t, p.KeepMetadata)
	assert.Equal(t, 60, p.MaxAge)
	assert.Equal(t, 600, p.SMaxAge)
	assert.Equal(t, "north", p.Gravity)
	assert.InDelta(t, 45.5, p.Rotate, 0)
}

func TestBackendNamespace_SeparatesBuckets(t *testing.T) {
	h := scopeHandler(t)

	assert.Equal(t, "main", h.backendNamespace("", ""))
	assert.Equal(t, "main", h.backendNamespace("", "main-alias"), "an alias is its target")
	assert.Equal(t, "other", h.backendNamespace("", "other"))
	assert.Equal(t, "other", h.backendNamespace("other", ""))
	assert.Equal(t, "main/remote", h.backendNamespace("", "remote"), "a remote bucket on the default backend")
	assert.NotEqual(t,
		cacheObjectKey(h.backendNamespace("", "main"), "avatar"),
		cacheObjectKey(h.backendNamespace("", "other"), "avatar"))
}
