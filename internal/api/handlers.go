package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ivangsm/imagine/internal/processor"
	"github.com/ivangsm/imagine/internal/storage"
)

// UploadResponse represents the response for successful uploads
type UploadResponse struct {
	Success bool       `json:"success"`
	Data    UploadData `json:"data,omitempty"`
	Error   *APIError  `json:"error,omitempty"`
}

// UploadData contains information about uploaded images
type UploadData struct {
	ID           string     `json:"id"`
	URL          string     `json:"url"`
	OriginalName string     `json:"original_name"`
	Format       string     `json:"format"`
	Size         int64      `json:"size"`
	Dimensions   Dimensions `json:"dimensions"`
	CreatedAt    time.Time  `json:"created_at"`
}

// Dimensions represents image dimensions
type Dimensions struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// APIError represents API error responses
type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// handleUpload handles image upload requests
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Check content type
	contentType := r.Header.Get("Content-Type")

	var imageReader io.Reader
	var filename string
	var quality int
	var format string

	// Handle multipart form upload
	if strings.Contains(contentType, "multipart/form-data") {
		// Parse multipart form
		err := r.ParseMultipartForm(32 << 20) // 32MB max memory
		if err != nil {
			s.sendError(w, http.StatusBadRequest, "INVALID_REQUEST", "Failed to parse multipart form")
			return
		}

		// Get uploaded file
		file, header, err := r.FormFile("file")
		if err != nil {
			s.sendError(w, http.StatusBadRequest, "MISSING_FILE", "No file uploaded")
			return
		}
		defer file.Close()

		imageReader = file
		filename = header.Filename

		// Get optional parameters
		if q := r.FormValue("quality"); q != "" {
			if quality, err = strconv.Atoi(q); err != nil {
				s.sendError(w, http.StatusBadRequest, "INVALID_QUALITY", "Invalid quality parameter")
				return
			}
		}

		if f := r.FormValue("format"); f != "" {
			format = f
		}

	} else if contentType == "application/json" {
		// Handle URL-based upload
		var uploadReq struct {
			URL     string `json:"url"`
			Quality int    `json:"quality,omitempty"`
			Format  string `json:"format,omitempty"`
		}

		if err := json.NewDecoder(r.Body).Decode(&uploadReq); err != nil {
			s.sendError(w, http.StatusBadRequest, "INVALID_JSON", "Invalid JSON payload")
			return
		}

		if uploadReq.URL == "" {
			s.sendError(w, http.StatusBadRequest, "MISSING_URL", "URL is required")
			return
		}

		// Validate URL
		if _, err := url.Parse(uploadReq.URL); err != nil {
			s.sendError(w, http.StatusBadRequest, "INVALID_URL", "Invalid URL format")
			return
		}

		// Download image from URL
		resp, err := http.Get(uploadReq.URL)
		if err != nil {
			s.logger.WithError(err).Error("Failed to download image from URL")
			s.sendError(w, http.StatusBadRequest, "DOWNLOAD_FAILED", "Failed to download image from URL")
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			s.sendError(w, http.StatusBadRequest, "DOWNLOAD_FAILED", "Failed to download image from URL")
			return
		}

		imageReader = resp.Body
		filename = extractFilenameFromURL(uploadReq.URL)
		quality = uploadReq.Quality
		format = uploadReq.Format

	} else {
		s.sendError(w, http.StatusBadRequest, "UNSUPPORTED_CONTENT_TYPE", "Unsupported content type")
		return
	}

	// Generate unique ID for the image
	imageID := generateImageID()

	// Process the image
	processedImage, err := s.imageProcessor.Process(ctx, imageReader, &processor.ProcessingParams{
		Quality: quality,
		Format:  format,
	})
	if err != nil {
		s.logger.WithError(err).Error("Failed to process image")
		s.sendError(w, http.StatusUnprocessableEntity, "PROCESSING_FAILED", "Failed to process image")
		return
	}

	// Store the processed image
	err = s.storage.Store(ctx, imageID, processedImage.Data, &storage.ImageMetadata{
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
		s.logger.WithError(err).Error("Failed to store image")
		s.sendError(w, http.StatusInternalServerError, "STORAGE_ERROR", "Failed to store image")
		return
	}

	// Send success response
	response := UploadResponse{
		Success: true,
		Data: UploadData{
			ID:           imageID,
			URL:          fmt.Sprintf("/api/v1/images/%s", imageID),
			OriginalName: filename,
			Format:       processedImage.Metadata.Format,
			Size:         processedImage.Metadata.Size,
			Dimensions: Dimensions{
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

// handleDelivery handles image delivery requests with optional transformations
func (s *Server) handleDelivery(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	imageID := chi.URLParam(r, "id")

	if imageID == "" {
		s.sendError(w, http.StatusBadRequest, "MISSING_ID", "Image ID is required")
		return
	}

	// Parse query parameters for transformations
	params := &processor.ProcessingParams{}

	if width := r.URL.Query().Get("w"); width != "" {
		if widthVal, err := strconv.Atoi(width); err == nil && widthVal > 0 && widthVal <= s.config.Processing.MaxDimensions.Width {
			params.Width = widthVal
		} else {
			s.sendError(w, http.StatusBadRequest, "INVALID_WIDTH", "Invalid width parameter")
			return
		}
	}

	if height := r.URL.Query().Get("h"); height != "" {
		if h, err := strconv.Atoi(height); err == nil && h > 0 && h <= s.config.Processing.MaxDimensions.Height {
			params.Height = h
		} else {
			s.sendError(w, http.StatusBadRequest, "INVALID_HEIGHT", "Invalid height parameter")
			return
		}
	}

	if quality := r.URL.Query().Get("q"); quality != "" {
		if q, err := strconv.Atoi(quality); err == nil && q > 0 && q <= 100 {
			params.Quality = q
		} else {
			s.sendError(w, http.StatusBadRequest, "INVALID_QUALITY", "Invalid quality parameter")
			return
		}
	}

	if format := r.URL.Query().Get("f"); format != "" {
		if s.imageProcessor.ValidateFormat(format) {
			params.Format = format
		} else {
			s.sendError(w, http.StatusBadRequest, "INVALID_FORMAT", "Unsupported format")
			return
		}
	}

	if fit := r.URL.Query().Get("fit"); fit != "" {
		if fit == "cover" || fit == "contain" || fit == "fill" {
			params.Fit = fit
		} else {
			s.sendError(w, http.StatusBadRequest, "INVALID_FIT", "Invalid fit parameter")
			return
		}
	}

	// Retrieve original image
	reader, metadata, err := s.storage.Retrieve(ctx, imageID)
	if err != nil {
		if storage.IsNotFound(err) {
			s.sendError(w, http.StatusNotFound, "IMAGE_NOT_FOUND", "Image not found")
			return
		}
		s.logger.WithError(err).Error("Failed to retrieve image")
		s.sendError(w, http.StatusInternalServerError, "RETRIEVAL_ERROR", "Failed to retrieve image")
		return
	}
	defer reader.Close()

	// If no transformations requested, serve original
	if params.Width == 0 && params.Height == 0 && params.Quality == 0 && params.Format == "" {
		s.serveImage(w, reader, metadata)
		return
	}

	// Process image with transformations
	processedImage, err := s.imageProcessor.Process(ctx, reader, params)
	if err != nil {
		s.logger.WithError(err).Error("Failed to process image")
		s.sendError(w, http.StatusUnprocessableEntity, "PROCESSING_FAILED", "Failed to process image")
		return
	}
	defer processedImage.Data.Close()

	// Convert processor metadata to storage metadata
	storageMetadata := &storage.ImageMetadata{
		ID:           processedImage.Metadata.ID,
		OriginalName: processedImage.Metadata.OriginalName,
		Format:       processedImage.Metadata.Format,
		Size:         processedImage.Metadata.Size,
		Width:        processedImage.Metadata.Width,
		Height:       processedImage.Metadata.Height,
		ContentType:  processedImage.Metadata.ContentType,
		CreatedAt:    processedImage.Metadata.CreatedAt,
	}

	s.serveImage(w, processedImage.Data, storageMetadata)
}

// handleHealth handles health check requests
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	health := map[string]interface{}{
		"status":    "healthy",
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"version":   "1.0.0",
		"uptime":    "unknown", // TODO: Implement uptime tracking
	}

	// Check storage health
	storageHealth := map[string]string{}
	if err := s.storage.Health(ctx); err != nil {
		storageHealth["status"] = "unhealthy"
		storageHealth["error"] = err.Error()
	} else {
		storageHealth["status"] = "healthy"
	}

	// Get storage stats
	if stats, err := s.storage.GetStats(ctx); err == nil {
		health["storage"] = map[string]interface{}{
			"status":       storageHealth["status"],
			"total_images": stats.TotalImages,
			"total_size":   stats.TotalSize,
			"free_space":   stats.FreeSpace,
		}
	} else {
		health["storage"] = storageHealth
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(health)
}

// Helper functions

func (s *Server) sendError(w http.ResponseWriter, statusCode int, code, message string) {
	response := UploadResponse{
		Success: false,
		Error: &APIError{
			Code:    code,
			Message: message,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(response)
}

func (s *Server) serveImage(w http.ResponseWriter, reader io.Reader, metadata *storage.ImageMetadata) {
	w.Header().Set("Content-Type", metadata.ContentType)
	w.Header().Set("Cache-Control", "public, max-age=31536000") // 1 year
	w.Header().Set("ETag", fmt.Sprintf(`"%s"`, metadata.ID))

	// Copy image data to response
	io.Copy(w, reader)
}

func generateImageID() string {
	return "img_" + strings.ReplaceAll(uuid.New().String(), "-", "")
}

func extractFilenameFromURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "image"
	}

	path := u.Path
	segments := strings.Split(path, "/")
	if len(segments) > 0 {
		return segments[len(segments)-1]
	}

	return "image"
}
