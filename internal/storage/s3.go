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
	// Via BaseEndpoint rather than EndpointResolverWithOptions: that resolver is
	// deprecated in aws-sdk-go-v2. r2.go already used the newer form.
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
			// MinIO and LocalStack style endpoints do not resolve buckets as
			// subdomains, so they have to be addressed path-style.
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

// List returns every object under prefix, following S3 pagination.
//
// The prefix is matched verbatim. This used to append a trailing slash, which
// made the same prefix list differently on S3 than on jay; directory semantics
// are the caller's to ask for.
func (s *S3Storage) List(ctx context.Context, prefix string) ([]ListResult, error) {
	var results []ListResult

	paginator := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket),
		Prefix: aws.String(prefix),
	})

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to list objects: %w", err)
		}
		results = append(results, s3Contents(page.Contents)...)
	}

	return results, nil
}

// ListPage returns one page of objects, honouring prefix, delimiter and cursor.
//
// The cursor is S3's continuation token, passed through opaquely.
func (s *S3Storage) ListPage(ctx context.Context, opts ListOptions) (*ListPage, error) {
	in := &s3.ListObjectsV2Input{
		Bucket:  aws.String(s.bucket),
		Prefix:  aws.String(opts.Prefix),
		MaxKeys: aws.Int32(int32(NormalizeMaxKeys(opts.MaxKeys))),
	}
	if opts.Delimiter != "" {
		in.Delimiter = aws.String(opts.Delimiter)
	}
	if opts.Cursor != "" {
		in.ContinuationToken = aws.String(opts.Cursor)
	}

	page, err := s.client.ListObjectsV2(ctx, in)
	if err != nil {
		return nil, fmt.Errorf("failed to list objects: %w", err)
	}

	out := &ListPage{
		Objects:     s3Contents(page.Contents),
		NextCursor:  aws.ToString(page.NextContinuationToken),
		IsTruncated: aws.ToBool(page.IsTruncated),
	}
	for _, cp := range page.CommonPrefixes {
		if p := aws.ToString(cp.Prefix); p != "" {
			out.CommonPrefixes = append(out.CommonPrefixes, p)
		}
	}
	return out, nil
}

// s3Contents converts a page of S3 objects, dropping the entries that are not
// real objects: empty keys and the zero-byte markers S3 consoles create to
// fake directories.
func s3Contents(contents []types.Object) []ListResult {
	out := make([]ListResult, 0, len(contents))
	for _, obj := range contents {
		key := aws.ToString(obj.Key)
		if key == "" {
			continue
		}
		size := aws.ToInt64(obj.Size)
		if size == 0 && key[len(key)-1] == '/' {
			continue
		}
		out = append(out, ListResult{
			Key:      key,
			Size:     size,
			Modified: aws.ToTime(obj.LastModified),
			ETag:     strings.Trim(aws.ToString(obj.ETag), `"`),
		})
	}
	return out
}

// isNotFoundError checks if an error indicates that an object was not found
func isNotFoundError(err error) bool {
	var notFound *types.NotFound
	var noSuchKey *types.NoSuchKey
	return errors.As(err, &notFound) || errors.As(err, &noSuchKey) ||
		err.Error() == "NotFound" || strings.Contains(err.Error(), "NoSuchKey")
}
