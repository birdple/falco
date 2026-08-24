package storage

import (
	"context"
	"io"
	"sync"

	"github.com/birdple/falco/internal/pkg/logger"
)

// BackupTarget pairs a storage backend with a replication mode.
type BackupTarget struct {
	Backend StorageBackend
	Mode    ReplicationMode
}

// ReplicatedStorage wraps a primary StorageBackend with N backup targets,
// each with its own replication mode (sync, async, read-fallback).
type ReplicatedStorage struct {
	primary StorageBackend
	backups []BackupTarget
	// async cuenta las réplicas en vuelo. Sin él, apagar el proceso mientras
	// una réplica async está a medio camino la pierde en silencio y el backup
	// queda desincronizado sin que nada lo diga. Close lo espera.
	async sync.WaitGroup
}

// NewReplicatedStorage creates a new replicated storage wrapper.
func NewReplicatedStorage(primary StorageBackend, backups []BackupTarget) *ReplicatedStorage {
	return &ReplicatedStorage{
		primary: primary,
		backups: backups,
	}
}

// Close espera a que terminen las réplicas asíncronas en vuelo.
//
// Devuelve el error del contexto si se agota antes de que terminen: las que
// queden se perdieron, y quien apaga el proceso tiene que poder enterarse en
// vez de suponer que salió bien.
func (rs *ReplicatedStorage) Close(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		rs.async.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		logger.Error().Err(ctx.Err()).
			Msg("Shutdown timed out with async replications still in flight — backups may be stale")
		return ctx.Err()
	}
}

// Store writes data to the primary backend and replicates to backup targets
// based on each target's replication mode.
func (rs *ReplicatedStorage) Store(ctx context.Context, key string, data io.Reader, metadata *ImageMetadata) error {
	buf, err := io.ReadAll(data)
	if err != nil {
		return err
	}

	// Always write to primary first
	if err := rs.primary.Store(ctx, key, newBytesReader(buf), metadata); err != nil {
		return err
	}

	for i, target := range rs.backups {
		switch target.Mode {
		case ReplicationSync:
			if err := target.Backend.Store(ctx, key, newBytesReader(buf), metadata); err != nil {
				logger.Error().Err(err).
					Str("key", key).
					Int("backup_index", i).
					Msg("Failed to replicate to backup (sync)")
				return err
			}
		case ReplicationAsync:
			t, idx := target, i
			rs.async.Go(func() {
				if err := t.Backend.Store(context.Background(), key, newBytesReader(buf), metadata); err != nil {
					logger.Error().Err(err).
						Str("key", key).
						Int("backup_index", idx).
						Msg("Failed to replicate to backup (async)")
				}
			})
		case ReplicationReadFallback:
			// In read-fallback mode, we only write to the primary
		}
	}

	return nil
}

// Retrieve reads from the primary backend. Falls back to read-fallback targets
// if the primary returns not found.
func (rs *ReplicatedStorage) Retrieve(ctx context.Context, key string) (io.ReadCloser, *ImageMetadata, error) {
	reader, metadata, err := rs.primary.Retrieve(ctx, key)
	if err == nil {
		return reader, metadata, nil
	}

	if IsNotFound(err) {
		for _, target := range rs.backups {
			if target.Mode == ReplicationReadFallback {
				logger.Debug().Str("key", key).Msg("Primary not found, trying read-fallback backup")
				r, m, e := target.Backend.Retrieve(ctx, key)
				if e == nil {
					return r, m, nil
				}
			}
		}
	}

	return nil, nil, err
}

// Delete removes from the primary and replicates deletion to backup targets.
func (rs *ReplicatedStorage) Delete(ctx context.Context, key string) error {
	if err := rs.primary.Delete(ctx, key); err != nil {
		return err
	}

	for i, target := range rs.backups {
		switch target.Mode {
		case ReplicationSync:
			if err := target.Backend.Delete(ctx, key); err != nil && !IsNotFound(err) {
				logger.Error().Err(err).
					Str("key", key).
					Int("backup_index", i).
					Msg("Failed to delete from backup (sync)")
				return err
			}
		case ReplicationAsync:
			t, idx := target, i
			rs.async.Go(func() {
				if err := t.Backend.Delete(context.Background(), key); err != nil && !IsNotFound(err) {
					logger.Error().Err(err).
						Str("key", key).
						Int("backup_index", idx).
						Msg("Failed to delete from backup (async)")
				}
			})
		case ReplicationReadFallback:
			// Best-effort delete: el error se registra, no se propaga — el
			// backup de read-fallback no es fuente de verdad para el borrado.
			t, idx := target, i
			rs.async.Go(func() {
				if err := t.Backend.Delete(context.Background(), key); err != nil && !IsNotFound(err) {
					logger.Warn().Err(err).
						Str("key", key).
						Int("backup_index", idx).
						Msg("Failed to delete from read-fallback backup (best-effort)")
				}
			})
		}
	}

	return nil
}

// Exists checks the primary, falls back to read-fallback targets.
func (rs *ReplicatedStorage) Exists(ctx context.Context, key string) (bool, error) {
	exists, err := rs.primary.Exists(ctx, key)
	if err == nil && exists {
		return true, nil
	}

	for _, target := range rs.backups {
		if target.Mode == ReplicationReadFallback {
			if e, err2 := target.Backend.Exists(ctx, key); err2 == nil && e {
				return true, nil
			}
		}
	}

	return exists, err
}

// List returns results from the primary backend.
func (rs *ReplicatedStorage) List(ctx context.Context, prefix string) ([]ListResult, error) {
	return rs.primary.List(ctx, prefix)
}

// Health checks the primary and all backup backends.
// Returns error if primary is unhealthy; logs warnings for unhealthy backups.
func (rs *ReplicatedStorage) Health(ctx context.Context) error {
	if err := rs.primary.Health(ctx); err != nil {
		return err
	}
	for i, target := range rs.backups {
		if err := target.Backend.Health(ctx); err != nil {
			logger.Warn().Err(err).Int("backup_index", i).Msg("Backup health check failed")
		}
	}
	return nil
}

// GetStats returns stats from the primary backend.
func (rs *ReplicatedStorage) GetStats(ctx context.Context) (*StorageStats, error) {
	return rs.primary.GetStats(ctx)
}

// WithBucket delegates to all backends that support it.
func (rs *ReplicatedStorage) WithBucket(bucket string) StorageBackend {
	primary := rs.primary
	if ba, ok := rs.primary.(BucketAware); ok {
		primary = ba.WithBucket(bucket)
	}

	newBackups := make([]BackupTarget, len(rs.backups))
	for i, t := range rs.backups {
		backend := t.Backend
		if ba, ok := t.Backend.(BucketAware); ok {
			backend = ba.WithBucket(bucket)
		}
		newBackups[i] = BackupTarget{Backend: backend, Mode: t.Mode}
	}

	return &ReplicatedStorage{
		primary: primary,
		backups: newBackups,
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

// Backups returns the backup targets.
func (rs *ReplicatedStorage) Backups() []BackupTarget {
	return rs.backups
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
