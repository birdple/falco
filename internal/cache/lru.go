package cache

import (
	"container/list"
	"sync"
	"time"
)

// CacheItem represents an item in the cache
type CacheItem struct {
	key       string
	value     []byte
	size      int64
	element   *list.Element
	createdAt time.Time
	ttl       time.Duration
}

// CacheStats holds cache statistics
type CacheStats struct {
	Hits      int64   `json:"hits"`
	Misses    int64   `json:"misses"`
	Size      int64   `json:"size"`
	MaxSize   int64   `json:"max_size"`
	ItemCount int     `json:"item_count"`
	HitRatio  float64 `json:"hit_ratio"`
}

// LRUCache implements an in-memory LRU cache with size limits and TTL
type LRUCache struct {
	maxSize         int64
	currentSize     int64
	items           map[string]*CacheItem
	evictList       *list.List
	mutex           sync.RWMutex
	cleanupInterval time.Duration
	stopCleanup     chan struct{}
}

// NewLRUCache creates a new LRU cache
func NewLRUCache(maxSize int64, cleanupInterval time.Duration) *LRUCache {
	cache := &LRUCache{
		maxSize:         maxSize,
		currentSize:     0,
		items:           make(map[string]*CacheItem),
		evictList:       list.New(),
		cleanupInterval: cleanupInterval,
		stopCleanup:     make(chan struct{}),
	}

	// Start cleanup goroutine
	go cache.cleanup()

	return cache
}

// Get retrieves a value from the cache
func (c *LRUCache) Get(key string) ([]byte, bool) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	item, exists := c.items[key]
	if !exists {
		return nil, false
	}

	// Check if item has expired
	if item.ttl > 0 && time.Since(item.createdAt) > item.ttl {
		c.removeItem(item)
		return nil, false
	}

	// Move item to front (most recently used)
	c.evictList.MoveToFront(item.element)

	return item.value, true
}

// Set stores a value in the cache
func (c *LRUCache) Set(key string, value []byte, ttl time.Duration) error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	size := int64(len(value))

	// If item already exists, update it
	if item, exists := c.items[key]; exists {
		c.currentSize -= item.size
		c.currentSize += size
		item.value = value
		item.size = size
		item.createdAt = time.Now()
		item.ttl = ttl
		c.evictList.MoveToFront(item.element)
	} else {
		// Create new item
		item := &CacheItem{
			key:       key,
			value:     value,
			size:      size,
			createdAt: time.Now(),
			ttl:       ttl,
		}
		item.element = c.evictList.PushFront(item)
		c.items[key] = item
		c.currentSize += size
	}

	// Evict items if we're over the limit
	for c.currentSize > c.maxSize && c.evictList.Len() > 0 {
		c.evictOldest()
	}

	return nil
}

// Delete removes an item from the cache
func (c *LRUCache) Delete(key string) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if item, exists := c.items[key]; exists {
		c.removeItem(item)
	}
}

// Clear removes all items from the cache
func (c *LRUCache) Clear() {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.items = make(map[string]*CacheItem)
	c.evictList = list.New()
	c.currentSize = 0
}

// Stats returns cache statistics
func (c *LRUCache) Stats() interface{} {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	totalRequests := float64(c.evictList.Len())
	hitRatio := 0.0
	if totalRequests > 0 {
		hitRatio = float64(c.evictList.Len()-len(c.items)) / totalRequests
	}

	return CacheStats{
		Hits:      int64(c.evictList.Len() - len(c.items)), // Approximation
		Misses:    int64(len(c.items)),                     // Approximation
		Size:      c.currentSize,
		MaxSize:   c.maxSize,
		ItemCount: len(c.items),
		HitRatio:  hitRatio,
	}
}

// Contains checks if a key exists in the cache
func (c *LRUCache) Contains(key string) bool {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	item, exists := c.items[key]
	if !exists {
		return false
	}

	// Check if item has expired
	if item.ttl > 0 && time.Since(item.createdAt) > item.ttl {
		return false
	}

	return true
}

// Keys returns all keys in the cache
func (c *LRUCache) Keys() []string {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	keys := make([]string, 0, len(c.items))
	for key := range c.items {
		keys = append(keys, key)
	}

	return keys
}

// Size returns the current cache size in bytes
func (c *LRUCache) Size() int64 {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	return c.currentSize
}

// MaxSize returns the maximum cache size in bytes
func (c *LRUCache) MaxSize() int64 {
	return c.maxSize
}

// Len returns the number of items in the cache
func (c *LRUCache) Len() int {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	return len(c.items)
}

// Stop stops the cleanup goroutine
func (c *LRUCache) Stop() {
	close(c.stopCleanup)
}

// evictOldest removes the least recently used item
func (c *LRUCache) evictOldest() {
	element := c.evictList.Back()
	if element != nil {
		item := element.Value.(*CacheItem)
		c.removeItem(item)
	}
}

// removeItem removes an item from the cache
func (c *LRUCache) removeItem(item *CacheItem) {
	c.evictList.Remove(item.element)
	delete(c.items, item.key)
	c.currentSize -= item.size
}

// cleanup periodically removes expired items
func (c *LRUCache) cleanup() {
	ticker := time.NewTicker(c.cleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			c.mutex.Lock()
			c.cleanupExpired()
			c.mutex.Unlock()
		case <-c.stopCleanup:
			return
		}
	}
}

// cleanupExpired removes all expired items
func (c *LRUCache) cleanupExpired() {
	now := time.Now()
	for _, item := range c.items {
		if item.ttl > 0 && now.Sub(item.createdAt) > item.ttl {
			c.removeItem(item)
		}
	}
}
