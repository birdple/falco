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
