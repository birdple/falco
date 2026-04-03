package cache

import (
	"testing"
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

	cache.Set("key1", []byte("hello"), 0)       // 5 bytes
	cache.Set("key2", []byte("world!!"), 0)      // 7 bytes

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
	cache := NewLRUCache(1024*1024, time.Hour)
	defer cache.Stop()

	cache.Set("key1", []byte("value1"), 50*time.Millisecond)

	// Should exist initially
	val, ok := cache.Get("key1")
	assert.True(t, ok)
	assert.Equal(t, []byte("value1"), val)

	// Wait for TTL to expire
	time.Sleep(60 * time.Millisecond)

	val, ok = cache.Get("key1")
	assert.False(t, ok)
	assert.Nil(t, val)
}

func TestLRUCache_Contains_TTL_Expired(t *testing.T) {
	cache := NewLRUCache(1024*1024, time.Hour)
	defer cache.Stop()

	cache.Set("key1", []byte("value1"), 50*time.Millisecond)
	assert.True(t, cache.Contains("key1"))

	time.Sleep(60 * time.Millisecond)
	assert.False(t, cache.Contains("key1"))
}

func TestLRUCache_Stats(t *testing.T) {
	cache := NewLRUCache(1024*1024, time.Hour)
	defer cache.Stop()

	cache.Set("key1", []byte("value1"), 0)
	cache.Get("key1")        // hit
	cache.Get("nonexistent") // miss

	stats := cache.Stats().(CacheStats)
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

	stats := cache.Stats().(CacheStats)
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
