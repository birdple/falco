package handlers

import (
	"bytes"
	jsonv2 "encoding/json/v2"
	"fmt"
	"net/http"
	"net/url"

	"github.com/birdple/falco/internal/api/types"
	"github.com/birdple/falco/internal/api/utils"
	"github.com/birdple/falco/internal/jsonx"
	"github.com/birdple/falco/internal/pkg/hashutil"
	"github.com/birdple/falco/internal/pkg/httputil"
	"github.com/birdple/falco/internal/pkg/logger"
	"github.com/birdple/falco/internal/processor"
	"github.com/birdple/falco/internal/storage"
)

// HandleUpdate handles image update requests
func (h *Handler) HandleUpdate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req types.UpdateRequest
	if err := jsonv2.UnmarshalRead(r.Body, &req, jsonx.Strict); err != nil {
		h.sendError(w, http.StatusBadRequest, "INVALID_JSON", "Invalid JSON payload")
		return
	}

	if valErr := h.validateUpdateRequest(req); valErr != nil {
		h.sendError(w, http.StatusBadRequest, valErr.code, valErr.message)
		return
	}

	maxSize := h.config.GetMaxFileSizeBytes()
	imageData, _, err := httputil.DownloadURL(ctx, h.httpClient, req.URL, maxSize)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to download image from URL")
		h.sendError(w, http.StatusBadRequest, "DOWNLOAD_FAILED", fmt.Sprintf("Failed to download image: %v", err))
		return
	}

	urlSize := int64(len(imageData))

	storageBackend, sbErr := h.getStorageBackendScoped(r, req.Storage, req.Bucket)
	if sbErr != nil {
		h.sendError(w, http.StatusForbidden, "ACCESS_DENIED", sbErr.Error())
		return
	}

	// Enforce per-image ownership BEFORE any mutation. Admin scope bypasses.
	if ownErr := h.checkOwnership(r, storageBackend, req.Key); ownErr != nil {
		if storage.IsNotFound(ownErr) {
			h.sendError(w, http.StatusNotFound, "NOT_FOUND", "Image not found")
			return
		}
		h.sendError(w, http.StatusForbidden, "FORBIDDEN", ownErr.Error())
		return
	}

	var existingSize int64
	var existingOwnerID string
	if exists, err := storageBackend.Exists(ctx, req.Key); err == nil && exists {
		if body, metadata, err := storageBackend.Retrieve(ctx, req.Key); err == nil {
			existingSize = metadata.Size
			if metadata != nil {
				existingOwnerID = metadata.OwnerID
			}
			_ = body.Close() // Only need metadata, close body immediately
		}
	}

	imageReader := bytes.NewReader(imageData)
	params := &processor.ProcessingParams{
		Quality: req.Quality,
		Format:  req.Format,
	}

	processedImage, err := h.imageProcessor.Process(ctx, imageReader, params, "")
	if err != nil {
		logger.Error().Err(err).Msg("Failed to process image")
		h.sendError(w, http.StatusUnprocessableEntity, "PROCESSING_FAILED", "Failed to process image")
		return
	}
	defer func() { _ = processedImage.Data.Close() }()

	imageID := hashutil.GenerateImageIDFromData(imageData)

	err = storageBackend.Store(ctx, req.Key, processedImage.Data, &storage.ImageMetadata{
		ID:           imageID,
		OriginalName: utils.ExtractFilenameFromURL(req.URL),
		Format:       processedImage.Metadata.Format,
		Size:         processedImage.Metadata.Size,
		Width:        processedImage.Metadata.Width,
		Height:       processedImage.Metadata.Height,
		ContentType:  processedImage.Metadata.ContentType,
		CreatedAt:    processedImage.Metadata.CreatedAt,
		OwnerID:      existingOwnerID,
	})
	if err != nil {
		logger.Error().Err(err).Msg("Failed to store image")
		h.sendError(w, http.StatusInternalServerError, "STORAGE_ERROR", "Failed to store image")
		return
	}

	// The bytes changed but the cache key did not — it derives from key+params —
	// so without invalidating, the old version kept being served for up to 24
	// hours and the update looked like it had never applied.
	h.invalidateCache(req.Key)

	newSize := processedImage.Metadata.Size
	savedBytes := existingSize - newSize
	savedPercent := float64(0)
	if existingSize > 0 {
		savedPercent = float64(savedBytes) / float64(existingSize) * 100
	}

	response := types.UpdateResponse{
		Success: true,
		Updated: []types.UpdateResult{
			{
				Key:          req.Key,
				URLSize:      urlSize,
				BucketSize:   existingSize,
				NewSize:      newSize,
				SavedBytes:   savedBytes,
				SavedPercent: savedPercent,
				Format:       processedImage.Metadata.Format,
				Quality:      req.Quality,
			},
		},
	}

	writeJSON(w, http.StatusOK, response)
}

// validateUpdateRequest checks an update request before anything is downloaded
// or written.
//
// Every field is required here, unlike upload: an update names an existing
// object, so there is nothing to infer and a missing field means the caller does
// not know what it is replacing.
func (h *Handler) validateUpdateRequest(req types.UpdateRequest) *paramError {
	switch {
	case req.URL == "":
		return &paramError{"MISSING_URL", "URL is required"}
	case req.Bucket == "":
		return &paramError{"MISSING_BUCKET", "Bucket is required"}
	case req.Key == "":
		return &paramError{"MISSING_KEY", "Key is required"}
	case req.Quality <= 0 || req.Quality > maxQuality:
		return &paramError{"INVALID_QUALITY", "Quality must be between 1 and 100"}
	case req.Format != "" && !h.imageProcessor.ValidateFormat(req.Format):
		return &paramError{"INVALID_FORMAT", "Unsupported format"}
	}

	parsedURL, err := url.Parse(req.URL)
	switch {
	case err != nil:
		return &paramError{"INVALID_URL", "Invalid URL format"}
	case parsedURL.Scheme != "https" && parsedURL.Scheme != "http":
		return &paramError{"INVALID_URL", "URL must use HTTP or HTTPS protocol"}
	case len(req.URL) > MaxURLLength:
		return &paramError{"INVALID_URL", fmt.Sprintf("URL too long (max %d characters)", MaxURLLength)}
	}
	return nil
}
