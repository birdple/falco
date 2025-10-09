package processor

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"

	"github.com/chai2010/webp"
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

// DecodeFromReader decodes an image from an io.Reader
func (d *multiFormatDecoder) DecodeFromReader(input io.Reader) (image.Image, string, error) {
	data, err := io.ReadAll(io.LimitReader(input, int64(d.maxFileSizeMB)*1024*1024))
	if err != nil {
		return nil, "", fmt.Errorf("failed to read image data: %w", err)
	}

	return d.Decode(data)
}
