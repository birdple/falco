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

// ImageEncoder handles image encoding to various formats
type ImageEncoder interface {
	Encode(img image.Image, format ImageFormat, quality int) ([]byte, error)
	EncodeToWriter(writer io.Writer, img image.Image, format ImageFormat, quality int) error
}

// multiFormatEncoder implements ImageEncoder for multiple formats
type multiFormatEncoder struct{}

// NewImageEncoder creates a new image encoder
func NewImageEncoder() ImageEncoder {
	return &multiFormatEncoder{}
}

// Encode encodes an image to the specified format and returns bytes
func (e *multiFormatEncoder) Encode(img image.Image, format ImageFormat, quality int) ([]byte, error) {
	var buf bytes.Buffer
	if err := e.EncodeToWriter(&buf, img, format, quality); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// EncodeToWriter encodes an image to the specified format and writes to io.Writer
func (e *multiFormatEncoder) EncodeToWriter(writer io.Writer, img image.Image, format ImageFormat, quality int) error {
	if quality <= 0 {
		quality = GetDefaultQuality(format)
	}

	// Ensure quality is within valid range
	if quality > 100 {
		quality = 100
	}

	switch format {
	case FormatJPEG:
		return jpeg.Encode(writer, img, &jpeg.Options{Quality: quality})
	case FormatPNG:
		return png.Encode(writer, img)
	case FormatWebP:
		return webp.Encode(writer, img, &webp.Options{Quality: float32(quality)})
	default:
		return fmt.Errorf("unsupported output format: %s", format)
	}
}
