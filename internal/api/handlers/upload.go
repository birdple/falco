package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/birdple/falco/internal/api/types"
	"github.com/birdple/falco/internal/api/utils"
	"github.com/birdple/falco/internal/pkg/hashutil"
	"github.com/birdple/falco/internal/pkg/httputil"
	"github.com/birdple/falco/internal/processor"
	"github.com/birdple/falco/internal/storage"
)

// HandleUpload handles image upload requests
func (h *Handler) HandleUpload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Get bucket and directory parameters
	bucket := r.URL.Query().Get("b")
	if bucket == "" {
		bucket = r.URL.Query().Get("bucket")
	}

	directory := r.URL.Query().Get("d")
	if directory == "" {
		directory = r.URL.Query().Get("dir")
	}
	if directory == "" {
		directory = r.URL.Query().Get("directory")
	}

	// Normalize and validate directory path
	directory = utils.NormalizeDirectoryPath(directory)
	if err := utils.ValidateDirectoryPath(directory); err != nil {
		h.sendError(w, http.StatusBadRequest, "INVALID_DIRECTORY", fmt.Sprintf("Invalid directory path: %v", err))
		return
	}

	// Check content type
	contentType := r.Header.Get("Content-Type")

	var imageReader io.Reader
	var filename string
	var quality int
	var format string

	var imageData []byte
	var err error
	var contentTypeFromURL string

	// Handle direct binary upload (image/*)
	if strings.HasPrefix(contentType, "image/") {
		// Read the entire body into memory
		imageData, err = io.ReadAll(r.Body)
		if err != nil {
			h.sendError(w, http.StatusBadRequest, "READ_ERROR", "Failed to read image data")
			return
		}
		filename = "image" + utils.GetExtensionFromContentType(contentType)

		// Get optional parameters from query string
		if q := r.URL.Query().Get("quality"); q != "" {
			if quality, err = strconv.Atoi(q); err != nil {
				h.sendError(w, http.StatusBadRequest, "INVALID_QUALITY", "Invalid quality parameter")
				return
			}
		}

		if f := r.URL.Query().Get("format"); f != "" {
			format = f
		}

	} else if strings.Contains(contentType, "multipart/form-data") {
		// Parse multipart form
		err := r.ParseMultipartForm(32 << 20) // 32MB max memory
		if err != nil {
			h.sendError(w, http.StatusBadRequest, "INVALID_REQUEST", "Failed to parse multipart form")
			return
		}

		// Get uploaded file
		file, header, err := r.FormFile("file")
		if err != nil {
			h.sendError(w, http.StatusBadRequest, "MISSING_FILE", "No file uploaded")
			return
		}
		defer file.Close()

		// Read file data
		imageData, err = io.ReadAll(file)
		if err != nil {
			h.sendError(w, http.StatusBadRequest, "READ_ERROR", "Failed to read image data")
			return
		}
		filename = header.Filename

		// Get optional parameters
		if q := r.FormValue("quality"); q != "" {
			if quality, err = strconv.Atoi(q); err != nil {
				h.sendError(w, http.StatusBadRequest, "INVALID_QUALITY", "Invalid quality parameter")
				return
			}
		}

		if f := r.FormValue("format"); f != "" {
			format = f
		}

		// Check for custom ID in form data
		if id := r.FormValue("id"); id != "" {
			if utils.IsValidImageID(id) {
				// Store for later use
				r.URL.RawQuery = fmt.Sprintf("id=%s&%s", id, r.URL.RawQuery)
			}
		}

	} else if contentType == "application/json" {
		// Handle URL-based upload
		var uploadReq struct {
			URL     string `json:"url"`
			Quality int    `json:"quality,omitempty"`
			Format  string `json:"format,omitempty"`
			ID      string `json:"id,omitempty"`
		}

		if err := json.NewDecoder(r.Body).Decode(&uploadReq); err != nil {
			h.sendError(w, http.StatusBadRequest, "INVALID_JSON", "Invalid JSON payload")
			return
		}

		if uploadReq.URL == "" {
			h.sendError(w, http.StatusBadRequest, "MISSING_URL", "URL is required")
			return
		}

		// Validate URL with stricter checks
		parsedURL, err := url.Parse(uploadReq.URL)
		if err != nil {
			h.sendError(w, http.StatusBadRequest, "INVALID_URL", "Invalid URL format")
			return
		}

		// Only allow HTTPS in production, allow HTTP in development
		if parsedURL.Scheme != "https" && parsedURL.Scheme != "http" {
			h.sendError(w, http.StatusBadRequest, "INVALID_URL", "URL must use HTTP or HTTPS protocol")
			return
		}

		// Additional validation for URL length and domain
		if len(uploadReq.URL) > 2048 {
			h.sendError(w, http.StatusBadRequest, "INVALID_URL", "URL too long (max 2048 characters)")
			return
		}

		// Download image from URL with timeout and validation
		maxSize := h.config.GetMaxFileSizeBytes()
		imageData, contentTypeFromURL, err = httputil.DownloadURL(ctx, h.httpClient, uploadReq.URL, maxSize)
		if err != nil {
			h.logger.WithError(err).Error("Failed to download image from URL")
			h.sendError(w, http.StatusBadRequest, "DOWNLOAD_FAILED", fmt.Sprintf("Failed to download image: %v", err))
			return
		}

		// Update content type from downloaded file
		_ = contentTypeFromURL // Content type already validated by DownloadURL
		filename = utils.ExtractFilenameFromURL(uploadReq.URL)
		quality = uploadReq.Quality
		format = uploadReq.Format

		// Check for custom ID in JSON payload
		if uploadReq.ID != "" {
			if utils.IsValidImageID(uploadReq.ID) {
				r.URL.RawQuery = fmt.Sprintf("id=%s", uploadReq.ID)
			}
		}

	} else {
		h.sendError(w, http.StatusBadRequest, "UNSUPPORTED_CONTENT_TYPE", "Unsupported content type")
		return
	}

	// Generate image ID from hash of original data, or use provided ID
	var imageID string

	// Check for optional 'id' parameter
	if customID := r.URL.Query().Get("id"); customID != "" {
		// Validate custom ID (alphanumeric, hyphens, underscores only)
		if utils.IsValidImageID(customID) {
			imageID = customID
		} else {
			h.sendError(w, http.StatusBadRequest, "INVALID_ID", "Invalid ID format. Use alphanumeric characters, hyphens, and underscores only")
			return
		}
	} else {
		// Use hash-based ID generation
		imageID = hashutil.GenerateImageIDFromData(imageData)
	}

	// Build full storage key with directory and image ID
	storageKey := utils.BuildStorageKey(directory, imageID)

	// Get bucket-aware storage instance
	storageBackend := h.getStorageForBucket(bucket)

	// Check if image already exists (using bucket-aware storage)
	exists, err := storageBackend.Exists(ctx, storageKey)
	if err != nil {
		h.logger.WithError(err).Warn("Failed to check image existence")
	}

	if exists {
		// Image already exists, retrieve metadata and return
		h.logger.WithField("image_id", imageID).Info("Image already exists, returning existing")

		_, metadata, err := storageBackend.Retrieve(ctx, storageKey)
		if err != nil {
			h.logger.WithError(err).Error("Failed to retrieve existing image metadata")
			h.sendError(w, http.StatusInternalServerError, "RETRIEVAL_ERROR", "Failed to retrieve image")
			return
		}

		// Build URL with bucket and directory parameters if provided
		imageURL := utils.BuildImageURL(imageID, bucket, directory)

		// Return existing image data
		response := types.UploadResponse{
			Success: true,
			Data: types.UploadData{
				ID:           imageID,
				URL:          imageURL,
				OriginalName: metadata.OriginalName,
				Format:       metadata.Format,
				Size:         metadata.Size,
				Dimensions: types.Dimensions{
					Width:  metadata.Width,
					Height: metadata.Height,
				},
				CreatedAt: metadata.CreatedAt,
			},
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK) // 200 instead of 201 for existing
		json.NewEncoder(w).Encode(response)
		return
	}

	// Image doesn't exist, process it
	imageReader = bytes.NewReader(imageData)

	// Process the image
	processedImage, err := h.imageProcessor.Process(ctx, imageReader, &processor.ProcessingParams{
		Quality: quality,
		Format:  format,
	})
	if err != nil {
		h.logger.WithError(err).Error("Failed to process image")
		h.sendError(w, http.StatusUnprocessableEntity, "PROCESSING_FAILED", "Failed to process image")
		return
	}

	// Store the processed image with full storage key
	err = storageBackend.Store(ctx, storageKey, processedImage.Data, &storage.ImageMetadata{
		ID:           imageID,
		OriginalName: filename,
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

	// Build URL with bucket and directory parameters if provided
	imageURL := utils.BuildImageURL(imageID, bucket, directory)

	// Send success response
	response := types.UploadResponse{
		Success: true,
		Data: types.UploadData{
			ID:           imageID,
			URL:          imageURL,
			OriginalName: filename,
			Format:       processedImage.Metadata.Format,
			Size:         processedImage.Metadata.Size,
			Dimensions: types.Dimensions{
				Width:  processedImage.Metadata.Width,
				Height: processedImage.Metadata.Height,
			},
			CreatedAt: processedImage.Metadata.CreatedAt,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(response)
}
