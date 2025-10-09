package processor

import (
	"image"
	"image/color"

	"github.com/disintegration/imaging"
)

// Transformations provides pure transformation functions

// CropTransform returns a transformation function for cropping
func CropTransform(x, y, width, height int) TransformFunc {
	return func(img image.Image) image.Image {
		bounds := img.Bounds()
		imgWidth := bounds.Dx()
		imgHeight := bounds.Dy()

		// Ensure crop dimensions don't exceed image bounds
		if x < 0 {
			x = 0
		}
		if y < 0 {
			y = 0
		}
		if x+width > imgWidth {
			width = imgWidth - x
		}
		if y+height > imgHeight {
			height = imgHeight - y
		}

		// Ensure minimum dimensions
		if width <= 0 || height <= 0 {
			return img
		}

		return imaging.Crop(img, image.Rect(x, y, x+width, y+height))
	}
}

// FlipTransform returns a transformation function for flipping
func FlipTransform(direction string) TransformFunc {
	return func(img image.Image) image.Image {
		switch direction {
		case "horizontal":
			return imaging.FlipH(img)
		case "vertical":
			return imaging.FlipV(img)
		default:
			return img
		}
	}
}

// RotateTransform returns a transformation function for rotation
func RotateTransform(angle float64) TransformFunc {
	return func(img image.Image) image.Image {
		// Normalize angle to 0-360 range
		for angle < 0 {
			angle += 360
		}
		angle = float64(int(angle) % 360)

		switch angle {
		case 90:
			return imaging.Rotate90(img)
		case 180:
			return imaging.Rotate180(img)
		case 270:
			return imaging.Rotate270(img)
		default:
			return imaging.Rotate(img, angle, color.Transparent)
		}
	}
}

// ResizeTransform returns a transformation function for resizing
func ResizeTransform(width, height int, fit string) TransformFunc {
	return func(img image.Image) image.Image {
		bounds := img.Bounds()
		originalWidth := bounds.Dx()
		originalHeight := bounds.Dy()

		w := width
		h := height

		// If only one dimension is specified, maintain aspect ratio
		if w == 0 && h > 0 {
			w = (originalWidth * h) / originalHeight
		} else if h == 0 && w > 0 {
			h = (originalHeight * w) / originalWidth
		}

		// Apply resizing based on fit mode
		switch fit {
		case "cover":
			return imaging.Fill(img, w, h, imaging.Center, imaging.Lanczos)
		case "contain":
			return imaging.Fit(img, w, h, imaging.Lanczos)
		case "fill":
			return imaging.Resize(img, w, h, imaging.Lanczos)
		default:
			return imaging.Fill(img, w, h, imaging.Center, imaging.Lanczos)
		}
	}
}

// MaxDimensionsTransform enforces maximum dimensions
func MaxDimensionsTransform(maxWidth, maxHeight int) TransformFunc {
	return func(img image.Image) image.Image {
		bounds := img.Bounds()
		width := bounds.Dx()
		height := bounds.Dy()

		// Check if resizing is needed
		if width <= maxWidth && height <= maxHeight {
			return img
		}

		// Calculate new dimensions maintaining aspect ratio
		if width > maxWidth {
			scale := float64(maxWidth) / float64(width)
			width = maxWidth
			height = int(float64(height) * scale)
		}

		if height > maxHeight {
			scale := float64(maxHeight) / float64(height)
			height = maxHeight
			width = int(float64(width) * scale)
		}

		return imaging.Resize(img, width, height, imaging.Lanczos)
	}
}

// BrightnessTransform adjusts brightness
func BrightnessTransform(value float64) TransformFunc {
	return func(img image.Image) image.Image {
		if value == 0 {
			return img
		}
		return imaging.AdjustBrightness(img, value)
	}
}

// ContrastTransform adjusts contrast
func ContrastTransform(value float64) TransformFunc {
	return func(img image.Image) image.Image {
		if value == 0 {
			return img
		}
		return imaging.AdjustContrast(img, value)
	}
}

// GammaTransform applies gamma correction
func GammaTransform(value float64) TransformFunc {
	return func(img image.Image) image.Image {
		if value == 0 {
			return img
		}
		return imaging.AdjustGamma(img, value)
	}
}

// SaturationTransform adjusts saturation
func SaturationTransform(value float64) TransformFunc {
	return func(img image.Image) image.Image {
		if value == 0 {
			return img
		}
		return imaging.AdjustSaturation(img, value)
	}
}

// BlurTransform applies blur effect
func BlurTransform(value float64) TransformFunc {
	return func(img image.Image) image.Image {
		if value == 0 {
			return img
		}
		return imaging.Blur(img, value)
	}
}

// SharpenTransform applies sharpening effect
func SharpenTransform(value float64) TransformFunc {
	return func(img image.Image) image.Image {
		if value == 0 {
			return img
		}
		return imaging.Sharpen(img, value)
	}
}
