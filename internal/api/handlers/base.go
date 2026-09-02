package handlers

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/singleflight"

	apimw "github.com/birdple/falco/internal/api/middleware"
	"github.com/birdple/falco/internal/api/types"
	"github.com/birdple/falco/internal/cache"
	"github.com/birdple/falco/internal/config"
	"github.com/birdple/falco/internal/jsonx"
	"github.com/birdple/falco/internal/pkg/httputil"
	"github.com/birdple/falco/internal/pkg/logger"
	"github.com/birdple/falco/internal/processor"
	"github.com/birdple/falco/internal/storage"
)

// negativeCacheSize / negativeCacheCleanup size the small side-cache used to
// remember recent upstream fetch failures (see rememberProxyFailure). Entries
// are a few dozen bytes each, so this cap is generous headroom, not a real
// memory concern.
const (
	negativeCacheSize     = 2 * 1024 * 1024
	negativeCacheCleanup  = 5 * time.Minute
	proxyNegativeCacheTTL = 3 * time.Minute
)

// Handler contains dependencies for all API handlers
type Handler struct {
	config          *config.Config
	storage         storage.StorageBackend
	storageRegistry *storage.Registry
	imageProcessor  processor.ImageProcessor
	startTime       time.Time
	httpClient      *http.Client

	// sf deduplicates concurrent fetch-and-process work that shares the same
	// cache key (proxy external-CDN fetches and delivery storage retrievals).
	// Without this, N concurrent requests for the same cold image each pay
	// their own network fetch, decode, and encode — measured as the
	// dominant cost of a crawler burst hitting an uncached image.
	sf singleflight.Group

	// negativeCache remembers recent upstream fetch failures (dead links,
	// non-2xx, non-image content-type) so a broken URL isn't re-fetched on
	// every single request. Separate from imageProcessor's image cache:
	// different eviction/size semantics, and entries here are tiny.
	negativeCache *cache.LRUCache
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
		negativeCache:  cache.NewLRUCache(negativeCacheSize, negativeCacheCleanup),
	}
}

// SetRegistry sets the storage registry for multi-backend support.
func (h *Handler) SetRegistry(r *storage.Registry) {
	h.storageRegistry = r
}

// sendError sends a JSON error response. Logs at a level based on statusCode:
//   - 5xx -> Error
//   - 4xx -> Warn
//   - other -> Info
//
// Callers should NOT log the same error before calling sendError; this is the
// single place where the API error response is recorded.
func (h *Handler) sendError(w http.ResponseWriter, statusCode int, code, message string) {
	response := types.UploadResponse{
		Success: false,
		Error: &types.APIError{
			Code:    code,
			Message: message,
		},
	}

	event := logger.Info()
	switch {
	case statusCode >= 500:
		event = logger.Error()
	case statusCode >= 400:
		event = logger.Warn()
	}
	event.
		Str("error_code", code).
		Int("status_code", statusCode).
		Str("error_message", message).
		Msg("API error response")

	writeJSON(w, statusCode, response)
}

// writeJSON marshals v and sends it with the requested status.
//
// It marshals BEFORE touching the ResponseWriter on purpose: if the marshal
// fails there is still time to answer an honest 500, whereas a streaming encoder
// would already have sent a 200 with a body cut off halfway.
//
// Uses encoding/json/v2's defaults, which do not escape &, < or >. That is a
// change in bytes from v1 but not in meaning: every consumer goes through a JSON
// parser and none of them drop the raw response into HTML.
func writeJSON(w http.ResponseWriter, statusCode int, v any) {
	data, err := jsonv2.Marshal(v)
	if err != nil {
		logger.Error().Err(err).Int("status_code", statusCode).Msg("Failed to marshal JSON response")
		http.Error(w, `{"success":false,"error":{"code":"ENCODING_ERROR","message":"Failed to encode response"}}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	if _, err := w.Write(data); err != nil {
		logger.Error().Err(err).Msg("Failed to write JSON response body")
	}
}

// fetchError carries enough context to reproduce the right HTTP response
// after a singleflight.Do call returns. When several requests block on the
// same in-flight key, only one goroutine actually runs the closure and logs
// the failure; every other caller gets this error back and must still send
// its own response to its own ResponseWriter — sendError is deliberately
// NOT called from inside the closure.
type fetchError struct {
	status  int
	code    string
	message string
}

func (e *fetchError) Error() string { return e.message }

// negativeCacheEntry is the JSON payload stored in Handler.negativeCache.
type negativeCacheEntry struct {
	Status  int    `json:"status"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// rememberFailure records a fetchError under key for proxyNegativeCacheTTL.
// Marshal failure is intentionally swallowed — negative caching is a
// best-effort optimization, never a correctness requirement.
func (h *Handler) rememberFailure(key string, fe *fetchError) {
	data, err := jsonv2.Marshal(negativeCacheEntry{Status: fe.status, Code: fe.code, Message: fe.message}, jsonx.Wire)
	if err != nil {
		return
	}
	_ = h.negativeCache.Set("neg:"+key, data, proxyNegativeCacheTTL)
}

// recallFailure returns a previously remembered fetchError for key, if any
// entry is still within its TTL.
func (h *Handler) recallFailure(key string) (*fetchError, bool) {
	data, found := h.negativeCache.Get("neg:" + key)
	if !found {
		return nil, false
	}
	var entry negativeCacheEntry
	if err := jsonv2.Unmarshal(data, &entry, jsonx.Wire); err != nil {
		return nil, false
	}
	return &fetchError{status: entry.Status, code: entry.Code, message: entry.Message}, true
}

// dropOriginVary removes the Origin token from the Vary response header,
// leaving every other token untouched.
//
// The CORS middleware adds `Vary: Origin` to every response it handles, but
// the delivery and proxy paths overwrite Access-Control-Allow-Origin with a
// literal `*` regardless of who asked (see serveImage). The bytes and the
// headers are therefore identical for every Origin, so advertising the
// variance only shards downstream caches over a variant that never differs.
//
// Only tokens already present when the handler runs are rewritten. The
// compression middleware appends `Vary: Accept-Encoding` later, from
// WriteHeader, so its token is added after this runs and survives.
func dropOriginVary(w http.ResponseWriter) {
	values := w.Header().Values("Vary")
	if len(values) == 0 {
		return
	}

	kept := make([]string, 0, len(values))
	for _, value := range values {
		for token := range strings.SplitSeq(value, ",") {
			token = strings.TrimSpace(token)
			if token == "" || strings.EqualFold(token, "Origin") {
				continue
			}
			kept = append(kept, token)
		}
	}

	w.Header().Del("Vary")
	for _, token := range kept {
		w.Header().Add("Vary", token)
	}
}

// serveImage serves an image with proper headers.
// Uses http.ServeContent for automatic ETag 304, If-Modified-Since, and Range support.
func (h *Handler) serveImage(w http.ResponseWriter, r *http.Request, reader io.Reader, metadata *storage.ImageMetadata) {
	// Content-Type must be set before ServeContent so it doesn't sniff.
	w.Header().Set("Content-Type", metadata.ContentType)

	// Images are public-by-design (HMAC signing protects against tampering, not
	// sharing). Emitting a wildcard ACAO lets cross-origin canvas/snapdom reads
	// work without per-origin CORS allowlisting, matching the proxy path.
	w.Header().Set("Access-Control-Allow-Origin", "*")
	// ...which makes the CORS middleware's `Vary: Origin` a lie: this
	// response is byte-identical for every Origin.
	dropOriginVary(w)

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

	// ETag set before ServeContent so it handles If-None-Match automatically.
	etag := fmt.Sprintf(`"%s-%d-%d"`, metadata.ID, metadata.Size, metadata.CreatedAt.Unix())
	w.Header().Set("ETag", etag)

	// If the reader is seekable, use http.ServeContent so clients get
	// automatic If-None-Match (304), If-Modified-Since (304), and
	// Range (206) handling. Pass "" as name to skip Content-Type sniffing
	// (already set above).
	if rs, ok := reader.(io.ReadSeeker); ok {
		http.ServeContent(w, r, "", metadata.CreatedAt, rs)
		return
	}

	// Non-seekable reader (e.g. Jay's io.ReadCloser): stream directly with
	// io.Copy instead of buffering the whole body into memory. This keeps
	// memory flat regardless of image size. Range requests are not supported
	// on this path — clients that rely on ranges must hit a seekable backend
	// or the LRU cache path, which already delivers a bytes.Reader.
	if metadata.Size > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(metadata.Size, 10))
	}
	w.Header().Set("Accept-Ranges", "none")
	w.Header().Set("Last-Modified", metadata.CreatedAt.UTC().Format(http.TimeFormat))

	// Honor If-None-Match for the streaming path too (no body transfer on 304).
	if match := r.Header.Get("If-None-Match"); match != "" && match == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}

	if _, err := io.Copy(w, reader); err != nil {
		// Headers are already flushed by io.Copy on first write; we can only log.
		logger.Warn().Err(err).
			Str("image_id", metadata.ID).
			Msg("Failed to stream image body to client")
	}
}

// getStorageBackendScoped resolves a storage backend and enforces scope from the request.
func (h *Handler) getStorageBackendScoped(r *http.Request, storageName, bucket string) (storage.StorageBackend, error) {
	scope := apimw.GetScope(r.Context())

	// Enforce bucket access (storageName is now the bucket name in the registry)
	effectiveBucket := storageName
	if effectiveBucket == "" {
		effectiveBucket = bucket
	}
	if scope != nil && effectiveBucket != "" && !scope.CanAccessBucket(effectiveBucket) {
		return nil, fmt.Errorf("access denied to bucket %q", effectiveBucket)
	}

	// Enforce bucket-level access for the provider bucket override
	if scope != nil && bucket != "" && !scope.CanAccessBucket(bucket) {
		return nil, fmt.Errorf("access denied to bucket %q", bucket)
	}

	return h.getStorageBackendWithScope(scope, storageName, bucket)
}

// checkOwnership verifies that the authenticated caller is allowed to mutate
// the object at `key` in `backend`. Rules:
//
//   - Admin-scoped keys (scope == nil || scope.IsAdmin) always pass.
//   - Otherwise the caller MUST present X-Owner-Id and it MUST match the
//     object's stored owner-id metadata.
//   - Objects with an empty stored owner-id are legacy (uploaded before
//     owner tracking). They are treated as immutable for scoped keys —
//     only admin keys may mutate them. This is fail-closed: we do not
//     allow any caller to "claim" a legacy image by supplying a matching
//     empty header.
//
// Returns nil on allow, an error describing the denial reason otherwise.
// Missing keys return storage.ErrImageNotFound so callers can distinguish
// "not found" from "forbidden" and map to the correct HTTP status.
func (h *Handler) checkOwnership(r *http.Request, backend storage.StorageBackend, key string) error {
	scope := apimw.GetScope(r.Context())
	if scope == nil || scope.IsAdmin {
		return nil
	}

	// Retrieve metadata. The storage interface has no Head-with-metadata, so
	// we use Retrieve and immediately close the body — this matches the
	// pattern already used in HandleUpdate's existing-size lookup.
	body, metadata, err := backend.Retrieve(r.Context(), key)
	if err != nil {
		return err
	}
	if body != nil {
		_ = body.Close()
	}

	storedOwner := ""
	if metadata != nil {
		storedOwner = metadata.OwnerID
	}

	callerOwner := r.Header.Get("X-Owner-Id")

	if storedOwner == "" {
		// Legacy / unowned object — only admin may mutate, and admin is
		// already handled above. Deny for scoped callers regardless of what
		// header they send.
		logger.Warn().
			Str("key", key).
			Str("key_name", scope.KeyName).
			Msg("Denied mutation of legacy (unowned) image by scoped key")
		return errors.New("image has no recorded owner; admin scope required")
	}

	if callerOwner == "" {
		return errors.New("X-Owner-Id header is required")
	}
	if callerOwner != storedOwner {
		logger.Warn().
			Str("key", key).
			Str("key_name", scope.KeyName).
			Msg("Owner mismatch on mutation attempt")
		return errors.New("caller is not the owner of this image")
	}
	return nil
}

// defaultStorageType returns the configured type of the default storage bucket
// for use in Prometheus metric labels.
func (h *Handler) defaultStorageType() string {
	if h.config.Storage.Default != "" {
		if b, ok := h.config.Storage.Buckets[h.config.Storage.Default]; ok && b.Type != "" {
			return b.Type
		}
	}
	return "unknown"
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
