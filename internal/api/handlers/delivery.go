package handlers

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/birdple/imagine/internal/api/utils"
	"github.com/birdple/imagine/internal/processor"
	"github.com/birdple/imagine/internal/storage"
)

// HandleDelivery handles image delivery requests with optional transformations
func (h *Handler) HandleDelivery(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	imageID := chi.URLParam(r, "id")

	if imageID == "" {
		h.sendError(w, http.StatusBadRequest, "MISSING_ID", "Image ID is required")
		return
	}

	// Get bucket and directory parameters
	bucket := r.URL.Query().Get("b")
	if bucket == "" {
		bucket = r.URL.Query().Get("bucket")
	}

	directory := r.URL.Query().Get("d")
	if directory == "" {
		directory = r.URL.Query().Get("dir")
	}
	if directory == "" {
		directory = r.URL.Query().Get("directory")
	}

	// Normalize directory path
	directory = utils.NormalizeDirectoryPath(directory)

	// Build full storage key
	storageKey := utils.BuildStorageKey(directory, imageID)

	// Get bucket-aware storage instance
	storageBackend := h.getStorageForBucket(bucket)

	// Parse query parameters for transformations
	params := &processor.ProcessingParams{}

	// Support both short (w) and long (width) parameter names
	if width := utils.GetQueryParam(r, "w", "width"); width != "" {
		if widthVal, err := strconv.Atoi(width); err == nil && widthVal > 0 && widthVal <= h.config.Processing.MaxDimensions.Width {
			params.Width = widthVal
		} else {
			h.sendError(w, http.StatusBadRequest, "INVALID_WIDTH", "Invalid width parameter")
			return
		}
	}

	if height := utils.GetQueryParam(r, "h", "height"); height != "" {
		if heightVal, err := strconv.Atoi(height); err == nil && heightVal > 0 && heightVal <= h.config.Processing.MaxDimensions.Height {
			params.Height = heightVal
		} else {
			h.sendError(w, http.StatusBadRequest, "INVALID_HEIGHT", "Invalid height parameter")
			return
		}
	}

	if quality := utils.GetQueryParam(r, "q", "quality"); quality != "" {
		if q, err := strconv.Atoi(quality); err == nil && q > 0 && q <= 100 {
			params.Quality = q
		} else {
			h.sendError(w, http.StatusBadRequest, "INVALID_QUALITY", "Invalid quality parameter")
			return
		}
	}

	if format := utils.GetQueryParam(r, "f", "format"); format != "" {
		if h.imageProcessor.ValidateFormat(format) {
			params.Format = format
		} else {
			h.sendError(w, http.StatusBadRequest, "INVALID_FORMAT", "Unsupported format")
			return
		}
	}

	if fit := r.URL.Query().Get("fit"); fit != "" {
		if fit == "cover" || fit == "contain" || fit == "fill" {
			params.Fit = fit
		} else {
			h.sendError(w, http.StatusBadRequest, "INVALID_FIT", "Invalid fit parameter")
			return
		}
	}

	// Parse advanced transformation parameters
	if cropX := r.URL.Query().Get("crop_x"); cropX != "" {
		if x, err := strconv.Atoi(cropX); err == nil && x >= 0 {
			params.CropX = x
		}
	}

	if cropY := r.URL.Query().Get("crop_y"); cropY != "" {
		if y, err := strconv.Atoi(cropY); err == nil && y >= 0 {
			params.CropY = y
		}
	}

	if cropW := r.URL.Query().Get("crop_w"); cropW != "" {
		if w, err := strconv.Atoi(cropW); err == nil && w > 0 {
			params.CropW = w
		}
	}

	if cropH := r.URL.Query().Get("crop_h"); cropH != "" {
		if h, err := strconv.Atoi(cropH); err == nil && h > 0 {
			params.CropH = h
		}
	}

	if rotate := r.URL.Query().Get("rotate"); rotate != "" {
		if r, err := strconv.ParseFloat(rotate, 64); err == nil {
			params.Rotate = r
		}
	}

	if flip := r.URL.Query().Get("flip"); flip != "" {
		if flip == "horizontal" || flip == "vertical" {
			params.Flip = flip
		}
	}

	if flop := r.URL.Query().Get("flop"); flop != "" {
		if f, err := strconv.ParseBool(flop); err == nil {
			params.Flop = f
		}
	}

	// Parse filter parameters
	if brightness := r.URL.Query().Get("brightness"); brightness != "" {
		if b, err := strconv.ParseFloat(brightness, 64); err == nil && b >= -100 && b <= 100 {
			params.Brightness = b
		}
	}

	if contrast := r.URL.Query().Get("contrast"); contrast != "" {
		if c, err := strconv.ParseFloat(contrast, 64); err == nil && c >= -100 && c <= 100 {
			params.Contrast = c
		}
	}

	if gamma := r.URL.Query().Get("gamma"); gamma != "" {
		if g, err := strconv.ParseFloat(gamma, 64); err == nil && g >= 0 && g <= 3 {
			params.Gamma = g
		}
	}

	if saturation := r.URL.Query().Get("saturation"); saturation != "" {
		if s, err := strconv.ParseFloat(saturation, 64); err == nil && s >= -100 && s <= 500 {
			params.Saturation = s
		}
	}

	if hue := r.URL.Query().Get("hue"); hue != "" {
		if h, err := strconv.Atoi(hue); err == nil && h >= -180 && h <= 180 {
			params.Hue = h
		}
	}

	if blur := r.URL.Query().Get("blur"); blur != "" {
		if b, err := strconv.ParseFloat(blur, 64); err == nil && b >= 0 && b <= 100 {
			params.Blur = b
		}
	}

	if sharpen := r.URL.Query().Get("sharpen"); sharpen != "" {
		if s, err := strconv.ParseFloat(sharpen, 64); err == nil && s >= 0 && s <= 100 {
			params.Sharpen = s
		}
	}

	// Set default format to WebP if not specified
	if params.Format == "" {
		params.Format = "webp"
	}

	// Retrieve original image using full storage key
	reader, metadata, err := storageBackend.Retrieve(ctx, storageKey)
	if err != nil {
		if storage.IsNotFound(err) {
			h.sendError(w, http.StatusNotFound, "IMAGE_NOT_FOUND", "Image not found")
			return
		}
		h.logger.WithError(err).Error("Failed to retrieve image")
		h.sendError(w, http.StatusInternalServerError, "RETRIEVAL_ERROR", "Failed to retrieve image")
		return
	}
	defer reader.Close()

	// If no transformations requested except format, still process for format conversion
	needsProcessing := params.Width != 0 || params.Height != 0 || params.Quality != 0 ||
		params.Format != "" || params.CropW != 0 || params.CropH != 0 ||
		params.Rotate != 0 || params.Flip != "" || params.Flop ||
		params.Brightness != 0 || params.Contrast != 0 || params.Gamma != 0 ||
		params.Saturation != 0 || params.Hue != 0 || params.Blur != 0 || params.Sharpen != 0

	if !needsProcessing {
		h.serveImage(w, reader, metadata)
		return
	}

	// Process image with transformations
	processedImage, err := h.imageProcessor.Process(ctx, reader, params)
	if err != nil {
		h.logger.WithError(err).Error("Failed to process image")
		h.sendError(w, http.StatusUnprocessableEntity, "PROCESSING_FAILED", "Failed to process image")
		return
	}
	defer processedImage.Data.Close()

	// Convert processor metadata to storage metadata
	storageMetadata := &storage.ImageMetadata{
		ID:           processedImage.Metadata.ID,
		OriginalName: processedImage.Metadata.OriginalName,
		Format:       processedImage.Metadata.Format,
		Size:         processedImage.Metadata.Size,
		Width:        processedImage.Metadata.Width,
		Height:       processedImage.Metadata.Height,
		ContentType:  processedImage.Metadata.ContentType,
		CreatedAt:    processedImage.Metadata.CreatedAt,
	}

	h.serveImage(w, processedImage.Data, storageMetadata)
}
