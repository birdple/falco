package handlers

import (
	"bytes"
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	apimw "github.com/birdple/falco/internal/api/middleware"
	"github.com/birdple/falco/internal/api/types"
	"github.com/birdple/falco/internal/api/utils"
	"github.com/birdple/falco/internal/jsonx"
	"github.com/birdple/falco/internal/pkg/hashutil"
	"github.com/birdple/falco/internal/pkg/httputil"
	"github.com/birdple/falco/internal/processor"
	"github.com/birdple/falco/internal/storage"
)

// HandleUpload handles image upload requests
func (h *Handler) HandleUpload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Parsed once for the whole handler (see HandleDelivery).
	query := r.URL.Query()

	storageName := query.Get("storage")
	// customID starts as the URL's ?id= and is overridden by the form or JSON
	// field when one arrives and is valid. Never by rewriting r.URL.RawQuery.
	customID := query.Get("id")
	bucket := utils.QueryParam(query, "b", "bucket")
	directory := utils.QueryParam(query, "d", "dir", "directory")

	directory = utils.NormalizeDirectoryPath(directory)
	if err := utils.ValidateDirectoryPath(directory); err != nil {
		h.sendError(w, http.StatusBadRequest, "INVALID_DIRECTORY", fmt.Sprintf("Invalid directory path: %v", err))
		return
	}

	// Limit request body size to prevent OOM from oversized uploads.
	maxBytes := int64(h.config.Processing.MaxFileSizeMB) * 1024 * 1024
	if maxBytes > 0 {
		r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	}

	payload, upErr := h.parseUploadPayload(ctx, r, query, maxBytes)
	if upErr != nil {
		h.sendError(w, upErr.status, upErr.code, upErr.message)
		return
	}
	imageData, filename, quality, format := payload.data, payload.filename, payload.quality, payload.format
	if payload.customID != "" {
		customID = payload.customID
	}

	var imageID string

	if customID != "" {
		if utils.IsValidImageID(customID) {
			imageID = customID
		} else {
			h.sendError(w, http.StatusBadRequest, "INVALID_ID", "Invalid ID format. Use alphanumeric characters, hyphens, and underscores only")
			return
		}
	} else {
		imageID = hashutil.GenerateImageIDFromData(imageData)
	}

	storageKey := utils.BuildStorageKey(directory, imageID)
	storageBackend, sbErr := h.getStorageBackendScoped(r, storageName, bucket)
	if sbErr != nil {
		h.sendStorageBackendError(w, sbErr)
		return
	}

	// Owner identity is supplied by the caller (opaque string, typically a
	// user id from the upstream service). Stored on the object and enforced
	// on mutating operations — see HandleDelete / HandleUpdate. Empty is
	// allowed for admin-driven uploads; those images can then only be
	// mutated by an admin-scoped key.
	ownerID := r.Header.Get("X-Owner-Id")

	// Dedup by key: storageKey derives from the content hash, so identical
	// uploads land on the same key and Store with the same bytes is a no-op at
	// the backend. A scoped caller, though, must not replace — or take the
	// ownership of — an object somebody else owns. With a caller-chosen id that
	// is a conflict; with a content-hash id the bytes are identical by
	// definition, so the existing object is the answer, left as it is.
	foreign, err := foreignObject(r, storageBackend, storageKey)
	switch {
	case err != nil:
		fe := retrieveFailure(err)
		h.sendError(w, fe.status, fe.code, fe.message)
		return
	case foreign != nil && customID != "":
		h.sendError(w, http.StatusConflict, "OBJECT_EXISTS", "An object with this id already exists and belongs to another owner")
		return
	case foreign != nil:
		writeUploadResponse(w, imageID, bucket, directory, foreign.OriginalName, foreign)
		return
	}

	storeReader, storedMeta, prepErr := h.prepareForStorage(ctx, imageData, uploadTarget{
		imageID:  imageID,
		filename: filename,
		ownerID:  ownerID,
		quality:  quality,
		format:   format,
	})
	if prepErr != nil {
		h.sendError(w, prepErr.status, prepErr.code, prepErr.message)
		return
	}

	if err := storageBackend.Store(ctx, storageKey, storeReader, &storedMeta); err != nil {
		h.sendError(w, http.StatusInternalServerError, "STORAGE_ERROR", fmt.Sprintf("Failed to store file: %v", err))
		return
	}
	// A custom ?id= can overwrite an existing object under the same key, and
	// the cache key does not change with the bytes.
	h.invalidateCache(h.backendNamespace(storageName, bucket), storageKey)

	writeUploadResponse(w, imageID, bucket, directory, filename, &storedMeta)
}

// writeUploadResponse answers a successful upload.
func writeUploadResponse(w http.ResponseWriter, imageID, bucket, directory, filename string, meta *storage.ImageMetadata) {
	writeJSON(w, http.StatusCreated, types.UploadResponse{
		Success: true,
		Data: types.UploadData{
			ID:           imageID,
			URL:          utils.BuildImageURL(imageID, bucket, directory),
			OriginalName: filename,
			Format:       meta.Format,
			Size:         meta.Size,
			Dimensions: types.Dimensions{
				Width:  meta.Width,
				Height: meta.Height,
			},
			CreatedAt: meta.CreatedAt,
		},
	})
}

// foreignObject returns the object already stored under key when a scoped
// caller does not own it, or nil when the caller may write there: nothing is
// stored, the caller owns it, or the caller is an admin (or auth is off).
func foreignObject(r *http.Request, backend storage.StorageBackend, key string) (*storage.ImageMetadata, error) {
	scope := apimw.GetScope(r.Context())
	if scope == nil || scope.IsAdmin {
		return nil, nil
	}
	existing, err := backend.Stat(r.Context(), key)
	switch {
	case storage.IsNotFound(err):
		return nil, nil
	case err != nil:
		return nil, err
	case existing == nil:
		return nil, nil
	}
	if existing.OwnerID != "" && existing.OwnerID == r.Header.Get("X-Owner-Id") {
		return nil, nil
	}
	return existing, nil
}

// uploadPayload is an upload request whose body has been read, whatever shape it
// arrived in.
//
// customID is empty unless the body carried one; the caller keeps whatever came
// from ?id= in that case.
type uploadPayload struct {
	data     []byte
	filename string
	quality  int
	format   string
	customID string
}

// parseUploadPayload reads the image out of the request according to its
// Content-Type. Falco accepts three shapes and they are genuinely different
// transports, not variations of one:
//
//   - image/*: the raw bytes are the body. Options come from the query string.
//   - multipart/form-data: a browser file upload. Options come from form fields.
//   - application/json: a URL for falco to fetch itself, with options as JSON.
//
// Anything else is rejected rather than guessed at.
func (h *Handler) parseUploadPayload(
	ctx context.Context, r *http.Request, query url.Values, maxBytes int64,
) (uploadPayload, *fetchError) {
	contentType := r.Header.Get("Content-Type")

	switch {
	case strings.HasPrefix(contentType, "image/"):
		return readRawBody(r, query, contentType)
	case strings.Contains(contentType, "multipart/form-data"):
		return readMultipartBody(r, maxBytes)
	case contentType == "application/json":
		return h.downloadFromJSONBody(ctx, r)
	default:
		return uploadPayload{}, &fetchError{http.StatusBadRequest, "UNSUPPORTED_CONTENT_TYPE", "Unsupported content type"}
	}
}

// bodyReadFailure maps an error reading the request body: past the
// MAX_FILE_SIZE_MB limit it is a 413, like a body the middleware refused up
// front for its Content-Length; anything else is the caller's malformed
// request.
func bodyReadFailure(err error, code, message string) *fetchError {
	if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
		return &fetchError{http.StatusRequestEntityTooLarge, "REQUEST_TOO_LARGE", "Image exceeds the maximum upload size"}
	}
	return &fetchError{http.StatusBadRequest, code, message}
}

// readRawBody handles an upload whose body is the image itself.
func readRawBody(r *http.Request, query url.Values, contentType string) (uploadPayload, *fetchError) {
	data, err := io.ReadAll(r.Body)
	if err != nil {
		return uploadPayload{}, bodyReadFailure(err, "READ_ERROR", "Failed to read image data")
	}

	payload := uploadPayload{
		data:     data,
		filename: "image" + utils.GetExtensionFromContentType(contentType),
		format:   query.Get("format"),
	}

	if raw := query.Get("quality"); raw != "" {
		quality, err := strconv.Atoi(raw)
		if err != nil {
			return uploadPayload{}, &fetchError{http.StatusBadRequest, "INVALID_QUALITY", "Invalid quality parameter"}
		}
		payload.quality = quality
	}
	return payload, nil
}

// readMultipartBody handles a browser file upload.
func readMultipartBody(r *http.Request, maxBytes int64) (uploadPayload, *fetchError) {
	maxFormBytes := maxBytes
	if maxFormBytes <= 0 {
		maxFormBytes = defaultMaxFormBytes
	}
	if err := r.ParseMultipartForm(maxFormBytes); err != nil {
		return uploadPayload{}, bodyReadFailure(err, "INVALID_REQUEST", "Failed to parse multipart form")
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		return uploadPayload{}, &fetchError{http.StatusBadRequest, "MISSING_FILE", "No file uploaded"}
	}
	defer func() { _ = file.Close() }()

	data, err := io.ReadAll(file)
	if err != nil {
		return uploadPayload{}, bodyReadFailure(err, "READ_ERROR", "Failed to read image data")
	}

	payload := uploadPayload{
		data:     data,
		filename: header.Filename,
		format:   r.FormValue("format"),
	}

	if raw := r.FormValue("quality"); raw != "" {
		quality, err := strconv.Atoi(raw)
		if err != nil {
			return uploadPayload{}, &fetchError{http.StatusBadRequest, "INVALID_QUALITY", "Invalid quality parameter"}
		}
		payload.quality = quality
	}

	// An invalid id in the form is ignored rather than rejected: the caller
	// keeps whatever ?id= carried, and an empty one falls back to the content
	// hash.
	if id := r.FormValue("id"); id != "" && utils.IsValidImageID(id) {
		payload.customID = id
	}
	return payload, nil
}

// downloadFromJSONBody handles an upload that names a URL for falco to fetch.
func (h *Handler) downloadFromJSONBody(ctx context.Context, r *http.Request) (uploadPayload, *fetchError) {
	var req struct {
		URL     string `json:"url"`
		Quality int    `json:"quality,omitzero"`
		Format  string `json:"format,omitempty"`
		ID      string `json:"id,omitempty"`
	}

	if err := jsonv2.UnmarshalRead(r.Body, &req, jsonx.Strict); err != nil {
		return uploadPayload{}, &fetchError{http.StatusBadRequest, "INVALID_JSON", "Invalid JSON payload"}
	}
	if req.URL == "" {
		return uploadPayload{}, &fetchError{http.StatusBadRequest, "MISSING_URL", "URL is required"}
	}

	parsedURL, err := url.Parse(req.URL)
	switch {
	case err != nil:
		return uploadPayload{}, &fetchError{http.StatusBadRequest, "INVALID_URL", "Invalid URL format"}
	case parsedURL.Scheme != "https" && parsedURL.Scheme != "http":
		return uploadPayload{}, &fetchError{http.StatusBadRequest, "INVALID_URL", "URL must use HTTP or HTTPS protocol"}
	case len(req.URL) > MaxURLLength:
		return uploadPayload{}, &fetchError{
			http.StatusBadRequest, "INVALID_URL",
			fmt.Sprintf("URL too long (max %d characters)", MaxURLLength),
		}
	}

	// This endpoint is authenticated and exists to fetch a URL the operator
	// chose, so there is no host allowlist to preserve across a redirect —
	// but the policy has to be stated: a fetch that declares none follows no
	// redirect at all. The dial-time guard still blocks private addresses.
	data, _, err := httputil.DownloadURL(
		httputil.WithAnyPublicHost(ctx), h.httpClient, req.URL, h.config.GetMaxFileSizeBytes(),
	)
	if err != nil {
		return uploadPayload{}, &fetchError{
			http.StatusBadRequest, "DOWNLOAD_FAILED",
			fmt.Sprintf("Failed to download image: %v", err),
		}
	}

	payload := uploadPayload{
		data:     data,
		filename: utils.ExtractFilenameFromURL(req.URL),
		quality:  req.Quality,
		format:   req.Format,
	}
	if req.ID != "" && utils.IsValidImageID(req.ID) {
		payload.customID = req.ID
	}
	return payload, nil
}

// uploadTarget is what an upload will be stored as, once its bytes are read.
type uploadTarget struct {
	imageID  string
	filename string
	ownerID  string
	quality  int
	format   string
}

// prepareForStorage turns raw upload bytes into something ready to store, and
// decides which of the two shapes it takes.
//
// An image goes through the processing pipeline and gets normalised. Anything
// else is stored byte-for-byte — falco is an object store as well as an image
// service, and re-encoding a PDF would corrupt it.
//
// The passthrough branch is where the dangerous-type check lives, and it has to:
// SVG, HTML and XML can execute script in a browser, and serving them from
// falco's origin would run that script with falco's origin's privileges.
func (h *Handler) prepareForStorage(
	ctx context.Context, imageData []byte, target uploadTarget,
) (io.Reader, storage.ImageMetadata, *fetchError) {
	detectedType := utils.DetectContentType(imageData)

	if !utils.IsImageContentType(detectedType) {
		if utils.IsDangerousContentType(detectedType) {
			return nil, storage.ImageMetadata{}, &fetchError{
				http.StatusUnsupportedMediaType, "DANGEROUS_CONTENT_TYPE",
				fmt.Sprintf("Content type %q is not allowed; SVG, HTML, and XML uploads are rejected for security", detectedType),
			}
		}

		ext := strings.TrimPrefix(filepath.Ext(target.filename), ".")
		meta := storage.ImageMetadata{
			ID:           target.imageID,
			OriginalName: target.filename,
			Format:       ext,
			Size:         int64(len(imageData)),
			ContentType:  detectedType,
			CreatedAt:    time.Now(),
			OwnerID:      target.ownerID,
		}
		return bytes.NewReader(imageData), meta, nil
	}

	processed, err := h.imageProcessor.Process(ctx, bytes.NewReader(imageData), &processor.ProcessingParams{
		Quality: target.quality,
		Format:  target.format,
	}, "")
	if err != nil {
		return nil, storage.ImageMetadata{}, &fetchError{
			http.StatusUnprocessableEntity, "PROCESSING_FAILED",
			fmt.Sprintf("Failed to process image: %v", err),
		}
	}

	meta := storage.ImageMetadata{
		ID:           target.imageID,
		OriginalName: target.filename,
		Format:       processed.Metadata.Format,
		Size:         processed.Metadata.Size,
		Width:        processed.Metadata.Width,
		Height:       processed.Metadata.Height,
		ContentType:  processed.Metadata.ContentType,
		CreatedAt:    processed.Metadata.CreatedAt,
		OwnerID:      target.ownerID,
	}
	return processed.Data, meta, nil
}
