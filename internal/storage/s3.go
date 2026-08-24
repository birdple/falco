package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// Default timeout for S3 storage operations
const s3OperationTimeout = 30 * time.Second

// S3Storage implements StorageBackend for Amazon S3 storage
type S3Storage struct {
	client          *s3.Client
	bucket          string
	defaultBucket   string
	metadataEncoder MetadataEncoder
}

// NewS3Storage creates a new S3 storage backend
func NewS3Storage(cfg *S3Config) (*S3Storage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	awsCfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(cfg.Region),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	// Override credentials if provided
	if cfg.AccessKey != "" && cfg.SecretKey != "" {
		awsCfg.Credentials = aws.NewCredentialsCache(
			aws.CredentialsProviderFunc(func(ctx context.Context) (aws.Credentials, error) {
				return aws.Credentials{
					AccessKeyID:     cfg.AccessKey,
					SecretAccessKey: cfg.SecretKey,
				}, nil
			}),
		)
	}

	// Override endpoint if provided (for MinIO, LocalStack, etc.).
	//
	// Vía BaseEndpoint y no EndpointResolverWithOptions: ese resolver está
	// deprecado en aws-sdk-go-v2. r2.go ya usaba la forma nueva.
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
			// Los endpoints tipo MinIO/LocalStack no resuelven buckets como
			// subdominio, así que hay que hablarles en path-style.
			o.UsePathStyle = true
		}
	})

	return &S3Storage{
		client:          client,
		bucket:          cfg.Bucket,
		defaultBucket:   cfg.Bucket,
		metadataEncoder: NewMetadataEncoder(),
	}, nil
}

// WithBucket returns a new S3Storage instance with a different bucket
func (s *S3Storage) WithBucket(bucket string) StorageBackend {
	if bucket == "" {
		bucket = s.defaultBucket
	}

	return &S3Storage{
		client:          s.client,
		bucket:          bucket,
		defaultBucket:   s.defaultBucket,
		metadataEncoder: s.metadataEncoder,
	}
}

// GetCurrentBucket returns the current bucket name
func (s *S3Storage) GetCurrentBucket() string {
	return s.bucket
}

// Store stores an image with the given key and metadata
func (s *S3Storage) Store(ctx context.Context, key string, data io.Reader, metadata *ImageMetadata) error {
	// Apply operation timeout
	ctx, cancel := context.WithTimeout(ctx, s3OperationTimeout)
	defer cancel()

	// Encode metadata
	s3Metadata, err := s.metadataEncoder.Encode(metadata)
	if err != nil {
		return fmt.Errorf("failed to encode metadata: %w", err)
	}

	// Upload object
	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(key),
		Body:        data,
		ContentType: aws.String(metadata.ContentType),
		Metadata:    s3Metadata,
	})

	if err != nil {
		return fmt.Errorf("failed to upload object: %w", err)
	}

	return nil
}

// Retrieve retrieves an image by key
func (s *S3Storage) Retrieve(ctx context.Context, key string) (io.ReadCloser, *ImageMetadata, error) {
	// Apply operation timeout
	ctx, cancel := context.WithTimeout(ctx, s3OperationTimeout)
	defer cancel()

	// Get object
	result, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})

	if err != nil {
		if isNotFoundError(err) {
			return nil, nil, ErrImageNotFound
		}
		return nil, nil, fmt.Errorf("failed to get object: %w", err)
	}

	// Decode metadata from S3 metadata
	metadata, err := s.metadataEncoder.Decode(result.Metadata)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to decode metadata: %w", err)
	}

	// Update metadata with S3-specific fields
	metadata.ID = key
	metadata.ContentType = aws.ToString(result.ContentType)
	metadata.Size = *result.ContentLength
	metadata.ETag = strings.Trim(aws.ToString(result.ETag), `"`)

	return result.Body, metadata, nil
}

// Delete deletes an image by key
func (s *S3Storage) Delete(ctx context.Context, key string) error {
	// Apply operation timeout
	ctx, cancel := context.WithTimeout(ctx, s3OperationTimeout)
	defer cancel()

	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})

	if err != nil {
		if isNotFoundError(err) {
			return ErrImageNotFound
		}
		return fmt.Errorf("failed to delete object: %w", err)
	}

	return nil
}

// Exists checks if an image exists by key
func (s *S3Storage) Exists(ctx context.Context, key string) (bool, error) {
	// Apply operation timeout
	ctx, cancel := context.WithTimeout(ctx, s3OperationTimeout)
	defer cancel()

	_, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})

	if err != nil {
		if isNotFoundError(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to check object existence: %w", err)
	}

	return true, nil
}

// Health checks the health of the S3 storage
func (s *S3Storage) Health(ctx context.Context) error {
	// Try to list objects with max 1 result
	_, err := s.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket:  aws.String(s.bucket),
		MaxKeys: aws.Int32(1),
	})

	if err != nil {
		return fmt.Errorf("S3 health check failed: %w", err)
	}

	return nil
}

// GetStats returns storage statistics
func (s *S3Storage) GetStats(ctx context.Context) (*StorageStats, error) {
	stats := &StorageStats{}

	// List all objects to get statistics
	paginator := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket),
	})

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to list objects: %w", err)
		}

		for _, obj := range page.Contents {
			stats.TotalImages++
			stats.TotalSize += *obj.Size
		}
	}

	return stats, nil
}

// List lists objects with the given prefix
func (s *S3Storage) List(ctx context.Context, prefix string) ([]ListResult, error) {
	var results []ListResult

	// Normalize prefix - add trailing slash if prefix is provided and doesn't end with /
	listPrefix := prefix
	if prefix != "" && prefix[len(prefix)-1] != '/' {
		listPrefix = prefix + "/"
	}

	// List objects with prefix
	paginator := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket),
		Prefix: aws.String(listPrefix),
	})

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to list objects: %w", err)
		}

		for _, obj := range page.Contents {
			key := aws.ToString(obj.Key)
			size := *obj.Size

			// Skip directory markers (objects with size 0 that end with /)
			if size == 0 && len(key) > 0 && key[len(key)-1] == '/' {
				continue
			}

			// Skip empty keys
			if key == "" {
				continue
			}

			results = append(results, ListResult{
				Key:      key,
				Size:     size,
				Modified: *obj.LastModified,
			})
		}
	}

	return results, nil
}

// isNotFoundError checks if an error indicates that an object was not found
func isNotFoundError(err error) bool {
	var notFound *types.NotFound
	var noSuchKey *types.NoSuchKey
	return errors.As(err, &notFound) || errors.As(err, &noSuchKey) ||
		err.Error() == "NotFound" || strings.Contains(err.Error(), "NoSuchKey")
}
