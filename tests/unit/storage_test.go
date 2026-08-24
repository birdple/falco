package unit

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/birdple/falco/internal/storage"
)

// Test error helper functions
func TestIsNotFound(t *testing.T) {
	assert.True(t, storage.IsNotFound(storage.ErrImageNotFound))
	assert.False(t, storage.IsNotFound(storage.ErrImageAlreadyExists))
	assert.False(t, storage.IsNotFound(errors.New("other error")))
}

func TestIsAlreadyExists(t *testing.T) {
	assert.True(t, storage.IsAlreadyExists(storage.ErrImageAlreadyExists))
	assert.False(t, storage.IsAlreadyExists(storage.ErrImageNotFound))
	assert.False(t, storage.IsAlreadyExists(errors.New("other error")))
}

func TestIsUnavailable(t *testing.T) {
	assert.True(t, storage.IsUnavailable(storage.ErrStorageUnavailable))
	assert.False(t, storage.IsUnavailable(storage.ErrImageNotFound))
	assert.False(t, storage.IsUnavailable(errors.New("other error")))
}

func TestIsNetworkError(t *testing.T) {
	assert.True(t, storage.IsNetworkError(storage.ErrNetworkError))
	assert.False(t, storage.IsNetworkError(storage.ErrImageNotFound))
	assert.False(t, storage.IsNetworkError(errors.New("other error")))
}

func TestIsTimeout(t *testing.T) {
	assert.True(t, storage.IsTimeout(storage.ErrTimeout))
	assert.False(t, storage.IsTimeout(storage.ErrImageNotFound))
	assert.False(t, storage.IsTimeout(errors.New("other error")))
}

// Test metadata encoder
func TestMetadataEncoder_Encode(t *testing.T) {
	encoder := storage.NewMetadataEncoder()

	metadata := &storage.ImageMetadata{
		ID:           "test-id",
		OriginalName: "test.jpg",
		Format:       "jpeg",
		Size:         1024,
		Width:        800,
		Height:       600,
		ContentType:  "image/jpeg",
		CreatedAt:    time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC),
		ETag:         "abc123",
	}

	encoded, err := encoder.Encode(metadata)
	assert.NoError(t, err)
	assert.NotNil(t, encoded)

	assert.Equal(t, "test.jpg", encoded["original-name"])
	assert.Equal(t, "jpeg", encoded["format"])
	assert.Equal(t, "800", encoded["width"])
	assert.Equal(t, "600", encoded["height"])
	assert.Contains(t, encoded["created-at"], "2024-01-01")
}

func TestMetadataEncoder_EncodeNil(t *testing.T) {
	encoder := storage.NewMetadataEncoder()

	encoded, err := encoder.Encode(nil)
	assert.Error(t, err)
	assert.Nil(t, encoded)
	assert.Contains(t, err.Error(), "metadata cannot be nil")
}

func TestMetadataEncoder_Decode(t *testing.T) {
	encoder := storage.NewMetadataEncoder()

	data := map[string]string{
		"original-name": "test.jpg",
		"format":        "jpeg",
		"width":         "800",
		"height":        "600",
		"created-at":    "2024-01-01T12:00:00Z",
	}

	decoded, err := encoder.Decode(data)
	assert.NoError(t, err)
	assert.NotNil(t, decoded)

	assert.Equal(t, "test.jpg", decoded.OriginalName)
	assert.Equal(t, "jpeg", decoded.Format)
	assert.Equal(t, 800, decoded.Width)
	assert.Equal(t, 600, decoded.Height)
	assert.Equal(t, 2024, decoded.CreatedAt.Year())
}

func TestMetadataEncoder_DecodeNil(t *testing.T) {
	encoder := storage.NewMetadataEncoder()

	decoded, err := encoder.Decode(nil)
	assert.NoError(t, err)
	assert.NotNil(t, decoded)
}

func TestMetadataEncoder_DecodePartial(t *testing.T) {
	encoder := storage.NewMetadataEncoder()

	data := map[string]string{
		"original-name": "test.jpg",
		// Missing other fields
	}

	decoded, err := encoder.Decode(data)
	assert.NoError(t, err)
	assert.NotNil(t, decoded)
	assert.Equal(t, "test.jpg", decoded.OriginalName)
	assert.Equal(t, "", decoded.Format)
	assert.Equal(t, 0, decoded.Width)
}

// Test JSON marshaling
func TestImageMetadata_MarshalJSON(t *testing.T) {
	metadata := &storage.ImageMetadata{
		ID:           "test-id",
		OriginalName: "test.jpg",
		Format:       "jpeg",
		Size:         1024,
		Width:        800,
		Height:       600,
		ContentType:  "image/jpeg",
		CreatedAt:    time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC),
		ETag:         "abc123",
	}

	data, err := json.Marshal(metadata)
	assert.NoError(t, err)
	assert.NotNil(t, data)

	// Verify JSON structure
	var result map[string]any
	err = json.Unmarshal(data, &result)
	assert.NoError(t, err)

	assert.Equal(t, "test-id", result["id"])
	assert.Equal(t, "test.jpg", result["original_name"])
	assert.Equal(t, "jpeg", result["format"])
	assert.Equal(t, float64(1024), result["size"])
	assert.Equal(t, float64(800), result["width"])
	assert.Equal(t, float64(600), result["height"])
	assert.Equal(t, "image/jpeg", result["content_type"])
	assert.Equal(t, "abc123", result["etag"])
	assert.Contains(t, result["created_at"], "2024-01-01")
}

func TestImageMetadata_UnmarshalJSON(t *testing.T) {
	jsonData := `{
		"id": "test-id",
		"original_name": "test.jpg",
		"format": "jpeg",
		"size": 1024,
		"width": 800,
		"height": 600,
		"content_type": "image/jpeg",
		"created_at": "2024-01-01T12:00:00Z",
		"etag": "abc123"
	}`

	var metadata storage.ImageMetadata
	err := json.Unmarshal([]byte(jsonData), &metadata)
	assert.NoError(t, err)

	assert.Equal(t, "test-id", metadata.ID)
	assert.Equal(t, "test.jpg", metadata.OriginalName)
	assert.Equal(t, "jpeg", metadata.Format)
	assert.Equal(t, int64(1024), metadata.Size)
	assert.Equal(t, 800, metadata.Width)
	assert.Equal(t, 600, metadata.Height)
	assert.Equal(t, "image/jpeg", metadata.ContentType)
	assert.Equal(t, 2024, metadata.CreatedAt.Year())
	assert.Equal(t, "abc123", metadata.ETag)
}

func TestImageMetadata_UnmarshalJSON_InvalidDate(t *testing.T) {
	jsonData := `{
		"id": "test-id",
		"created_at": "invalid-date"
	}`

	var metadata storage.ImageMetadata
	err := json.Unmarshal([]byte(jsonData), &metadata)
	assert.Error(t, err)
}

func TestImageMetadata_UnmarshalJSON_Invalid(t *testing.T) {
	jsonData := `{invalid json`

	var metadata storage.ImageMetadata
	err := json.Unmarshal([]byte(jsonData), &metadata)
	assert.Error(t, err)
}

// Test ListResult
func TestListResult_Structure(t *testing.T) {
	result := storage.ListResult{
		Key:      "test.jpg",
		Size:     1024,
		Modified: time.Now(),
	}

	assert.Equal(t, "test.jpg", result.Key)
	assert.Equal(t, int64(1024), result.Size)
	assert.False(t, result.Modified.IsZero())
}

// Test StorageStats
func TestStorageStats_Structure(t *testing.T) {
	stats := storage.StorageStats{
		TotalImages: 100,
		TotalSize:   1024000,
		FreeSpace:   5120000,
	}

	assert.Equal(t, int64(100), stats.TotalImages)
	assert.Equal(t, int64(1024000), stats.TotalSize)
	assert.Equal(t, int64(5120000), stats.FreeSpace)
}
