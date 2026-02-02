// Package handlers provides HTTP handlers for the Imagine image processing API.
package handlers

// Processing constants
const (
	// MinDimensionPixels is the minimum allowed dimension for image resize operations
	MinDimensionPixels = 16

	// MaxBrightnessValue is the maximum allowed brightness adjustment value
	MaxBrightnessValue = 100
	// MinBrightnessValue is the minimum allowed brightness adjustment value
	MinBrightnessValue = -100

	// MaxContrastValue is the maximum allowed contrast adjustment value
	MaxContrastValue = 100
	// MinContrastValue is the minimum allowed contrast adjustment value
	MinContrastValue = -100

	// MaxSaturationValue is the maximum allowed saturation adjustment value
	MaxSaturationValue = 500
	// MinSaturationValue is the minimum allowed saturation adjustment value
	MinSaturationValue = -100

	// MaxGammaValue is the maximum allowed gamma value
	MaxGammaValue = 3.0
	// MinGammaValue is the minimum allowed gamma value
	MinGammaValue = 0.0

	// MaxHueValue is the maximum allowed hue adjustment value
	MaxHueValue = 180
	// MinHueValue is the minimum allowed hue adjustment value
	MinHueValue = -180

	// MaxBlurValue is the maximum allowed blur value
	MaxBlurValue = 100.0

	// MaxSharpenValue is the maximum allowed sharpen value
	MaxSharpenValue = 100.0
)

// Fit mode constants for image resizing
const (
	FitCover   = "cover"
	FitContain = "contain"
	FitFill    = "fill"
)

// Flip direction constants
const (
	FlipHorizontal = "horizontal"
	FlipVertical   = "vertical"
)
