package cache

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/birdple/falco/internal/pkg/logger"
)

// keyPrefix is the namespace prefix for all Falco cache keys in Redis.
// This prevents collisions with other applications sharing the same Redis instance.
const keyPrefix = "falco:"

// RedisCache implements a Redis-based cache for persistent storage
type RedisCache struct {
	client *redis.Client
	ttl    time.Duration
	// This process's own counters: Redis' INFO aggregates every client of the
	// server, but each of our own Gets knows how it was answered.
	hits   atomic.Int64
	misses atomic.Int64
	// failures counts failed Redis calls, and lastErrorLog (unix nanos) throttles
	// the warning about them: see logError.
	failures     atomic.Int64
	lastErrorLog atomic.Int64
}

// errorLogInterval is the most often a failing Redis is reported. Every request
// goes through Get, so logging each failure would put a line per request in the
// log for as long as Redis is down.
const errorLogInterval = 10 * time.Second

// logError reports a failed Redis call at warn level, at most once per
// errorLogInterval; the count of failures since process start goes with it, so
// the throttled ones are not lost.
func (r *RedisCache) logError(op string, err error) {
	total := r.failures.Add(1)
	now := time.Now().UnixNano()
	last := r.lastErrorLog.Load()
	if now-last < int64(errorLogInterval) || !r.lastErrorLog.CompareAndSwap(last, now) {
		logger.Debug().Err(err).Str("op", op).Msg("Redis cache call failed")
		return
	}
	logger.Warn().Err(err).
		Str("op", op).
		Int64("failures_total", total).
		Msg("Redis cache call failed — treated as a miss / partial result")
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

// Get retrieves a value from Redis.
//
// A Redis that cannot be reached is answered as a miss — the caller renders
// the image instead — but it is logged: before, a dead Redis looked exactly
// like a cold cache.
func (r *RedisCache) Get(key string) ([]byte, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	val, err := r.client.Get(ctx, prefixedKey(key)).Bytes()
	if err != nil {
		if !errors.Is(err, redis.Nil) {
			r.logError("get", err)
		}
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
			r.failures.Add(1)
			logger.Warn().Err(err).Msg("Redis SCAN failed: Clear() stopped early, some keys remain")
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
// Hits and Misses are real: this process counts them on every Get. Size,
// MaxSize and ItemCount are statUnmeasured because knowing them would mean a
// DBSIZE or a full SCAN per call, and returning 0 would be indistinguishable
// from an empty cache. For connection-pool detail, see PoolStats.
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

// PoolStats exposes go-redis connection-pool metrics, apart from Stats, which
// has one uniform type across the three backends.
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
//
// processor.Cache gives Keys no way to return an error, so a SCAN that fails
// partway still returns the keys gathered so far — but it is logged at warn,
// unthrottled, with how many were gathered: a short list must not pass for a
// complete one without a trace.
func (r *RedisCache) Keys() []string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var allKeys []string
	var cursor uint64
	for {
		keys, nextCursor, err := r.client.Scan(ctx, cursor, keyPrefix+"*", 100).Result()
		if err != nil {
			r.failures.Add(1)
			logger.Warn().Err(err).
				Int("keys_returned", len(allKeys)).
				Msg("Redis SCAN failed: Keys() is returning a PARTIAL list")
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

// Size always returns 0: this cache does NOT measure its size.
//
// The old comment claimed "total used memory in bytes", which it never was.
// Knowing the real figure would take a MEMORY USAGE per key or a full SCAN on
// every call, and Redis's own used_memory covers every client of the server,
// not falco's keys. processor.Cache has no way to say "unknown" here, so 0 it
// is; Stats reports the same thing honestly, as statUnmeasured (-1).
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
			r.failures.Add(1)
			logger.Warn().Err(err).Int("counted", count).
				Msg("Redis SCAN failed: Len() is returning a PARTIAL count")
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
	_ = r.client.Close()
}
