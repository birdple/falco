package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/birdple/imagine/internal/pkg/hashutil"
	"github.com/birdple/imagine/internal/processor"
	"github.com/birdple/imagine/internal/storage"
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

// UpdateRequest represents the request for updating images
type UpdateRequest struct {
	URL     string `json:"url,omitempty"`     // URL of external image to process and replace
	Bucket  string `json:"bucket,omitempty"`  // Bucket where to store/replace the image
	Key     string `json:"key,omitempty"`     // Storage key for the image
	Quality int    `json:"quality,omitempty"` // Processing quality (1-100)
	Format  string `json:"format,omitempty"`  // Output format
}

// UpdateResponse represents the response for updating images
type UpdateResponse struct {
	Success bool         `json:"success"`
	Updated []UpdateResult `json:"updated,omitempty"`
	Error   *APIError    `json:"error,omitempty"`
}

// UpdateResult represents the result of updating a single image
type UpdateResult struct {
	Key          string  `json:"key"`
	URLSize      int64   `json:"url_size"`      // Size of image from URL
	BucketSize   int64   `json:"bucket_size"`   // Size of existing image in bucket
	NewSize      int64   `json:"new_size"`      // Size of processed image
	SavedBytes   int64   `json:"saved_bytes"`   // Bytes saved vs existing bucket image
	SavedPercent float64 `json:"saved_percent"` // Percentage saved vs existing bucket image
	Format       string  `json:"format"`
	Quality      int     `json:"quality"`
}

// ListRequest represents the request for listing files
type ListRequest struct {
	Bucket string `json:"bucket,omitempty"` // Bucket to list from
	Prefix string `json:"prefix,omitempty"` // Prefix/directory to filter by
}

// ListResponse represents the response for listing files
type ListResponse struct {
	Success     bool             `json:"success"`
	Prefix      string           `json:"prefix,omitempty"`
	Count       int              `json:"count"`
	Files       []ListItem       `json:"files,omitempty"`
	Directories []DirectoryInfo  `json:"directories,omitempty"`
	Error       *APIError        `json:"error,omitempty"`
}

// ListItem represents a single file in a list response
type ListItem struct {
	Key      string    `json:"key"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
}

// DirectoryInfo represents information about a subdirectory
type DirectoryInfo struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	FileCount int    `json:"file_count"`
}

// DeleteRequest represents the request for deleting files or directories
type DeleteRequest struct {
	Bucket string   `json:"bucket,omitempty"` // Bucket to delete from
	Keys   []string `json:"keys,omitempty"`   // List of keys to delete
	Prefix string   `json:"prefix,omitempty"` // Prefix/directory to delete (deletes all files with this prefix)
}

// DeleteResponse represents the response for delete operations
type DeleteResponse struct {
	Success bool        `json:"success"`
	Deleted []string    `json:"deleted,omitempty"` // List of deleted keys
	Count   int         `json:"count"`
	Error   *APIError   `json:"error,omitempty"`
}

// handleUpload handles image upload requests
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
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

	// Normalize directory path
	directory = normalizeDirectoryPath(directory)

	// Check content type
	contentType := r.Header.Get("Content-Type")

	var imageReader io.Reader
	var filename string
	var quality int
	var format string

	var imageData []byte
	var err error

	// Handle direct binary upload (image/*)
	if strings.HasPrefix(contentType, "image/") {
		// Read the entire body into memory
		imageData, err = io.ReadAll(r.Body)
		if err != nil {
			s.sendError(w, http.StatusBadRequest, "READ_ERROR", "Failed to read image data")
			return
		}
		filename = "image" + getExtensionFromContentType(contentType)

		// Get optional parameters from query string
		if q := r.URL.Query().Get("quality"); q != "" {
			if quality, err = strconv.Atoi(q); err != nil {
				s.sendError(w, http.StatusBadRequest, "INVALID_QUALITY", "Invalid quality parameter")
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

		// Read file data
		imageData, err = io.ReadAll(file)
		if err != nil {
			s.sendError(w, http.StatusBadRequest, "READ_ERROR", "Failed to read image data")
			return
		}
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

		// Check for custom ID in form data
		if id := r.FormValue("id"); id != "" {
			if isValidImageID(id) {
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

		// Read downloaded data
		imageData, err = io.ReadAll(resp.Body)
		if err != nil {
			s.sendError(w, http.StatusBadRequest, "READ_ERROR", "Failed to read image data")
			return
		}
		filename = extractFilenameFromURL(uploadReq.URL)
		quality = uploadReq.Quality
		format = uploadReq.Format

		// Check for custom ID in JSON payload
		if uploadReq.ID != "" {
			if isValidImageID(uploadReq.ID) {
				r.URL.RawQuery = fmt.Sprintf("id=%s", uploadReq.ID)
			}
		}

	} else {
		s.sendError(w, http.StatusBadRequest, "UNSUPPORTED_CONTENT_TYPE", "Unsupported content type")
		return
	}

	// Generate image ID from hash of original data, or use provided ID
	var imageID string

	// Check for optional 'id' parameter
	if customID := r.URL.Query().Get("id"); customID != "" {
		// Validate custom ID (alphanumeric, hyphens, underscores only)
		if isValidImageID(customID) {
			imageID = customID
		} else {
			s.sendError(w, http.StatusBadRequest, "INVALID_ID", "Invalid ID format. Use alphanumeric characters, hyphens, and underscores only")
			return
		}
	} else {
		// Use hash-based ID generation
		imageID = hashutil.GenerateImageIDFromData(imageData)
	}

	// Build full storage key with directory and image ID
	storageKey := buildStorageKey(directory, imageID)

	// Get bucket-aware storage instance
	storageBackend := s.getStorageForBucket(bucket)

	// Check if image already exists (using bucket-aware storage)
	exists, err := storageBackend.Exists(ctx, storageKey)
	if err != nil {
		s.logger.WithError(err).Warn("Failed to check image existence")
	}

	if exists {
		// Image already exists, retrieve metadata and return
		s.logger.WithField("image_id", imageID).Info("Image already exists, returning existing")

		_, metadata, err := storageBackend.Retrieve(ctx, storageKey)
		if err != nil {
			s.logger.WithError(err).Error("Failed to retrieve existing image metadata")
			s.sendError(w, http.StatusInternalServerError, "RETRIEVAL_ERROR", "Failed to retrieve image")
			return
		}

		// Build URL with bucket and directory parameters if provided
		imageURL := buildImageURL(imageID, bucket, directory)

		// Return existing image data
		response := UploadResponse{
			Success: true,
			Data: UploadData{
				ID:           imageID,
				URL:          imageURL,
				OriginalName: metadata.OriginalName,
				Format:       metadata.Format,
				Size:         metadata.Size,
				Dimensions: Dimensions{
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
	processedImage, err := s.imageProcessor.Process(ctx, imageReader, &processor.ProcessingParams{
		Quality: quality,
		Format:  format,
	})
	if err != nil {
		s.logger.WithError(err).Error("Failed to process image")
		s.sendError(w, http.StatusUnprocessableEntity, "PROCESSING_FAILED", "Failed to process image")
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
		s.logger.WithError(err).Error("Failed to store image")
		s.sendError(w, http.StatusInternalServerError, "STORAGE_ERROR", "Failed to store image")
		return
	}

	// Build URL with bucket and directory parameters if provided
	imageURL := buildImageURL(imageID, bucket, directory)

	// Send success response
	response := UploadResponse{
		Success: true,
		Data: UploadData{
			ID:           imageID,
			URL:          imageURL,
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

	// Normalize directory path
	directory = normalizeDirectoryPath(directory)

	// Build full storage key
	storageKey := buildStorageKey(directory, imageID)

	// Get bucket-aware storage instance
	storageBackend := s.getStorageForBucket(bucket)

	// Parse query parameters for transformations
	params := &processor.ProcessingParams{}

	// Support both short (w) and long (width) parameter names
	if width := getQueryParam(r, "w", "width"); width != "" {
		if widthVal, err := strconv.Atoi(width); err == nil && widthVal > 0 && widthVal <= s.config.Processing.MaxDimensions.Width {
			params.Width = widthVal
		} else {
			s.sendError(w, http.StatusBadRequest, "INVALID_WIDTH", "Invalid width parameter")
			return
		}
	}

	if height := getQueryParam(r, "h", "height"); height != "" {
		if h, err := strconv.Atoi(height); err == nil && h > 0 && h <= s.config.Processing.MaxDimensions.Height {
			params.Height = h
		} else {
			s.sendError(w, http.StatusBadRequest, "INVALID_HEIGHT", "Invalid height parameter")
			return
		}
	}

	if quality := getQueryParam(r, "q", "quality"); quality != "" {
		if q, err := strconv.Atoi(quality); err == nil && q > 0 && q <= 100 {
			params.Quality = q
		} else {
			s.sendError(w, http.StatusBadRequest, "INVALID_QUALITY", "Invalid quality parameter")
			return
		}
	}

	if format := getQueryParam(r, "f", "format"); format != "" {
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

	// Parse advanced transformation parameters
	if cropX := r.URL.Query().Get("crop_x"); cropX != "" {
		if x, err := strconv.Atoi(cropX); err == nil && x >= 0 {
			params.CropX = x
		}
	}

	if cropY := r.URL.Query().Get("crop_y"); cropY != "" {
		if y, err := strconv.Atoi(cropY); err == nil && y >= 0 {
			params.CropY = y
		}
	}

	if cropW := r.URL.Query().Get("crop_w"); cropW != "" {
		if w, err := strconv.Atoi(cropW); err == nil && w > 0 {
			params.CropW = w
		}
	}

	if cropH := r.URL.Query().Get("crop_h"); cropH != "" {
		if h, err := strconv.Atoi(cropH); err == nil && h > 0 {
			params.CropH = h
		}
	}

	if rotate := r.URL.Query().Get("rotate"); rotate != "" {
		if r, err := strconv.ParseFloat(rotate, 64); err == nil {
			params.Rotate = r
		}
	}

	if flip := r.URL.Query().Get("flip"); flip != "" {
		if flip == "horizontal" || flip == "vertical" {
			params.Flip = flip
		}
	}

	if flop := r.URL.Query().Get("flop"); flop != "" {
		if f, err := strconv.ParseBool(flop); err == nil {
			params.Flop = f
		}
	}

	// Parse filter parameters
	if brightness := r.URL.Query().Get("brightness"); brightness != "" {
		if b, err := strconv.ParseFloat(brightness, 64); err == nil && b >= -100 && b <= 100 {
			params.Brightness = b
		}
	}

	if contrast := r.URL.Query().Get("contrast"); contrast != "" {
		if c, err := strconv.ParseFloat(contrast, 64); err == nil && c >= -100 && c <= 100 {
			params.Contrast = c
		}
	}

	if gamma := r.URL.Query().Get("gamma"); gamma != "" {
		if g, err := strconv.ParseFloat(gamma, 64); err == nil && g >= 0 && g <= 3 {
			params.Gamma = g
		}
	}

	if saturation := r.URL.Query().Get("saturation"); saturation != "" {
		if s, err := strconv.ParseFloat(saturation, 64); err == nil && s >= -100 && s <= 500 {
			params.Saturation = s
		}
	}

	if hue := r.URL.Query().Get("hue"); hue != "" {
		if h, err := strconv.Atoi(hue); err == nil && h >= -180 && h <= 180 {
			params.Hue = h
		}
	}

	if blur := r.URL.Query().Get("blur"); blur != "" {
		if b, err := strconv.ParseFloat(blur, 64); err == nil && b >= 0 && b <= 100 {
			params.Blur = b
		}
	}

	if sharpen := r.URL.Query().Get("sharpen"); sharpen != "" {
		if s, err := strconv.ParseFloat(sharpen, 64); err == nil && s >= 0 && s <= 100 {
			params.Sharpen = s
		}
	}

	// Set default format to WebP if not specified
	if params.Format == "" {
		params.Format = "webp"
	}

	// Retrieve original image using full storage key
	reader, metadata, err := storageBackend.Retrieve(ctx, storageKey)
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

	// If no transformations requested except format, still process for format conversion
	needsProcessing := params.Width != 0 || params.Height != 0 || params.Quality != 0 ||
		params.Format != "" || params.CropW != 0 || params.CropH != 0 ||
		params.Rotate != 0 || params.Flip != "" || params.Flop ||
		params.Brightness != 0 || params.Contrast != 0 || params.Gamma != 0 ||
		params.Saturation != 0 || params.Hue != 0 || params.Blur != 0 || params.Sharpen != 0

	if !needsProcessing {
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
		"uptime":    time.Since(s.startTime).String(),
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
		}
	} else {
		health["storage"] = storageHealth
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(health)
}

// handleUpdate handles image update requests
func (s *Server) handleUpdate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Parse request body
	var req UpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.sendError(w, http.StatusBadRequest, "INVALID_JSON", "Invalid JSON payload")
		return
	}

	// Validate required parameters
	if req.URL == "" {
		s.sendError(w, http.StatusBadRequest, "MISSING_URL", "URL is required")
		return
	}

	if req.Bucket == "" {
		s.sendError(w, http.StatusBadRequest, "MISSING_BUCKET", "Bucket is required")
		return
	}

	if req.Key == "" {
		s.sendError(w, http.StatusBadRequest, "MISSING_KEY", "Key is required")
		return
	}

	if req.Quality <= 0 || req.Quality > 100 {
		s.sendError(w, http.StatusBadRequest, "INVALID_QUALITY", "Quality must be between 1 and 100")
		return
	}

	if req.Format != "" && !s.imageProcessor.ValidateFormat(req.Format) {
		s.sendError(w, http.StatusBadRequest, "INVALID_FORMAT", "Unsupported format")
		return
	}

	// Validate URL
	if _, err := url.Parse(req.URL); err != nil {
		s.sendError(w, http.StatusBadRequest, "INVALID_URL", "Invalid URL format")
		return
	}

	// Download image from URL
	resp, err := http.Get(req.URL)
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

	// Read downloaded data
	imageData, err := io.ReadAll(resp.Body)
	if err != nil {
		s.logger.WithError(err).Error("Failed to read image data")
		s.sendError(w, http.StatusBadRequest, "READ_ERROR", "Failed to read image data")
		return
	}

	urlSize := int64(len(imageData))

	// Get bucket-aware storage instance
	storageBackend := s.getStorageForBucket(req.Bucket)

	// Check if image already exists to calculate savings
	var existingSize int64
	if exists, err := storageBackend.Exists(ctx, req.Key); err == nil && exists {
		if _, metadata, err := storageBackend.Retrieve(ctx, req.Key); err == nil {
			existingSize = metadata.Size
		}
	}

	// Process the image
	imageReader := bytes.NewReader(imageData)
	params := &processor.ProcessingParams{
		Quality: req.Quality,
		Format:  req.Format,
	}

	processedImage, err := s.imageProcessor.Process(ctx, imageReader, params)
	if err != nil {
		s.logger.WithError(err).Error("Failed to process image")
		s.sendError(w, http.StatusUnprocessableEntity, "PROCESSING_FAILED", "Failed to process image")
		return
	}
	defer processedImage.Data.Close()

	// Generate ID from hash of processed data for consistency
	imageID := hashutil.GenerateImageIDFromData(imageData)

	// Store the processed image
	err = storageBackend.Store(ctx, req.Key, processedImage.Data, &storage.ImageMetadata{
		ID:           imageID,
		OriginalName: extractFilenameFromURL(req.URL),
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

	// Calculate savings
	newSize := processedImage.Metadata.Size
	savedBytes := existingSize - newSize
	savedPercent := float64(0)
	if existingSize > 0 {
		savedPercent = float64(savedBytes) / float64(existingSize) * 100
	}

	// Send response
	response := UpdateResponse{
		Success: true,
		Updated: []UpdateResult{
			{
				Key:          req.Key,
				URLSize:      urlSize,
				BucketSize:   existingSize,
				NewSize:      newSize,
				SavedBytes:   savedBytes,
				SavedPercent: savedPercent,
				Format:       processedImage.Metadata.Format,
				Quality:      req.Quality,
			},
		},
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(response)
}

// Helper functions

// isValidImageID validates that an image ID contains only safe characters
func isValidImageID(id string) bool {
	if id == "" || len(id) > 100 {
		return false
	}

	for _, c := range id {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
			return false
		}
	}

	return true
}

// getQueryParam returns the first non-empty value from multiple parameter names
func getQueryParam(r *http.Request, names ...string) string {
	for _, name := range names {
		if value := r.URL.Query().Get(name); value != "" {
			return value
		}
	}
	return ""
}

// getExtensionFromContentType returns file extension based on content type
func getExtensionFromContentType(contentType string) string {
	switch {
	case strings.Contains(contentType, "image/png"):
		return ".png"
	case strings.Contains(contentType, "image/jpeg"), strings.Contains(contentType, "image/jpg"):
		return ".jpg"
	case strings.Contains(contentType, "image/webp"):
		return ".webp"
	case strings.Contains(contentType, "image/gif"):
		return ".gif"
	case strings.Contains(contentType, "image/svg+xml"), strings.Contains(contentType, "image/svg"):
		return ".svg"
	default:
		return ".bin"
	}
}

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

// generateImageID has been replaced by hashutil.GenerateImageIDFromData
// to enable content-based deduplication

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

// normalizeDirectoryPath normalizes and validates the directory path
// Removes leading/trailing slashes and validates characters
func normalizeDirectoryPath(path string) string {
	if path == "" {
		return ""
	}

	// Trim leading and trailing slashes
	path = strings.Trim(path, "/")

	// Remove any double slashes
	for strings.Contains(path, "//") {
		path = strings.ReplaceAll(path, "//", "/")
	}

	return path
}

// buildStorageKey combines directory path and image ID to create full storage key
func buildStorageKey(directory, imageID string) string {
	if directory == "" {
		return imageID
	}
	return directory + "/" + imageID
}

// buildImageURL builds the image URL with optional bucket and directory parameters
func buildImageURL(imageID, bucket, directory string) string {
	baseURL := fmt.Sprintf("/api/v1/images/%s", imageID)
	params := []string{}

	if bucket != "" {
		params = append(params, fmt.Sprintf("b=%s", bucket))
	}
	if directory != "" {
		params = append(params, fmt.Sprintf("d=%s", directory))
	}

	if len(params) > 0 {
		return fmt.Sprintf("%s?%s", baseURL, strings.Join(params, "&"))
	}
	return baseURL
}

// getStorageForBucket returns a storage backend instance for the specified bucket
// If bucket is empty or storage doesn't support BucketAware, returns default storage
func (s *Server) getStorageForBucket(bucket string) storage.StorageBackend {
	if bucket == "" {
		return s.storage
	}

	// Check if storage supports dynamic bucket selection
	if bucketAware, ok := s.storage.(storage.BucketAware); ok {
		return bucketAware.WithBucket(bucket)
	}

	// If storage doesn't support BucketAware, return default storage
	return s.storage
}

// handleList handles listing files in a bucket/directory
func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Get bucket and prefix from query parameters
	bucket := r.URL.Query().Get("b")
	if bucket == "" {
		bucket = r.URL.Query().Get("bucket")
	}

	prefix := r.URL.Query().Get("p")
	if prefix == "" {
		prefix = r.URL.Query().Get("prefix")
	}
	if prefix == "" {
		prefix = r.URL.Query().Get("d")
	}
	if prefix == "" {
		prefix = r.URL.Query().Get("dir")
	}
	if prefix == "" {
		prefix = r.URL.Query().Get("directory")
	}

	// Normalize prefix path
	prefix = normalizeDirectoryPath(prefix)

	// Get bucket-aware storage instance
	storageBackend := s.getStorageForBucket(bucket)

	// List files
	results, err := storageBackend.List(ctx, prefix)
	if err != nil {
		s.logger.WithError(err).Error("Failed to list files")
		s.sendError(w, http.StatusInternalServerError, "LIST_ERROR", "Failed to list files")
		return
	}

	// Separate files and directories
	var files []ListItem
	directoryMap := make(map[string]*DirectoryInfo)

	// Build the prefix path for comparison
	prefixPath := prefix
	if prefixPath != "" && prefixPath[len(prefixPath)-1] != '/' {
		prefixPath = prefixPath + "/"
	}

	for _, result := range results {
		// Remove prefix from key
		key := result.Key
		if prefixPath != "" && strings.HasPrefix(key, prefixPath) {
			key = strings.TrimPrefix(key, prefixPath)
		}

		// Check if this is a direct file or in a subdirectory
		if strings.Contains(key, "/") {
			// File is in a subdirectory
			parts := strings.SplitN(key, "/", 2)
			dirName := parts[0]

			if _, exists := directoryMap[dirName]; !exists {
				fullPath := prefixPath + dirName
				if prefixPath == "" {
					fullPath = dirName
				}
				directoryMap[dirName] = &DirectoryInfo{
					Name:      dirName,
					Path:      strings.TrimSuffix(fullPath, "/"),
					FileCount: 0,
				}
			}
			directoryMap[dirName].FileCount++
		} else {
			// Direct file in this directory
			files = append(files, ListItem{
				Key:      key,
				Size:     result.Size,
				Modified: result.Modified,
			})
		}
	}

	// Convert directory map to slice
	var directories []DirectoryInfo
	for _, dir := range directoryMap {
		directories = append(directories, *dir)
	}

	// Sort directories by name for consistent output
	sort.Slice(directories, func(i, j int) bool {
		return directories[i].Name < directories[j].Name
	})

	// Sort files by key for consistent output
	sort.Slice(files, func(i, j int) bool {
		return files[i].Key < files[j].Key
	})

	response := ListResponse{
		Success:     true,
		Prefix:      prefix,
		Count:       len(results),
		Files:       files,
		Directories: directories,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(response)
}

// handleDelete handles deleting files or entire directories
func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Parse request body
	var req DeleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.sendError(w, http.StatusBadRequest, "INVALID_JSON", "Invalid JSON payload")
		return
	}

	// Validate that either keys or prefix is provided
	if len(req.Keys) == 0 && req.Prefix == "" {
		s.sendError(w, http.StatusBadRequest, "MISSING_PARAMETERS", "Either 'keys' or 'prefix' must be provided")
		return
	}

	// Get bucket-aware storage instance
	storageBackend := s.getStorageForBucket(req.Bucket)

	var deletedKeys []string

	// If prefix is provided, list all files with that prefix and delete them
	if req.Prefix != "" {
		// Normalize prefix path
		prefix := normalizeDirectoryPath(req.Prefix)

		// List all files with this prefix
		results, err := storageBackend.List(ctx, prefix)
		if err != nil {
			s.logger.WithError(err).Error("Failed to list files for deletion")
			s.sendError(w, http.StatusInternalServerError, "LIST_ERROR", "Failed to list files for deletion")
			return
		}

		// Delete each file
		for _, result := range results {
			if err := storageBackend.Delete(ctx, result.Key); err != nil {
				s.logger.WithError(err).WithField("key", result.Key).Warn("Failed to delete file")
				continue
			}
			deletedKeys = append(deletedKeys, result.Key)
		}
	}

	// Delete specific keys if provided
	if len(req.Keys) > 0 {
		for _, key := range req.Keys {
			if err := storageBackend.Delete(ctx, key); err != nil {
				if storage.IsNotFound(err) {
					s.logger.WithField("key", key).Warn("File not found for deletion")
					continue
				}
				s.logger.WithError(err).WithField("key", key).Warn("Failed to delete file")
				continue
			}
			deletedKeys = append(deletedKeys, key)
		}
	}

	response := DeleteResponse{
		Success: true,
		Deleted: deletedKeys,
		Count:   len(deletedKeys),
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(response)
}
