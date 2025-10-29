package storage

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// MinIOStorage implements StorageBackend for MinIO object storage
type MinIOStorage struct {
	client          *minio.Client
	bucket          string
	defaultBucket   string
	metadataEncoder MetadataEncoder
}

// NewMinIOStorage creates a new MinIO storage backend
func NewMinIOStorage(cfg *MinIOConfig) (*MinIOStorage, error) {
	// Initialize MinIO client object (EXACTLY like user's working example)
	minioClient, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.Secure,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to initialize MinIO client: %w", err)
	}

	// Try to create bucket with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	location := "us-east-1" // Default location

	err = minioClient.MakeBucket(ctx, cfg.Bucket, minio.MakeBucketOptions{Region: location})
	if err != nil {
		// Check to see if we already own this bucket (which happens if you run this twice)
		exists, errBucketExists := minioClient.BucketExists(ctx, cfg.Bucket)
		if errBucketExists == nil && exists {
			// Bucket already exists, this is fine
		} else {
			return nil, fmt.Errorf("failed to create bucket: %w", err)
		}
	}

	return &MinIOStorage{
		client:          minioClient,
		bucket:          cfg.Bucket,
		defaultBucket:   cfg.Bucket,
		metadataEncoder: NewMetadataEncoder(),
	}, nil
}

// WithBucket returns a new MinIOStorage instance with a different bucket
func (m *MinIOStorage) WithBucket(bucket string) StorageBackend {
	if bucket == "" {
		bucket = m.defaultBucket
	}

	// Ensure bucket exists with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := m.client.MakeBucket(ctx, bucket, minio.MakeBucketOptions{Region: "us-east-1"})
	if err != nil {
		// Check if bucket already exists
		exists, errBucketExists := m.client.BucketExists(ctx, bucket)
		if errBucketExists != nil || !exists {
			// If we can't create or verify bucket, return original storage
			return m
		}
	}

	return &MinIOStorage{
		client:          m.client,
		bucket:          bucket,
		defaultBucket:   m.defaultBucket,
		metadataEncoder: m.metadataEncoder,
	}
}

// GetCurrentBucket returns the current bucket name
func (m *MinIOStorage) GetCurrentBucket() string {
	return m.bucket
}

// Store stores an image with the given key and metadata
func (m *MinIOStorage) Store(ctx context.Context, key string, data io.Reader, metadata *ImageMetadata) error {
	// Encode metadata
	userMetadata, err := m.metadataEncoder.Encode(metadata)
	if err != nil {
		return fmt.Errorf("failed to encode metadata: %w", err)
	}

	// Upload object
	info, err := m.client.PutObject(ctx, m.bucket, key, data, -1, minio.PutObjectOptions{
		ContentType:  metadata.ContentType,
		UserMetadata: userMetadata,
	})
	if err != nil {
		return fmt.Errorf("failed to upload object: %w", err)
	}

	// Update metadata with size from MinIO response
	metadata.Size = info.Size
	metadata.ETag = info.ETag

	return nil
}

// Retrieve retrieves an image by key
func (m *MinIOStorage) Retrieve(ctx context.Context, key string) (io.ReadCloser, *ImageMetadata, error) {
	// Get object
	object, err := m.client.GetObject(ctx, m.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			return nil, nil, ErrImageNotFound
		}
		return nil, nil, fmt.Errorf("failed to get object: %w", err)
	}

	// Get object info for metadata
	info, err := object.Stat()
	if err != nil {
		object.Close()
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			return nil, nil, ErrImageNotFound
		}
		return nil, nil, fmt.Errorf("failed to get object info: %w", err)
	}

	// Decode metadata from MinIO user metadata
	metadata, err := m.metadataEncoder.Decode(info.UserMetadata)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to decode metadata: %w", err)
	}

	// Update metadata with MinIO-specific fields
	metadata.ID = key
	metadata.ContentType = info.ContentType
	metadata.Size = info.Size
	metadata.ETag = info.ETag

	return object, metadata, nil
}

// Delete deletes an image by key
func (m *MinIOStorage) Delete(ctx context.Context, key string) error {
	err := m.client.RemoveObject(ctx, m.bucket, key, minio.RemoveObjectOptions{})
	if err != nil {
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			return ErrImageNotFound
		}
		return fmt.Errorf("failed to delete object: %w", err)
	}

	return nil
}

// Exists checks if an image exists by key
func (m *MinIOStorage) Exists(ctx context.Context, key string) (bool, error) {
	_, err := m.client.StatObject(ctx, m.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			return false, nil
		}
		return false, fmt.Errorf("failed to check object existence: %w", err)
	}

	return true, nil
}

// Health checks the health of the MinIO storage
func (m *MinIOStorage) Health(ctx context.Context) error {
	if m.client == nil {
		return fmt.Errorf("MinIO client is not initialized")
	}

	// Try to list objects with max 1 result
	for range m.client.ListObjects(ctx, m.bucket, minio.ListObjectsOptions{MaxKeys: 1}) {
		// Just iterate to check if connection works
		break
	}

	return nil
}

// GetStats returns storage statistics
func (m *MinIOStorage) GetStats(ctx context.Context) (*StorageStats, error) {
	stats := &StorageStats{}

	// List all objects to get statistics
	for objectInfo := range m.client.ListObjects(ctx, m.bucket, minio.ListObjectsOptions{}) {
		if objectInfo.Err != nil {
			return nil, fmt.Errorf("failed to list objects: %w", objectInfo.Err)
		}

		stats.TotalImages++
		stats.TotalSize += objectInfo.Size
	}

	return stats, nil
}

// List lists objects with the given prefix
func (m *MinIOStorage) List(ctx context.Context, prefix string) ([]ListResult, error) {
	var results []ListResult

	// Normalize prefix - add trailing slash if prefix is provided and doesn't end with /
	listPrefix := prefix
	if prefix != "" && prefix[len(prefix)-1] != '/' {
		listPrefix = prefix + "/"
	}

	// List objects with prefix
	for objectInfo := range m.client.ListObjects(ctx, m.bucket, minio.ListObjectsOptions{
		Prefix:    listPrefix,
		Recursive: true, // List all objects recursively, not just top-level
	}) {
		if objectInfo.Err != nil {
			return nil, fmt.Errorf("failed to list objects: %w", objectInfo.Err)
		}

		// Skip directory markers (objects with size 0 that end with /)
		if objectInfo.Size == 0 && len(objectInfo.Key) > 0 && objectInfo.Key[len(objectInfo.Key)-1] == '/' {
			continue
		}

		// Skip empty keys
		if objectInfo.Key == "" {
			continue
		}

		results = append(results, ListResult{
			Key:      objectInfo.Key,
			Size:     objectInfo.Size,
			Modified: objectInfo.LastModified,
		})
	}

	return results, nil
}
