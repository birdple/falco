package cache

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"

	"github.com/birdple/falco/internal/pkg/logger"
)

// unreachableRedis returns a RedisCache whose server does not exist, and a
// buffer collecting what it logs. NewRedisCache is bypassed because it pings.
func unreachableRedis(t *testing.T) (*RedisCache, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	orig := logger.Logger
	logger.Logger = zerolog.New(&buf).Level(zerolog.WarnLevel)
	t.Cleanup(func() { logger.Logger = orig })

	client := redis.NewClient(&redis.Options{
		Addr:        "127.0.0.1:1", // nothing listens on port 1
		DialTimeout: 100 * time.Millisecond,
		MaxRetries:  -1,
	})
	t.Cleanup(func() { _ = client.Close() })
	return &RedisCache{client: client, ttl: time.Minute}, &buf
}

// A Redis that cannot be reached is still a miss for the caller, but it is no
// longer silent: it used to look exactly like a cold cache. The warning is
// throttled, since every request goes through Get.
func TestRedisCache_GetLogsConnectionErrors(t *testing.T) {
	r, logs := unreachableRedis(t)

	for range 3 {
		val, ok := r.Get("k")
		assert.False(t, ok)
		assert.Nil(t, val)
	}

	assert.Equal(t, int64(3), r.Stats().Misses)
	assert.Equal(t, int64(3), r.failures.Load())
	assert.Equal(t, 1, strings.Count(logs.String(), `"level":"warn"`),
		"one warning per errorLogInterval, not one per request: %s", logs.String())
}

// Keys cannot return an error through processor.Cache, so a SCAN that fails
// returns what it has — and says so.
func TestRedisCache_KeysLogsPartialResult(t *testing.T) {
	r, logs := unreachableRedis(t)

	assert.Empty(t, r.Keys())
	assert.Contains(t, logs.String(), "PARTIAL")
}
