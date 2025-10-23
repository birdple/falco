package utils

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// GetQueryParam returns the first non-empty value from multiple parameter names
func GetQueryParam(r *http.Request, names ...string) string {
	for _, name := range names {
		if value := r.URL.Query().Get(name); value != "" {
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
// Removes leading/trailing slashes and validates characters
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

// BuildStorageKey combines directory path and image ID to create full storage key
func BuildStorageKey(directory, imageID string) string {
	if directory == "" {
		return imageID
	}
	return directory + "/" + imageID
}

// BuildImageURL builds the image URL with optional bucket and directory parameters
func BuildImageURL(imageID, bucket, directory string) string {
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
