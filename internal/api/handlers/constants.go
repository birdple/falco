// Package handlers provides HTTP handlers for the Falco image processing API.
package handlers

// Processing constants
const (
	// MinDimensionPixels is the minimum allowed dimension for image resize operations
	MinDimensionPixels = 16
)

// Fit mode constants for image resizing
const (
	FitCover   = "cover"
	FitContain = "contain"
	FitFill    = "fill"
)

// validGravities is the set of accepted gravity values for smart cropping.
// Hoisted to package level to avoid re-allocation on every delivery request.
var validGravities = map[string]bool{
	"center": true, "north": true, "south": true, "east": true, "west": true,
	"northeast": true, "northwest": true, "southeast": true, "southwest": true,
	"smart": true, "entropy": true,
}
