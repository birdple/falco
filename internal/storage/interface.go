package storage

import (
	"context"
	"io"
	"time"
)

// ImageMetadata holds metadata about stored images
type ImageMetadata struct {
	ID           string    `json:"id"`
	OriginalName string    `json:"original_name"`
	Format       string    `json:"format"`
	Size         int64     `json:"size"`
	Width        int       `json:"width"`
	Height       int       `json:"height"`
	ContentType  string    `json:"content_type"`
	CreatedAt    time.Time `json:"created_at"`
	ETag         string    `json:"etag,omitempty"`
}

// StorageBackend defines the interface for storage backends
type StorageBackend interface {
	// Store stores an image with the given key and metadata
	Store(ctx context.Context, key string, data io.Reader, metadata *ImageMetadata) error

	// Retrieve retrieves an image by key
	Retrieve(ctx context.Context, key string) (io.ReadCloser, *ImageMetadata, error)

	// Delete deletes an image by key
	Delete(ctx context.Context, key string) error

	// Exists checks if an image exists by key
	Exists(ctx context.Context, key string) (bool, error)

	// Health checks the health of the storage backend
	Health(ctx context.Context) error

	// GetStats returns storage statistics
	GetStats(ctx context.Context) (*StorageStats, error)
}

// StorageStats holds storage statistics
type StorageStats struct {
	TotalImages int64 `json:"total_images"`
	TotalSize   int64 `json:"total_size_bytes"`
	FreeSpace   int64 `json:"free_space_bytes,omitempty"`
}

// StorageType represents the type of storage backend
type StorageType string

const (
	StorageTypeFilesystem StorageType = "filesystem"
	StorageTypeS3         StorageType = "s3"
)

// StorageConfig holds configuration for storage backends
type StorageConfig struct {
	Type       StorageType
	LocalPath  string
	S3Bucket   string
	S3Region   string
	S3Endpoint string
	AccessKey  string
	SecretKey  string
}

// NewStorageBackend creates a new storage backend based on the configuration
func NewStorageBackend(config *StorageConfig) (StorageBackend, error) {
	switch config.Type {
	case StorageTypeFilesystem:
		return NewFilesystemStorage(config.LocalPath)
	case StorageTypeS3:
		return NewS3Storage(&S3Config{
			Bucket:    config.S3Bucket,
			Region:    config.S3Region,
			Endpoint:  config.S3Endpoint,
			AccessKey: config.AccessKey,
			SecretKey: config.SecretKey,
		})
	default:
		return nil, ErrUnsupportedStorageType
	}
}
