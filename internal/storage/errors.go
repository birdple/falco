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
	ErrStorageUnavailable     = errors.New("storage backend unavailable")
	ErrUnsupportedStorageType = errors.New("unsupported storage type")
	ErrInvalidConfiguration   = errors.New("invalid storage configuration")
	ErrBackendNotFound        = errors.New("storage backend not found")

	// ErrBucketNotHonoured means the request named a bucket falco cannot serve:
	// it is not a registered bucket, not a declared alias, and the backend it
	// resolved to has no way to switch to it.
	//
	// It exists so that case is visible: WithBucket returns a backend and not
	// an error, and the circuit-breaker wrapper implements it for every backend
	// by handing back itself, so without this error an unreachable bucket
	// would end up written into the default one with a 201.
	ErrBucketNotHonoured = errors.New("requested bucket cannot be served")

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
