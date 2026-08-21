package handlers

import (
	"bytes"
	jsonv2 "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

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

	// Un solo parseo del query para todo el handler (ver HandleDelivery).
	query := r.URL.Query()

	storageName := query.Get("storage")
	// customID arranca con el ?id= de la URL y lo pisan el campo del form o el
	// del JSON cuando vienen y son válidos. Antes esto se hacía reescribiendo
	// r.URL.RawQuery a mitad del handler para que la lectura de más abajo lo
	// viera — y la rama JSON además borraba el resto del query al hacerlo.
	customID := query.Get("id")
	bucket := utils.QueryParam(query, "b", "bucket")
	directory := utils.QueryParam(query, "d", "dir", "directory")

	directory = utils.NormalizeDirectoryPath(directory)
	if err := utils.ValidateDirectoryPath(directory); err != nil {
		h.sendError(w, http.StatusBadRequest, "INVALID_DIRECTORY", fmt.Sprintf("Invalid directory path: %v", err))
		return
	}

	contentType := r.Header.Get("Content-Type")

	// Limit request body size to prevent OOM from oversized uploads.
	maxBytes := int64(h.config.Processing.MaxFileSizeMB) * 1024 * 1024
	if maxBytes > 0 {
		r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	}

	var imageReader io.Reader
	var filename string
	var quality int
	var format string

	var imageData []byte
	var err error

	if strings.HasPrefix(contentType, "image/") {
		imageData, err = io.ReadAll(r.Body)
		if err != nil {
			h.sendError(w, http.StatusBadRequest, "READ_ERROR", "Failed to read image data")
			return
		}
		filename = "image" + utils.GetExtensionFromContentType(contentType)

		if q := query.Get("quality"); q != "" {
			if quality, err = strconv.Atoi(q); err != nil {
				h.sendError(w, http.StatusBadRequest, "INVALID_QUALITY", "Invalid quality parameter")
				return
			}
		}

		if f := query.Get("format"); f != "" {
			format = f
		}

	} else if strings.Contains(contentType, "multipart/form-data") {
		maxFormBytes := maxBytes
		if maxFormBytes <= 0 {
			maxFormBytes = 32 << 20
		}
		err := r.ParseMultipartForm(maxFormBytes)
		if err != nil {
			h.sendError(w, http.StatusBadRequest, "INVALID_REQUEST", "Failed to parse multipart form")
			return
		}

		file, header, err := r.FormFile("file")
		if err != nil {
			h.sendError(w, http.StatusBadRequest, "MISSING_FILE", "No file uploaded")
			return
		}
		defer file.Close()

		imageData, err = io.ReadAll(file)
		if err != nil {
			h.sendError(w, http.StatusBadRequest, "READ_ERROR", "Failed to read image data")
			return
		}
		filename = header.Filename

		if q := r.FormValue("quality"); q != "" {
			if quality, err = strconv.Atoi(q); err != nil {
				h.sendError(w, http.StatusBadRequest, "INVALID_QUALITY", "Invalid quality parameter")
				return
			}
		}

		if f := r.FormValue("format"); f != "" {
			format = f
		}

		if id := r.FormValue("id"); id != "" && utils.IsValidImageID(id) {
			customID = id
		}

	} else if contentType == "application/json" {
		var uploadReq struct {
			URL     string `json:"url"`
			Quality int    `json:"quality,omitzero"`
			Format  string `json:"format,omitempty"`
			ID      string `json:"id,omitempty"`
		}

		if err := jsonv2.UnmarshalRead(r.Body, &uploadReq, jsonx.Strict); err != nil {
			h.sendError(w, http.StatusBadRequest, "INVALID_JSON", "Invalid JSON payload")
			return
		}

		if uploadReq.URL == "" {
			h.sendError(w, http.StatusBadRequest, "MISSING_URL", "URL is required")
			return
		}

		parsedURL, err := url.Parse(uploadReq.URL)
		if err != nil {
			h.sendError(w, http.StatusBadRequest, "INVALID_URL", "Invalid URL format")
			return
		}

		if parsedURL.Scheme != "https" && parsedURL.Scheme != "http" {
			h.sendError(w, http.StatusBadRequest, "INVALID_URL", "URL must use HTTP or HTTPS protocol")
			return
		}

		if len(uploadReq.URL) > MaxURLLength {
			h.sendError(w, http.StatusBadRequest, "INVALID_URL", fmt.Sprintf("URL too long (max %d characters)", MaxURLLength))
			return
		}

		maxSize := h.config.GetMaxFileSizeBytes()
		imageData, _, err = httputil.DownloadURL(ctx, h.httpClient, uploadReq.URL, maxSize)
		if err != nil {
			h.sendError(w, http.StatusBadRequest, "DOWNLOAD_FAILED", fmt.Sprintf("Failed to download image: %v", err))
			return
		}

		filename = utils.ExtractFilenameFromURL(uploadReq.URL)
		quality = uploadReq.Quality
		format = uploadReq.Format

		if uploadReq.ID != "" && utils.IsValidImageID(uploadReq.ID) {
			customID = uploadReq.ID
		}

	} else {
		h.sendError(w, http.StatusBadRequest, "UNSUPPORTED_CONTENT_TYPE", "Unsupported content type")
		return
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
		h.sendError(w, http.StatusForbidden, "ACCESS_DENIED", sbErr.Error())
		return
	}

	// Owner identity is supplied by the caller (opaque string, typically a
	// user id from the upstream service). Stored on the object and enforced
	// on mutating operations — see HandleDelete / HandleUpdate. Empty is
	// allowed for admin-driven uploads; those images can then only be
	// mutated by an admin-scoped key.
	ownerID := r.Header.Get("X-Owner-Id")

	// Dedup-by-key: the storageKey is derived from a content hash of the raw
	// upload, so identical uploads land on the same key. The Jay backend is
	// idempotent by key — overwriting with the same bytes is a no-op at the
	// storage layer. We therefore skip the previous Exists + Retrieve-discard
	// round-trips and rely on Store alone. Callers still get the stable
	// `{id, url}` response, which is what dedup semantics actually guarantee.
	//
	// Detect content type to decide: process as image or store as passthrough
	detectedType := utils.DetectContentType(imageData)
	isImage := utils.IsImageContentType(detectedType)

	var storedMeta storage.ImageMetadata
	var storeReader io.Reader

	if isImage {
		// Process image through the pipeline
		imageReader = bytes.NewReader(imageData)
		processedImage, procErr := h.imageProcessor.Process(ctx, imageReader, &processor.ProcessingParams{
			Quality: quality,
			Format:  format,
		}, "")
		if procErr != nil {
			h.sendError(w, http.StatusUnprocessableEntity, "PROCESSING_FAILED", fmt.Sprintf("Failed to process image: %v", procErr))
			return
		}

		storedMeta = storage.ImageMetadata{
			ID:           imageID,
			OriginalName: filename,
			Format:       processedImage.Metadata.Format,
			Size:         processedImage.Metadata.Size,
			Width:        processedImage.Metadata.Width,
			Height:       processedImage.Metadata.Height,
			ContentType:  processedImage.Metadata.ContentType,
			CreatedAt:    processedImage.Metadata.CreatedAt,
			OwnerID:      ownerID,
		}
		storeReader = processedImage.Data
	} else {
		// Block dangerous content types that can execute code in browsers
		// (SVG with embedded JS, HTML, XML with XSLT, etc.)
		if utils.IsDangerousContentType(detectedType) {
			h.sendError(w, http.StatusUnsupportedMediaType, "DANGEROUS_CONTENT_TYPE",
				fmt.Sprintf("Content type %q is not allowed; SVG, HTML, and XML uploads are rejected for security", detectedType))
			return
		}

		// Passthrough: store file as-is without processing
		ext := filepath.Ext(filename)
		if ext != "" {
			ext = ext[1:] // remove leading dot
		}
		storedMeta = storage.ImageMetadata{
			ID:           imageID,
			OriginalName: filename,
			Format:       ext,
			Size:         int64(len(imageData)),
			ContentType:  detectedType,
			CreatedAt:    time.Now(),
			OwnerID:      ownerID,
		}
		storeReader = bytes.NewReader(imageData)
	}

	err = storageBackend.Store(ctx, storageKey, storeReader, &storedMeta)
	if err != nil {
		h.sendError(w, http.StatusInternalServerError, "STORAGE_ERROR", fmt.Sprintf("Failed to store file: %v", err))
		return
	}

	fileURL := utils.BuildImageURL(imageID, bucket, directory)

	response := types.UploadResponse{
		Success: true,
		Data: types.UploadData{
			ID:           imageID,
			URL:          fileURL,
			OriginalName: filename,
			Format:       storedMeta.Format,
			Size:         storedMeta.Size,
			Dimensions: types.Dimensions{
				Width:  storedMeta.Width,
				Height: storedMeta.Height,
			},
			CreatedAt: storedMeta.CreatedAt,
		},
	}

	writeJSON(w, http.StatusCreated, response)
}
