package types

import "time"

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

// UpdateResponse represents the response for updating images
type UpdateResponse struct {
	Success bool           `json:"success"`
	Updated []UpdateResult `json:"updated,omitempty"`
	Error   *APIError      `json:"error,omitempty"`
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

// ListResponse represents the response for listing files
type ListResponse struct {
	Success     bool            `json:"success"`
	Prefix      string          `json:"prefix,omitempty"`
	Count       int             `json:"count"`
	Files       []ListItem      `json:"files,omitempty"`
	Directories []DirectoryInfo `json:"directories,omitempty"`
	Error       *APIError       `json:"error,omitempty"`
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

// DeleteResponse represents the response for delete operations
type DeleteResponse struct {
	Success   bool      `json:"success"`
	Deleted   []string  `json:"deleted,omitempty"` // List of deleted keys
	Count     int       `json:"count"`
	Truncated bool      `json:"truncated,omitempty"` // True when more items remain under the prefix (caller should repeat)
	Error     *APIError `json:"error,omitempty"`
}
