package processor

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"

	"github.com/chai2010/webp"
	"github.com/srwiley/oksvg"
	"github.com/srwiley/rasterx"
)

// ImageDecoder handles image decoding from various formats
type ImageDecoder interface {
	Decode(data []byte) (image.Image, string, error)
}

// multiFormatDecoder implements ImageDecoder for multiple formats
type multiFormatDecoder struct {
	maxFileSizeMB int
}

// NewImageDecoder creates a new image decoder
func NewImageDecoder(maxFileSizeMB int) ImageDecoder {
	return &multiFormatDecoder{
		maxFileSizeMB: maxFileSizeMB,
	}
}

// Decode decodes an image from bytes, automatically detecting the format
func (d *multiFormatDecoder) Decode(data []byte) (image.Image, string, error) {
	reader := bytes.NewReader(data)

	// Try SVG first (check for SVG signature)
	if d.isSVG(data) {
		img, err := d.decodeSVG(data)
		if err == nil {
			return img, "svg", nil
		}
	}
	reader.Seek(0, 0)

	// Try JPEG
	if img, err := jpeg.Decode(reader); err == nil {
		return img, "jpeg", nil
	}
	reader.Seek(0, 0)

	// Try PNG
	if img, err := png.Decode(reader); err == nil {
		return img, "png", nil
	}
	reader.Seek(0, 0)

	// Try WebP
	if img, err := webp.Decode(reader); err == nil {
		return img, "webp", nil
	}

	return nil, "", fmt.Errorf("unsupported image format")
}

// isSVG checks if the data is an SVG file
func (d *multiFormatDecoder) isSVG(data []byte) bool {
	// Check for common SVG signatures
	if len(data) < 10 {
		return false
	}

	// Check for XML declaration or SVG tag at the start
	dataStr := string(data[:min(len(data), 1000)])
	return bytes.Contains([]byte(dataStr), []byte("<svg")) ||
	       bytes.Contains([]byte(dataStr), []byte("<?xml"))
}

// decodeSVG decodes an SVG file into a raster image
func (d *multiFormatDecoder) decodeSVG(data []byte) (image.Image, error) {
	// Parse SVG
	icon, err := oksvg.ReadIconStream(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed to parse SVG: %w", err)
	}

	// Set default dimensions if not specified
	width, height := int(icon.ViewBox.W), int(icon.ViewBox.H)
	if width == 0 || height == 0 {
		// Default to 1024x1024 if no viewBox is defined
		width, height = 1024, 1024
		icon.SetTarget(0, 0, float64(width), float64(height))
	}

	// Create RGBA image to render into
	img := image.NewRGBA(image.Rect(0, 0, width, height))

	// Create rasterizer
	scanner := rasterx.NewScannerGV(width, height, img, img.Bounds())
	raster := rasterx.NewDasher(width, height, scanner)

	// Render SVG
	icon.Draw(raster, 1.0)

	return img, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// DecodeFromReader decodes an image from an io.Reader
func (d *multiFormatDecoder) DecodeFromReader(input io.Reader) (image.Image, string, error) {
	data, err := io.ReadAll(io.LimitReader(input, int64(d.maxFileSizeMB)*1024*1024))
	if err != nil {
		return nil, "", fmt.Errorf("failed to read image data: %w", err)
	}

	return d.Decode(data)
}
