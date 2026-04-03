package storage

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMetadataEncoder_Encode(t *testing.T) {
	encoder := NewMetadataEncoder()
	now := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)

	metadata := &ImageMetadata{
		OriginalName: "photo.jpg",
		Format:       "jpeg",
		Width:        1920,
		Height:       1080,
		CreatedAt:    now,
	}

	result, err := encoder.Encode(metadata)
	require.NoError(t, err)
	assert.Equal(t, "photo.jpg", result["original-name"])
	assert.Equal(t, "jpeg", result["format"])
	assert.Equal(t, "1920", result["width"])
	assert.Equal(t, "1080", result["height"])
	assert.Equal(t, now.Format(time.RFC3339), result["created-at"])
}

func TestMetadataEncoder_Encode_NilMetadata(t *testing.T) {
	encoder := NewMetadataEncoder()
	_, err := encoder.Encode(nil)
	assert.Error(t, err)
}

func TestMetadataEncoder_Decode(t *testing.T) {
	encoder := NewMetadataEncoder()
	now := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)

	data := map[string]string{
		"original-name": "photo.jpg",
		"format":        "jpeg",
		"width":         "1920",
		"height":        "1080",
		"created-at":    now.Format(time.RFC3339),
	}

	metadata, err := encoder.Decode(data)
	require.NoError(t, err)
	assert.Equal(t, "photo.jpg", metadata.OriginalName)
	assert.Equal(t, "jpeg", metadata.Format)
	assert.Equal(t, 1920, metadata.Width)
	assert.Equal(t, 1080, metadata.Height)
	assert.Equal(t, now, metadata.CreatedAt)
}

func TestMetadataEncoder_Decode_NilData(t *testing.T) {
	encoder := NewMetadataEncoder()
	metadata, err := encoder.Decode(nil)
	require.NoError(t, err)
	assert.NotNil(t, metadata)
}

func TestMetadataEncoder_Decode_PartialData(t *testing.T) {
	encoder := NewMetadataEncoder()
	data := map[string]string{
		"original-name": "photo.jpg",
		"format":        "png",
	}

	metadata, err := encoder.Decode(data)
	require.NoError(t, err)
	assert.Equal(t, "photo.jpg", metadata.OriginalName)
	assert.Equal(t, "png", metadata.Format)
	assert.Equal(t, 0, metadata.Width)
	assert.Equal(t, 0, metadata.Height)
}

func TestMetadataEncoder_RoundTrip(t *testing.T) {
	encoder := NewMetadataEncoder()
	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)

	original := &ImageMetadata{
		OriginalName: "test.webp",
		Format:       "webp",
		Width:        800,
		Height:       600,
		CreatedAt:    now,
	}

	encoded, err := encoder.Encode(original)
	require.NoError(t, err)

	decoded, err := encoder.Decode(encoded)
	require.NoError(t, err)

	assert.Equal(t, original.OriginalName, decoded.OriginalName)
	assert.Equal(t, original.Format, decoded.Format)
	assert.Equal(t, original.Width, decoded.Width)
	assert.Equal(t, original.Height, decoded.Height)
	assert.Equal(t, original.CreatedAt, decoded.CreatedAt)
}

func TestImageMetadata_MarshalJSON(t *testing.T) {
	now := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)
	metadata := &ImageMetadata{
		ID:           "abc123",
		OriginalName: "photo.jpg",
		Format:       "jpeg",
		Size:         1024,
		Width:        1920,
		Height:       1080,
		ContentType:  "image/jpeg",
		CreatedAt:    now,
	}

	data, err := json.Marshal(metadata)
	require.NoError(t, err)

	var result map[string]interface{}
	err = json.Unmarshal(data, &result)
	require.NoError(t, err)

	assert.Equal(t, "abc123", result["id"])
	assert.Equal(t, now.Format(time.RFC3339), result["created_at"])
}

func TestImageMetadata_UnmarshalJSON(t *testing.T) {
	jsonData := `{
		"id": "abc123",
		"original_name": "photo.jpg",
		"format": "jpeg",
		"size": 1024,
		"width": 1920,
		"height": 1080,
		"content_type": "image/jpeg",
		"created_at": "2024-01-15T10:30:00Z"
	}`

	var metadata ImageMetadata
	err := json.Unmarshal([]byte(jsonData), &metadata)
	require.NoError(t, err)

	assert.Equal(t, "abc123", metadata.ID)
	assert.Equal(t, "photo.jpg", metadata.OriginalName)
	assert.Equal(t, "jpeg", metadata.Format)
	assert.Equal(t, int64(1024), metadata.Size)
	assert.Equal(t, 1920, metadata.Width)
	assert.Equal(t, 1080, metadata.Height)
	assert.Equal(t, time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC), metadata.CreatedAt)
}

func BenchmarkMetadataEncoder_Encode(b *testing.B) {
	encoder := NewMetadataEncoder()
	now := time.Now()
	metadata := &ImageMetadata{
		OriginalName: "photo.jpg",
		Format:       "jpeg",
		Width:        1920,
		Height:       1080,
		CreatedAt:    now,
	}
	b.ResetTimer()
	for range b.N {
		encoder.Encode(metadata)
	}
}

func BenchmarkMetadataEncoder_Decode(b *testing.B) {
	encoder := NewMetadataEncoder()
	data := map[string]string{
		"original-name": "photo.jpg",
		"format":        "jpeg",
		"width":         "1920",
		"height":        "1080",
		"created-at":    time.Now().Format(time.RFC3339),
	}
	b.ResetTimer()
	for range b.N {
		encoder.Decode(data)
	}
}

func BenchmarkMetadataEncoder_RoundTrip(b *testing.B) {
	encoder := NewMetadataEncoder()
	metadata := &ImageMetadata{
		OriginalName: "photo.jpg",
		Format:       "jpeg",
		Width:        1920,
		Height:       1080,
		CreatedAt:    time.Now(),
	}
	b.ResetTimer()
	for range b.N {
		encoded, _ := encoder.Encode(metadata)
		encoder.Decode(encoded)
	}
}

func BenchmarkImageMetadata_MarshalJSON(b *testing.B) {
	metadata := &ImageMetadata{
		ID:           "abc123",
		OriginalName: "photo.jpg",
		Format:       "jpeg",
		Size:         1024,
		Width:        1920,
		Height:       1080,
		ContentType:  "image/jpeg",
		CreatedAt:    time.Now(),
	}
	b.ResetTimer()
	for range b.N {
		metadata.MarshalJSON()
	}
}

func TestImageMetadata_JSON_RoundTrip(t *testing.T) {
	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	original := ImageMetadata{
		ID:           "test123",
		OriginalName: "image.png",
		Format:       "png",
		Size:         2048,
		Width:        640,
		Height:       480,
		ContentType:  "image/png",
		CreatedAt:    now,
	}

	data, err := json.Marshal(&original)
	require.NoError(t, err)

	var decoded ImageMetadata
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, original.ID, decoded.ID)
	assert.Equal(t, original.OriginalName, decoded.OriginalName)
	assert.Equal(t, original.Format, decoded.Format)
	assert.Equal(t, original.Size, decoded.Size)
	assert.Equal(t, original.Width, decoded.Width)
	assert.Equal(t, original.Height, decoded.Height)
	assert.Equal(t, original.CreatedAt, decoded.CreatedAt)
}
