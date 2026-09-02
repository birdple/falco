package storage

import "fmt"

// BackendFactory defines the function signature for creating storage backends
type BackendFactory func(config *StorageConfig) (StorageBackend, error)

// registry holds registered storage backend factories
var registry = make(map[StorageType]BackendFactory)

func init() {
	// Register built-in storage backends
	Register(StorageTypeFilesystem, func(config *StorageConfig) (StorageBackend, error) {
		return NewFilesystemStorage(config.LocalPath)
	})
	Register(StorageTypeS3, newS3StorageFromConfig)
	Register(StorageTypeR2, newR2StorageFromConfig)
	Register(StorageTypeJay, newJayStorageFromConfig)
}

// Register registers a new storage backend factory
func Register(storageType StorageType, factory BackendFactory) {
	registry[storageType] = factory
}

// NewStorageBackend creates a new storage backend based on the configuration
func NewStorageBackend(config *StorageConfig) (StorageBackend, error) {
	factory, exists := registry[config.Type]
	if !exists {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedStorageType, config.Type)
	}

	return factory(config)
}

// newS3StorageFromConfig adapter for S3 storage creation
func newS3StorageFromConfig(config *StorageConfig) (StorageBackend, error) {
	return NewS3Storage(&S3Config{
		Bucket:    config.S3Bucket,
		Region:    config.S3Region,
		Endpoint:  config.S3Endpoint,
		AccessKey: config.AccessKey,
		SecretKey: config.SecretKey,
	})
}

// newR2StorageFromConfig adapter for Cloudflare R2 storage creation
func newR2StorageFromConfig(config *StorageConfig) (StorageBackend, error) {
	return NewR2Storage(&R2Config{
		Bucket:    config.R2Bucket,
		AccountID: config.R2AccountID,
		AccessKey: config.R2AccessKey,
		SecretKey: config.R2SecretKey,
	})
}

// newJayStorageFromConfig adapter for Jay storage creation.
func newJayStorageFromConfig(config *StorageConfig) (StorageBackend, error) {
	return NewJayStorage(&JayConfig{
		Addr:      config.JayAddr,
		AdminAddr: config.JayAdminAddr,
		TokenID:   config.JayTokenID,
		Secret:    config.JayTokenSec,
		Bucket:    config.JayBucket,
		PoolSize:  config.JayPoolSize,
	})
}
