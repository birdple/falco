package ui

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	views "github.com/birdple/falco/internal/api/views/templ"
)

// paramGroups is the playground's control catalogue.
//
// It mirrors parseDeliveryParams one for one, including the distinction the
// parser makes on purpose: parameters that change geometry or encoding are
// STRICT (a malformed value is a 400, because serving a different image would
// be worse than failing), while cosmetic ones fall back to their default.
// Marking that in the UI is the difference between "this was ignored" and
// "this was rejected".
func (h *Handler) paramGroups() []views.ParamGroup {
	watermarkHosts := watermarkAllowedHosts()

	return []views.ParamGroup{
		{
			Title: "Geometry",
			Note:  "Both width and height is an exact box and will upscale. One alone is a cap and never upscales.",
			Fields: []views.ParamField{
				{Name: "w", Label: "Width", Kind: "number", Min: "16", Strict: true, Help: "16 to the configured maximum"},
				{Name: "h", Label: "Height", Kind: "number", Min: "16", Strict: true},
				{Name: "fit", Label: "Fit", Kind: "select", Options: []string{"cover", "contain", "fill"}, Strict: true, Default: "cover"},
				{
					Name: "gravity", Label: "Gravity", Kind: "select",
					Options: []string{"center", "north", "south", "east", "west", "northeast", "northwest", "southeast", "southwest", "smart", "entropy"},
					Help:    "Compass points crop by position; smart and entropy let libvips choose.",
				},
				{Name: "crop_x", Label: "Crop X", Kind: "number", Min: "0", Strict: true},
				{Name: "crop_y", Label: "Crop Y", Kind: "number", Min: "0", Strict: true},
				{Name: "crop_w", Label: "Crop width", Kind: "number", Min: "1", Strict: true, Help: "Width and height must be given together."},
				{Name: "crop_h", Label: "Crop height", Kind: "number", Min: "1", Strict: true},
				{Name: "rotate", Label: "Rotate", Kind: "number", Min: "-360", Max: "360", Strict: true, Help: "90, 180 and 270 rotate without resampling."},
				{Name: "flip", Label: "Flip", Kind: "select", Options: []string{"horizontal", "vertical"}, Strict: true},
				{Name: "trim", Label: "Trim borders", Kind: "toggle", Help: "Remove a uniform border"},
				{Name: "trim_threshold", Label: "Trim threshold", Kind: "number", Min: "0", Max: "255", Default: "10"},
				{Name: "pad_top", Label: "Pad top", Kind: "number", Min: "0"},
				{Name: "pad_right", Label: "Pad right", Kind: "number", Min: "0"},
				{Name: "pad_bottom", Label: "Pad bottom", Kind: "number", Min: "0"},
				{Name: "pad_left", Label: "Pad left", Kind: "number", Min: "0"},
				{Name: "pad_color", Label: "Pad colour", Kind: "color", Default: "FFFFFF", Help: "Hex RRGGBB"},
			},
		},
		{
			Title: "Encoding",
			Fields: []views.ParamField{
				{Name: "f", Label: "Format", Kind: "select", Options: []string{"webp", "jpeg", "png", "avif", "heic"}, Strict: true, Default: h.cfg.Processing.DefaultFormat, Help: "AVIF falls back to WebP when the encoder fails — check the served content type."},
				{Name: "q", Label: "Quality", Kind: "number", Min: "1", Max: "100", Strict: true, Default: strconv.Itoa(h.cfg.Processing.DefaultQuality)},
				{Name: "orient", Label: "Auto-orient", Kind: "select", Options: []string{"0"}, Default: "on", Help: "0 disables EXIF auto-orientation."},
				{Name: "meta", Label: "Keep metadata", Kind: "select", Options: []string{"1"}, Help: "Off by default: a CDN origin should not hand out camera GPS."},
			},
		},
		{
			Title: "Colour",
			Note:  "Cosmetic: a malformed value falls back to its default rather than failing the request.",
			Fields: []views.ParamField{
				{Name: "brightness", Label: "Brightness", Kind: "number", Min: "-100", Max: "100", Default: "0"},
				{Name: "contrast", Label: "Contrast", Kind: "number", Min: "-100", Max: "100", Default: "0"},
				{Name: "gamma", Label: "Gamma", Kind: "number", Min: "0", Max: "3", Step: "0.1", Default: "1.0"},
				{Name: "saturation", Label: "Saturation", Kind: "number", Min: "-100", Max: "500", Default: "0"},
				{Name: "hue", Label: "Hue", Kind: "number", Min: "-180", Max: "180", Default: "0"},
				{Name: "blur", Label: "Blur", Kind: "number", Min: "0", Max: "100", Step: "0.5", Default: "0"},
				{Name: "sharpen", Label: "Sharpen", Kind: "number", Min: "0", Max: "100", Step: "0.5", Default: "0"},
			},
		},
		{
			Title: "Watermark",
			Note:  "A watermark failure is always reported: an image served without the mark that was asked for looks identical to one that worked.",
			Fields: []views.ParamField{
				{Name: "wm", Label: "Watermark object", Kind: "text", Strict: true, Help: "A storage key in this same bucket."},
				{
					Name: "wm_url", Label: "Watermark URL", Kind: "text", Strict: true,
					Disabled:       len(watermarkHosts) == 0,
					DisabledReason: "WATERMARK_ALLOWED_HOSTS is not set, so ?wm_url= is refused with 403. There is no fallback on purpose: opening it would turn an image URL into an arbitrary outbound fetch.",
				},
				{Name: "wm_opacity", Label: "Opacity", Kind: "number", Min: "0", Max: "1", Step: "0.05", Default: "0"},
				{Name: "wm_position", Label: "Position", Kind: "select", Options: []string{"top-left", "top-right", "bottom-left", "bottom-right", "center"}, Default: "bottom-right"},
				{Name: "wm_scale", Label: "Scale", Kind: "number", Min: "0.01", Max: "1", Step: "0.05", Default: "0.2", Help: "Fraction of the final width."},
			},
		},
		{
			Title: "Cache headers",
			Note:  "These change the response headers only; they are not part of the cache key.",
			Fields: []views.ParamField{
				{Name: "maxage", Label: "max-age", Kind: "number", Min: "0", Default: strconv.Itoa(h.cfg.Cache.DefaultMaxAge)},
				{Name: "smaxage", Label: "s-maxage", Kind: "number", Min: "0", Default: strconv.Itoa(h.cfg.Cache.DefaultSMaxAge)},
			},
		},
	}
}

// featureStates lists what is on and what is off, with the variable that
// explains each absence.
//
// Each of these otherwise fails at the moment of use with an error that reads
// like a bug unless you already know which variable is missing.
func (h *Handler) featureStates() []views.FeatureState {
	signing := h.signingEnabled()
	watermarkURL := len(watermarkAllowedHosts()) > 0

	return []views.FeatureState{
		{
			Name:    "Signed URLs",
			Enabled: signing,
			Reason:  "HMAC_KEY and HMAC_SALT must both be set. Without them POST /api/v1/sign answers 501 and the panel cannot sign thumbnails.",
		},
		{
			Name:    "Signature required on delivery",
			Enabled: h.cfg.Security.HMACRequired,
			Reason:  "HMAC_REQUIRED is off, so /api/v1/images/* is authorised by API key instead of by signature.",
		},
		{
			Name:    "Expiry required on signatures",
			Enabled: requireExpiry(),
			Reason:  "HMAC_REQUIRE_EXPIRY is off, so a signed URL without an expiry never stops working.",
		},
		{
			Name:    "External watermarks",
			Enabled: watermarkURL,
			Reason:  "WATERMARK_ALLOWED_HOSTS is not set, so ?wm_url= is refused. Watermarks from this bucket (?wm=) still work.",
		},
		{
			Name:    "Transform cache",
			Enabled: h.cfg.Cache.SizeMB > 0,
			Reason:  "CACHE_SIZE_MB is 0, so every transformation is recomputed on each request.",
		},
		{
			Name:    "Prometheus metrics",
			Enabled: h.cfg.Development.EnableMetrics,
			Reason:  "ENABLE_METRICS is off, so /metrics is not mounted at all.",
		},
	}
}

// effectiveConfig reports the settings that change behaviour.
//
// Secrets are reported as set or not set. Their presence is what an operator
// needs to diagnose a 501 or a 403; their value is never useful here.
func (h *Handler) effectiveConfig() []views.ConfigItem {
	setLabel := func(v string) string {
		if v != "" {
			return "set"
		}
		return "not set"
	}

	return []views.ConfigItem{
		{Name: "STORAGE_DEFAULT", Value: h.cfg.GetDefaultBucketName()},
		{Name: "DEFAULT_FORMAT", Value: h.cfg.Processing.DefaultFormat},
		{Name: "DEFAULT_QUALITY", Value: strconv.Itoa(h.cfg.Processing.DefaultQuality)},
		{Name: "MAX_FILE_SIZE_MB", Value: strconv.Itoa(h.cfg.Processing.MaxFileSizeMB)},
		{Name: "CONCURRENT_WORKERS", Value: strconv.Itoa(h.cfg.Processing.ConcurrentWorkers)},
		{Name: "CACHE_SIZE_MB", Value: strconv.Itoa(h.cfg.Cache.SizeMB)},
		{Name: "CACHE_TTL_HOURS", Value: strconv.Itoa(h.cfg.Cache.TTLHrs)},
		{Name: "CACHE_CLEANUP_INTERVAL", Value: h.cfg.Cache.CleanupInterval.String()},
		{Name: "API_KEY_REQUIRED", Value: strconv.FormatBool(h.cfg.Security.APIKeyRequired)},
		{Name: "HMAC_REQUIRED", Value: strconv.FormatBool(h.cfg.Security.HMACRequired)},
		{Name: "HMAC_REQUIRE_EXPIRY", Value: strconv.FormatBool(requireExpiry())},
		{Name: "API_KEY", Value: setLabel(h.cfg.Security.APIKey), Secret: true},
		{Name: "HMAC_KEY", Value: setLabel(h.cfg.Security.HMACKey), Secret: true},
		{Name: "HMAC_SALT", Value: setLabel(h.cfg.Security.HMACKeySalt), Secret: true},
		{Name: "TRUSTED_PROXIES", Value: joinOrNone(h.cfg.Security.TrustedProxies)},
		{Name: "WATERMARK_ALLOWED_HOSTS", Value: joinOrNone(watermarkAllowedHosts())},
		{Name: "ENABLE_METRICS", Value: strconv.FormatBool(h.cfg.Development.EnableMetrics)},
		{Name: "ENABLE_REDIS", Value: strconv.FormatBool(h.cfg.Cache.EnableRedis)},
	}
}

func joinOrNone(values []string) string {
	if len(values) == 0 {
		return "none"
	}
	return strings.Join(values, ", ")
}

// requireExpiry reads HMAC_REQUIRE_EXPIRY.
//
// It is read straight from the environment, outside viper, exactly as
// delivery.go does — the panel must report the same value delivery enforces,
// and a default here would report a policy that is not the one in effect.
func requireExpiry() bool {
	v, err := strconv.ParseBool(strings.TrimSpace(os.Getenv("HMAC_REQUIRE_EXPIRY")))
	return err == nil && v
}

// watermarkAllowedHosts reads WATERMARK_ALLOWED_HOSTS. Nil means the feature is
// off; there is deliberately no fallback list.
func watermarkAllowedHosts() []string {
	raw := strings.TrimSpace(os.Getenv("WATERMARK_ALLOWED_HOSTS"))
	if raw == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if host := strings.TrimSpace(part); host != "" {
			out = append(out, host)
		}
	}
	return out
}

// contextWithTimeout bounds a per-backend call on the ops screen.
func contextWithTimeout(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), d)
}
