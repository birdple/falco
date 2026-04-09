package cache

import (
	"hash/fnv"
	"time"
)

const defaultShardCount = 16

// ShardedCache distributes keys across multiple LRU shards to reduce
// lock contention. Each shard is an independent LRUCache with its own
// mutex, so concurrent Get/Set calls on different keys rarely contend.
type ShardedCache struct {
	shards []*LRUCache
	count  uint32
}

// NewShardedCache creates a sharded cache. maxSize is the total max
// size in bytes (divided equally among shards).
func NewShardedCache(maxSize int64, cleanupInterval time.Duration) *ShardedCache {
	shardSize := maxSize / int64(defaultShardCount)
	if shardSize < 1 {
		shardSize = 1
	}
	sc := &ShardedCache{
		shards: make([]*LRUCache, defaultShardCount),
		count:  defaultShardCount,
	}
	for i := range sc.shards {
		sc.shards[i] = NewLRUCache(shardSize, cleanupInterval)
	}
	return sc
}

func (sc *ShardedCache) shard(key string) *LRUCache {
	h := fnv.New32a()
	h.Write([]byte(key))
	return sc.shards[h.Sum32()%sc.count]
}

// Get retrieves a value from the cache.
func (sc *ShardedCache) Get(key string) ([]byte, bool) {
	return sc.shard(key).Get(key)
}

// Set stores a value in the cache.
func (sc *ShardedCache) Set(key string, value []byte, ttl time.Duration) error {
	return sc.shard(key).Set(key, value, ttl)
}

// Delete removes an item from the cache.
func (sc *ShardedCache) Delete(key string) {
	sc.shard(key).Delete(key)
}

// Contains checks if a key exists in the cache without promoting it.
func (sc *ShardedCache) Contains(key string) bool {
	return sc.shard(key).Contains(key)
}

// Clear removes all items from every shard.
func (sc *ShardedCache) Clear() {
	for _, s := range sc.shards {
		s.Clear()
	}
}

// Stop gracefully stops cleanup goroutines on all shards.
func (sc *ShardedCache) Stop() {
	for _, s := range sc.shards {
		s.Stop()
	}
}

// Size returns the total current size in bytes across all shards.
func (sc *ShardedCache) Size() int64 {
	var total int64
	for _, s := range sc.shards {
		total += s.Size()
	}
	return total
}

// MaxSize returns the total maximum size in bytes across all shards.
func (sc *ShardedCache) MaxSize() int64 {
	var total int64
	for _, s := range sc.shards {
		total += s.MaxSize()
	}
	return total
}

// Len returns the total number of items across all shards.
func (sc *ShardedCache) Len() int {
	var total int
	for _, s := range sc.shards {
		total += s.Len()
	}
	return total
}

// Keys returns all keys from every shard.
func (sc *ShardedCache) Keys() []string {
	var all []string
	for _, s := range sc.shards {
		all = append(all, s.Keys()...)
	}
	return all
}

// Stats returns aggregated CacheStats across all shards.
func (sc *ShardedCache) Stats() interface{} {
	var totalHits, totalMisses int64
	var totalSize int64
	var totalItems int
	for _, s := range sc.shards {
		stats := s.Stats().(CacheStats)
		totalHits += stats.Hits
		totalMisses += stats.Misses
		totalSize += stats.Size
		totalItems += stats.ItemCount
	}
	totalReqs := totalHits + totalMisses
	hitRatio := 0.0
	if totalReqs > 0 {
		hitRatio = float64(totalHits) / float64(totalReqs)
	}
	return CacheStats{
		Hits:      totalHits,
		Misses:    totalMisses,
		Size:      totalSize,
		MaxSize:   sc.MaxSize(),
		ItemCount: totalItems,
		HitRatio:  hitRatio,
	}
}
