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

// Un objeto que no existe no es una avería del backend, y contarlo como tal
// tumbaba el bucket entero: cinco lecturas seguidas de imágenes cuyas filas
// sobrevivieron a sus bytes abrían el breaker, y la siguiente **subida** moría
// con "circuit breaker is open" durante los 30 s del timeout.
func TestNotFoundDoesNotTripTheBreaker(t *testing.T) {
	mb := new(mockBackend)
	mb.On("Retrieve", mock.Anything, mock.Anything).Return(nil, nil, storage.ErrImageNotFound)

	cb := NewStorageBackend(mb, DefaultSettings("test"))

	// Muy por encima del umbral de 5 fallos consecutivos.
	for range 10 {
		_, _, err := cb.Retrieve(context.Background(), "missing")
		// El error se sigue reportando a quien llama: sólo no cuenta acá.
		require.ErrorIs(t, err, storage.ErrImageNotFound)
	}

	assert.False(t, cb.IsOpen(), "un 404 no puede abrir el breaker")
	assert.Equal(t, gobreaker.StateClosed, cb.State())
}

// Y la subida que venía detrás sigue pasando, que es lo que de verdad se rompía.
func TestStoreStillWorksAfterManyNotFounds(t *testing.T) {
	mb := new(mockBackend)
	mb.On("Retrieve", mock.Anything, mock.Anything).Return(nil, nil, storage.ErrImageNotFound)
	mb.On("Store", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)

	cb := NewStorageBackend(mb, DefaultSettings("test"))
	for range 10 {
		_, _, _ = cb.Retrieve(context.Background(), "missing")
	}

	err := cb.Store(context.Background(), "nueva", strings.NewReader("bytes"), nil)
	assert.NoError(t, err)
}

// La contracara: una avería de verdad tiene que seguir abriéndolo.
func TestRealFailuresStillTripTheBreaker(t *testing.T) {
	mb := new(mockBackend)
	mb.On("Retrieve", mock.Anything, mock.Anything).Return(nil, nil, errors.New("connection refused"))

	cb := NewStorageBackend(mb, DefaultSettings("test"))
	for range 5 {
		_, _, _ = cb.Retrieve(context.Background(), "cualquiera")
	}

	assert.True(t, cb.IsOpen(), "un backend caído SÍ debe abrir el breaker")
}

func TestIsBackendFailure(t *testing.T) {
	assert.False(t, IsBackendFailure(nil))
	assert.False(t, IsBackendFailure(storage.ErrImageNotFound))
	assert.False(t, IsBackendFailure(fmt.Errorf("envuelto: %w", storage.ErrImageNotFound)))
	assert.True(t, IsBackendFailure(errors.New("connection refused")))
	assert.True(t, IsBackendFailure(storage.ErrStorageUnavailable))
}
