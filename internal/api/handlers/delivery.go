package handlers

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/birdple/falco/internal/api/utils"
	"github.com/birdple/falco/internal/pkg/metrics"
	"github.com/birdple/falco/internal/processor"
	"github.com/birdple/falco/internal/security"
	"github.com/birdple/falco/internal/storage"
)

// HandleDelivery handles image delivery requests with optional transformations
func (h *Handler) HandleDelivery(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	imageID := chi.URLParam(r, "*")

	if imageID == "" {
		// Fallback for old clients or unexpected route match
		imageID = chi.URLParam(r, "id")
	}

	if imageID == "" {
		h.sendError(w, http.StatusBadRequest, "MISSING_ID", "Image ID is required")
		return
	}

	// HMAC signature verification
	if h.config.Security.HMACKey != "" || h.config.Security.HMACRequired {
		sig := r.URL.Query().Get("sig")

		// Build the signed string: path + query params excluding "sig"
		q := r.URL.Query()
		q.Del("sig")
		signedPath := r.URL.Path
		if len(q) > 0 {
			signedPath = r.URL.Path + "?" + q.Encode()
		}

		if err := security.VerifyURL(
			sig,
			signedPath,
			h.config.Security.HMACKey,
			h.config.Security.HMACKeySalt,
			h.config.Security.HMACSignatureSize,
			h.config.Security.HMACRequired,
		); err != nil {
			h.logger.WithField("path", r.URL.Path).Warn("Invalid signature")
			h.sendError(w, http.StatusForbidden, "INVALID_SIGNATURE", "Invalid or missing URL signature")
			return
		}
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

	if maxAge := r.URL.Query().Get("maxage"); maxAge != "" {
		if ma, err := strconv.Atoi(maxAge); err == nil && ma >= 0 {
			params.MaxAge = ma
		}
	}

	if sMaxAge := r.URL.Query().Get("smaxage"); sMaxAge != "" {
		if sma, err := strconv.Atoi(sMaxAge); err == nil && sma >= 0 {
			params.SMaxAge = sma
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

	// Gravity for smart crop
	if gravity := r.URL.Query().Get("gravity"); gravity != "" {
		validGravities := map[string]bool{
			"center": true, "north": true, "south": true, "east": true, "west": true,
			"northeast": true, "northwest": true, "southeast": true, "southwest": true,
			"smart": true, "entropy": true,
		}
		if validGravities[gravity] {
			params.Gravity = gravity
		}
	}

	// Watermark scale
	if sc := r.URL.Query().Get("wm_scale"); sc != "" {
		if v, err := strconv.ParseFloat(sc, 64); err == nil && v > 0 && v <= 1 {
			params.WatermarkScale = v
		}
	}

	// Trim
	if r.URL.Query().Get("trim") == "1" {
		params.TrimEnabled = true
		if th := r.URL.Query().Get("trim_threshold"); th != "" {
			if v, err := strconv.ParseFloat(th, 64); err == nil && v >= 0 && v <= 255 {
				params.TrimThreshold = v
			}
		}
	}

	// Padding
	if pt := r.URL.Query().Get("pad_top"); pt != "" {
		if v, err := strconv.Atoi(pt); err == nil && v >= 0 {
			params.PaddingTop = v
		}
	}
	if pr := r.URL.Query().Get("pad_right"); pr != "" {
		if v, err := strconv.Atoi(pr); err == nil && v >= 0 {
			params.PaddingRight = v
		}
	}
	if pb := r.URL.Query().Get("pad_bottom"); pb != "" {
		if v, err := strconv.Atoi(pb); err == nil && v >= 0 {
			params.PaddingBottom = v
		}
	}
	if pl := r.URL.Query().Get("pad_left"); pl != "" {
		if v, err := strconv.Atoi(pl); err == nil && v >= 0 {
			params.PaddingLeft = v
		}
	}
	if pc := r.URL.Query().Get("pad_color"); pc != "" {
		params.PaddingColor = pc
	}

	// Auto-orient from EXIF (default true)
	params.AutoOrient = r.URL.Query().Get("orient") != "0"

	// Strip metadata (default true)
	params.StripMetadata = r.URL.Query().Get("meta") != "1"

	// Retrieve original image using full storage key
	storageStart := time.Now()
	reader, metadata, err := storageBackend.Retrieve(ctx, storageKey)
	storageDuration := time.Since(storageStart).Seconds()

	// Track storage metrics
	m := metrics.Default()
	if err != nil {
		m.StorageOperationsTotal.WithLabelValues("retrieve", "minio", "error").Inc()
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
	defer reader.Close()

	// Determine if processing is needed
	hasTransformations := params.Width != 0 || params.Height != 0 || params.Quality != 0 ||
		params.CropW != 0 || params.CropH != 0 ||
		params.Rotate != 0 || params.Flip != "" || params.Flop ||
		params.Brightness != 0 || params.Contrast != 0 || params.Gamma != 0 ||
		params.Saturation != 0 || params.Hue != 0 || params.Blur != 0 || params.Sharpen != 0 ||
		params.Gravity != "" || params.TrimEnabled ||
		params.PaddingTop != 0 || params.PaddingRight != 0 || params.PaddingBottom != 0 || params.PaddingLeft != 0

	// We process if:
	// 1. There are explicit transformations
	// 2. A specific format was requested that is different from original
	// 3. The original format is unknown (we process to detect and set correct Content-Type)
	needsProcessing := hasTransformations ||
		(params.Format != "" && params.Format != metadata.Format) ||
		(metadata.Format == "" || metadata.ContentType == "application/octet-stream")

	if !needsProcessing {
		h.serveImage(w, reader, metadata)
		return
	}

	// If we need to process but no format was specified, use the default from config or WebP
	if params.Format == "" {
		params.Format = h.config.Processing.DefaultFormat
		if params.Format == "" {
			params.Format = "webp"
		}
	}

	// Process image with transformations (includes automatic format detection)
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
