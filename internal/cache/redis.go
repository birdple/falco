package cache

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisCache implements a Redis-based cache for persistent storage
type RedisCache struct {
	client *redis.Client
	ttl    time.Duration
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

// Get retrieves a value from Redis
func (r *RedisCache) Get(key string) ([]byte, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	val, err := r.client.Get(ctx, key).Bytes()
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
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return r.client.Set(ctx, key, value, ttl).Err()
}

// Delete removes an item from Redis
func (r *RedisCache) Delete(key string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	r.client.Del(ctx, key)
}

// Clear removes all items (CAUTION: FLUSHDB)
func (r *RedisCache) Clear() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r.client.FlushDB(ctx)
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
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	n, err := r.client.Exists(ctx, key).Result()
	return err == nil && n > 0
}

// Keys returns all keys in Redis (CAUTION: KEYS * is slow)
func (r *RedisCache) Keys() []string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return r.client.Keys(ctx, "*").Val()
}

// Size returns total used memory in bytes
func (r *RedisCache) Size() int64 {
	return 0
}

// MaxSize returns 0 for Redis as it's managed externally
func (r *RedisCache) MaxSize() int64 {
	return 0
}

// Len returns the number of keys in the current DB
func (r *RedisCache) Len() int {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return int(r.client.DBSize(ctx).Val())
}

// Stop closes the Redis client
func (r *RedisCache) Stop() {
	r.client.Close()
}
