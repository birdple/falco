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
	"github.com/birdple/falco/internal/pkg/logger"
	"github.com/birdple/falco/internal/processor"
	"github.com/birdple/falco/internal/storage"
)

// HandleUpload handles image upload requests
func (h *Handler) HandleUpload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

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

	directory = utils.NormalizeDirectoryPath(directory)
	if err := utils.ValidateDirectoryPath(directory); err != nil {
		h.sendError(w, http.StatusBadRequest, "INVALID_DIRECTORY", fmt.Sprintf("Invalid directory path: %v", err))
		return
	}

	contentType := r.Header.Get("Content-Type")

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
		err := r.ParseMultipartForm(32 << 20)
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

		if id := r.FormValue("id"); id != "" {
			if utils.IsValidImageID(id) {
				r.URL.RawQuery = fmt.Sprintf("id=%s&%s", id, r.URL.RawQuery)
			}
		}

	} else if contentType == "application/json" {
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

		parsedURL, err := url.Parse(uploadReq.URL)
		if err != nil {
			h.sendError(w, http.StatusBadRequest, "INVALID_URL", "Invalid URL format")
			return
		}

		if parsedURL.Scheme != "https" && parsedURL.Scheme != "http" {
			h.sendError(w, http.StatusBadRequest, "INVALID_URL", "URL must use HTTP or HTTPS protocol")
			return
		}

		if len(uploadReq.URL) > 2048 {
			h.sendError(w, http.StatusBadRequest, "INVALID_URL", "URL too long (max 2048 characters)")
			return
		}

		maxSize := h.config.GetMaxFileSizeBytes()
		imageData, _, err = httputil.DownloadURL(ctx, h.httpClient, uploadReq.URL, maxSize)
		if err != nil {
			logger.Error().Err(err).Msg("Failed to download image from URL")
			h.sendError(w, http.StatusBadRequest, "DOWNLOAD_FAILED", fmt.Sprintf("Failed to download image: %v", err))
			return
		}

		filename = utils.ExtractFilenameFromURL(uploadReq.URL)
		quality = uploadReq.Quality
		format = uploadReq.Format

		if uploadReq.ID != "" {
			if utils.IsValidImageID(uploadReq.ID) {
				r.URL.RawQuery = fmt.Sprintf("id=%s", uploadReq.ID)
			}
		}

	} else {
		h.sendError(w, http.StatusBadRequest, "UNSUPPORTED_CONTENT_TYPE", "Unsupported content type")
		return
	}

	var imageID string

	if customID := r.URL.Query().Get("id"); customID != "" {
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
	storageBackend := h.getStorageForBucket(bucket)

	exists, err := storageBackend.Exists(ctx, storageKey)
	if err != nil {
		logger.Warn().Err(err).Msg("Failed to check image existence")
	}

	if exists {
		logger.Info().Str("image_id", imageID).Msg("Image already exists, returning existing")

		_, metadata, err := storageBackend.Retrieve(ctx, storageKey)
		if err != nil {
			logger.Error().Err(err).Msg("Failed to retrieve existing image metadata")
			h.sendError(w, http.StatusInternalServerError, "RETRIEVAL_ERROR", "Failed to retrieve image")
			return
		}

		imageURL := utils.BuildImageURL(imageID, bucket, directory)

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
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(response)
		return
	}

	imageReader = bytes.NewReader(imageData)

	processedImage, err := h.imageProcessor.Process(ctx, imageReader, &processor.ProcessingParams{
		Quality: quality,
		Format:  format,
	})
	if err != nil {
		logger.Error().Err(err).Msg("Failed to process image")
		h.sendError(w, http.StatusUnprocessableEntity, "PROCESSING_FAILED", "Failed to process image")
		return
	}

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
		logger.Error().Err(err).Msg("Failed to store image")
		h.sendError(w, http.StatusInternalServerError, "STORAGE_ERROR", "Failed to store image")
		return
	}

	imageURL := utils.BuildImageURL(imageID, bucket, directory)

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
