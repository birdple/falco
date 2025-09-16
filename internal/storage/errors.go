package storage

import "errors"

// Common storage errors
var (
	ErrImageNotFound          = errors.New("image not found")
	ErrImageAlreadyExists     = errors.New("image already exists")
	ErrInvalidImageFormat     = errors.New("invalid image format")
	ErrStorageUnavailable     = errors.New("storage backend unavailable")
	ErrUnsupportedStorageType = errors.New("unsupported storage type")
	ErrInvalidConfiguration   = errors.New("invalid storage configuration")
	ErrPermissionDenied       = errors.New("permission denied")
	ErrDiskFull               = errors.New("disk full")
	ErrNetworkError           = errors.New("network error")
	ErrTimeout                = errors.New("operation timeout")
	ErrInvalidKey             = errors.New("invalid storage key")
	ErrCorruptedData          = errors.New("corrupted data")
)

// IsNotFound returns true if the error indicates that an image was not found
func IsNotFound(err error) bool {
	return errors.Is(err, ErrImageNotFound)
}

// IsAlreadyExists returns true if the error indicates that an image already exists
func IsAlreadyExists(err error) bool {
	return errors.Is(err, ErrImageAlreadyExists)
}

// IsUnavailable returns true if the error indicates that storage is unavailable
func IsUnavailable(err error) bool {
	return errors.Is(err, ErrStorageUnavailable)
}

// IsNetworkError returns true if the error indicates a network problem
func IsNetworkError(err error) bool {
	return errors.Is(err, ErrNetworkError)
}

// IsTimeout returns true if the error indicates a timeout
func IsTimeout(err error) bool {
	return errors.Is(err, ErrTimeout)
}
