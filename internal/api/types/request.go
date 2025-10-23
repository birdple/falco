package types

// UpdateRequest represents the request for updating images
type UpdateRequest struct {
	URL     string `json:"url,omitempty"`     // URL of external image to process and replace
	Bucket  string `json:"bucket,omitempty"`  // Bucket where to store/replace the image
	Key     string `json:"key,omitempty"`     // Storage key for the image
	Quality int    `json:"quality,omitempty"` // Processing quality (1-100)
	Format  string `json:"format,omitempty"`  // Output format
}

// ListRequest represents the request for listing files
type ListRequest struct {
	Bucket string `json:"bucket,omitempty"` // Bucket to list from
	Prefix string `json:"prefix,omitempty"` // Prefix/directory to filter by
}

// DeleteRequest represents the request for deleting files or directories
type DeleteRequest struct {
	Bucket string   `json:"bucket,omitempty"` // Bucket to delete from
	Keys   []string `json:"keys,omitempty"`   // List of keys to delete
	Prefix string   `json:"prefix,omitempty"` // Prefix/directory to delete (deletes all files with this prefix)
}
