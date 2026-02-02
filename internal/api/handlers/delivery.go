package handlers

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/birdple/imagine/internal/api/utils"
	"github.com/birdple/imagine/internal/pkg/metrics"
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

	// Normalize and validate directory path
	directory = utils.NormalizeDirectoryPath(directory)
	if err := utils.ValidateDirectoryPath(directory); err != nil {
		h.sendError(w, http.StatusBadRequest, "INVALID_DIRECTORY", fmt.Sprintf("Invalid directory path: %v", err))
		return
	}

	// Build full storage key
	storageKey := utils.BuildStorageKey(directory, imageID)

	// Get bucket-aware storage instance
	storageBackend := h.getStorageForBucket(bucket)

	// Parse query parameters for transformations
	params := &processor.ProcessingParams{}

	// Support both short (w) and long (width) parameter names
	if width := utils.GetQueryParam(r, "w", "width"); width != "" {
		if widthVal, err := strconv.Atoi(width); err == nil && widthVal > 0 && widthVal <= h.config.Processing.MaxDimensions.Width {
			// Validate minimum dimensions
			if widthVal < MinDimensionPixels {
				h.sendError(w, http.StatusBadRequest, "INVALID_WIDTH", "Width must be at least 16 pixels")
				return
			}
			params.Width = widthVal
		} else {
			h.sendError(w, http.StatusBadRequest, "INVALID_WIDTH", "Invalid width parameter")
			return
		}
	}

	if height := utils.GetQueryParam(r, "h", "height"); height != "" {
		if heightVal, err := strconv.Atoi(height); err == nil && heightVal > 0 && heightVal <= h.config.Processing.MaxDimensions.Height {
			// Validate minimum dimensions
			if heightVal < MinDimensionPixels {
				h.sendError(w, http.StatusBadRequest, "INVALID_HEIGHT", "Height must be at least 16 pixels")
				return
			}
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
		if fit == FitCover || fit == FitContain || fit == FitFill {
			params.Fit = fit
		} else {
			h.sendError(w, http.StatusBadRequest, "INVALID_FIT", "Invalid fit parameter")
			return
		}
	}

	// Parse advanced transformation parameters with limits
	maxDim := h.config.Processing.MaxDimensions.Width
	if h.config.Processing.MaxDimensions.Height > maxDim {
		maxDim = h.config.Processing.MaxDimensions.Height
	}

	if cropX := r.URL.Query().Get("crop_x"); cropX != "" {
		if x, err := strconv.Atoi(cropX); err == nil && x >= 0 && x <= maxDim {
			params.CropX = x
		} else {
			h.sendError(w, http.StatusBadRequest, "INVALID_CROP_X", fmt.Sprintf("crop_x must be between 0 and %d", maxDim))
			return
		}
	}

	if cropY := r.URL.Query().Get("crop_y"); cropY != "" {
		if y, err := strconv.Atoi(cropY); err == nil && y >= 0 && y <= maxDim {
			params.CropY = y
		} else {
			h.sendError(w, http.StatusBadRequest, "INVALID_CROP_Y", fmt.Sprintf("crop_y must be between 0 and %d", maxDim))
			return
		}
	}

	if cropW := r.URL.Query().Get("crop_w"); cropW != "" {
		if cropWidth, err := strconv.Atoi(cropW); err == nil && cropWidth > 0 && cropWidth <= maxDim {
			params.CropW = cropWidth
		} else {
			h.sendError(w, http.StatusBadRequest, "INVALID_CROP_W", fmt.Sprintf("crop_w must be between 1 and %d", maxDim))
			return
		}
	}

	if cropH := r.URL.Query().Get("crop_h"); cropH != "" {
		if cropHeight, err := strconv.Atoi(cropH); err == nil && cropHeight > 0 && cropHeight <= maxDim {
			params.CropH = cropHeight
		} else {
			h.sendError(w, http.StatusBadRequest, "INVALID_CROP_H", fmt.Sprintf("crop_h must be between 1 and %d", maxDim))
			return
		}
	}

	if rotate := r.URL.Query().Get("rotate"); rotate != "" {
		if r, err := strconv.ParseFloat(rotate, 64); err == nil {
			params.Rotate = r
		}
	}

	if flip := r.URL.Query().Get("flip"); flip != "" {
		if flip == FlipHorizontal || flip == FlipVertical {
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
		if b, err := strconv.ParseFloat(brightness, 64); err == nil && b >= MinBrightnessValue && b <= MaxBrightnessValue {
			params.Brightness = b
		}
	}

	if contrast := r.URL.Query().Get("contrast"); contrast != "" {
		if c, err := strconv.ParseFloat(contrast, 64); err == nil && c >= MinContrastValue && c <= MaxContrastValue {
			params.Contrast = c
		}
	}

	if gamma := r.URL.Query().Get("gamma"); gamma != "" {
		if g, err := strconv.ParseFloat(gamma, 64); err == nil && g >= MinGammaValue && g <= MaxGammaValue {
			params.Gamma = g
		}
	}

	if saturation := r.URL.Query().Get("saturation"); saturation != "" {
		if s, err := strconv.ParseFloat(saturation, 64); err == nil && s >= MinSaturationValue && s <= MaxSaturationValue {
			params.Saturation = s
		}
	}

	if hue := r.URL.Query().Get("hue"); hue != "" {
		if h, err := strconv.Atoi(hue); err == nil && h >= MinHueValue && h <= MaxHueValue {
			params.Hue = h
		}
	}

	if blur := r.URL.Query().Get("blur"); blur != "" {
		if b, err := strconv.ParseFloat(blur, 64); err == nil && b >= 0 && b <= MaxBlurValue {
			params.Blur = b
		}
	}

	if sharpen := r.URL.Query().Get("sharpen"); sharpen != "" {
		if s, err := strconv.ParseFloat(sharpen, 64); err == nil && s >= 0 && s <= MaxSharpenValue {
			params.Sharpen = s
		}
	}

	// Set default format to WebP if not specified
	if params.Format == "" {
		params.Format = "webp"
	}

	// Retrieve original image using full storage key
	storageStart := time.Now()
	reader, metadata, err := storageBackend.Retrieve(ctx, storageKey)
	storageDuration := time.Since(storageStart).Seconds()

	// Track storage metrics
	m := metrics.Default()
	if err != nil {
		m.StorageOperationsTotal.WithLabelValues("retrieve", "minio", "error").Inc()
		m.CacheMisses.Inc()
		if storage.IsNotFound(err) {
			h.sendError(w, http.StatusNotFound, "IMAGE_NOT_FOUND", "Image not found")
			return
		}
		h.logger.WithError(err).Error("Failed to retrieve image")
		h.sendError(w, http.StatusInternalServerError, "RETRIEVAL_ERROR", "Failed to retrieve image")
		return
	}
	m.StorageOperationsTotal.WithLabelValues("retrieve", "minio", "success").Inc()
	m.StorageOperationDuration.WithLabelValues("retrieve", "minio").Observe(storageDuration)
	m.CacheHits.Inc()
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
	processStart := time.Now()
	processedImage, err := h.imageProcessor.Process(ctx, reader, params)
	processDuration := time.Since(processStart).Seconds()

	// Track processing metrics
	if err != nil {
		m.ImageProcessingTotal.WithLabelValues(metadata.Format, params.Format, "error").Inc()
		h.logger.WithError(err).Error("Failed to process image")
		h.sendError(w, http.StatusUnprocessableEntity, "PROCESSING_FAILED", "Failed to process image")
		return
	}
	m.ImageProcessingTotal.WithLabelValues(metadata.Format, params.Format, "success").Inc()
	m.ImageProcessingDuration.WithLabelValues("transform").Observe(processDuration)

	// Track image sizes
	if inputData, err := io.ReadAll(reader); err == nil {
		m.ImageProcessingSize.WithLabelValues("input").Observe(float64(len(inputData)))
	}
	m.ImageProcessingSize.WithLabelValues("output").Observe(float64(processedImage.Metadata.Size))

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

	// Fallback: Ensure ContentType is set based on format if empty
	if storageMetadata.ContentType == "" {
		storageMetadata.ContentType = h.imageProcessor.GetContentType(storageMetadata.Format)
	}

	h.serveImage(w, processedImage.Data, storageMetadata)
}
