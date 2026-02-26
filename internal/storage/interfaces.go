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
	MaxAge       int       `json:"maxage,omitempty"`
	SMaxAge      int       `json:"smaxage,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	ETag         string    `json:"etag,omitempty"`
}

// ListResult holds information about a listed object
type ListResult struct {
	Key      string    `json:"key"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
}

// Lister defines the interface for listing operations
type Lister interface {
	List(ctx context.Context, prefix string) ([]ListResult, error)
}

// Reader defines the interface for reading operations
type Reader interface {
	Retrieve(ctx context.Context, key string) (io.ReadCloser, *ImageMetadata, error)
	Exists(ctx context.Context, key string) (bool, error)
}

// Writer defines the interface for writing operations
type Writer interface {
	Store(ctx context.Context, key string, data io.Reader, metadata *ImageMetadata) error
}

// Deleter defines the interface for deletion operations
type Deleter interface {
	Delete(ctx context.Context, key string) error
}

// BucketAware defines the interface for storage backends that support dynamic bucket selection
type BucketAware interface {
	WithBucket(bucket string) StorageBackend
	GetCurrentBucket() string
}

// HealthChecker defines the interface for health checking
type HealthChecker interface {
	Health(ctx context.Context) error
}

// StatsProvider defines the interface for statistics
type StatsProvider interface {
	GetStats(ctx context.Context) (*StorageStats, error)
}

// StorageBackend combines all storage interfaces (Composite Pattern)
type StorageBackend interface {
	Reader
	Writer
	Deleter
	Lister
	HealthChecker
	StatsProvider
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
	StorageTypeMinIO      StorageType = "minio"
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
	// MinIO specific fields
	MinIOEndpoint string
	MinIOBucket   string
	MinIORegion   string
	MinIOSecure   bool
}

// MinIOConfig holds MinIO storage configuration
type MinIOConfig struct {
	Bucket    string
	Endpoint  string
	Region    string
	AccessKey string
	SecretKey string
	Secure    bool
}

// S3Config holds S3 storage configuration
type S3Config struct {
	Bucket    string
	Region    string
	Endpoint  string
	AccessKey string
	SecretKey string
}
