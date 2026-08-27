package config

import (
	"fmt"
	"time"
)

// GetServerAddress returns the server address string
func (c *Config) GetServerAddress() string {
	return fmt.Sprintf("%s:%d", c.Server.Host, c.Server.Port)
}

// GetMaxFileSizeBytes returns the maximum file size in bytes
func (c *Config) GetMaxFileSizeBytes() int64 {
	return int64(c.Processing.MaxFileSizeMB) * 1024 * 1024
}

// GetCacheSizeBytes returns the cache size in bytes
func (c *Config) GetCacheSizeBytes() int64 {
	return int64(c.Cache.SizeMB) * 1024 * 1024
}

// GetCacheTTL returns the cache TTL duration
func (c *Config) GetCacheTTL() time.Duration {
	return time.Duration(c.Cache.TTLHrs) * time.Hour
}

// IsDevelopment returns true if running in development mode
func (c *Config) IsDevelopment() bool {
	return c.Development.Debug
}

// GetBucketConfig returns the bucket configuration for the given name.
// Returns an error if the bucket is not found.
func (c *Config) GetBucketConfig(name string) (*BucketConfig, error) {
	if name == "" {
		name = c.Storage.Default
	}
	bucket, ok := c.Storage.Buckets[name]
	if !ok {
		return nil, fmt.Errorf("bucket %q not found", name)
	}
	return &bucket, nil
}

// GetDefaultBucketName returns the name of the default bucket.
func (c *Config) GetDefaultBucketName() string {
	return c.Storage.Default
}

// CollectAllKeys flattens the whole scoped-key configuration into one map from
// API key to the buckets that key may touch.
//
// Three levels contribute, and they compose by INTERSECTION rather than union —
// a key can only ever narrow the scope it sits in, never widen it:
//
//   - a bucket key reaches exactly its own bucket;
//   - a group key reaches the group's buckets, or the subset it names;
//   - a subgroup key reaches the subgroup's buckets — themselves already
//     intersected with the parent group's — or the subset it names.
//
// That is what makes the configuration safe to hand-edit: naming a bucket a key
// has no business in silently drops it instead of granting it.
//
// The admin key is deliberately absent: it is not scoped, and is checked
// separately.
func (c *Config) CollectAllKeys() map[string]KeyScope {
	result := make(map[string]KeyScope)

	for bucketName, bucket := range c.Storage.Buckets {
		for _, k := range bucket.Keys {
			result[k.Key] = KeyScope{
				Name:    k.Name,
				Buckets: map[string]bool{bucketName: true},
			}
		}
	}

	for _, group := range c.Storage.Groups {
		groupBuckets := bucketSet(group.Buckets)
		addScopedKeys(result, group.Keys, groupBuckets)

		for _, sub := range group.Subgroups {
			// The subgroup's buckets are intersected with the parent's, so a
			// subgroup cannot reach outside the group that contains it.
			addScopedKeys(result, sub.Keys, intersect(bucketSet(sub.Buckets), groupBuckets))
		}
	}

	return result
}

// bucketSet turns a bucket name list into a set.
func bucketSet(names []string) map[string]bool {
	set := make(map[string]bool, len(names))
	for _, name := range names {
		set[name] = true
	}
	return set
}

// intersect returns the members of a that are also in b.
func intersect(a, b map[string]bool) map[string]bool {
	out := make(map[string]bool, len(a))
	for name := range a {
		if b[name] {
			out[name] = true
		}
	}
	return out
}

// addScopedKeys records each key against the buckets it may reach: the whole
// available set, or the subset the key names — intersected, so naming a bucket
// outside the available set grants nothing.
func addScopedKeys(result map[string]KeyScope, keys []GroupKeyConfig, available map[string]bool) {
	for _, k := range keys {
		allowed := available
		if len(k.Buckets) > 0 {
			allowed = intersect(bucketSet(k.Buckets), available)
		}
		result[k.Key] = KeyScope{Name: k.Name, Buckets: allowed}
	}
}

// KeyScope represents the resolved access scope for an API key.
type KeyScope struct {
	Name    string
	Buckets map[string]bool
}
