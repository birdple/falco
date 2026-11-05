package circuitbreaker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/sony/gobreaker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/birdple/falco/internal/storage"
)

// mockBackend implements storage.StorageBackend for testing
type mockBackend struct {
	mock.Mock
}

func (m *mockBackend) Store(ctx context.Context, key string, data io.Reader, metadata *storage.ImageMetadata) error {
	args := m.Called(ctx, key, data, metadata)
	return args.Error(0)
}

func (m *mockBackend) Retrieve(ctx context.Context, key string) (io.ReadCloser, *storage.ImageMetadata, error) {
	args := m.Called(ctx, key)
	var reader io.ReadCloser
	if args.Get(0) != nil {
		reader = args.Get(0).(io.ReadCloser)
	}
	var meta *storage.ImageMetadata
	if args.Get(1) != nil {
		meta = args.Get(1).(*storage.ImageMetadata)
	}
	return reader, meta, args.Error(2)
}

func (m *mockBackend) Stat(ctx context.Context, key string) (*storage.ImageMetadata, error) {
	args := m.Called(ctx, key)
	var meta *storage.ImageMetadata
	if args.Get(0) != nil {
		meta = args.Get(0).(*storage.ImageMetadata)
	}
	return meta, args.Error(1)
}

func (m *mockBackend) Delete(ctx context.Context, key string) error {
	args := m.Called(ctx, key)
	return args.Error(0)
}

func (m *mockBackend) Exists(ctx context.Context, key string) (bool, error) {
	args := m.Called(ctx, key)
	return args.Bool(0), args.Error(1)
}

func (m *mockBackend) Health(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

func (m *mockBackend) GetStats(ctx context.Context) (*storage.StorageStats, error) {
	args := m.Called(ctx)
	var stats *storage.StorageStats
	if args.Get(0) != nil {
		stats = args.Get(0).(*storage.StorageStats)
	}
	return stats, args.Error(1)
}

func (m *mockBackend) List(ctx context.Context, prefix string) ([]storage.ListResult, error) {
	args := m.Called(ctx, prefix)
	var results []storage.ListResult
	if args.Get(0) != nil {
		results = args.Get(0).([]storage.ListResult)
	}
	return results, args.Error(1)
}

func TestDefaultSettings(t *testing.T) {
	settings := DefaultSettings("test")
	assert.Equal(t, "test", settings.Name)
	assert.Equal(t, uint32(3), settings.MaxRequests)
	assert.Equal(t, 10*time.Second, settings.Interval)
	assert.Equal(t, 30*time.Second, settings.Timeout)
	assert.NotNil(t, settings.ReadyToTrip)

	// ReadyToTrip should trip after 5 consecutive failures
	assert.False(t, settings.ReadyToTrip(gobreaker.Counts{ConsecutiveFailures: 4}))
	assert.True(t, settings.ReadyToTrip(gobreaker.Counts{ConsecutiveFailures: 5}))
}

func TestNewStorageBackend(t *testing.T) {
	mb := new(mockBackend)
	settings := DefaultSettings("test")
	cb := NewStorageBackend(mb, settings)
	require.NotNil(t, cb)
}

func TestStore_Success(t *testing.T) {
	mb := new(mockBackend)
	ctx := context.Background()
	reader := strings.NewReader("data")
	meta := &storage.ImageMetadata{ID: "test"}

	mb.On("Store", ctx, "key", reader, meta).Return(nil)

	cb := NewStorageBackend(mb, DefaultSettings("test"))
	err := cb.Store(ctx, "key", reader, meta)
	assert.NoError(t, err)
	mb.AssertExpectations(t)
}

func TestStore_Error(t *testing.T) {
	mb := new(mockBackend)
	ctx := context.Background()

	mb.On("Store", ctx, "key", mock.Anything, mock.Anything).Return(errors.New("storage error"))

	cb := NewStorageBackend(mb, DefaultSettings("test"))
	err := cb.Store(ctx, "key", strings.NewReader("data"), &storage.ImageMetadata{})
	assert.Error(t, err)
}

func TestRetrieve_Success(t *testing.T) {
	mb := new(mockBackend)
	ctx := context.Background()
	expectedMeta := &storage.ImageMetadata{ID: "test", Format: "jpeg"}
	expectedReader := io.NopCloser(strings.NewReader("image data"))

	mb.On("Retrieve", ctx, "key").Return(expectedReader, expectedMeta, nil)

	cb := NewStorageBackend(mb, DefaultSettings("test"))
	reader, meta, err := cb.Retrieve(ctx, "key")
	require.NoError(t, err)
	assert.NotNil(t, reader)
	assert.Equal(t, "jpeg", meta.Format)
	mb.AssertExpectations(t)
}

func TestRetrieve_Error(t *testing.T) {
	mb := new(mockBackend)
	ctx := context.Background()

	mb.On("Retrieve", ctx, "key").Return(nil, nil, errors.New("not found"))

	cb := NewStorageBackend(mb, DefaultSettings("test"))
	reader, meta, err := cb.Retrieve(ctx, "key")
	assert.Error(t, err)
	assert.Nil(t, reader)
	assert.Nil(t, meta)
}

func TestDelete_Success(t *testing.T) {
	mb := new(mockBackend)
	ctx := context.Background()

	mb.On("Delete", ctx, "key").Return(nil)

	cb := NewStorageBackend(mb, DefaultSettings("test"))
	err := cb.Delete(ctx, "key")
	assert.NoError(t, err)
	mb.AssertExpectations(t)
}

func TestExists_Success(t *testing.T) {
	mb := new(mockBackend)
	ctx := context.Background()

	mb.On("Exists", ctx, "key").Return(true, nil)

	cb := NewStorageBackend(mb, DefaultSettings("test"))
	exists, err := cb.Exists(ctx, "key")
	assert.NoError(t, err)
	assert.True(t, exists)
}

func TestExists_NotFound(t *testing.T) {
	mb := new(mockBackend)
	ctx := context.Background()

	mb.On("Exists", ctx, "key").Return(false, nil)

	cb := NewStorageBackend(mb, DefaultSettings("test"))
	exists, err := cb.Exists(ctx, "key")
	assert.NoError(t, err)
	assert.False(t, exists)
}

func TestHealth_Success(t *testing.T) {
	mb := new(mockBackend)
	ctx := context.Background()

	mb.On("Health", ctx).Return(nil)

	cb := NewStorageBackend(mb, DefaultSettings("test"))
	err := cb.Health(ctx)
	assert.NoError(t, err)
}

func TestGetStats_Success(t *testing.T) {
	mb := new(mockBackend)
	ctx := context.Background()
	expectedStats := &storage.StorageStats{TotalImages: 42, TotalSize: 1024}

	mb.On("GetStats", ctx).Return(expectedStats, nil)

	cb := NewStorageBackend(mb, DefaultSettings("test"))
	stats, err := cb.GetStats(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(42), stats.TotalImages)
	assert.Equal(t, int64(1024), stats.TotalSize)
}

func TestList_Success(t *testing.T) {
	mb := new(mockBackend)
	ctx := context.Background()
	expectedList := []storage.ListResult{
		{Key: "img1", Size: 100},
		{Key: "img2", Size: 200},
	}

	mb.On("List", ctx, "prefix/").Return(expectedList, nil)

	cb := NewStorageBackend(mb, DefaultSettings("test"))
	results, err := cb.List(ctx, "prefix/")
	require.NoError(t, err)
	assert.Len(t, results, 2)
}

func TestState(t *testing.T) {
	mb := new(mockBackend)
	cb := NewStorageBackend(mb, DefaultSettings("test"))
	assert.Equal(t, gobreaker.StateClosed, cb.State())
}

func TestCounts(t *testing.T) {
	mb := new(mockBackend)
	cb := NewStorageBackend(mb, DefaultSettings("test"))
	counts := cb.Counts()
	assert.Equal(t, uint32(0), counts.Requests)
}

func TestIsOpen(t *testing.T) {
	mb := new(mockBackend)
	cb := NewStorageBackend(mb, DefaultSettings("test"))
	assert.False(t, cb.IsOpen())
}

func TestWithBucket_NonBucketAware(t *testing.T) {
	mb := new(mockBackend)
	cb := NewStorageBackend(mb, DefaultSettings("test"))

	// mockBackend doesn't implement BucketAware, should return self
	result := cb.WithBucket("new-bucket")
	assert.Equal(t, cb, result)
}

func TestGetCurrentBucket_NonBucketAware(t *testing.T) {
	mb := new(mockBackend)
	cb := NewStorageBackend(mb, DefaultSettings("test"))

	// Should return empty string for non-BucketAware backends
	assert.Equal(t, "", cb.GetCurrentBucket())
}

// A missing object is not a backend failure: counting it would open the
// breaker over a few reads and take down uploads for the whole bucket.
func TestNotFoundDoesNotTripTheBreaker(t *testing.T) {
	mb := new(mockBackend)
	mb.On("Retrieve", mock.Anything, mock.Anything).Return(nil, nil, storage.ErrImageNotFound)

	cb := NewStorageBackend(mb, DefaultSettings("test"))

	// Well above the threshold of 5 consecutive failures.
	for range 10 {
		_, _, err := cb.Retrieve(context.Background(), "missing")
		// The error is still reported to the caller: it just does not count here.
		require.ErrorIs(t, err, storage.ErrImageNotFound)
	}

	assert.False(t, cb.IsOpen(), "a 404 must not open the breaker")
	assert.Equal(t, gobreaker.StateClosed, cb.State())
}

// And the upload that came next still goes through, which is what actually broke.
func TestStoreStillWorksAfterManyNotFounds(t *testing.T) {
	mb := new(mockBackend)
	mb.On("Retrieve", mock.Anything, mock.Anything).Return(nil, nil, storage.ErrImageNotFound)
	mb.On("Store", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)

	cb := NewStorageBackend(mb, DefaultSettings("test"))
	for range 10 {
		_, _, _ = cb.Retrieve(context.Background(), "missing")
	}

	err := cb.Store(context.Background(), "new", strings.NewReader("bytes"), nil)
	assert.NoError(t, err)
}

// The flip side: a real outage must still open it.
func TestRealFailuresStillTripTheBreaker(t *testing.T) {
	mb := new(mockBackend)
	mb.On("Retrieve", mock.Anything, mock.Anything).Return(nil, nil, errors.New("connection refused"))

	cb := NewStorageBackend(mb, DefaultSettings("test"))
	for range 5 {
		_, _, _ = cb.Retrieve(context.Background(), "any")
	}

	assert.True(t, cb.IsOpen(), "a down backend MUST open the breaker")
}

func TestIsBackendFailure(t *testing.T) {
	assert.False(t, IsBackendFailure(nil))
	assert.False(t, IsBackendFailure(storage.ErrImageNotFound))
	assert.False(t, IsBackendFailure(fmt.Errorf("envuelto: %w", storage.ErrImageNotFound)))
	// A prefix bigger than one listing is an answer, not an outage: five in a
	// row must not open the breaker and take uploads down with them.
	assert.False(t, IsBackendFailure(storage.ErrListingTooLarge))
	assert.False(t, IsBackendFailure(fmt.Errorf("jay: %w", storage.ErrListingTooLarge)))
	assert.True(t, IsBackendFailure(errors.New("connection refused")))
	assert.True(t, IsBackendFailure(storage.ErrStorageUnavailable))
	// Cancellation is whoever asked giving up, never the backend.
	assert.False(t, IsBackendFailure(context.Canceled))
	assert.False(t, IsBackendFailure(fmt.Errorf("jay client: %w", context.Canceled)))
}

// A caller hanging up is not the backend failing. The jay client reports a
// cancelled context wrapped, so this goes through errors.Is.
func TestCallerCancellationDoesNotTripTheBreaker(t *testing.T) {
	mb := new(mockBackend)
	mb.On("Retrieve", mock.Anything, mock.Anything).
		Return(nil, nil, fmt.Errorf("jay: get k: jay client: %w", context.Canceled))

	cb := NewStorageBackend(mb, DefaultSettings("test"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for range 10 {
		_, _, err := cb.Retrieve(ctx, "k")
		require.ErrorIs(t, err, context.Canceled, "the caller still sees its own error")
	}
	assert.False(t, cb.IsOpen())

	// context.Canceled never counts, even if the caller's ctx is still alive.
	for range 10 {
		_, _, _ = cb.Retrieve(context.Background(), "k")
	}
	assert.False(t, cb.IsOpen())
}

// A deadline is the caller's when the caller's context is the one that ran out,
// and then it does not count. The same error with the caller's context alive
// came from a deadline further down — the backend being too slow — and counts.
func TestDeadlineCountsOnlyWhenItIsNotTheCallers(t *testing.T) {
	deadline := fmt.Errorf("jay client: %w", context.DeadlineExceeded)

	mb := new(mockBackend)
	mb.On("Retrieve", mock.Anything, mock.Anything).Return(nil, nil, deadline)
	cb := NewStorageBackend(mb, DefaultSettings("test"))

	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	for range 10 {
		_, _, err := cb.Retrieve(expired, "k")
		require.ErrorIs(t, err, context.DeadlineExceeded)
	}
	assert.False(t, cb.IsOpen(), "the caller's own deadline must not open the breaker")

	for range 5 {
		_, _, _ = cb.Retrieve(context.Background(), "k")
	}
	assert.True(t, cb.IsOpen(), "a backend deadline with the caller still waiting is a failure")
}

// An open breaker answers with storage.ErrStorageUnavailable, so a handler can
// map it to 503 without importing gobreaker; the gobreaker error stays in the
// chain.
func TestOpenBreakerReportsStorageUnavailable(t *testing.T) {
	mb := new(mockBackend)
	mb.On("Retrieve", mock.Anything, mock.Anything).Return(nil, nil, errors.New("connection refused"))
	cb := NewStorageBackend(mb, DefaultSettings("test"))
	for range 5 {
		_, _, _ = cb.Retrieve(context.Background(), "k")
	}
	require.True(t, cb.IsOpen())

	_, err := cb.Stat(context.Background(), "k")
	assert.True(t, storage.IsUnavailable(err), "got %v", err)
	assert.ErrorIs(t, err, gobreaker.ErrOpenState)
	mb.AssertNotCalled(t, "Stat", mock.Anything, mock.Anything)
}

func TestStat(t *testing.T) {
	mb := new(mockBackend)
	want := &storage.ImageMetadata{ID: "k", OwnerID: "o"}
	mb.On("Stat", mock.Anything, "k").Return(want, nil)
	mb.On("Stat", mock.Anything, "missing").Return(nil, storage.ErrImageNotFound)

	cb := NewStorageBackend(mb, DefaultSettings("test"))
	got, err := cb.Stat(context.Background(), "k")
	require.NoError(t, err)
	assert.Same(t, want, got)

	_, err = cb.Stat(context.Background(), "missing")
	assert.ErrorIs(t, err, storage.ErrImageNotFound)
}

// closingBackend records the shutdown calls the wrapper forwards.
type closingBackend struct {
	mockBackend
	drained, closed bool
}

func (c *closingBackend) Drain(context.Context) error { c.drained = true; return nil }
func (c *closingBackend) Close(context.Context) error { c.closed = true; return nil }

// Registry.CloseAll finds Drainer and Closer by type assertion on what it was
// handed — this wrapper. Without forwarding it found neither, so shutdown
// neither waited for async replications nor released jay's pool.
func TestShutdownIsForwardedToTheWrappedBackend(t *testing.T) {
	inner := &closingBackend{}
	reg := storage.NewRegistry(NewStorageBackend(inner, DefaultSettings("test")))

	assert.Empty(t, reg.CloseAll(context.Background()))
	assert.True(t, inner.drained, "Drain reached the wrapped backend")
	assert.True(t, inner.closed, "Close reached the wrapped backend")

	// A backend with nothing to release is a no-op, not an error.
	plain := NewStorageBackend(new(mockBackend), DefaultSettings("plain"))
	assert.NoError(t, plain.Drain(context.Background()))
	assert.NoError(t, plain.Close(context.Background()))
}
