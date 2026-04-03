package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	apimw "github.com/birdple/falco/internal/api/middleware"
	"github.com/birdple/falco/internal/api/types"
	"github.com/birdple/falco/internal/config"
	"github.com/birdple/falco/internal/pkg/httputil"
	"github.com/birdple/falco/internal/pkg/logger"
	"github.com/birdple/falco/internal/processor"
	"github.com/birdple/falco/internal/storage"
)

// Handler contains dependencies for all API handlers
type Handler struct {
	config          *config.Config
	storage         storage.StorageBackend
	storageRegistry *storage.Registry
	imageProcessor  processor.ImageProcessor
	startTime       time.Time
	httpClient      *http.Client
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

// SetRegistry sets the storage registry for multi-backend support.
func (h *Handler) SetRegistry(r *storage.Registry) {
	h.storageRegistry = r
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

// getStorageBackend resolves a storage backend by name (from registry) and
// optional bucket override. If storageName is empty, uses the default backend.
// Enforces scoped API key restrictions from the request context.
func (h *Handler) getStorageBackend(storageName, bucket string) (storage.StorageBackend, error) {
	return h.getStorageBackendWithScope(nil, storageName, bucket)
}

// getStorageBackendScoped resolves a storage backend and enforces scope from the request.
func (h *Handler) getStorageBackendScoped(r *http.Request, storageName, bucket string) (storage.StorageBackend, error) {
	scope := apimw.GetScope(r.Context())

	// Enforce storage access
	if scope != nil && storageName != "" && !scope.CanAccessStorage(storageName) {
		return nil, fmt.Errorf("access denied to storage %q", storageName)
	}

	// Enforce bucket access
	if scope != nil && bucket != "" && !scope.CanAccessBucket(bucket) {
		return nil, fmt.Errorf("access denied to bucket %q", bucket)
	}

	return h.getStorageBackendWithScope(scope, storageName, bucket)
}

// getStorageBackendWithScope is the internal resolver.
func (h *Handler) getStorageBackendWithScope(scope *apimw.APIScope, storageName, bucket string) (storage.StorageBackend, error) {
	var backend storage.StorageBackend

	if h.storageRegistry != nil && storageName != "" {
		b, err := h.storageRegistry.Get(storageName)
		if err != nil {
			return nil, err
		}
		backend = b
	} else if h.storageRegistry != nil {
		backend = h.storageRegistry.Default()
	} else {
		backend = h.storage
	}

	// Apply bucket override if provided
	if bucket != "" {
		if bucketAware, ok := backend.(storage.BucketAware); ok {
			backend = bucketAware.WithBucket(bucket)
		}
	}

	return backend, nil
}
