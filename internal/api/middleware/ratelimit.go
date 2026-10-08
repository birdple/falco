package middleware

import (
	"container/list"
	"hash/fnv"
	"math"
	"net"
	"net/http"
	"strconv"
	"sync"

	"golang.org/x/time/rate"

	"github.com/birdple/falco/internal/pkg/httputil"
	"github.com/birdple/falco/internal/pkg/logger"
)

// rateShards splits the client table so that concurrent requests from
// different clients rarely contend on the same mutex.
const rateShards = 16

// defaultMaxRateClients bounds how many clients are tracked at once. Past it
// the least recently seen client is forgotten, which only ever errs towards
// letting a request through.
const defaultMaxRateClients = 100_000

// RateLimiter limits requests per client with a token bucket per client.
//
// Each client may make requestsPerMinute + burst requests in a burst and then
// requestsPerMinute per minute. Clients live in a sharded LRU, so a flood of
// new addresses costs O(1) per request instead of the O(n) scan under one
// global lock the previous implementation did once it was full, and there is
// no background sweeper to start or stop.
type RateLimiter struct {
	perMinute int
	limit     rate.Limit
	capacity  int
	shards    [rateShards]rateShard
}

type rateShard struct {
	mu      sync.Mutex
	max     int
	entries map[string]*list.Element
	lru     list.List // front = most recently seen; values are *rateEntry
}

type rateEntry struct {
	key     string
	limiter *rate.Limiter
}

// NewRateLimiter creates a rate limiter allowing requestsPerMinute steady and
// burst on top of it.
func NewRateLimiter(requestsPerMinute, burst int) *RateLimiter {
	return newBoundedRateLimiter(requestsPerMinute, burst, defaultMaxRateClients)
}

func newBoundedRateLimiter(requestsPerMinute, burst, maxClients int) *RateLimiter {
	rl := &RateLimiter{
		perMinute: requestsPerMinute,
		limit:     rate.Limit(float64(requestsPerMinute) / 60),
		capacity:  max(requestsPerMinute+burst, 1),
	}
	perShard := max(maxClients/rateShards, 1)
	for i := range rl.shards {
		rl.shards[i].max = perShard
		rl.shards[i].entries = make(map[string]*list.Element)
	}
	return rl
}

// limiterFor returns the bucket for a client key, creating it if needed.
func (rl *RateLimiter) limiterFor(key string) *rate.Limiter {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	shard := &rl.shards[h.Sum32()%rateShards]

	shard.mu.Lock()
	defer shard.mu.Unlock()

	if el, ok := shard.entries[key]; ok {
		shard.lru.MoveToFront(el)
		return el.Value.(*rateEntry).limiter
	}
	if shard.lru.Len() >= shard.max {
		oldest := shard.lru.Back()
		shard.lru.Remove(oldest)
		delete(shard.entries, oldest.Value.(*rateEntry).key)
	}
	entry := &rateEntry{key: key, limiter: rate.NewLimiter(rl.limit, rl.capacity)}
	shard.entries[key] = shard.lru.PushFront(entry)
	return entry.limiter
}

// rateKey groups a client address for limiting. IPv6 is keyed by its /64: a
// single host routinely holds a whole /64, so per-address buckets let one
// client rotate through billions of fresh ones.
func rateKey(ip string) string {
	parsed := net.ParseIP(ip)
	if parsed == nil || parsed.To4() != nil {
		return ip
	}
	return parsed.Mask(net.CIDRMask(64, 128)).String() + "/64"
}

// Handler returns the rate limiting middleware handler
func (rl *RateLimiter) Handler(next http.Handler) http.Handler {
	retryAfter := "60"
	if rl.limit > 0 {
		retryAfter = strconv.Itoa(int(math.Ceil(1 / float64(rl.limit))))
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clientIP := httputil.GetClientIP(r)
		limiter := rl.limiterFor(rateKey(clientIP))

		w.Header().Set("X-RateLimit-Limit", strconv.Itoa(rl.perMinute))
		if !limiter.Allow() {
			logger.Warn().
				Str("ip", clientIP).
				Str("user_agent", httputil.GetUserAgent(r)).
				Str("path", r.URL.Path).
				Msg("Rate limit exceeded")

			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("Retry-After", retryAfter)
			writeError(w, http.StatusTooManyRequests, "RATE_LIMITED", "Too many requests")
			return
		}

		remaining := max(int(limiter.Tokens()), 0)
		w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
		next.ServeHTTP(w, r)
	})
}
