package storage

import (
	"encoding/json"
	"fmt"
	"time"
)

// MetadataEncoder handles metadata encoding/decoding
type MetadataEncoder interface {
	Encode(metadata *ImageMetadata) (map[string]string, error)
	Decode(data map[string]string) (*ImageMetadata, error)
}

// defaultMetadataEncoder implements MetadataEncoder
type defaultMetadataEncoder struct{}

// NewMetadataEncoder creates a new metadata encoder
func NewMetadataEncoder() MetadataEncoder {
	return &defaultMetadataEncoder{}
}

// Encode converts ImageMetadata to a string map for storage
// Note: content-type is NOT included as it's a reserved HTTP header
func (e *defaultMetadataEncoder) Encode(metadata *ImageMetadata) (map[string]string, error) {
	if metadata == nil {
		return nil, fmt.Errorf("metadata cannot be nil")
	}

	result := map[string]string{
		"original-name": metadata.OriginalName,
		"format":        metadata.Format,
		"width":         fmt.Sprintf("%d", metadata.Width),
		"height":        fmt.Sprintf("%d", metadata.Height),
		"created-at":    metadata.CreatedAt.Format(time.RFC3339),
	}

	return result, nil
}

// Decode converts a string map to ImageMetadata
func (e *defaultMetadataEncoder) Decode(data map[string]string) (*ImageMetadata, error) {
	if data == nil {
		return &ImageMetadata{}, nil
	}

	metadata := &ImageMetadata{
		OriginalName: data["original-name"],
		Format:       data["format"],
	}

	if widthStr, ok := data["width"]; ok {
		fmt.Sscanf(widthStr, "%d", &metadata.Width)
	}

	if heightStr, ok := data["height"]; ok {
		fmt.Sscanf(heightStr, "%d", &metadata.Height)
	}

	if createdAtStr, ok := data["created-at"]; ok {
		if t, err := time.Parse(time.RFC3339, createdAtStr); err == nil {
			metadata.CreatedAt = t
		}
	}

	return metadata, nil
}

// MarshalJSON implements json.Marshaler for ImageMetadata
func (m *ImageMetadata) MarshalJSON() ([]byte, error) {
	type Alias ImageMetadata
	return json.Marshal(&struct {
		*Alias
		CreatedAt string `json:"created_at"`
	}{
		Alias:     (*Alias)(m),
		CreatedAt: m.CreatedAt.Format(time.RFC3339),
	})
}

// UnmarshalJSON implements json.Unmarshaler for ImageMetadata
func (m *ImageMetadata) UnmarshalJSON(data []byte) error {
	type Alias ImageMetadata
	aux := &struct {
		*Alias
		CreatedAt string `json:"created_at"`
	}{
		Alias: (*Alias)(m),
	}

	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	if aux.CreatedAt != "" {
		t, err := time.Parse(time.RFC3339, aux.CreatedAt)
		if err != nil {
			return err
		}
		m.CreatedAt = t
	}

	return nil
}
