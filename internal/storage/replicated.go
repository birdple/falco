package storage

import (
	"context"
	"io"

	"github.com/birdple/falco/internal/pkg/logger"
)

// ReplicatedStorage wraps a primary and secondary StorageBackend
// to provide replication with configurable modes.
type ReplicatedStorage struct {
	primary   StorageBackend
	secondary StorageBackend
	mode      ReplicationMode
}

// NewReplicatedStorage creates a new replicated storage wrapper.
func NewReplicatedStorage(primary, secondary StorageBackend, mode ReplicationMode) *ReplicatedStorage {
	return &ReplicatedStorage{
		primary:   primary,
		secondary: secondary,
		mode:      mode,
	}
}

// Store writes data to the primary backend and replicates to the secondary
// based on the configured replication mode.
func (rs *ReplicatedStorage) Store(ctx context.Context, key string, data io.Reader, metadata *ImageMetadata) error {
	// For sync and async modes, we need to buffer the data so both backends can read it
	buf, err := io.ReadAll(data)
	if err != nil {
		return err
	}

	// Always write to primary first
	if err := rs.primary.Store(ctx, key, newBytesReader(buf), metadata); err != nil {
		return err
	}

	switch rs.mode {
	case ReplicationSync:
		if err := rs.secondary.Store(ctx, key, newBytesReader(buf), metadata); err != nil {
			logger.Error().Err(err).Str("key", key).Msg("Failed to replicate to secondary storage (sync)")
			return err
		}
	case ReplicationAsync:
		go func() {
			if err := rs.secondary.Store(context.Background(), key, newBytesReader(buf), metadata); err != nil {
				logger.Error().Err(err).Str("key", key).Msg("Failed to replicate to secondary storage (async)")
			}
		}()
	case ReplicationReadFallback:
		// In read-fallback mode, we only write to the primary
	}

	return nil
}

// Retrieve reads from the primary backend. In read-fallback mode,
// falls back to the secondary if the primary returns not found.
func (rs *ReplicatedStorage) Retrieve(ctx context.Context, key string) (io.ReadCloser, *ImageMetadata, error) {
	reader, metadata, err := rs.primary.Retrieve(ctx, key)
	if err == nil {
		return reader, metadata, nil
	}

	if rs.mode == ReplicationReadFallback && IsNotFound(err) {
		logger.Debug().Str("key", key).Msg("Primary not found, falling back to secondary")
		return rs.secondary.Retrieve(ctx, key)
	}

	return nil, nil, err
}

// Delete removes from the primary and, in sync/async modes, from the secondary.
func (rs *ReplicatedStorage) Delete(ctx context.Context, key string) error {
	if err := rs.primary.Delete(ctx, key); err != nil {
		return err
	}

	switch rs.mode {
	case ReplicationSync:
		if err := rs.secondary.Delete(ctx, key); err != nil && !IsNotFound(err) {
			logger.Error().Err(err).Str("key", key).Msg("Failed to delete from secondary storage (sync)")
			return err
		}
	case ReplicationAsync:
		go func() {
			if err := rs.secondary.Delete(context.Background(), key); err != nil && !IsNotFound(err) {
				logger.Error().Err(err).Str("key", key).Msg("Failed to delete from secondary storage (async)")
			}
		}()
	case ReplicationReadFallback:
		// Best-effort delete from secondary
		go func() {
			_ = rs.secondary.Delete(context.Background(), key)
		}()
	}

	return nil
}

// Exists checks the primary, falls back to secondary in read-fallback mode.
func (rs *ReplicatedStorage) Exists(ctx context.Context, key string) (bool, error) {
	exists, err := rs.primary.Exists(ctx, key)
	if err == nil && exists {
		return true, nil
	}

	if rs.mode == ReplicationReadFallback {
		return rs.secondary.Exists(ctx, key)
	}

	return exists, err
}

// List returns results from the primary backend.
func (rs *ReplicatedStorage) List(ctx context.Context, prefix string) ([]ListResult, error) {
	return rs.primary.List(ctx, prefix)
}

// Health checks both backends. Returns error if primary is unhealthy.
func (rs *ReplicatedStorage) Health(ctx context.Context) error {
	if err := rs.primary.Health(ctx); err != nil {
		return err
	}
	// Log secondary health issues but don't fail the health check
	if err := rs.secondary.Health(ctx); err != nil {
		logger.Warn().Err(err).Msg("Secondary storage health check failed")
	}
	return nil
}

// GetStats returns stats from the primary backend.
func (rs *ReplicatedStorage) GetStats(ctx context.Context) (*StorageStats, error) {
	return rs.primary.GetStats(ctx)
}

// WithBucket delegates to both backends if they support it.
func (rs *ReplicatedStorage) WithBucket(bucket string) StorageBackend {
	primary := rs.primary
	secondary := rs.secondary

	if ba, ok := rs.primary.(BucketAware); ok {
		primary = ba.WithBucket(bucket)
	}
	if ba, ok := rs.secondary.(BucketAware); ok {
		secondary = ba.WithBucket(bucket)
	}

	return &ReplicatedStorage{
		primary:   primary,
		secondary: secondary,
		mode:      rs.mode,
	}
}

// GetCurrentBucket returns the current bucket from the primary backend.
func (rs *ReplicatedStorage) GetCurrentBucket() string {
	if ba, ok := rs.primary.(BucketAware); ok {
		return ba.GetCurrentBucket()
	}
	return ""
}

// Primary returns the underlying primary backend.
func (rs *ReplicatedStorage) Primary() StorageBackend {
	return rs.primary
}

// Secondary returns the underlying secondary backend.
func (rs *ReplicatedStorage) Secondary() StorageBackend {
	return rs.secondary
}

// Mode returns the replication mode.
func (rs *ReplicatedStorage) Mode() ReplicationMode {
	return rs.mode
}

// newBytesReader creates a new bytes reader (helper to avoid import in callers)
func newBytesReader(b []byte) io.Reader {
	return &bytesReader{data: b, pos: 0}
}

type bytesReader struct {
	data []byte
	pos  int
}

func (r *bytesReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}
