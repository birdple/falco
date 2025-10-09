package storage

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// S3Storage implements StorageBackend for Amazon S3 storage
type S3Storage struct {
	client          *s3.Client
	bucket          string
	metadataEncoder MetadataEncoder
}

// NewS3Storage creates a new S3 storage backend
func NewS3Storage(cfg *S3Config) (*S3Storage, error) {
	awsCfg, err := config.LoadDefaultConfig(context.Background(),
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

	// Override endpoint if provided (for MinIO, LocalStack, etc.)
	if cfg.Endpoint != "" {
		awsCfg.EndpointResolverWithOptions = aws.EndpointResolverWithOptionsFunc(func(service, region string, options ...interface{}) (aws.Endpoint, error) {
			return aws.Endpoint{
				URL:           cfg.Endpoint,
				SigningRegion: region,
			}, nil
		})
	}

	client := s3.NewFromConfig(awsCfg)

	return &S3Storage{
		client:          client,
		bucket:          cfg.Bucket,
		metadataEncoder: NewMetadataEncoder(),
	}, nil
}

// Store stores an image with the given key and metadata
func (s *S3Storage) Store(ctx context.Context, key string, data io.Reader, metadata *ImageMetadata) error {
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
	metadata.Size = result.ContentLength
	metadata.ETag = strings.Trim(aws.ToString(result.ETag), `"`)

	return result.Body, metadata, nil
}

// Delete deletes an image by key
func (s *S3Storage) Delete(ctx context.Context, key string) error {
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
		MaxKeys: 1,
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
			stats.TotalSize += obj.Size
		}
	}

	return stats, nil
}

// isNotFoundError checks if an error indicates that an object was not found
func isNotFoundError(err error) bool {
	var notFoundErr *types.NotFound
	var noSuchKeyErr *types.NoSuchKey
	return err.Error() == "NotFound" ||
		strings.Contains(err.Error(), "NoSuchKey") ||
		(notFoundErr != nil && err == notFoundErr) ||
		(noSuchKeyErr != nil && err == noSuchKeyErr)
}
