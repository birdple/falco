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
	"github.com/birdple/falco/internal/processor"
	"github.com/birdple/falco/internal/storage"
)

// HandleUpdate handles image update requests
func (h *Handler) HandleUpdate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Parse request body
	var req types.UpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.sendError(w, http.StatusBadRequest, "INVALID_JSON", "Invalid JSON payload")
		return
	}

	// Validate required parameters
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

	// Validate URL with stricter checks
	parsedURL, err := url.Parse(req.URL)
	if err != nil {
		h.sendError(w, http.StatusBadRequest, "INVALID_URL", "Invalid URL format")
		return
	}

	// Only allow HTTPS in production, allow HTTP in development
	if parsedURL.Scheme != "https" && parsedURL.Scheme != "http" {
		h.sendError(w, http.StatusBadRequest, "INVALID_URL", "URL must use HTTP or HTTPS protocol")
		return
	}

	// Additional validation for URL length
	if len(req.URL) > 2048 {
		h.sendError(w, http.StatusBadRequest, "INVALID_URL", "URL too long (max 2048 characters)")
		return
	}

	// Download image from URL with timeout and validation
	maxSize := h.config.GetMaxFileSizeBytes()
	imageData, _, err := httputil.DownloadURL(ctx, h.httpClient, req.URL, maxSize)
	if err != nil {
		h.logger.WithError(err).Error("Failed to download image from URL")
		h.sendError(w, http.StatusBadRequest, "DOWNLOAD_FAILED", fmt.Sprintf("Failed to download image: %v", err))
		return
	}

	urlSize := int64(len(imageData))

	// Get bucket-aware storage instance
	storageBackend := h.getStorageForBucket(req.Bucket)

	// Check if image already exists to calculate savings
	var existingSize int64
	if exists, err := storageBackend.Exists(ctx, req.Key); err == nil && exists {
		if _, metadata, err := storageBackend.Retrieve(ctx, req.Key); err == nil {
			existingSize = metadata.Size
		}
	}

	// Process the image
	imageReader := bytes.NewReader(imageData)
	params := &processor.ProcessingParams{
		Quality: req.Quality,
		Format:  req.Format,
	}

	processedImage, err := h.imageProcessor.Process(ctx, imageReader, params)
	if err != nil {
		h.logger.WithError(err).Error("Failed to process image")
		h.sendError(w, http.StatusUnprocessableEntity, "PROCESSING_FAILED", "Failed to process image")
		return
	}
	defer processedImage.Data.Close()

	// Generate ID from hash of processed data for consistency
	imageID := hashutil.GenerateImageIDFromData(imageData)

	// Store the processed image
	err = storageBackend.Store(ctx, req.Key, processedImage.Data, &storage.ImageMetadata{
		ID:           imageID,
		OriginalName: utils.ExtractFilenameFromURL(req.URL),
		Format:       processedImage.Metadata.Format,
		Size:         processedImage.Metadata.Size,
		Width:        processedImage.Metadata.Width,
		Height:       processedImage.Metadata.Height,
		ContentType:  processedImage.Metadata.ContentType,
		CreatedAt:    processedImage.Metadata.CreatedAt,
	})
	if err != nil {
		h.logger.WithError(err).Error("Failed to store image")
		h.sendError(w, http.StatusInternalServerError, "STORAGE_ERROR", "Failed to store image")
		return
	}

	// Calculate savings
	newSize := processedImage.Metadata.Size
	savedBytes := existingSize - newSize
	savedPercent := float64(0)
	if existingSize > 0 {
		savedPercent = float64(savedBytes) / float64(existingSize) * 100
	}

	// Send response
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
