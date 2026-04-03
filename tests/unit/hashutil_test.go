package unit

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/birdple/falco/internal/pkg/hashutil"
)

func TestGenerateSHA256(t *testing.T) {
	data := []byte("test data")
	hash := hashutil.GenerateSHA256(data)

	assert.NotEmpty(t, hash)
	assert.Len(t, hash, 64) // SHA256 produces 64 hex characters

	// Same data should produce same hash
	hash2 := hashutil.GenerateSHA256(data)
	assert.Equal(t, hash, hash2)

	// Different data should produce different hash
	hash3 := hashutil.GenerateSHA256([]byte("different data"))
	assert.NotEqual(t, hash, hash3)
}

func TestGenerateSHA256FromReader(t *testing.T) {
	data := []byte("test data")
	reader := bytes.NewReader(data)

	hash, err := hashutil.GenerateSHA256FromReader(reader)
	assert.NoError(t, err)
	assert.NotEmpty(t, hash)
	assert.Len(t, hash, 64)

	// Should produce same hash as GenerateSHA256
	directHash := hashutil.GenerateSHA256(data)
	assert.Equal(t, directHash, hash)
}

func TestGenerateSHA256FromReader_Error(t *testing.T) {
	// Create a reader that returns an error
	reader := &errorReader{}

	hash, err := hashutil.GenerateSHA256FromReader(reader)
	assert.Error(t, err)
	assert.Empty(t, hash)
	assert.Contains(t, err.Error(), "failed to hash data")
}

func TestGenerateImageID(t *testing.T) {
	hash := strings.Repeat("a", 64) // 64 character hash
	imageID := hashutil.GenerateImageID(hash)

	assert.Len(t, imageID, 16)
	assert.Equal(t, strings.Repeat("a", 16), imageID)
}

func TestGenerateImageIDFromData(t *testing.T) {
	data := []byte("test data")
	imageID := hashutil.GenerateImageIDFromData(data)

	assert.NotEmpty(t, imageID)
	assert.Len(t, imageID, 16)

	// Same data should produce same ID
	imageID2 := hashutil.GenerateImageIDFromData(data)
	assert.Equal(t, imageID, imageID2)

	// Different data should produce different ID
	imageID3 := hashutil.GenerateImageIDFromData([]byte("different"))
	assert.NotEqual(t, imageID, imageID3)
}

func TestGenerateImageIDFromData_Consistency(t *testing.T) {
	data := []byte("consistent test data")

	// Generate multiple times
	id1 := hashutil.GenerateImageIDFromData(data)
	id2 := hashutil.GenerateImageIDFromData(data)
	id3 := hashutil.GenerateImageIDFromData(data)

	// All should be identical
	assert.Equal(t, id1, id2)
	assert.Equal(t, id2, id3)
}

// Helper type for testing reader errors
type errorReader struct{}

func (e *errorReader) Read(p []byte) (n int, err error) {
	return 0, assert.AnError
}
