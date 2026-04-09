package handlers

import (
	"bytes"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/birdple/falco/internal/api/utils"
	"github.com/birdple/falco/internal/pkg/logger"
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
		imageID = chi.URLParam(r, "id")
	}

	if imageID == "" {
		h.sendError(w, http.StatusBadRequest, "MISSING_ID", "Image ID is required")
		return
	}

	// HMAC signature verification.
	//
	// HMAC_REQUIRED is the single switch: when true the signature MUST be
	// present and valid, when false verification is skipped entirely (dev
	// only). The config validator enforces that API_KEY_REQUIRED=true implies
	// HMAC_REQUIRED=true, so production deployments cannot land here with an
	// unprotected delivery route.
	if h.config.Security.HMACRequired {
		sig := r.URL.Query().Get("sig")
		// VerifyURL canonicalizes the path internally (sorts query, strips sig).
		signedPath := r.URL.Path
		if raw := r.URL.RawQuery; raw != "" {
			signedPath = r.URL.Path + "?" + raw
		}

		if err := security.VerifyURL(
			sig,
			signedPath,
			h.config.Security.HMACKey,
			h.config.Security.HMACKeySalt,
			h.config.Security.HMACSignatureSize,
			true,
		); err != nil {
			logger.Warn().Str("error", err.Error()).Msg("Invalid signature")
			h.sendError(w, http.StatusForbidden, "INVALID_SIGNATURE", "Invalid or missing URL signature")
			return
		}
	}

	// Get storage, bucket, and directory parameters
	storageName := r.URL.Query().Get("storage")

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

	directory = utils.NormalizeDirectoryPath(directory)
	if err := utils.ValidateDirectoryPath(directory); err != nil {
		h.sendError(w, http.StatusBadRequest, "INVALID_DIRECTORY", fmt.Sprintf("Invalid directory path: %v", err))
		return
	}

	storageKey := utils.BuildStorageKey(directory, imageID)

	storageBackend, err := h.getStorageBackendScoped(r, storageName, bucket)
	if err != nil {
		h.sendError(w, http.StatusForbidden, "ACCESS_DENIED", err.Error())
		return
	}

	// Parse query parameters for transformations
	params := &processor.ProcessingParams{}

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

	if gravity := r.URL.Query().Get("gravity"); gravity != "" {
		if validGravities[gravity] {
			params.Gravity = gravity
		}
	}

	if sc := r.URL.Query().Get("wm_scale"); sc != "" {
		if v, err := strconv.ParseFloat(sc, 64); err == nil && v > 0 && v <= 1 {
			params.WatermarkScale = v
		}
	}

	if r.URL.Query().Get("trim") == "1" {
		params.TrimEnabled = true
		if th := r.URL.Query().Get("trim_threshold"); th != "" {
			if v, err := strconv.ParseFloat(th, 64); err == nil && v >= 0 && v <= 255 {
				params.TrimThreshold = v
			}
		}
	}

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

	params.AutoOrient = r.URL.Query().Get("orient") != "0"
	params.StripMetadata = r.URL.Query().Get("meta") != "1"

	m := metrics.Default()

	// Determine if the request carries explicit transformations (decidable
	// from query params alone, before any storage I/O).
	hasTransformations := params.Width != 0 || params.Height != 0 || params.Quality != 0 ||
		params.CropW != 0 || params.CropH != 0 ||
		params.Rotate != 0 || params.Flip != "" || params.Flop ||
		params.Brightness != 0 || params.Contrast != 0 || params.Gamma != 0 ||
		params.Saturation != 0 || params.Hue != 0 || params.Blur != 0 || params.Sharpen != 0 ||
		params.Gravity != "" || params.TrimEnabled ||
		params.PaddingTop != 0 || params.PaddingRight != 0 || params.PaddingBottom != 0 || params.PaddingLeft != 0

	// ── Cache-first path ──────────────────────────────────────────────
	// When the request carries transformations or an explicit format, we
	// can compute a cache key from (storageKey, params) alone — no
	// storage round-trip needed. A cache hit serves the response
	// immediately, skipping the Jay TCP fetch entirely.
	wantsProcessing := hasTransformations || params.Format != ""
	var cacheKey string

	if wantsProcessing {
		// Resolve output format now so the cache key is deterministic.
		if params.Format == "" {
			params.Format = h.config.Processing.DefaultFormat
			if params.Format == "" {
				params.Format = "webp"
			}
		}

		cacheKey = h.imageProcessor.GenerateCacheKey(storageKey, params)
		if cachedData, found := h.imageProcessor.GetFromCache(cacheKey); found {
			contentType := h.imageProcessor.GetContentType(params.Format)
			cachedMeta := &storage.ImageMetadata{
				ID:          imageID,
				ContentType: contentType,
				Format:      params.Format,
				Size:        int64(len(cachedData)),
				MaxAge:      params.MaxAge,
				SMaxAge:     params.SMaxAge,
				CreatedAt:   time.Now(),
			}
			h.serveImage(w, r, bytes.NewReader(cachedData), cachedMeta)
			return
		}
	}

	// ── Cache miss (or raw delivery) — fetch from storage ─────────────
	storageStart := time.Now()
	reader, metadata, err := storageBackend.Retrieve(ctx, storageKey)
	storageDuration := time.Since(storageStart).Seconds()

	if err != nil {
		m.StorageOperationsTotal.WithLabelValues("retrieve", h.defaultStorageType(), "error").Inc()
		if storage.IsNotFound(err) {
			h.sendError(w, http.StatusNotFound, "IMAGE_NOT_FOUND", "Image not found")
			return
		}
		logger.Error().Err(err).Msg("Failed to retrieve image")
		h.sendError(w, http.StatusInternalServerError, "RETRIEVAL_ERROR", "Failed to retrieve image")
		return
	}
	m.StorageOperationsTotal.WithLabelValues("retrieve", h.defaultStorageType(), "success").Inc()
	m.StorageOperationDuration.WithLabelValues("retrieve", h.defaultStorageType()).Observe(storageDuration)
	defer reader.Close()

	// Non-image files: serve directly without any processing
	isImage := utils.IsImageContentType(metadata.ContentType)
	if !isImage {
		h.serveImage(w, r, reader, metadata)
		return
	}

	// Re-evaluate processing need now that we have storage metadata
	// (handles edge cases like unknown format in storage).
	needsProcessing := hasTransformations ||
		(params.Format != "" && params.Format != metadata.Format) ||
		(metadata.Format == "" || metadata.ContentType == "" || metadata.ContentType == "application/octet-stream")

	if !needsProcessing {
		h.serveImage(w, r, reader, metadata)
		return
	}

	// Ensure format + cacheKey are set (handles the metadata-only edge
	// case where wantsProcessing was false but needsProcessing is true).
	if params.Format == "" {
		params.Format = h.config.Processing.DefaultFormat
		if params.Format == "" {
			params.Format = "webp"
		}
	}
	if cacheKey == "" {
		cacheKey = h.imageProcessor.GenerateCacheKey(storageKey, params)
	}

	processStart := time.Now()
	processedImage, err := h.imageProcessor.Process(ctx, reader, params, cacheKey)
	processDuration := time.Since(processStart).Seconds()

	if err != nil {
		m.ImageProcessingTotal.WithLabelValues(metadata.Format, params.Format, "error").Inc()
		logger.Error().Err(err).Msg("Failed to process image")
		h.sendError(w, http.StatusUnprocessableEntity, "PROCESSING_FAILED", "Failed to process image")
		return
	}
	m.ImageProcessingTotal.WithLabelValues(metadata.Format, params.Format, "success").Inc()
	m.ImageProcessingDuration.WithLabelValues("transform").Observe(processDuration)

	defer processedImage.Data.Close()

	storageMetadata := &storage.ImageMetadata{
		ID:           processedImage.Metadata.ID,
		OriginalName: processedImage.Metadata.OriginalName,
		Format:       processedImage.Metadata.Format,
		Size:         processedImage.Metadata.Size,
		Width:        processedImage.Metadata.Width,
		Height:       processedImage.Metadata.Height,
		ContentType:  processedImage.Metadata.ContentType,
		CreatedAt:    processedImage.Metadata.CreatedAt,
		MaxAge:       params.MaxAge,
		SMaxAge:      params.SMaxAge,
	}

	if storageMetadata.ContentType == "" {
		storageMetadata.ContentType = h.imageProcessor.GetContentType(storageMetadata.Format)
	}

	h.serveImage(w, r, processedImage.Data, storageMetadata)
}
