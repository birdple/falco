// Package circuitbreaker provides a circuit breaker wrapper for storage backends.
package circuitbreaker

import (
	"context"
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
// A missing object is NOT a backend failure: the backend answered, correctly,
// that the key does not exist. Counting it as one is how a screen listing a few
// images whose rows outlived their bytes trips the breaker — and an open
// breaker rejects every other operation on the bucket, uploads included.
//
// That is not hypothetical. With the default of 5 consecutive failures, a
// client rendering a handful of dangling image URLs opened the breaker and the
// next upload died with "circuit breaker is open" for the whole 30 s timeout —
// a read problem taking writes down with it, and a self-sustaining one: failed
// uploads leave more dangling URLs, which trip the breaker again.
//
// The breaker exists for a backend that is down or unreachable. `ErrImageNotFound`
// is a normal answer and is reported to the caller either way; it just does not
// count here.
func IsBackendFailure(err error) bool {
	return err != nil && !storage.IsNotFound(err)
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
	// El default vive acá y no sólo en `DefaultSettings` porque un llamador
	// puede construir `CircuitBreakerSettings{}` a mano: con `IsSuccessful` en
	// nil, gobreaker cuenta como fallo **todo** error no nulo, y volvería el
	// "no existe" a tumbar el bucket entero.
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
		IsSuccessful:  isSuccessful,
	}

	return &StorageBackend{
		backend: backend,
		cb:      gobreaker.NewCircuitBreaker(cbSettings),
	}
}

// execute corre fn detrás del circuit breaker y devuelve su resultado ya
// tipado.
//
// Es un método con su propio type param (Go 1.27). gobreaker.Execute trabaja
// con `any`, así que sin esto cada wrapper repetía la misma aserción de tipo
// sin comprobar — siete oportunidades de paniquear si alguna devolvía otra
// cosa. Aquí la aserción es una sola y sí se comprueba.
func (s *StorageBackend) execute[T any](fn func() (T, error)) (T, error) {
	var zero T
	res, err := s.cb.Execute(func() (any, error) {
		v, err := fn()
		if err != nil {
			return nil, err
		}
		return v, nil
	})
	if err != nil {
		return zero, err
	}
	v, ok := res.(T)
	if !ok {
		return zero, fmt.Errorf("circuitbreaker: expected %T from breaker, got %T", zero, res)
	}
	return v, nil
}

// executeVoid corre una operación sin valor de retorno detrás del breaker.
func (s *StorageBackend) executeVoid(fn func() error) error {
	_, err := s.cb.Execute(func() (any, error) {
		return nil, fn()
	})
	return err
}

// Store stores an image with circuit breaker protection
func (s *StorageBackend) Store(ctx context.Context, key string, data io.Reader, metadata *storage.ImageMetadata) error {
	return s.executeVoid(func() error {
		return s.backend.Store(ctx, key, data, metadata)
	})
}

// Retrieve retrieves an image with circuit breaker protection
func (s *StorageBackend) Retrieve(ctx context.Context, key string) (io.ReadCloser, *storage.ImageMetadata, error) {
	r, err := s.execute(func() (*retrieveResult, error) {
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
	return s.executeVoid(func() error { return s.backend.Delete(ctx, key) })
}

// Exists checks if an image exists with circuit breaker protection
func (s *StorageBackend) Exists(ctx context.Context, key string) (bool, error) {
	return s.execute(func() (bool, error) { return s.backend.Exists(ctx, key) })
}

// Health checks the health of the storage with circuit breaker protection
func (s *StorageBackend) Health(ctx context.Context) error {
	return s.executeVoid(func() error { return s.backend.Health(ctx) })
}

// GetStats returns storage statistics with circuit breaker protection
func (s *StorageBackend) GetStats(ctx context.Context) (*storage.StorageStats, error) {
	return s.execute(func() (*storage.StorageStats, error) { return s.backend.GetStats(ctx) })
}

// List lists objects with circuit breaker protection
func (s *StorageBackend) List(ctx context.Context, prefix string) ([]storage.ListResult, error) {
	return s.execute(func() ([]storage.ListResult, error) { return s.backend.List(ctx, prefix) })
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
