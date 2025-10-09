package integration

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/birdple/imagine/internal/cache"
)

func TestLRUCache_BasicOperations(t *testing.T) {
	// Create a cache with 1MB limit
	cache := cache.NewLRUCache(1024*1024, 10*time.Minute)
	defer cache.Stop()

	// Test basic set/get
	key := "test_key"
	value := []byte("test_value")

	err := cache.Set(key, value, time.Hour)
	assert.NoError(t, err)

	retrieved, found := cache.Get(key)
	assert.True(t, found)
	assert.Equal(t, value, retrieved)

	// Test cache stats
	stats := cache.Stats()
	assert.NotNil(t, stats)

	// Test cache size
	assert.True(t, cache.Len() > 0)
	assert.True(t, cache.Size() > 0)
}

func TestLRUCache_CacheMiss(t *testing.T) {
	cache := cache.NewLRUCache(1024*1024, 10*time.Minute)
	defer cache.Stop()

	_, found := cache.Get("nonexistent_key")
	assert.False(t, found)
}

func TestLRUCache_TTL(t *testing.T) {
	cache := cache.NewLRUCache(1024*1024, 10*time.Minute)
	defer cache.Stop()

	key := "ttl_test"
	value := []byte("ttl_value")

	// Set with short TTL
	err := cache.Set(key, value, 100*time.Millisecond)
	assert.NoError(t, err)

	// Should be available immediately
	_, found := cache.Get(key)
	assert.True(t, found)

	// Wait for TTL to expire
	time.Sleep(200 * time.Millisecond)

	// Should be expired
	_, found = cache.Get(key)
	assert.False(t, found)
}

func TestLRUCache_SizeLimit(t *testing.T) {
	// Small cache size to test eviction
	cache := cache.NewLRUCache(100, 10*time.Minute) // 100 bytes
	defer cache.Stop()

	// Add items that exceed cache size
	largeValue := make([]byte, 80) // 80 bytes
	for i := 0; i < 5; i++ {
		key := fmt.Sprintf("key_%d", i)
		err := cache.Set(key, largeValue, time.Hour)
		assert.NoError(t, err)
	}

	// Cache should have evicted some items to stay within size limit
	assert.True(t, cache.Size() <= 100)
	assert.True(t, cache.Len() <= 2) // Should only fit 1-2 items
}
