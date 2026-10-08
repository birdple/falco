package storage

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// slowBackend lets the test control when a Store/Delete finishes, so the
// "replica in flight" state can be observed.
type slowBackend struct {
	release chan struct{}
	stores  atomic.Int64
	deletes atomic.Int64
}

func newSlowBackend() *slowBackend {
	return &slowBackend{release: make(chan struct{})}
}

func (b *slowBackend) Store(_ context.Context, _ string, data io.Reader, _ *ImageMetadata) error {
	<-b.release
	_, _ = io.Copy(io.Discard, data)
	b.stores.Add(1)
	return nil
}

func (b *slowBackend) Delete(context.Context, string) error {
	<-b.release
	b.deletes.Add(1)
	return nil
}

func (b *slowBackend) Retrieve(context.Context, string) (io.ReadCloser, *ImageMetadata, error) {
	return nil, nil, ErrImageNotFound
}
func (b *slowBackend) Stat(context.Context, string) (*ImageMetadata, error) {
	return nil, ErrImageNotFound
}
func (b *slowBackend) Exists(context.Context, string) (bool, error) { return false, nil }
func (b *slowBackend) Health(context.Context) error                 { return nil }
func (b *slowBackend) GetStats(context.Context) (*StorageStats, error) {
	return &StorageStats{}, nil
}
func (b *slowBackend) List(context.Context, string) ([]ListResult, error) { return nil, nil }

// fastBackend is a primary that answers immediately.
type fastBackend struct{ stores atomic.Int64 }

func (b *fastBackend) Store(_ context.Context, _ string, data io.Reader, _ *ImageMetadata) error {
	_, _ = io.Copy(io.Discard, data)
	b.stores.Add(1)
	return nil
}
func (b *fastBackend) Delete(context.Context, string) error { return nil }
func (b *fastBackend) Retrieve(context.Context, string) (io.ReadCloser, *ImageMetadata, error) {
	return nil, nil, ErrImageNotFound
}
func (b *fastBackend) Stat(context.Context, string) (*ImageMetadata, error) {
	return nil, ErrImageNotFound
}
func (b *fastBackend) Exists(context.Context, string) (bool, error) { return false, nil }
func (b *fastBackend) Health(context.Context) error                 { return nil }
func (b *fastBackend) GetStats(context.Context) (*StorageStats, error) {
	return &StorageStats{}, nil
}
func (b *fastBackend) List(context.Context, string) ([]ListResult, error) { return nil, nil }

// TestReplicatedStorage_CloseWaitsForAsyncStore: shutdown really waits for
// the in-flight replica; a fixed sleep does not know whether it finished.
func TestReplicatedStorage_CloseWaitsForAsyncStore(t *testing.T) {
	primary := &fastBackend{}
	backup := newSlowBackend()

	rs := NewReplicatedStorage(primary, []BackupTarget{
		{Backend: backup, Mode: ReplicationAsync},
	})

	require.NoError(t, rs.Store(t.Context(), "k", strings.NewReader("data"), &ImageMetadata{}))
	assert.Equal(t, int64(1), primary.stores.Load(), "the primary is written inline")

	// The replica is still in flight: Close with an already expired context
	// has to report it instead of pretending it went fine.
	expired, cancel := context.WithTimeout(t.Context(), time.Millisecond)
	defer cancel()
	assert.Error(t, rs.Close(expired), "Close must not report success with replicas in flight")
	assert.Equal(t, int64(0), backup.stores.Load())

	// Release the replica: now Close waits and returns nil.
	close(backup.release)
	require.NoError(t, rs.Close(t.Context()))
	assert.Equal(t, int64(1), backup.stores.Load(), "Close waited for the replica to finish")
}

// TestReplicatedStorage_CloseWaitsForAsyncDelete covers the same on the delete
// side. The read-fallback target is deleted synchronously instead (see
// TestReplicatedStorage_ReadFallbackDeleteIsVisibleImmediately), so it is done
// by the time Delete returns.
func TestReplicatedStorage_CloseWaitsForAsyncDelete(t *testing.T) {
	primary := &fastBackend{}
	asyncBackup := newSlowBackend()
	fallbackBackup := newSlowBackend()
	close(fallbackBackup.release)

	rs := NewReplicatedStorage(primary, []BackupTarget{
		{Backend: asyncBackup, Mode: ReplicationAsync},
		{Backend: fallbackBackup, Mode: ReplicationReadFallback},
	})

	require.NoError(t, rs.Delete(t.Context(), "k"))
	assert.Equal(t, int64(1), fallbackBackup.deletes.Load(),
		"the read-fallback delete happens before Delete returns")

	close(asyncBackup.release)
	require.NoError(t, rs.Close(t.Context()))

	assert.Equal(t, int64(1), asyncBackup.deletes.Load())
}

// newFS returns a filesystem backend in a fresh temp dir.
func newFS(t *testing.T) *FilesystemStorage {
	t.Helper()
	fs, err := NewFilesystemStorage(t.TempDir())
	require.NoError(t, err)
	return fs
}

// TestReplicatedStorage_ReadFallbackDeleteIsVisibleImmediately: Retrieve falls
// back to read-fallback targets whenever the primary misses, so when their
// delete was asynchronous a deleted image went on being served from the backup
// until the goroutine got to it — or forever, if it failed.
func TestReplicatedStorage_ReadFallbackDeleteIsVisibleImmediately(t *testing.T) {
	primary, fallback := newFS(t), newFS(t)
	rs := NewReplicatedStorage(primary, []BackupTarget{{Backend: fallback, Mode: ReplicationReadFallback}})

	// The fallback holds its own copy, as an older backup would.
	require.NoError(t, primary.Store(t.Context(), "k", strings.NewReader("data"), &ImageMetadata{ID: "k"}))
	require.NoError(t, fallback.Store(t.Context(), "k", strings.NewReader("data"), &ImageMetadata{ID: "k"}))

	require.NoError(t, rs.Delete(t.Context(), "k"))

	// No Drain/Close: the delete must already be visible.
	_, _, err := rs.Retrieve(t.Context(), "k")
	assert.ErrorIs(t, err, ErrImageNotFound)
	_, err = rs.Stat(t.Context(), "k")
	assert.ErrorIs(t, err, ErrImageNotFound)
}

// failingDeleteBackend is a fastBackend whose Delete fails.
type failingDeleteBackend struct{ fastBackend }

func (*failingDeleteBackend) Delete(context.Context, string) error {
	return errors.New("backup unreachable")
}

// notFoundPrimary is a fastBackend whose Delete reports the key missing, as jay
// does.
type notFoundPrimary struct {
	fastBackend
}

func (*notFoundPrimary) Delete(context.Context, string) error { return ErrImageNotFound }

// A read-fallback delete that fails is reported — the image is still served —
// and a retry, which finds the primary already empty, still reaches the backup.
func TestReplicatedStorage_ReadFallbackDeleteFailureIsReported(t *testing.T) {
	rs := NewReplicatedStorage(&fastBackend{}, []BackupTarget{
		{Backend: &failingDeleteBackend{}, Mode: ReplicationReadFallback},
	})
	require.Error(t, rs.Delete(t.Context(), "k"))

	fallback := newSlowBackend()
	close(fallback.release)
	retry := NewReplicatedStorage(&notFoundPrimary{}, []BackupTarget{
		{Backend: fallback, Mode: ReplicationReadFallback},
	})
	require.NoError(t, retry.Delete(t.Context(), "k"))
	assert.Equal(t, int64(1), fallback.deletes.Load(), "the retry reached the read-fallback copy")
}

// TestReplicatedStorage_StatFallsBack mirrors Retrieve: a key the primary lacks
// is looked up in read-fallback targets.
func TestReplicatedStorage_StatFallsBack(t *testing.T) {
	primary, fallback := newFS(t), newFS(t)
	require.NoError(t, fallback.Store(t.Context(), "k", strings.NewReader("data"), &ImageMetadata{ID: "k", OwnerID: "o"}))

	rs := NewReplicatedStorage(primary, []BackupTarget{{Backend: fallback, Mode: ReplicationReadFallback}})
	meta, err := rs.Stat(t.Context(), "k")
	require.NoError(t, err)
	assert.Equal(t, "o", meta.OwnerID)

	_, err = rs.Stat(t.Context(), "missing")
	assert.ErrorIs(t, err, ErrImageNotFound)
}

// stampingBackend writes into the metadata it is handed, as every real backend
// does (jay sets ETag and StorageKey, the filesystem Size and CreatedAt).
type stampingBackend struct {
	fastBackend
	etag string
}

func (b *stampingBackend) Store(ctx context.Context, key string, data io.Reader, m *ImageMetadata) error {
	m.ETag = b.etag
	m.StorageKey = key
	return b.fastBackend.Store(ctx, key, data, m)
}

// TestReplicatedStorage_BackupsGetTheirOwnMetadata is a data race under -race
// when async backups share the caller's *ImageMetadata: they write into it
// while the caller reads it after Store returns. Sync backups overwrote the
// primary's values in it too.
func TestReplicatedStorage_BackupsGetTheirOwnMetadata(t *testing.T) {
	rs := NewReplicatedStorage(&stampingBackend{etag: "primary"}, []BackupTarget{
		{Backend: &stampingBackend{etag: "sync"}, Mode: ReplicationSync},
		{Backend: &stampingBackend{etag: "async-1"}, Mode: ReplicationAsync},
		{Backend: &stampingBackend{etag: "async-2"}, Mode: ReplicationAsync},
	})

	meta := &ImageMetadata{ID: "k"}
	require.NoError(t, rs.Store(t.Context(), "k", strings.NewReader("data"), meta))
	for range 100 {
		assert.Equal(t, "primary", meta.ETag, "the caller sees what the primary wrote")
	}
	require.NoError(t, rs.Close(t.Context()))
	assert.Equal(t, "primary", meta.ETag)
}

// TestReplicatedStorage_WithBucketSharesInFlightState: the registry only ever
// drains the registered instance, while requests go through WithBucket copies.
// With a WaitGroup per copy, replications started through a copy were never
// waited for.
func TestReplicatedStorage_WithBucketSharesInFlightState(t *testing.T) {
	backup := newSlowBackend()
	rs := NewReplicatedStorage(&fastBackend{}, []BackupTarget{{Backend: backup, Mode: ReplicationAsync}})

	copied := rs.WithBucket("other")
	require.NoError(t, copied.Store(t.Context(), "k", strings.NewReader("data"), &ImageMetadata{}))

	expired, cancel := context.WithTimeout(t.Context(), time.Millisecond)
	defer cancel()
	assert.Error(t, rs.Drain(expired), "the original sees the copy's replication in flight")

	close(backup.release)
	require.NoError(t, rs.Drain(t.Context()))
	assert.Equal(t, int64(1), backup.stores.Load())
}

// TestReplicatedStorage_AsyncReplicationIsBounded: every in-flight replication
// pins a full copy of the object, so a stalled backup used to grow memory
// without limit. Past the bound, replications are dropped and counted.
func TestReplicatedStorage_AsyncReplicationIsBounded(t *testing.T) {
	backup := newSlowBackend()
	rs := NewReplicatedStorage(&fastBackend{}, []BackupTarget{{Backend: backup, Mode: ReplicationAsync}})

	const extra = 5
	for range maxAsyncReplications + extra {
		require.NoError(t, rs.Store(t.Context(), "k", strings.NewReader("data"), &ImageMetadata{}),
			"the primary write still succeeds when its replication is dropped")
	}
	assert.Equal(t, int64(extra), rs.DroppedReplications())

	close(backup.release)
	require.NoError(t, rs.Drain(t.Context()))
	assert.Equal(t, int64(maxAsyncReplications), backup.stores.Load())

	// Slots are returned: with the backup answering again, nothing is dropped.
	require.NoError(t, rs.Store(t.Context(), "k", strings.NewReader("data"), &ImageMetadata{}))
	require.NoError(t, rs.Drain(t.Context()))
	assert.Equal(t, int64(extra), rs.DroppedReplications())
}

// TestRegistry_CloseAllDrainsBeforeClosing: a bucket's backend can be another
// bucket's async backup target. Closing it before that bucket drained would
// close the connection its replications are still writing through.
func TestRegistry_CloseAllDrainsBeforeClosing(t *testing.T) {
	shared := &closeTrackingBackend{slowBackend: newSlowBackend()}
	rs := NewReplicatedStorage(&fastBackend{}, []BackupTarget{{Backend: shared, Mode: ReplicationAsync}})

	reg := NewRegistry(shared)
	reg.Register("replicated", rs)
	require.NoError(t, rs.Store(t.Context(), "k", strings.NewReader("data"), &ImageMetadata{}))

	go func() {
		time.Sleep(20 * time.Millisecond)
		close(shared.release)
	}()
	assert.Empty(t, reg.CloseAll(t.Context()))
	assert.Equal(t, int64(1), shared.storesBeforeClose.Load(),
		"the replication finished before the backend it writes to was closed")
}

// closeTrackingBackend records how many stores had completed when it was first
// closed.
type closeTrackingBackend struct {
	*slowBackend
	closed            atomic.Bool
	storesBeforeClose atomic.Int64
}

func (b *closeTrackingBackend) Close(context.Context) error {
	if b.closed.CompareAndSwap(false, true) {
		b.storesBeforeClose.Store(b.stores.Load())
	}
	return nil
}

// TestRegistry_CloseAll checks that the registry waits for backends with work
// in flight and ignores those without.
func TestRegistry_CloseAll(t *testing.T) {
	backup := newSlowBackend()
	close(backup.release)

	rs := NewReplicatedStorage(&fastBackend{}, []BackupTarget{
		{Backend: backup, Mode: ReplicationAsync},
	})

	reg := NewRegistry(&fastBackend{}) // the default does not implement Closer
	reg.Register("replicated", rs)

	require.NoError(t, rs.Store(t.Context(), "k", strings.NewReader("data"), &ImageMetadata{}))

	failures := reg.CloseAll(t.Context())
	assert.Empty(t, failures)
	assert.Equal(t, int64(1), backup.stores.Load())
}
