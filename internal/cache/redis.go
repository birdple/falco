package cache

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

// keyPrefix is the namespace prefix for all Falco cache keys in Redis.
// This prevents collisions with other applications sharing the same Redis instance.
const keyPrefix = "falco:"

// RedisCache implements a Redis-based cache for persistent storage
type RedisCache struct {
	client *redis.Client
	ttl    time.Duration
	// Contadores propios de este proceso. Redis no puede decirnos cuántos
	// aciertos tuvo ESTA cache (su INFO agrega a todos los clientes del
	// servidor), pero nosotros sí sabemos cómo respondió cada Get.
	hits   atomic.Int64
	misses atomic.Int64
}

// NewRedisCache creates a new Redis cache
func NewRedisCache(url string, ttl time.Duration) (*RedisCache, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, err
	}

	client := redis.NewClient(opts)

	// Test connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, err
	}

	return &RedisCache{
		client: client,
		ttl:    ttl,
	}, nil
}

// prefixedKey returns the key with the Falco namespace prefix
func prefixedKey(key string) string {
	return keyPrefix + key
}

// Get retrieves a value from Redis
func (r *RedisCache) Get(key string) ([]byte, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	val, err := r.client.Get(ctx, prefixedKey(key)).Bytes()
	if err != nil {
		r.misses.Add(1)
		return nil, false
	}
	r.hits.Add(1)
	return val, true
}

// Set stores a value in Redis with TTL
func (r *RedisCache) Set(key string, value []byte, ttl time.Duration) error {
	if ttl == 0 {
		ttl = r.ttl
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return r.client.Set(ctx, prefixedKey(key), value, ttl).Err()
}

// Delete removes an item from Redis
func (r *RedisCache) Delete(key string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	r.client.Del(ctx, prefixedKey(key))
}

// Clear removes only Falco-namespaced keys using SCAN (non-blocking).
// This is safe for shared Redis instances unlike FLUSHDB.
func (r *RedisCache) Clear() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var cursor uint64
	for {
		keys, nextCursor, err := r.client.Scan(ctx, cursor, keyPrefix+"*", 100).Result()
		if err != nil {
			return
		}
		if len(keys) > 0 {
			r.client.Unlink(ctx, keys...)
		}
		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}
}

// Stats returns Redis cache statistics.
//
// Hits y Misses son reales: los cuenta este proceso en cada Get. Size, MaxSize
// e ItemCount van en statUnmeasured porque conocerlos exigiría un DBSIZE o un
// SCAN completo en cada llamada, y devolver 0 sería indistinguible de una cache
// vacía. Para el detalle del pool de conexiones está PoolStats.
func (r *RedisCache) Stats() CacheStats {
	hits := r.hits.Load()
	misses := r.misses.Load()
	total := hits + misses
	hitRatio := 0.0
	if total > 0 {
		hitRatio = float64(hits) / float64(total)
	}
	return CacheStats{
		Backend:   "redis",
		Hits:      hits,
		Misses:    misses,
		Size:      statUnmeasured,
		MaxSize:   statUnmeasured,
		ItemCount: statUnmeasured,
		HitRatio:  hitRatio,
	}
}

// PoolStats expone las métricas del pool de conexiones de go-redis. Antes
// venían mezcladas dentro del map que devolvía Stats; ahora que Stats tiene un
// tipo uniforme para los tres backends, viven aquí.
func (r *RedisCache) PoolStats() *redis.PoolStats {
	return r.client.PoolStats()
}

// Contains checks if a key exists in Redis
func (r *RedisCache) Contains(key string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	n, err := r.client.Exists(ctx, prefixedKey(key)).Result()
	return err == nil && n > 0
}

// Keys returns all Falco-namespaced keys using SCAN (non-blocking).
func (r *RedisCache) Keys() []string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var allKeys []string
	var cursor uint64
	for {
		keys, nextCursor, err := r.client.Scan(ctx, cursor, keyPrefix+"*", 100).Result()
		if err != nil {
			return allKeys
		}
		// Strip prefix before returning
		for _, k := range keys {
			allKeys = append(allKeys, k[len(keyPrefix):])
		}
		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}
	return allKeys
}

// Size returns total used memory in bytes
func (r *RedisCache) Size() int64 {
	return 0
}

// MaxSize returns 0 for Redis as it's managed externally
func (r *RedisCache) MaxSize() int64 {
	return 0
}

// Len returns the number of Falco-namespaced keys
func (r *RedisCache) Len() int {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var count int
	var cursor uint64
	for {
		keys, nextCursor, err := r.client.Scan(ctx, cursor, keyPrefix+"*", 100).Result()
		if err != nil {
			return count
		}
		count += len(keys)
		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}
	return count
}

// Stop closes the Redis client
func (r *RedisCache) Stop() {
	r.client.Close()
}
