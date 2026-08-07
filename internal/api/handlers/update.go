package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/birdple/falco/internal/api/types"
	"github.com/birdple/falco/internal/api/utils"
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.sendError(w, http.StatusBadRequest, "INVALID_JSON", "Invalid JSON payload")
		return
	}

	if req.URL == "" {
		h.sendError(w, http.StatusBadRequest, "MISSING_URL", "URL is required")
		return
	}

	if req.Bucket == "" {
		h.sendError(w, http.StatusBadRequest, "MISSING_BUCKET", "Bucket is required")
		return
	}

	if req.Key == "" {
		h.sendError(w, http.StatusBadRequest, "MISSING_KEY", "Key is required")
		return
	}

	if req.Quality <= 0 || req.Quality > 100 {
		h.sendError(w, http.StatusBadRequest, "INVALID_QUALITY", "Quality must be between 1 and 100")
		return
	}

	if req.Format != "" && !h.imageProcessor.ValidateFormat(req.Format) {
		h.sendError(w, http.StatusBadRequest, "INVALID_FORMAT", "Unsupported format")
		return
	}

	parsedURL, err := url.Parse(req.URL)
	if err != nil {
		h.sendError(w, http.StatusBadRequest, "INVALID_URL", "Invalid URL format")
		return
	}

	if parsedURL.Scheme != "https" && parsedURL.Scheme != "http" {
		h.sendError(w, http.StatusBadRequest, "INVALID_URL", "URL must use HTTP or HTTPS protocol")
		return
	}

	if len(req.URL) > MaxURLLength {
		h.sendError(w, http.StatusBadRequest, "INVALID_URL", fmt.Sprintf("URL too long (max %d characters)", MaxURLLength))
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
			body.Close() // Only need metadata, close body immediately
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
	defer processedImage.Data.Close()

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

	// Los bytes cambiaron pero la clave de cache no —se deriva de key+params—,
	// así que sin invalidar se seguía entregando la versión vieja hasta 24 h y
	// el update parecía no haberse aplicado.
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

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(response)
}
