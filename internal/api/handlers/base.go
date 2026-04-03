package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/birdple/falco/internal/api/types"
	"github.com/birdple/falco/internal/config"
	"github.com/birdple/falco/internal/pkg/httputil"
	"github.com/birdple/falco/internal/processor"
	"github.com/birdple/falco/internal/storage"
)

// Handler contains dependencies for all API handlers
type Handler struct {
	config         *config.Config
	logger         *logrus.Logger
	storage        storage.StorageBackend
	imageProcessor processor.ImageProcessor
	startTime      time.Time
	httpClient     *http.Client
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
		httpClient:     httputil.NewHTTPClient(30 * time.Second), // 30s timeout for URL downloads
	}
}

// sendError sends a JSON error response with enhanced logging
func (h *Handler) sendError(w http.ResponseWriter, statusCode int, code, message string) {
	response := types.UploadResponse{
		Success: false,
		Error: &types.APIError{
			Code:    code,
			Message: message,
		},
	}

	// Enhanced logging for errors
	h.logger.WithFields(logrus.Fields{
		"error_code":    code,
		"status_code":   statusCode,
		"error_message": message,
		"client_ip":     w.Header().Get("X-Forwarded-For"), // Will be set by middleware
	}).Warn("API error response")

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(response)
}

// serveImage serves an image with proper headers
func (h *Handler) serveImage(w http.ResponseWriter, reader io.Reader, metadata *storage.ImageMetadata) {
	w.Header().Set("Content-Type", metadata.ContentType)

	// Smart cache control based on metadata if available, otherwise defaults from config
	maxAge := h.config.Cache.DefaultMaxAge
	sMaxAge := h.config.Cache.DefaultSMaxAge

	if metadata.MaxAge > 0 {
		maxAge = metadata.MaxAge
	}
	if metadata.SMaxAge > 0 {
		sMaxAge = metadata.SMaxAge
	}

	// Dynamic override for large images if no explicit TTL was requested
	if metadata.MaxAge == 0 && metadata.SMaxAge == 0 {
		if metadata.Size >= 1024*1024 { // >= 1MB
			// For large images, we boost the cache time if the defaults are low
			if maxAge < 86400 {
				maxAge = 86400
			}
			if sMaxAge < 604800 {
				sMaxAge = 604800
			}
		}
	}

	w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d, s-maxage=%d", maxAge, sMaxAge))

	// Enhanced ETag with size and timestamp for better cache validation
	etag := fmt.Sprintf(`"%s-%d-%d"`, metadata.ID, metadata.Size, metadata.CreatedAt.Unix())
	w.Header().Set("ETag", etag)

	// Additional headers for better caching and performance
	w.Header().Set("Last-Modified", metadata.CreatedAt.UTC().Format(http.TimeFormat))
	w.Header().Set("Accept-Ranges", "bytes")

	// Copy image data to response with error handling
	if _, err := io.Copy(w, reader); err != nil {
		h.logger.WithError(err).WithFields(logrus.Fields{
			"image_id":     metadata.ID,
			"content_type": metadata.ContentType,
		}).Error("Failed to write image to response")
		// Note: Cannot send error response here as headers are already written
		// Client will detect incomplete response
	}
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
