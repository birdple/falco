package storage

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/birdple/falco/internal/jsonx"
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
		return nil, errors.New("metadata cannot be nil")
	}

	result := map[string]string{
		"original-name": metadata.OriginalName,
		"format":        metadata.Format,
		"width":         strconv.Itoa(metadata.Width),
		"height":        strconv.Itoa(metadata.Height),
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
		width, err := strconv.Atoi(widthStr)
		if err != nil {
			return nil, fmt.Errorf("metadata: ancho inválido (%q): %w", widthStr, err)
		}
		metadata.Width = width
	}

	if heightStr, ok := data["height"]; ok {
		height, err := strconv.Atoi(heightStr)
		if err != nil {
			return nil, fmt.Errorf("metadata: alto inválido (%q): %w", heightStr, err)
		}
		metadata.Height = height
	}

	if createdAtStr, ok := data["created-at"]; ok {
		if t, err := time.Parse(time.RFC3339, createdAtStr); err == nil {
			metadata.CreatedAt = t
		}
	}

	return metadata, nil
}

// MarshalJSON implements json.Marshaler for ImageMetadata.
//
// Marshalled with jsonx.Wire and NOT with encoding/json/v2's defaults: these
// bytes are the on-disk metadata format stored in Jay and on the filesystem, so
// they have to keep coming out byte-identical to what v1 emitted. The
// differential test in internal/jsonx is what guards that.
func (m *ImageMetadata) MarshalJSON() ([]byte, error) {
	type Alias ImageMetadata
	return jsonv2.Marshal(&struct {
		*Alias
		CreatedAt string `json:"created_at"`
	}{
		Alias:     (*Alias)(m),
		CreatedAt: m.CreatedAt.Format(time.RFC3339),
	}, jsonx.Wire)
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

	// Lenient on read: metadata written by older falco versions can carry
	// duplicate keys or broken UTF-8 in the original filename, and a file like
	// that must not become unreadable.
	if err := jsonv2.Unmarshal(data, &aux, jsonx.Lenient); err != nil {
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
