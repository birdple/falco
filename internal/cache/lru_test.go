package cache

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewLRUCache(t *testing.T) {
	cache := NewLRUCache(1024, time.Minute)
	defer cache.Stop()
	require.NotNil(t, cache)
	assert.Equal(t, int64(1024), cache.MaxSize())
	assert.Equal(t, int64(0), cache.Size())
	assert.Equal(t, 0, cache.Len())
}

func TestLRUCache_SetAndGet(t *testing.T) {
	cache := NewLRUCache(1024*1024, time.Hour)
	defer cache.Stop()

	err := cache.Set("key1", []byte("value1"), 0)
	require.NoError(t, err)

	val, ok := cache.Get("key1")
	assert.True(t, ok)
	assert.Equal(t, []byte("value1"), val)
}

func TestLRUCache_Get_Miss(t *testing.T) {
	cache := NewLRUCache(1024, time.Hour)
	defer cache.Stop()

	val, ok := cache.Get("nonexistent")
	assert.False(t, ok)
	assert.Nil(t, val)
}

func TestLRUCache_Set_UpdateExisting(t *testing.T) {
	cache := NewLRUCache(1024*1024, time.Hour)
	defer cache.Stop()

	cache.Set("key1", []byte("value1"), 0)
	cache.Set("key1", []byte("updated"), 0)

	val, ok := cache.Get("key1")
	assert.True(t, ok)
	assert.Equal(t, []byte("updated"), val)
	assert.Equal(t, 1, cache.Len())
}

func TestLRUCache_Delete(t *testing.T) {
	cache := NewLRUCache(1024*1024, time.Hour)
	defer cache.Stop()

	cache.Set("key1", []byte("value1"), 0)
	cache.Delete("key1")

	val, ok := cache.Get("key1")
	assert.False(t, ok)
	assert.Nil(t, val)
	assert.Equal(t, 0, cache.Len())
}

func TestLRUCache_Delete_NonExistent(t *testing.T) {
	cache := NewLRUCache(1024, time.Hour)
	defer cache.Stop()

	// Should not panic
	cache.Delete("nonexistent")
}

func TestLRUCache_Clear(t *testing.T) {
	cache := NewLRUCache(1024*1024, time.Hour)
	defer cache.Stop()

	cache.Set("key1", []byte("value1"), 0)
	cache.Set("key2", []byte("value2"), 0)
	cache.Clear()

	assert.Equal(t, 0, cache.Len())
	assert.Equal(t, int64(0), cache.Size())
}

func TestLRUCache_Contains(t *testing.T) {
	cache := NewLRUCache(1024*1024, time.Hour)
	defer cache.Stop()

	cache.Set("key1", []byte("value1"), 0)

	assert.True(t, cache.Contains("key1"))
	assert.False(t, cache.Contains("key2"))
}

func TestLRUCache_Keys(t *testing.T) {
	cache := NewLRUCache(1024*1024, time.Hour)
	defer cache.Stop()

	cache.Set("key1", []byte("v1"), 0)
	cache.Set("key2", []byte("v2"), 0)

	keys := cache.Keys()
	assert.Len(t, keys, 2)
	assert.Contains(t, keys, "key1")
	assert.Contains(t, keys, "key2")
}

func TestLRUCache_Size(t *testing.T) {
	cache := NewLRUCache(1024*1024, time.Hour)
	defer cache.Stop()

	cache.Set("key1", []byte("hello"), 0)   // 5 bytes
	cache.Set("key2", []byte("world!!"), 0) // 7 bytes

	assert.Equal(t, int64(12), cache.Size())
}

func TestLRUCache_Eviction(t *testing.T) {
	cache := NewLRUCache(10, time.Hour) // Only 10 bytes
	defer cache.Stop()

	cache.Set("key1", []byte("12345"), 0) // 5 bytes
	cache.Set("key2", []byte("67890"), 0) // 5 bytes - total 10
	cache.Set("key3", []byte("abcde"), 0) // 5 bytes - should evict key1

	assert.False(t, cache.Contains("key1")) // evicted
	assert.True(t, cache.Contains("key2"))
	assert.True(t, cache.Contains("key3"))
}

func TestLRUCache_LRU_Order(t *testing.T) {
	cache := NewLRUCache(15, time.Hour)
	defer cache.Stop()

	cache.Set("key1", []byte("12345"), 0) // 5 bytes
	cache.Set("key2", []byte("67890"), 0) // 5 bytes
	cache.Set("key3", []byte("abcde"), 0) // 5 bytes - total 15

	// Access key1 to make it recently used
	cache.Get("key1")

	// Adding another item should evict key2 (least recently used), not key1
	cache.Set("key4", []byte("fghij"), 0)

	assert.True(t, cache.Contains("key1"))  // recently accessed
	assert.False(t, cache.Contains("key2")) // evicted (LRU)
	assert.True(t, cache.Contains("key3"))
	assert.True(t, cache.Contains("key4"))
}

func TestLRUCache_TTL_Expiration(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cache := NewLRUCache(1024*1024, time.Hour)
		defer cache.Stop()

		cache.Set("key1", []byte("value1"), 50*time.Millisecond)

		// Should exist initially
		val, ok := cache.Get("key1")
		assert.True(t, ok)
		assert.Equal(t, []byte("value1"), val)

		// Reloj falso: synctest.Sleep adelanta el tiempo del bubble al
		// instante, así que el TTL vence sin que el test espere de verdad ni
		// dependa de un margen de 10 ms que en una máquina cargada se queda
		// corto.
		synctest.Sleep(60 * time.Millisecond)

		val, ok = cache.Get("key1")
		assert.False(t, ok)
		assert.Nil(t, val)
	})
}

func TestLRUCache_Contains_TTL_Expired(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cache := NewLRUCache(1024*1024, time.Hour)
		defer cache.Stop()

		cache.Set("key1", []byte("value1"), 50*time.Millisecond)
		assert.True(t, cache.Contains("key1"))

		synctest.Sleep(60 * time.Millisecond)
		assert.False(t, cache.Contains("key1"))
	})
}

func TestLRUCache_Stats(t *testing.T) {
	cache := NewLRUCache(1024*1024, time.Hour)
	defer cache.Stop()

	cache.Set("key1", []byte("value1"), 0)
	cache.Get("key1")        // hit
	cache.Get("nonexistent") // miss

	stats := cache.Stats()
	assert.Equal(t, "lru", stats.Backend)
	assert.Equal(t, int64(1), stats.Hits)
	assert.Equal(t, int64(1), stats.Misses)
	assert.Equal(t, int64(6), stats.Size)
	assert.Equal(t, int64(1024*1024), stats.MaxSize)
	assert.Equal(t, 1, stats.ItemCount)
	assert.InDelta(t, 0.5, stats.HitRatio, 0.01)
}

func TestLRUCache_Stats_NoRequests(t *testing.T) {
	cache := NewLRUCache(1024, time.Hour)
	defer cache.Stop()

	stats := cache.Stats()
	assert.Equal(t, int64(0), stats.Hits)
	assert.Equal(t, int64(0), stats.Misses)
	assert.InDelta(t, 0.0, stats.HitRatio, 0.01)
}

func TestLRUCache_MaxSize(t *testing.T) {
	cache := NewLRUCache(2048, time.Hour)
	defer cache.Stop()
	assert.Equal(t, int64(2048), cache.MaxSize())
}

func TestLRUCache_Stop(t *testing.T) {
	cache := NewLRUCache(1024, 100*time.Millisecond)
	// Should not panic on double stop
	cache.Stop()
	cache.Stop()
}

// The sweep goroutine stops for good on Stop: a bubble only finishes once all
// of its goroutines have exited, so a cleanup loop still running would hang it.
func TestLRUCache_StopEndsTheSweep(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cache := NewLRUCache(1024, time.Second)
		synctest.Sleep(3 * time.Second)
		cache.Stop()
		cache.Stop()
	})
}

// A non-positive interval made time.NewTicker panic inside the sweep goroutine,
// whose recover started a new one that panicked the same way, forever. It now
// falls back to the default, and the sweep runs.
func TestLRUCache_NonPositiveCleanupIntervalFallsBack(t *testing.T) {
	for _, interval := range []time.Duration{0, -time.Second} {
		synctest.Test(t, func(t *testing.T) {
			cache := NewLRUCache(1024, interval)
			defer cache.Stop()
			assert.Equal(t, DefaultCleanupInterval, cache.cleanupInterval)

			require.NoError(t, cache.Set("k", []byte("v"), time.Millisecond))
			synctest.Sleep(DefaultCleanupInterval + time.Second)
			assert.Equal(t, 0, cache.Len(), "the expired entry was swept")
		})
	}
}

// An item bigger than the whole cache used to evict everything else and then
// itself: a full flush that cached nothing.
func TestLRUCache_RejectsItemLargerThanMaxSize(t *testing.T) {
	cache := NewLRUCache(10, time.Hour)
	defer cache.Stop()

	require.NoError(t, cache.Set("small", []byte("12345"), 0))
	require.NoError(t, cache.Set("big", []byte("old"), 0))

	err := cache.Set("big", []byte("this is far too large"), 0)
	require.ErrorIs(t, err, ErrItemTooLarge)

	assert.True(t, cache.Contains("small"), "the rest of the cache survives")
	assert.False(t, cache.Contains("big"), "the stale value under the key is not served instead")
	assert.Equal(t, int64(5), cache.Size())

	// Exactly the max size still fits.
	require.NoError(t, cache.Set("exact", []byte("0123456789"), 0))
}

func TestShardedCache_RejectsItemLargerThanShard(t *testing.T) {
	sc := NewShardedCache(16*10, time.Hour) // 10 bytes per shard
	defer sc.Stop()

	require.NoError(t, sc.Set("k", []byte("0123456789"), 0))
	assert.ErrorIs(t, sc.Set("k2", []byte("01234567890"), 0), ErrItemTooLarge)
	assert.Equal(t, 1, sc.Len())
}

func BenchmarkLRUCache_Set(b *testing.B) {
	cache := NewLRUCache(256*1024*1024, time.Hour)
	defer cache.Stop()
	val := []byte("benchmark-image-data")
	b.ResetTimer()
	for i := range b.N {
		cache.Set(string(rune(i)), val, 0)
	}
}

func BenchmarkLRUCache_Get_Hit(b *testing.B) {
	cache := NewLRUCache(256*1024*1024, time.Hour)
	defer cache.Stop()
	val := []byte("benchmark-image-data")
	cache.Set("key", val, 0)
	b.ResetTimer()
	for range b.N {
		cache.Get("key")
	}
}

func BenchmarkLRUCache_Get_Miss(b *testing.B) {
	cache := NewLRUCache(1024, time.Hour)
	defer cache.Stop()
	b.ResetTimer()
	for range b.N {
		cache.Get("nonexistent")
	}
}

func BenchmarkLRUCache_SetWithEviction(b *testing.B) {
	cache := NewLRUCache(100, time.Hour) // tiny cache to force evictions
	defer cache.Stop()
	val := []byte("12345") // 5 bytes
	b.ResetTimer()
	for i := range b.N {
		key := string([]byte{byte(i % 256), byte(i / 256)})
		cache.Set(key, val, 0)
	}
}

func BenchmarkLRUCache_Parallel(b *testing.B) {
	cache := NewLRUCache(256*1024*1024, time.Hour)
	defer cache.Stop()
	val := []byte("benchmark-image-data")
	cache.Set("shared-key", val, 0)
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			if i%2 == 0 {
				cache.Get("shared-key")
			} else {
				cache.Set("shared-key", val, 0)
			}
			i++
		}
	})
}

func TestLRUCache_Set_SizeTracking(t *testing.T) {
	cache := NewLRUCache(1024*1024, time.Hour)
	defer cache.Stop()

	cache.Set("key1", []byte("hello"), 0) // 5 bytes
	assert.Equal(t, int64(5), cache.Size())

	// Update with different size
	cache.Set("key1", []byte("hello world"), 0) // 11 bytes
	assert.Equal(t, int64(11), cache.Size())

	cache.Delete("key1")
	assert.Equal(t, int64(0), cache.Size())
}
