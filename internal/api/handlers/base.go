package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/birdple/falco/internal/api/types"
	"github.com/birdple/falco/internal/config"
	"github.com/birdple/falco/internal/pkg/httputil"
	"github.com/birdple/falco/internal/pkg/logger"
	"github.com/birdple/falco/internal/processor"
	"github.com/birdple/falco/internal/storage"
)

// Handler contains dependencies for all API handlers
type Handler struct {
	config         *config.Config
	storage        storage.StorageBackend
	imageProcessor processor.ImageProcessor
	startTime      time.Time
	httpClient     *http.Client
}

// NewHandler creates a new handler instance
func NewHandler(
	cfg *config.Config,
	storageBackend storage.StorageBackend,
	imageProc processor.ImageProcessor,
	startTime time.Time,
) *Handler {
	return &Handler{
		config:         cfg,
		storage:        storageBackend,
		imageProcessor: imageProc,
		startTime:      startTime,
		httpClient:     httputil.NewSafeHTTPClient(30 * time.Second),
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

	logger.Warn().
		Str("error_code", code).
		Int("status_code", statusCode).
		Str("error_message", message).
		Msg("API error response")

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
			if maxAge < 86400 {
				maxAge = 86400
			}
			if sMaxAge < 604800 {
				sMaxAge = 604800
			}
		}
	}

	w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d, s-maxage=%d, immutable", maxAge, sMaxAge))

	etag := fmt.Sprintf(`"%s-%d-%d"`, metadata.ID, metadata.Size, metadata.CreatedAt.Unix())
	w.Header().Set("ETag", etag)

	w.Header().Set("Last-Modified", metadata.CreatedAt.UTC().Format(http.TimeFormat))
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Vary", "Accept")

	if _, err := io.Copy(w, reader); err != nil {
		logger.Error().Err(err).
			Str("image_id", metadata.ID).
			Str("content_type", metadata.ContentType).
			Msg("Failed to write image to response")
	}
}

// getStorageForBucket returns a storage backend instance for the specified bucket
func (h *Handler) getStorageForBucket(bucket string) storage.StorageBackend {
	if bucket == "" {
		return h.storage
	}

	if bucketAware, ok := h.storage.(storage.BucketAware); ok {
		return bucketAware.WithBucket(bucket)
	}

	return h.storage
}
