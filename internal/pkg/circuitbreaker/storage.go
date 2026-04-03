// Package circuitbreaker provides a circuit breaker wrapper for storage backends.
package circuitbreaker

import (
	"context"
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
	}
}

// StorageBackend wraps a storage backend with circuit breaker protection
type StorageBackend struct {
	backend storage.StorageBackend
	cb      *gobreaker.CircuitBreaker
}

// NewStorageBackend creates a new circuit breaker wrapped storage backend
func NewStorageBackend(backend storage.StorageBackend, settings CircuitBreakerSettings) *StorageBackend {
	cbSettings := gobreaker.Settings{
		Name:          settings.Name,
		MaxRequests:   settings.MaxRequests,
		Interval:      settings.Interval,
		Timeout:       settings.Timeout,
		ReadyToTrip:   settings.ReadyToTrip,
		OnStateChange: settings.OnStateChange,
	}

	return &StorageBackend{
		backend: backend,
		cb:      gobreaker.NewCircuitBreaker(cbSettings),
	}
}

// Store stores an image with circuit breaker protection
func (s *StorageBackend) Store(ctx context.Context, key string, data io.Reader, metadata *storage.ImageMetadata) error {
	_, err := s.cb.Execute(func() (interface{}, error) {
		return nil, s.backend.Store(ctx, key, data, metadata)
	})
	return err
}

// Retrieve retrieves an image with circuit breaker protection
func (s *StorageBackend) Retrieve(ctx context.Context, key string) (io.ReadCloser, *storage.ImageMetadata, error) {
	result, err := s.cb.Execute(func() (interface{}, error) {
		reader, metadata, err := s.backend.Retrieve(ctx, key)
		if err != nil {
			return nil, err
		}
		return &retrieveResult{reader: reader, metadata: metadata}, nil
	})
	if err != nil {
		return nil, nil, err
	}
	r := result.(*retrieveResult)
	return r.reader, r.metadata, nil
}

type retrieveResult struct {
	reader   io.ReadCloser
	metadata *storage.ImageMetadata
}

// Delete deletes an image with circuit breaker protection
func (s *StorageBackend) Delete(ctx context.Context, key string) error {
	_, err := s.cb.Execute(func() (interface{}, error) {
		return nil, s.backend.Delete(ctx, key)
	})
	return err
}

// Exists checks if an image exists with circuit breaker protection
func (s *StorageBackend) Exists(ctx context.Context, key string) (bool, error) {
	result, err := s.cb.Execute(func() (interface{}, error) {
		exists, err := s.backend.Exists(ctx, key)
		return exists, err
	})
	if err != nil {
		return false, err
	}
	return result.(bool), nil
}

// Health checks the health of the storage with circuit breaker protection
func (s *StorageBackend) Health(ctx context.Context) error {
	_, err := s.cb.Execute(func() (interface{}, error) {
		return nil, s.backend.Health(ctx)
	})
	return err
}

// GetStats returns storage statistics with circuit breaker protection
func (s *StorageBackend) GetStats(ctx context.Context) (*storage.StorageStats, error) {
	result, err := s.cb.Execute(func() (interface{}, error) {
		return s.backend.GetStats(ctx)
	})
	if err != nil {
		return nil, err
	}
	return result.(*storage.StorageStats), nil
}

// List lists objects with circuit breaker protection
func (s *StorageBackend) List(ctx context.Context, prefix string) ([]storage.ListResult, error) {
	result, err := s.cb.Execute(func() (interface{}, error) {
		return s.backend.List(ctx, prefix)
	})
	if err != nil {
		return nil, err
	}
	return result.([]storage.ListResult), nil
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
