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
	Register(StorageTypeMinIO, newMinIOStorageFromConfig)
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

// newMinIOStorageFromConfig adapter for MinIO storage creation
func newMinIOStorageFromConfig(config *StorageConfig) (StorageBackend, error) {
	return NewMinIOStorage(&MinIOConfig{
		Bucket:    config.MinIOBucket,
		Endpoint:  config.MinIOEndpoint,
		Region:    config.MinIORegion,
		AccessKey: config.AccessKey,
		SecretKey: config.SecretKey,
		Secure:    config.MinIOSecure,
	})
}
