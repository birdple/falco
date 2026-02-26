package cache

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisCache implements a Redis-based cache for persistent storage
type RedisCache struct {
	client *redis.Client
	ctx    context.Context
	ttl    time.Duration
}

// NewRedisCache creates a new Redis cache
func NewRedisCache(url string, ttl time.Duration) (*RedisCache, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, err
	}

	client := redis.NewClient(opts)
	ctx := context.Background()

	// Test connection
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, err
	}

	return &RedisCache{
		client: client,
		ctx:    ctx,
		ttl:    ttl,
	}, nil
}

// Get retrieves a value from Redis
func (r *RedisCache) Get(key string) ([]byte, bool) {
	val, err := r.client.Get(r.ctx, key).Bytes()
	if err != nil {
		return nil, false
	}
	return val, true
}

// Set stores a value in Redis with TTL
func (r *RedisCache) Set(key string, value []byte, ttl time.Duration) error {
	if ttl == 0 {
		ttl = r.ttl
	}
	return r.client.Set(r.ctx, key, value, ttl).Err()
}

// Delete removes an item from Redis
func (r *RedisCache) Delete(key string) {
	r.client.Del(r.ctx, key)
}

// Clear removes all items (CAUTION: FLUSHDB)
func (r *RedisCache) Clear() {
	r.client.FlushDB(r.ctx)
}

// Stats returns Redis statistics
func (r *RedisCache) Stats() interface{} {
	stats := r.client.PoolStats()
	return map[string]interface{}{
		"hits":          "N/A (Redis internally handles it)",
		"misses":        "N/A",
		"pool_hits":     stats.Hits,
		"pool_misses":   stats.Misses,
		"pool_timeouts": stats.Timeouts,
		"total_conns":   stats.TotalConns,
		"idle_conns":    stats.IdleConns,
		"stale_conns":   stats.StaleConns,
	}
}

// Contains checks if a key exists in Redis
func (r *RedisCache) Contains(key string) bool {
	n, err := r.client.Exists(r.ctx, key).Result()
	return err == nil && n > 0
}

// Keys returns all keys in Redis (CAUTION: KEYS * is slow)
func (r *RedisCache) Keys() []string {
	return r.client.Keys(r.ctx, "*").Val()
}

// Size returns total used memory in bytes according to info
func (r *RedisCache) Size() int64 {
	return 0 // Complex to get accurately without parsing INFO
}

// MaxSize returns 0 for Redis as it's managed externally
func (r *RedisCache) MaxSize() int64 {
	return 0
}

// Len returns the number of keys in the current DB
func (r *RedisCache) Len() int {
	return int(r.client.DBSize(r.ctx).Val())
}

// Stop closes the Redis client
func (r *RedisCache) Stop() {
	r.client.Close()
}
