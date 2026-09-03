// Package utils holds the request-parsing helpers shared by falco's handlers:
// query parameters with aliases, directory path validation and storage key
// construction.
//
// The path validation is security-relevant, not cosmetic: it is what stops a
// directory parameter from escaping its bucket.
package utils

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
)

// GetQueryParam returns the first non-empty value from multiple parameter names.
//
// Parses the query ONCE: the previous version called r.URL.Query() inside the
// loop, so GetQueryParam(r, "p", "prefix", "d", "dir", "directory") reparsed
// RawQuery up to five times per call.
func GetQueryParam(r *http.Request, names ...string) string {
	return QueryParam(r.URL.Query(), names...)
}

// QueryParam is GetQueryParam over an already-parsed url.Values, for handlers
// that hoisted r.URL.Query() into a local.
func QueryParam(query url.Values, names ...string) string {
	for _, name := range names {
		if value := query.Get(name); value != "" {
			return value
		}
	}
	return ""
}

// GetExtensionFromContentType returns file extension based on content type
func GetExtensionFromContentType(contentType string) string {
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

// IsImageContentType returns true if the content type represents an image
// that can be processed by the image processor.
func IsImageContentType(contentType string) bool {
	ct := strings.ToLower(contentType)
	return strings.HasPrefix(ct, "image/") &&
		!strings.Contains(ct, "svg") // SVG is not raster, skip processing
}

// IsDangerousContentType returns true for content types that can execute
// code when rendered in a browser (SVG, HTML, XML, XHTML). These must
// never be stored and served back with their original Content-Type.
func IsDangerousContentType(contentType string) bool {
	ct := strings.ToLower(contentType)
	switch {
	case strings.Contains(ct, "svg"):
		return true
	case strings.Contains(ct, "html"):
		return true
	case ct == "text/xml" || ct == "application/xml" || strings.HasSuffix(ct, "+xml"):
		return true
	case strings.Contains(ct, "javascript"):
		return true
	default:
		return false
	}
}

// DetectContentType uses http.DetectContentType on the first 512 bytes
// and returns the detected MIME type.
func DetectContentType(data []byte) string {
	return http.DetectContentType(data)
}

// ExtractFilenameFromURL extracts filename from URL path
func ExtractFilenameFromURL(rawURL string) string {
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

// NormalizeDirectoryPath normalizes and validates the directory path
// Removes leading/trailing slashes and validates against path traversal
func NormalizeDirectoryPath(path string) string {
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

// ValidateDirectoryPath validates a directory path against path traversal attacks
// Returns an error if the path is invalid or contains malicious patterns
func ValidateDirectoryPath(path string) error {
	if path == "" {
		return nil // Empty path is valid
	}

	// Check for path traversal patterns
	if strings.Contains(path, "..") {
		return errors.New("path traversal detected: .. not allowed")
	}

	// Check for absolute paths
	if strings.HasPrefix(path, "/") || strings.HasPrefix(path, "\\") {
		return errors.New("absolute paths not allowed")
	}

	// Check for Windows drive letters
	if len(path) >= 2 && path[1] == ':' {
		return errors.New("drive letters not allowed")
	}

	// Check for null bytes
	if strings.Contains(path, "\x00") {
		return errors.New("null bytes not allowed")
	}

	// Clean the path using filepath.Clean and verify it hasn't changed significantly
	cleaned := filepath.Clean(path)
	if strings.Contains(cleaned, "..") {
		return errors.New("path traversal detected after cleaning")
	}

	// Ensure path doesn't try to escape using special characters
	forbidden := []string{"\x00", "\r", "\n", "\t"}
	for _, char := range forbidden {
		if strings.Contains(path, char) {
			return fmt.Errorf("forbidden character detected: %q", char)
		}
	}

	// Validate characters (allow alphanumeric, hyphens, underscores, forward slashes)
	for _, char := range path {
		switch {
		case char >= 'a' && char <= 'z',
			char >= 'A' && char <= 'Z',
			char >= '0' && char <= '9',
			char == '-', char == '_', char == '/', char == '.':
		default:
			return fmt.Errorf("invalid character in path: %q", char)
		}
	}

	return nil
}

// BuildStorageKey combines directory path and image ID to create full storage key
func BuildStorageKey(directory, imageID string) string {
	if directory == "" {
		return imageID
	}
	return directory + "/" + imageID
}

// BuildImageURL builds the image URL with optional bucket and directory parameters
func BuildImageURL(imageID, bucket, directory string) string {
	baseURL := "/api/v1/images/" + imageID
	params := []string{}

	if bucket != "" {
		params = append(params, "b="+url.QueryEscape(bucket))
	}
	if directory != "" {
		params = append(params, "d="+url.QueryEscape(directory))
	}

	if len(params) > 0 {
		return fmt.Sprintf("%s?%s", baseURL, strings.Join(params, "&"))
	}
	return baseURL
}
