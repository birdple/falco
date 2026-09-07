// Package storage abstracts where object bytes live.
//
// Several backends are supported — jay, S3, R2, plain filesystem, and a
// replicated wrapper over any of them. birdple-v2 runs on jay alone; the rest
// are what other users of the project run on.
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
	ErrBackendNotFound        = errors.New("storage backend not found")

	// ErrUnsupportedOperation means the backend cannot do this at all — not that
	// it failed. Callers use it to degrade explicitly (say so in the UI) instead
	// of pretending the operation happened.
	ErrUnsupportedOperation = errors.New("operation not supported by this storage backend")

	// ErrListingTooLarge means a whole-prefix listing hit MaxFullListingObjects
	// before the prefix ran out. Nothing is broken and nothing is missing: the
	// prefix simply has to be read one page at a time.
	//
	// It is an error and not a short slice on purpose. A caller holding a
	// listing cannot tell a complete one from a clipped one, which is exactly
	// how "delete this prefix" leaves objects behind and still answers
	// success.
	ErrListingTooLarge = errors.New("listing exceeds the safety cap")
)

// IsNotFound returns true if the error indicates that an image was not found
func IsNotFound(err error) bool {
	return errors.Is(err, ErrImageNotFound)
}

// IsAlreadyExists returns true if the error indicates that an image already exists
func IsAlreadyExists(err error) bool {
	return errors.Is(err, ErrImageAlreadyExists)
}

// IsListingTooLarge returns true if the error indicates a whole-prefix listing
// hit the safety cap. Callers use it to answer "ask for it a page at a time"
// instead of reporting a backend failure, which is not what happened.
func IsListingTooLarge(err error) bool {
	return errors.Is(err, ErrListingTooLarge)
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
