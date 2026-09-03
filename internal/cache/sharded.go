package cache

import (
	"hash/maphash"
	"time"
)

const defaultShardCount = 16

// ShardedCache distributes keys across multiple LRU shards to reduce
// lock contention. Each shard is an independent LRUCache with its own
// mutex, so concurrent Get/Set calls on different keys rarely contend.
type ShardedCache struct {
	shards []*LRUCache
	count  uint64
	// The seed is created once. maphash.String hashes the string without
	// copying it to a []byte and without allocating a hasher per call, which is
	// what the previous fnv version did on EVERY Get/Set/Delete/Contains.
	seed maphash.Seed
}

// NewShardedCache creates a sharded cache. maxSize is the total max
// size in bytes (divided equally among shards).
func NewShardedCache(maxSize int64, cleanupInterval time.Duration) *ShardedCache {
	shardSize := max(maxSize/int64(defaultShardCount), 1)
	sc := &ShardedCache{
		shards: make([]*LRUCache, defaultShardCount),
		count:  defaultShardCount,
		seed:   maphash.MakeSeed(),
	}
	for i := range sc.shards {
		sc.shards[i] = NewLRUCache(shardSize, cleanupInterval)
	}
	return sc
}

func (sc *ShardedCache) shard(key string) *LRUCache {
	return sc.shards[maphash.String(sc.seed, key)%sc.count]
}

// forEach corre f sobre cada shard.
func (sc *ShardedCache) forEach(f func(*LRUCache)) {
	for _, s := range sc.shards {
		f(s)
	}
}

// sum accumulates the value pick returns across every shard.
//
// Generic so that Size, MaxSize and Len are one line each instead of three
// copies of the same loop.
func (sc *ShardedCache) sum[T ~int | ~int64](pick func(*LRUCache) T) T {
	var total T
	for _, s := range sc.shards {
		total += pick(s)
	}
	return total
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
func (sc *ShardedCache) Clear() { sc.forEach((*LRUCache).Clear) }

// Stop gracefully stops cleanup goroutines on all shards.
func (sc *ShardedCache) Stop() { sc.forEach((*LRUCache).Stop) }

// Size returns the total current size in bytes across all shards.
func (sc *ShardedCache) Size() int64 { return sc.sum((*LRUCache).Size) }

// MaxSize returns the total maximum size in bytes across all shards.
func (sc *ShardedCache) MaxSize() int64 { return sc.sum((*LRUCache).MaxSize) }

// Len returns the total number of items across all shards.
func (sc *ShardedCache) Len() int { return sc.sum((*LRUCache).Len) }

// Keys returns all keys from every shard.
func (sc *ShardedCache) Keys() []string {
	all := make([]string, 0, sc.Len())
	sc.forEach(func(s *LRUCache) { all = append(all, s.Keys()...) })
	return all
}

// Stats returns aggregated CacheStats across all shards.
func (sc *ShardedCache) Stats() CacheStats {
	var totalHits, totalMisses, totalSize, totalMaxSize int64
	var totalItems int
	sc.forEach(func(s *LRUCache) {
		stats := s.Stats()
		totalHits += stats.Hits
		totalMisses += stats.Misses
		totalSize += stats.Size
		totalMaxSize += stats.MaxSize
		totalItems += stats.ItemCount
	})
	totalReqs := totalHits + totalMisses
	hitRatio := 0.0
	if totalReqs > 0 {
		hitRatio = float64(totalHits) / float64(totalReqs)
	}
	return CacheStats{
		Backend:   "sharded-lru",
		Hits:      totalHits,
		Misses:    totalMisses,
		Size:      totalSize,
		MaxSize:   totalMaxSize,
		ItemCount: totalItems,
		HitRatio:  hitRatio,
	}
}
