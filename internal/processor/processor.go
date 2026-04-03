package processor

// NewImageProcessor creates a new image processor using libvips.
func NewImageProcessor(maxFileSizeMB, defaultQuality int, defaultFormat ImageFormat, maxWidth, maxHeight int) ImageProcessor {
	return NewVipsProcessor(maxFileSizeMB, defaultQuality, defaultFormat, maxWidth, maxHeight)
}
