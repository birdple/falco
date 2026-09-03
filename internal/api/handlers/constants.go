// Package handlers provides HTTP handlers for the Falco image processing API.
package handlers

import "time"

// Processing constants
const (
	// MinDimensionPixels is the minimum allowed dimension for image resize operations
	MinDimensionPixels = 16

	// maxQuality is the top of the JPEG/WebP quality scale.
	maxQuality = 100

	// maxTrimThreshold is the widest tolerance accepted when trimming uniform
	// borders: it is an 8-bit channel distance, so 255 means "trim anything".
	maxTrimThreshold = 255

	// defaultMaxFormBytes is the multipart parse budget used when no file-size
	// limit is configured. It bounds what ParseMultipartForm keeps in memory.
	defaultMaxFormBytes = 32 << 20

	// MaxURLLength caps externally-supplied URLs (JSON upload, update).
	// Matches typical browser/address-bar limits and prevents SSRF payload bloat.
	MaxURLLength = 2048

	// maxRotateDegrees bounds the rotation angle. A full turn either way covers
	// every distinct result; anything beyond it is a caller doing arithmetic
	// wrong, not a rotation they meant.
	maxRotateDegrees = 360

	// maxCropExtent caps a manual crop's width and height. libvips rejects a
	// crop larger than the image anyway, but rejecting it here keeps the error
	// a 400 with a name on it instead of a 422 from the encoder.
	maxCropExtent = 20000

	// maxWatermarkBytes caps the overlay Falco will fetch or read. A logo is
	// kilobytes; anything at this size is a mistake worth failing on.
	maxWatermarkBytes = 2 << 20

	// watermarkFetchTimeout bounds the external fetch. It is short because the
	// caller is waiting on it inside an image request.
	watermarkFetchTimeout = 5 * time.Second

	// watermarkStoredPrefix marks a watermark source that names an image in
	// Falco's own storage rather than an external URL, so the two can never
	// collide in a cache key.
	watermarkStoredPrefix = "id:"
)

// Flip directions accepted on the delivery route.
const (
	FlipHorizontal = "horizontal"
	FlipVertical   = "vertical"
)

// Fit mode constants for image resizing
const (
	FitCover   = "cover"
	FitContain = "contain"
	FitFill    = "fill"
)

// AllowedImageExtensions maps URL path extensions to the canonical format
// names accepted by the image processor. Shared by delivery and proxy handlers.
var AllowedImageExtensions = map[string]string{
	"webp": "webp",
	"jpg":  "jpeg",
	"jpeg": "jpeg",
	"png":  "png",
	"avif": "avif",
}

// validGravities is the set of accepted gravity values for smart cropping.
// Hoisted to package level to avoid re-allocation on every delivery request.
var validGravities = map[string]bool{
	"center": true, "north": true, "south": true, "east": true, "west": true,
	"northeast": true, "northwest": true, "southeast": true, "southwest": true,
	"smart": true, "entropy": true,
}
