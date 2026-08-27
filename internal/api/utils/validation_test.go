package utils

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsValidImageID(t *testing.T) {
	tests := []struct {
		name     string
		id       string
		expected bool
	}{
		{"valid alphanumeric", "abc123", true},
		{"valid with hyphens", "my-image-id", true},
		{"valid with underscores", "my_image_id", true},
		{"valid mixed", "My-Image_123", true},
		{"empty string", "", false},
		{"too long", string(make([]byte, 101)), false},
		{"contains dots", "image.png", false},
		{"contains slashes", "path/to/image", false},
		{"contains spaces", "my image", false},
		{"contains special chars", "image@123", false},
		{"single char", "a", true},
		{"max length", string(make([]byte, 100)), false}, // all zeros which are 0x00
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsValidImageID(tt.id)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func BenchmarkIsValidImageID_Valid(b *testing.B) {
	b.ResetTimer()
	for range b.N {
		IsValidImageID("b94d27b9934d3e08")
	}
}

func BenchmarkIsValidImageID_Invalid(b *testing.B) {
	b.ResetTimer()
	for range b.N {
		IsValidImageID("../etc/passwd")
	}
}

func TestIsValidImageID_MaxLength(t *testing.T) {
	// Create a valid 100-char ID
	id := ""
	var idSb54 strings.Builder
	for range 100 {
		idSb54.WriteString("a")
	}
	id += idSb54.String()
	assert.True(t, IsValidImageID(id))

	// 101 chars should fail
	id += "a"
	assert.False(t, IsValidImageID(id))
}
