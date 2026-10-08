// Package circuitbreaker provides a circuit breaker wrapper for storage backends.
package circuitbreaker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/sony/gobreaker"

	"github.com/birdple/falco/internal/storage"
)

// CircuitBreakerSettings holds configuration for the circuit breaker
type CircuitBreakerSettings struct {
	// Name is the name of the circuit breaker
	Name string
	// MaxRequests is the maximum number of requests allowed to pass through
	// when the circuit breaker is half-open
	MaxRequests uint32
	// Interval is the cyclic period of the closed state
	// If Interval is 0, the circuit breaker doesn't clear counts during closed state
	Interval time.Duration
	// Timeout is the period of the open state, after which the state changes to half-open
	Timeout time.Duration
	// ReadyToTrip is called with a copy of Counts whenever a request fails in the closed state
	// If ReadyToTrip returns true, the circuit breaker will be placed into the open state
	// Default: trips after 5 consecutive failures
	ReadyToTrip func(counts gobreaker.Counts) bool
	// OnStateChange is called whenever the state of the circuit breaker changes
	OnStateChange func(name string, from gobreaker.State, to gobreaker.State)
	// IsSuccessful decides whether an error counts against the breaker.
	// If nil, IsBackendFailure is used.
	IsSuccessful func(err error) bool
}

// IsBackendFailure reports whether err should count against the circuit breaker.
//
// The breaker exists for a backend that is down or unreachable. A missing
// object and an oversized prefix are normal answers from the backend: counting
// them would open the breaker over a handful of dangling URLs and take down
// uploads for the whole bucket. They are reported to the caller either way;
// they just do not count here.
//
// context.Canceled is not one either: it means whoever asked stopped waiting,
// and says nothing about the backend — five visitors closing a tab mid-image
// would otherwise open the breaker for everyone. A DeadlineExceeded can be
// either, so it is decided in the wrapper, which knows whose deadline ran out
// (see execute).
func IsBackendFailure(err error) bool {
	return err != nil && !storage.IsNotFound(err) && !storage.IsListingTooLarge(err) &&
		!errors.Is(err, context.Canceled)
}

// abandoned marks an error the caller brought on itself by giving up — its own
// context was cancelled or hit its deadline. It only travels from the wrapped
// call to the breaker's IsSuccessful and is unwrapped again before the caller
// sees the error.
type abandoned struct{ err error }

func (a *abandoned) Error() string { return a.err.Error() }
func (a *abandoned) Unwrap() error { return a.err }

// callerGaveUp reports whether err is the caller's own context ending rather
// than the backend failing.
//
// The jay client wraps ctx.Err() into what it returns, hence errors.Is. A
// DeadlineExceeded with the caller's context still alive came from a deadline
// below it — S3's per-operation timeout, say — and that one IS the backend
// being too slow, so it still counts.
func callerGaveUp(ctx context.Context, err error) bool {
	if ctx.Err() == nil {
		return false
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// DefaultSettings returns default circuit breaker settings
func DefaultSettings(name string) CircuitBreakerSettings {
	return CircuitBreakerSettings{
		Name:        name,
		MaxRequests: 3,
		Interval:    10 * time.Second,
		Timeout:     30 * time.Second,
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			// Trip after 5 consecutive failures
			return counts.ConsecutiveFailures >= 5
		},
		IsSuccessful: func(err error) bool { return !IsBackendFailure(err) },
	}
}

// StorageBackend wraps a storage backend with circuit breaker protection
type StorageBackend struct {
	backend storage.StorageBackend
	cb      *gobreaker.CircuitBreaker
}

// NewStorageBackend creates a new circuit breaker wrapped storage backend
func NewStorageBackend(backend storage.StorageBackend, settings CircuitBreakerSettings) *StorageBackend {
	// The default lives here and not only in DefaultSettings because a caller
	// can build CircuitBreakerSettings{} by hand: with IsSuccessful nil,
	// gobreaker counts EVERY non-nil error as a failure, which would let plain
	// "object not found" responses trip the breaker for the whole bucket.
	isSuccessful := settings.IsSuccessful
	if isSuccessful == nil {
		isSuccessful = func(err error) bool { return !IsBackendFailure(err) }
	}

	cbSettings := gobreaker.Settings{
		Name:          settings.Name,
		MaxRequests:   settings.MaxRequests,
		Interval:      settings.Interval,
		Timeout:       settings.Timeout,
		ReadyToTrip:   settings.ReadyToTrip,
		OnStateChange: settings.OnStateChange,
		// A caller that gave up is never the backend's fault, whatever the
		// configured IsSuccessful says. gobreaker v1 has no "neither" outcome,
		// so it counts as a success: that resets the consecutive-failure run,
		// which is the lesser evil next to tripping on cancellations.
		IsSuccessful: func(err error) bool {
			if _, ok := errors.AsType[*abandoned](err); ok {
				return true
			}
			return isSuccessful(err)
		},
	}

	return &StorageBackend{
		backend: backend,
		cb:      gobreaker.NewCircuitBreaker(cbSettings),
	}
}

// execute runs fn behind the circuit breaker and returns its result already
// typed.
//
// gobreaker.Execute works in terms of `any`, so without this every wrapper
// repeated the same unchecked type assertion — seven separate chances to panic
// if one of them ever returned something else. Here the assertion happens once,
// and it is checked.
//
// ctx is the caller's context, consulted only to classify a failure: an error
// that is the caller giving up goes to the breaker marked as abandoned, so it
// is not held against the backend.
//
// When the breaker itself refuses the call — open, or half-open and already
// probing — the error wraps storage.ErrStorageUnavailable, so callers can tell
// "the backend is being shielded, try later" (a 503) from a failure of their
// request, without importing gobreaker.
func (s *StorageBackend) execute[T any](ctx context.Context, fn func() (T, error)) (T, error) {
	var zero T
	res, err := s.cb.Execute(func() (any, error) {
		v, err := fn()
		if err != nil {
			if callerGaveUp(ctx, err) {
				return nil, &abandoned{err: err}
			}
			return nil, err
		}
		return v, nil
	})
	if err != nil {
		if a, ok := errors.AsType[*abandoned](err); ok {
			return zero, a.err
		}
		if errors.Is(err, gobreaker.ErrOpenState) || errors.Is(err, gobreaker.ErrTooManyRequests) {
			return zero, fmt.Errorf("%w: %s: %w", storage.ErrStorageUnavailable, s.cb.Name(), err)
		}
		return zero, err
	}
	v, ok := res.(T)
	if !ok {
		return zero, fmt.Errorf("circuitbreaker: expected %T from breaker, got %T", zero, res)
	}
	return v, nil
}

// executeVoid runs an operation with no return value behind the breaker.
func (s *StorageBackend) executeVoid(ctx context.Context, fn func() error) error {
	_, err := s.execute(ctx, func() (struct{}, error) {
		return struct{}{}, fn()
	})
	return err
}

// Store stores an image with circuit breaker protection
func (s *StorageBackend) Store(ctx context.Context, key string, data io.Reader, metadata *storage.ImageMetadata) error {
	return s.executeVoid(ctx, func() error {
		return s.backend.Store(ctx, key, data, metadata)
	})
}

// Stat returns an object's metadata with circuit breaker protection
func (s *StorageBackend) Stat(ctx context.Context, key string) (*storage.ImageMetadata, error) {
	return s.execute(ctx, func() (*storage.ImageMetadata, error) { return s.backend.Stat(ctx, key) })
}

// Retrieve retrieves an image with circuit breaker protection
func (s *StorageBackend) Retrieve(ctx context.Context, key string) (io.ReadCloser, *storage.ImageMetadata, error) {
	r, err := s.execute(ctx, func() (*retrieveResult, error) {
		reader, metadata, err := s.backend.Retrieve(ctx, key)
		if err != nil {
			return nil, err
		}
		return &retrieveResult{reader: reader, metadata: metadata}, nil
	})
	if err != nil {
		return nil, nil, err
	}
	return r.reader, r.metadata, nil
}

type retrieveResult struct {
	reader   io.ReadCloser
	metadata *storage.ImageMetadata
}

// Delete deletes an image with circuit breaker protection
func (s *StorageBackend) Delete(ctx context.Context, key string) error {
	return s.executeVoid(ctx, func() error { return s.backend.Delete(ctx, key) })
}

// Exists checks if an image exists with circuit breaker protection
func (s *StorageBackend) Exists(ctx context.Context, key string) (bool, error) {
	return s.execute(ctx, func() (bool, error) { return s.backend.Exists(ctx, key) })
}

// Health checks the health of the storage with circuit breaker protection
func (s *StorageBackend) Health(ctx context.Context) error {
	return s.executeVoid(ctx, func() error { return s.backend.Health(ctx) })
}

// GetStats returns storage statistics with circuit breaker protection
func (s *StorageBackend) GetStats(ctx context.Context) (*storage.StorageStats, error) {
	return s.execute(ctx, func() (*storage.StorageStats, error) { return s.backend.GetStats(ctx) })
}

// List lists objects with circuit breaker protection
func (s *StorageBackend) List(ctx context.Context, prefix string) ([]storage.ListResult, error) {
	return s.execute(ctx, func() ([]storage.ListResult, error) { return s.backend.List(ctx, prefix) })
}

// ListPage lists one page of objects with circuit breaker protection.
//
// The wrapper sits between the registry and the real backend, so if it did not
// forward this the capability would be invisible to every caller: a type
// assertion for storage.PagedLister would fail even when the backend
// underneath implements it.
func (s *StorageBackend) ListPage(ctx context.Context, opts storage.ListOptions) (*storage.ListPage, error) {
	pager, ok := s.backend.(storage.PagedLister)
	if !ok {
		return nil, fmt.Errorf("%w: %T cannot list by page", storage.ErrUnsupportedOperation, s.backend)
	}
	return s.execute(ctx, func() (*storage.ListPage, error) { return pager.ListPage(ctx, opts) })
}

// WithBucket returns a new storage backend with a different bucket
// Note: The circuit breaker state is shared across all bucket instances
// Only works if the underlying backend implements BucketAware interface
func (s *StorageBackend) WithBucket(bucket string) storage.StorageBackend {
	if ba, ok := s.backend.(storage.BucketAware); ok {
		return &StorageBackend{
			backend: ba.WithBucket(bucket),
			cb:      s.cb, // Share the same circuit breaker
		}
	}
	// If backend doesn't support bucket switching, return self
	return s
}

// GetCurrentBucket returns the current bucket name
// Returns empty string if the underlying backend doesn't implement BucketAware
func (s *StorageBackend) GetCurrentBucket() string {
	if ba, ok := s.backend.(storage.BucketAware); ok {
		return ba.GetCurrentBucket()
	}
	return ""
}

// State returns the current state of the circuit breaker
func (s *StorageBackend) State() gobreaker.State {
	return s.cb.State()
}

// Counts returns the current counts of the circuit breaker
func (s *StorageBackend) Counts() gobreaker.Counts {
	return s.cb.Counts()
}

// IsOpen returns true if the circuit breaker is open
func (s *StorageBackend) IsOpen() bool {
	return s.cb.State() == gobreaker.StateOpen
}

// Drain forwards to the wrapped backend when it has in-flight work to wait for
// (storage.Drainer), and is a no-op otherwise.
//
// Like ListPage, this is a capability the registry can only see through the
// wrapper: without forwarding, Registry.CloseAll found nothing to wait for and
// a shutdown cut ReplicatedStorage's async replications short. It bypasses the
// breaker on purpose — shutdown has to wait for that work even with the
// breaker open.
func (s *StorageBackend) Drain(ctx context.Context) error {
	if d, ok := s.backend.(storage.Drainer); ok {
		return d.Drain(ctx)
	}
	return nil
}

// Close forwards to the wrapped backend when it holds something to release
// (storage.Closer) — jay's connection pool, a replicated backend's pending
// work — and is a no-op otherwise. It bypasses the breaker for the same reason
// Drain does.
func (s *StorageBackend) Close(ctx context.Context) error {
	if c, ok := s.backend.(storage.Closer); ok {
		return c.Close(ctx)
	}
	return nil
}

// Compile-time proof that the wrapper forwards the capabilities the registry
// discovers by type assertion. Losing one here would make every backend look
// like it lacks it, since the registry hands out this wrapper and not the
// backend itself.
var (
	_ storage.PagedLister = (*StorageBackend)(nil)
	_ storage.Closer      = (*StorageBackend)(nil)
	_ storage.Drainer     = (*StorageBackend)(nil)
)

// StateName returns the breaker state as a word: "closed", "open" or
// "half-open".
//
// It exists so that callers can report the state without importing gobreaker
// just to name an enum — the ops screen needs the word, not the type.
func (s *StorageBackend) StateName() string {
	return s.cb.State().String()
}
