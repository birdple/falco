package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/birdple/imagine/internal/api/types"
	"github.com/birdple/imagine/internal/config"
	"github.com/birdple/imagine/internal/processor"
	"github.com/birdple/imagine/internal/storage"
)

// Handler contains dependencies for all API handlers
type Handler struct {
	config         *config.Config
	logger         *logrus.Logger
	storage        storage.StorageBackend
	imageProcessor processor.ImageProcessor
	startTime      time.Time
}

// NewHandler creates a new handler instance
func NewHandler(
	cfg *config.Config,
	logger *logrus.Logger,
	storageBackend storage.StorageBackend,
	imageProc processor.ImageProcessor,
	startTime time.Time,
) *Handler {
	return &Handler{
		config:         cfg,
		logger:         logger,
		storage:        storageBackend,
		imageProcessor: imageProc,
		startTime:      startTime,
	}
}

// sendError sends a JSON error response
func (h *Handler) sendError(w http.ResponseWriter, statusCode int, code, message string) {
	response := types.UploadResponse{
		Success: false,
		Error: &types.APIError{
			Code:    code,
			Message: message,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(response)
}

// serveImage serves an image with proper headers
func (h *Handler) serveImage(w http.ResponseWriter, reader io.Reader, metadata *storage.ImageMetadata) {
	w.Header().Set("Content-Type", metadata.ContentType)
	w.Header().Set("Cache-Control", "public, max-age=31536000") // 1 year
	w.Header().Set("ETag", "\""+metadata.ID+"\"")

	// Copy image data to response
	io.Copy(w, reader)
}

// getStorageForBucket returns a storage backend instance for the specified bucket
// If bucket is empty or storage doesn't support BucketAware, returns default storage
func (h *Handler) getStorageForBucket(bucket string) storage.StorageBackend {
	if bucket == "" {
		return h.storage
	}

	// Check if storage supports dynamic bucket selection
	if bucketAware, ok := h.storage.(storage.BucketAware); ok {
		return bucketAware.WithBucket(bucket)
	}

	// If storage doesn't support BucketAware, return default storage
	return h.storage
}
