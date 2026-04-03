package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// R2Storage implements StorageBackend for Cloudflare R2 storage.
// R2 is S3-compatible, so this wraps S3Storage with R2-specific configuration.
type R2Storage struct {
	*S3Storage
}

// NewR2Storage creates a new Cloudflare R2 storage backend
func NewR2Storage(cfg *R2Config) (*R2Storage, error) {
	if cfg.AccountID == "" {
		return nil, fmt.Errorf("%w: R2 account ID is required", ErrInvalidConfiguration)
	}
	if cfg.Bucket == "" {
		return nil, fmt.Errorf("%w: R2 bucket is required", ErrInvalidConfiguration)
	}

	endpoint := fmt.Sprintf("https://%s.r2.cloudflarestorage.com", cfg.AccountID)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	awsCfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion("auto"),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			cfg.AccessKey, cfg.SecretKey, "",
		)),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config for R2: %w", err)
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
	})

	s3s := &S3Storage{
		client:          client,
		bucket:          cfg.Bucket,
		defaultBucket:   cfg.Bucket,
		metadataEncoder: NewMetadataEncoder(),
	}

	return &R2Storage{S3Storage: s3s}, nil
}

// WithBucket returns a new R2Storage instance with a different bucket
func (r *R2Storage) WithBucket(bucket string) StorageBackend {
	if bucket == "" {
		bucket = r.defaultBucket
	}

	return &R2Storage{
		S3Storage: &S3Storage{
			client:          r.client,
			bucket:          bucket,
			defaultBucket:   r.defaultBucket,
			metadataEncoder: r.metadataEncoder,
		},
	}
}
