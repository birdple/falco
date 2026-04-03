package utils

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGetQueryParam(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/?format=webp&q=85", nil)

	assert.Equal(t, "webp", GetQueryParam(req, "format"))
	assert.Equal(t, "85", GetQueryParam(req, "q"))
	assert.Equal(t, "", GetQueryParam(req, "missing"))

	// Multiple names - returns first match
	assert.Equal(t, "webp", GetQueryParam(req, "fmt", "format"))
}

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
		{"text/html", ".bin"},
		{"", ".bin"},
	}

	for _, tt := range tests {
		t.Run(tt.contentType, func(t *testing.T) {
			assert.Equal(t, tt.expected, GetExtensionFromContentType(tt.contentType))
		})
	}
}

func TestExtractFilenameFromURL(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		expected string
	}{
		{"simple", "https://example.com/photo.jpg", "photo.jpg"},
		{"nested path", "https://example.com/images/2024/photo.jpg", "photo.jpg"},
		{"no extension", "https://example.com/images/photo", "photo"},
		{"invalid url", "://invalid", "image"},
		{"root path", "https://example.com/", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, ExtractFilenameFromURL(tt.url))
		})
	}
}

func TestNormalizeDirectoryPath(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"empty", "", ""},
		{"simple", "photos", "photos"},
		{"leading slash", "/photos", "photos"},
		{"trailing slash", "photos/", "photos"},
		{"both slashes", "/photos/", "photos"},
		{"nested", "photos/2024/january", "photos/2024/january"},
		{"double slashes", "photos//2024", "photos/2024"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, NormalizeDirectoryPath(tt.input))
		})
	}
}

func TestValidateDirectoryPath(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		wantErr bool
		errMsg  string
	}{
		{"empty is valid", "", false, ""},
		{"simple path", "photos", false, ""},
		{"nested path", "photos/2024/jan", false, ""},
		{"path traversal", "../etc/passwd", true, "path traversal"},
		{"hidden traversal", "photos/../../etc", true, "path traversal"},
		{"absolute path", "/etc/passwd", true, "absolute paths"},
		{"windows absolute", "C:\\Windows", true, "drive letters"},
		{"null byte", "photos\x00evil", true, "null bytes"},
		{"special chars", "photos/[evil]", true, "invalid character"},
		{"with hyphens", "my-photos", false, ""},
		{"with underscores", "my_photos", false, ""},
		{"with dots", "photos.backup", false, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateDirectoryPath(tt.path)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestBuildStorageKey(t *testing.T) {
	assert.Equal(t, "abc123", BuildStorageKey("", "abc123"))
	assert.Equal(t, "photos/abc123", BuildStorageKey("photos", "abc123"))
	assert.Equal(t, "photos/2024/abc123", BuildStorageKey("photos/2024", "abc123"))
}

func BenchmarkValidateDirectoryPath_Valid(b *testing.B) {
	b.ResetTimer()
	for range b.N {
		ValidateDirectoryPath("photos/2024/january")
	}
}

func BenchmarkValidateDirectoryPath_Invalid(b *testing.B) {
	b.ResetTimer()
	for range b.N {
		ValidateDirectoryPath("../etc/passwd")
	}
}

func BenchmarkNormalizeDirectoryPath(b *testing.B) {
	b.ResetTimer()
	for range b.N {
		NormalizeDirectoryPath("/photos//2024/january/")
	}
}

func BenchmarkBuildStorageKey(b *testing.B) {
	b.ResetTimer()
	for range b.N {
		BuildStorageKey("photos/2024", "abc123def456")
	}
}

func BenchmarkBuildImageURL(b *testing.B) {
	b.ResetTimer()
	for range b.N {
		BuildImageURL("abc123def456", "mybucket", "photos/2024")
	}
}

func BenchmarkGetExtensionFromContentType(b *testing.B) {
	b.ResetTimer()
	for range b.N {
		GetExtensionFromContentType("image/webp")
	}
}

func TestBuildImageURL(t *testing.T) {
	tests := []struct {
		name      string
		imageID   string
		bucket    string
		directory string
		expected  string
	}{
		{"no params", "abc123", "", "", "/api/v1/images/abc123"},
		{"with bucket", "abc123", "mybucket", "", "/api/v1/images/abc123?b=mybucket"},
		{"with directory", "abc123", "", "photos", "/api/v1/images/abc123?d=photos"},
		{"both params", "abc123", "mybucket", "photos", "/api/v1/images/abc123?b=mybucket&d=photos"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, BuildImageURL(tt.imageID, tt.bucket, tt.directory))
		})
	}
}
