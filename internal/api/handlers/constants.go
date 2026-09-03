// Package handlers provides HTTP handlers for the Falco image processing API.
package handlers

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
