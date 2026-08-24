package storage

import (
	"context"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// slowBackend deja que el test controle cuándo termina un Store/Delete, para
// poder observar el estado "réplica en vuelo".
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
func (b *slowBackend) Exists(context.Context, string) (bool, error) { return false, nil }
func (b *slowBackend) Health(context.Context) error                 { return nil }
func (b *slowBackend) GetStats(context.Context) (*StorageStats, error) {
	return &StorageStats{}, nil
}
func (b *slowBackend) List(context.Context, string) ([]ListResult, error) { return nil, nil }

// fastBackend es un primary que responde de inmediato.
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
func (b *fastBackend) Exists(context.Context, string) (bool, error) { return false, nil }
func (b *fastBackend) Health(context.Context) error                 { return nil }
func (b *fastBackend) GetStats(context.Context) (*StorageStats, error) {
	return &StorageStats{}, nil
}
func (b *fastBackend) List(context.Context, string) ([]ListResult, error) { return nil, nil }

// TestReplicatedStorage_CloseWaitsForAsyncStore es la prueba de que el apagado
// espera de verdad. Antes esto se "resolvía" con un time.Sleep(100ms) en main:
// un sleep no sabe si la réplica terminó, sólo espera un rato y sigue.
func TestReplicatedStorage_CloseWaitsForAsyncStore(t *testing.T) {
	primary := &fastBackend{}
	backup := newSlowBackend()

	rs := NewReplicatedStorage(primary, []BackupTarget{
		{Backend: backup, Mode: ReplicationAsync},
	})

	require.NoError(t, rs.Store(t.Context(), "k", strings.NewReader("datos"), &ImageMetadata{}))
	assert.Equal(t, int64(1), primary.stores.Load(), "el primary se escribe en línea")

	// La réplica sigue en vuelo: Close con un contexto ya vencido tiene que
	// reportarlo en vez de fingir que salió bien.
	expired, cancel := context.WithTimeout(t.Context(), time.Millisecond)
	defer cancel()
	assert.Error(t, rs.Close(expired), "Close no puede reportar éxito con réplicas en vuelo")
	assert.Equal(t, int64(0), backup.stores.Load())

	// Se libera la réplica: ahora Close espera y devuelve nil.
	close(backup.release)
	require.NoError(t, rs.Close(t.Context()))
	assert.Equal(t, int64(1), backup.stores.Load(), "Close esperó a que la réplica terminara")
}

// TestReplicatedStorage_CloseWaitsForAsyncDelete cubre lo mismo del lado del
// borrado, incluido el target read-fallback (best-effort, pero igual esperado).
func TestReplicatedStorage_CloseWaitsForAsyncDelete(t *testing.T) {
	primary := &fastBackend{}
	asyncBackup := newSlowBackend()
	fallbackBackup := newSlowBackend()

	rs := NewReplicatedStorage(primary, []BackupTarget{
		{Backend: asyncBackup, Mode: ReplicationAsync},
		{Backend: fallbackBackup, Mode: ReplicationReadFallback},
	})

	require.NoError(t, rs.Delete(t.Context(), "k"))

	close(asyncBackup.release)
	close(fallbackBackup.release)
	require.NoError(t, rs.Close(t.Context()))

	assert.Equal(t, int64(1), asyncBackup.deletes.Load())
	assert.Equal(t, int64(1), fallbackBackup.deletes.Load(),
		"el borrado best-effort también se espera antes de apagar")
}

// TestRegistry_CloseAll comprueba que el registry sabe esperar a los backends
// que tienen trabajo en vuelo y que ignora a los que no.
func TestRegistry_CloseAll(t *testing.T) {
	backup := newSlowBackend()
	close(backup.release)

	rs := NewReplicatedStorage(&fastBackend{}, []BackupTarget{
		{Backend: backup, Mode: ReplicationAsync},
	})

	reg := NewRegistry(&fastBackend{}) // el default no implementa Closer
	reg.Register("replicado", rs)

	require.NoError(t, rs.Store(t.Context(), "k", strings.NewReader("datos"), &ImageMetadata{}))

	failures := reg.CloseAll(t.Context())
	assert.Empty(t, failures)
	assert.Equal(t, int64(1), backup.stores.Load())
}
