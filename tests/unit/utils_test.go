package unit

import (
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/birdple/falco/internal/api/utils"
)

// Tests for GetQueryParam
func TestGetQueryParam(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		params   []string
		expected string
	}{
		{
			name:     "First parameter exists",
			url:      "/test?w=100",
			params:   []string{"w", "width"},
			expected: "100",
		},
		{
			name:     "Second parameter exists",
			url:      "/test?width=200",
			params:   []string{"w", "width"},
			expected: "200",
		},
		{
			name:     "No parameters exist",
			url:      "/test",
			params:   []string{"w", "width"},
			expected: "",
		},
		{
			name:     "Empty value",
			url:      "/test?w=",
			params:   []string{"w", "width"},
			expected: "",
		},
		{
			name:     "Multiple values - returns first",
			url:      "/test?w=100&width=200",
			params:   []string{"w", "width"},
			expected: "100",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tt.url, nil)
			result := utils.GetQueryParam(req, tt.params...)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// Tests for GetExtensionFromContentType
func TestGetExtensionFromContentType(t *testing.T) {
	tests := []struct {
		contentType string
		expected    string
	}{
		{"image/png", ".png"},
		{"image/jpeg", ".jpg"},
		{"image/jpg", ".jpg"},
		{"image/webp", ".webp"},
		{"image/gif", ".gif"},
		{"image/svg+xml", ".svg"},
		{"image/svg", ".svg"},
		{"application/octet-stream", ".bin"},
		{"text/plain", ".bin"},
		{"", ".bin"},
	}

	for _, tt := range tests {
		t.Run(tt.contentType, func(t *testing.T) {
			result := utils.GetExtensionFromContentType(tt.contentType)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// Tests for ExtractFilenameFromURL
func TestExtractFilenameFromURL(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		expected string
	}{
		{
			name:     "Simple URL",
			url:      "https://example.com/image.jpg",
			expected: "image.jpg",
		},
		{
			name:     "URL with path",
			url:      "https://example.com/images/photos/sunset.png",
			expected: "sunset.png",
		},
		{
			name:     "URL with query params",
			url:      "https://example.com/image.jpg?w=100",
			expected: "image.jpg",
		},
		{
			name:     "URL with fragment",
			url:      "https://example.com/image.jpg#top",
			expected: "image.jpg",
		},
		{
			name:     "Invalid URL",
			url:      "://invalid",
			expected: "image",
		},
		{
			name:     "Root path",
			url:      "https://example.com/",
			expected: "",
		},
		{
			name:     "No filename",
			url:      "https://example.com",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := utils.ExtractFilenameFromURL(tt.url)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// Tests for NormalizeDirectoryPath
func TestNormalizeDirectoryPath(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Empty path",
			input:    "",
			expected: "",
		},
		{
			name:     "Leading slash",
			input:    "/images",
			expected: "images",
		},
		{
			name:     "Trailing slash",
			input:    "images/",
			expected: "images",
		},
		{
			name:     "Both slashes",
			input:    "/images/",
			expected: "images",
		},
		{
			name:     "Multiple slashes",
			input:    "images//photos//vacation",
			expected: "images/photos/vacation",
		},
		{
			name:     "Mixed slashes",
			input:    "//images///photos//",
			expected: "images/photos",
		},
		{
			name:     "Normal path",
			input:    "images/photos",
			expected: "images/photos",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := utils.NormalizeDirectoryPath(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// Tests for BuildStorageKey
func TestBuildStorageKey(t *testing.T) {
	tests := []struct {
		name      string
		directory string
		imageID   string
		expected  string
	}{
		{
			name:      "With directory",
			directory: "images/photos",
			imageID:   "abc123",
			expected:  "images/photos/abc123",
		},
		{
			name:      "Without directory",
			directory: "",
			imageID:   "abc123",
			expected:  "abc123",
		},
		{
			name:      "Empty directory",
			directory: "",
			imageID:   "test-image",
			expected:  "test-image",
		},
		{
			name:      "Complex path",
			directory: "users/john/uploads",
			imageID:   "profile-pic",
			expected:  "users/john/uploads/profile-pic",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := utils.BuildStorageKey(tt.directory, tt.imageID)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// Tests for BuildImageURL
func TestBuildImageURL(t *testing.T) {
	tests := []struct {
		name      string
		imageID   string
		bucket    string
		directory string
		expected  string
	}{
		{
			name:      "Only image ID",
			imageID:   "abc123",
			bucket:    "",
			directory: "",
			expected:  "/api/v1/images/abc123",
		},
		{
			name:      "With bucket",
			imageID:   "abc123",
			bucket:    "my-bucket",
			directory: "",
			expected:  "/api/v1/images/abc123?b=my-bucket",
		},
		{
			name:      "With directory",
			imageID:   "abc123",
			bucket:    "",
			directory: "photos",
			expected:  "/api/v1/images/abc123?d=photos",
		},
		{
			name:      "With both bucket and directory",
			imageID:   "abc123",
			bucket:    "my-bucket",
			directory: "photos",
			expected:  "/api/v1/images/abc123?b=my-bucket&d=photos",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := utils.BuildImageURL(tt.imageID, tt.bucket, tt.directory)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// Tests for IsValidImageID
func TestIsValidImageID(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{
			name:     "Valid alphanumeric",
			input:    "abc123",
			expected: true,
		},
		{
			name:     "Valid with hyphens",
			input:    "image-123-test",
			expected: true,
		},
		{
			name:     "Valid with underscores",
			input:    "image_123_test",
			expected: true,
		},
		{
			name:     "Valid mixed case",
			input:    "AbC123-DeF_456",
			expected: true,
		},
		{
			name:     "Empty string",
			input:    "",
			expected: false,
		},
		{
			name:     "Too long",
			input:    "a" + string(make([]byte, 100)),
			expected: false,
		},
		{
			name:     "With slash",
			input:    "image/123",
			expected: false,
		},
		{
			name:     "With dot",
			input:    "image.jpg",
			expected: false,
		},
		{
			name:     "With space",
			input:    "image 123",
			expected: false,
		},
		{
			name:     "With special chars",
			input:    "image@123",
			expected: false,
		},
		{
			name:     "With unicode",
			input:    "image-😀",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := utils.IsValidImageID(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}
