package storage

import (
	"context"
	"io"
	"time"
)

// ImageMetadata holds metadata about stored images
type ImageMetadata struct {
	ID           string    `json:"id"`
	StorageKey   string    `json:"storage_key,omitempty"`
	OriginalName string    `json:"original_name"`
	Format       string    `json:"format"`
	Size         int64     `json:"size"`
	Width        int       `json:"width"`
	Height       int       `json:"height"`
	ContentType  string    `json:"content_type"`
	MaxAge       int       `json:"maxage,omitzero"`
	SMaxAge      int       `json:"smaxage,omitzero"`
	CreatedAt    time.Time `json:"created_at"`
	ETag         string    `json:"etag,omitempty"`
	// OwnerID is the opaque identifier (supplied by the caller via X-Owner-Id
	// on upload) of the entity that owns this image. Falco does not interpret
	// it — it is used only to enforce per-image authorization on mutating
	// operations (delete/update). Empty means legacy/unowned: the image was
	// uploaded before owner tracking existed and may only be mutated by an
	// admin-scoped API key.
	OwnerID string `json:"owner_id,omitempty"`
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
	FreeSpace   int64 `json:"free_space_bytes,omitzero"`
}

// StorageType represents the type of storage backend
type StorageType string

// The storage backends falco ships with. Each one is registered in the factory
// and selected through STORAGE_BUCKET_<NAME>_TYPE.
//
// A MinIO deployment is addressed with s3: NewS3Storage points BaseEndpoint at
// the custom endpoint and switches to path-style addressing.
const (
	StorageTypeFilesystem StorageType = "filesystem"
	StorageTypeS3         StorageType = "s3"
	StorageTypeR2         StorageType = "r2"
	StorageTypeJay        StorageType = "jay"
)

// ReplicationMode defines how primary and secondary storage interact
type ReplicationMode string

// How a replicated backend treats its secondaries.
//
// sync waits for every secondary before acknowledging a write, async returns as
// soon as the primary commits, and read-fallback replicates nothing but serves
// reads from a secondary when the primary misses.
const (
	ReplicationSync         ReplicationMode = "sync"
	ReplicationAsync        ReplicationMode = "async"
	ReplicationReadFallback ReplicationMode = "read-fallback"
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
	// R2 specific fields
	R2Bucket    string
	R2AccountID string
	R2AccessKey string
	R2SecretKey string
	// Jay-specific fields
	JayAddr      string // native protocol address, e.g. "jay:4012"
	JayAdminAddr string // HTTP address for GetStats (same port as S3 API), e.g. "jay:4010"
	JayTokenID   string
	JayTokenSec  string
	JayBucket    string
	JayPoolSize  int
}

// S3Config holds S3 storage configuration
type S3Config struct {
	Bucket    string
	Region    string
	Endpoint  string
	AccessKey string
	SecretKey string
}

// R2Config holds Cloudflare R2 storage configuration
type R2Config struct {
	Bucket    string
	AccountID string
	AccessKey string
	SecretKey string
}
