package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	"github.com/birdple/falco/internal/pkg/logger"
	"github.com/birdple/falco/internal/pkg/metrics"
)

// BackupTarget pairs a storage backend with a replication mode.
type BackupTarget struct {
	Backend StorageBackend
	Mode    ReplicationMode
}

// maxAsyncReplications bounds how many asynchronous replications (stores and
// deletes to async targets) can be in flight at once, across every copy of a
// ReplicatedStorage made by WithBucket.
//
// Each one holds the whole object in memory until its backup answers, so
// without a bound a slow or hung backup turns every upload into a goroutine
// pinning its buffer, and the process grows until it dies. Past the bound a
// replication is dropped — counted, logged and exported as
// falco_storage_replications_dropped_total — rather than queued: the primary
// already has the object, and a stale backup that says so beats an OOM.
const maxAsyncReplications = 64

// replicationQueue is the in-flight state of async replication. It lives behind
// a pointer so that every copy WithBucket hands out shares it: draining the
// registered instance has to wait for the replications started through those
// copies too, and the bound has to hold across all of them.
type replicationQueue struct {
	// wg tracks in-flight replications. Without it, shutting the process down
	// mid-replication loses that copy silently and leaves the backup out of
	// sync with nothing to say so. Drain waits on it.
	wg      sync.WaitGroup
	slots   chan struct{}
	dropped atomic.Int64
}

// ReplicatedStorage wraps a primary StorageBackend with N backup targets,
// each with its own replication mode (sync, async, read-fallback).
type ReplicatedStorage struct {
	primary StorageBackend
	backups []BackupTarget
	async   *replicationQueue
}

// NewReplicatedStorage creates a new replicated storage wrapper.
func NewReplicatedStorage(primary StorageBackend, backups []BackupTarget) *ReplicatedStorage {
	return &ReplicatedStorage{
		primary: primary,
		backups: backups,
		async:   &replicationQueue{slots: make(chan struct{}, maxAsyncReplications)},
	}
}

// Drain waits for in-flight asynchronous replications to finish.
//
// Returns the context's error if it expires first: whatever was still in flight
// is lost, and whoever is shutting the process down needs to be able to find
// that out rather than assume it went fine.
func (rs *ReplicatedStorage) Drain(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		rs.async.wg.Wait()
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

// Close drains in-flight replications and then closes the primary and every
// backup that implements Closer.
//
// Backup targets are usually bucket backends in their own right, registered
// and closed under their own names as well. That is why Registry.CloseAll
// drains every backend before it closes any, and why Close implementations
// have to be idempotent.
func (rs *ReplicatedStorage) Close(ctx context.Context) error {
	if err := rs.Drain(ctx); err != nil {
		return err
	}
	var errs []error
	if c, ok := rs.primary.(Closer); ok {
		errs = append(errs, c.Close(ctx))
	}
	for _, t := range rs.backups {
		if c, ok := t.Backend.(Closer); ok {
			errs = append(errs, c.Close(ctx))
		}
	}
	return errors.Join(errs...)
}

// DroppedReplications returns how many async replications were dropped because
// maxAsyncReplications were already in flight.
func (rs *ReplicatedStorage) DroppedReplications() int64 {
	return rs.async.dropped.Load()
}

// replicate runs fn in the background if a slot is free, and drops it
// otherwise. fn reports its own errors: only it knows what it was doing.
func (rs *ReplicatedStorage) replicate(op, key string, backupIndex int, fn func()) {
	select {
	case rs.async.slots <- struct{}{}:
	default:
		dropped := rs.async.dropped.Add(1)
		metrics.Default().StorageReplicationsDropped.Inc()
		logger.Error().
			Str("operation", op).
			Str("key", key).
			Int("backup_index", backupIndex).
			Int64("dropped_total", dropped).
			Int("max_in_flight", maxAsyncReplications).
			Msg("Async replication dropped: too many in flight — the backup is now stale for this key")
		return
	}
	rs.async.wg.Go(func() {
		defer func() { <-rs.async.slots }()
		fn()
	})
}

// cloneMetadata returns a copy the callee can write into. Backends fill in the
// metadata they are handed (size, etag, storage key, created-at), so two of
// them sharing one pointer race — and an async replication sharing the
// caller's pointer races with the caller reading it after Store returns.
func cloneMetadata(m *ImageMetadata) *ImageMetadata {
	if m == nil {
		return nil
	}
	c := *m
	return &c
}

// Store writes data to the primary backend and replicates to backup targets
// based on each target's replication mode.
//
// The primary writes into the caller's metadata, as any backend does; each
// backup gets its own copy, taken once the primary is done with it.
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
			if err := target.Backend.Store(ctx, key, newBytesReader(buf), cloneMetadata(metadata)); err != nil {
				logger.Error().Err(err).
					Str("key", key).
					Int("backup_index", i).
					Msg("Failed to replicate to backup (sync)")
				return err
			}
		case ReplicationAsync:
			t, idx, meta := target, i, cloneMetadata(metadata)
			rs.replicate("store", key, idx, func() {
				if err := t.Backend.Store(context.Background(), key, newBytesReader(buf), meta); err != nil {
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

// Stat reads metadata from the primary, falling back to read-fallback targets
// when the primary does not have the key — the same rule as Retrieve, so the
// two agree on whether an object exists.
func (rs *ReplicatedStorage) Stat(ctx context.Context, key string) (*ImageMetadata, error) {
	metadata, err := rs.primary.Stat(ctx, key)
	if err == nil || !IsNotFound(err) {
		return metadata, err
	}

	for _, target := range rs.backups {
		if target.Mode == ReplicationReadFallback {
			if m, e := target.Backend.Stat(ctx, key); e == nil {
				return m, nil
			}
		}
	}

	return nil, err
}

// Delete removes from the primary and replicates deletion to backup targets.
//
// Read-fallback targets are deleted synchronously and before answering, unlike
// the other modes: Retrieve and Stat read from them whenever the primary
// misses, so a delete that only reached the primary would go on serving the
// deleted image from the backup. A failure there is returned, since the image
// is still being served. For the same reason a primary that no longer has the
// key does not end the delete: the read-fallback copy is the visible one, and
// this is what lets a retry after such a failure finish the job.
func (rs *ReplicatedStorage) Delete(ctx context.Context, key string) error {
	primaryErr := rs.primary.Delete(ctx, key)
	if primaryErr != nil && !IsNotFound(primaryErr) {
		return primaryErr
	}

	deletedFromFallback := false
	for i, target := range rs.backups {
		if target.Mode != ReplicationReadFallback {
			continue
		}
		err := target.Backend.Delete(ctx, key)
		switch {
		case err == nil:
			deletedFromFallback = true
		case IsNotFound(err):
		default:
			logger.Error().Err(err).
				Str("key", key).
				Int("backup_index", i).
				Msg("Failed to delete from read-fallback backup — it would still be served")
			return fmt.Errorf("delete %s from read-fallback backup %d: %w", key, i, err)
		}
	}

	if primaryErr != nil {
		if deletedFromFallback {
			return nil
		}
		return primaryErr
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
			rs.replicate("delete", key, idx, func() {
				if err := t.Backend.Delete(context.Background(), key); err != nil && !IsNotFound(err) {
					logger.Error().Err(err).
						Str("key", key).
						Int("backup_index", idx).
						Msg("Failed to delete from backup (async)")
				}
			})
		case ReplicationReadFallback:
			// Already deleted above, synchronously.
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

// ListPage lists one page from the primary backend.
//
// Listing never consults the backups: a read-fallback target holds the same
// objects the primary does, so paging across both would return duplicates and
// a cursor that means two different positions.
func (rs *ReplicatedStorage) ListPage(ctx context.Context, opts ListOptions) (*ListPage, error) {
	pager, ok := rs.primary.(PagedLister)
	if !ok {
		return nil, fmt.Errorf("%w: primary %T cannot list by page", ErrUnsupportedOperation, rs.primary)
	}
	return pager.ListPage(ctx, opts)
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
//
// The copy shares the async replication state with rs: CloseAll only reaches
// the registered instance, and it has to wait for replications started through
// any copy of it.
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
		async:   rs.async,
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
