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

// CollectAllKeys builds a flat map of API key -> allowed bucket names
// by resolving bucket keys, group keys (with inheritance), and subgroup keys.
// Admin key is not included; it is handled separately.
func (c *Config) CollectAllKeys() map[string]KeyScope {
	result := make(map[string]KeyScope)

	// Bucket-level keys: each key gets access to that bucket only
	for bucketName, bucket := range c.Storage.Buckets {
		for _, k := range bucket.Keys {
			result[k.Key] = KeyScope{
				Name:    k.Name,
				Buckets: map[string]bool{bucketName: true},
			}
		}
	}

	// Group-level keys
	for _, group := range c.Storage.Groups {
		groupBucketSet := make(map[string]bool, len(group.Buckets))
		for _, b := range group.Buckets {
			groupBucketSet[b] = true
		}

		for _, k := range group.Keys {
			allowed := groupBucketSet
			if len(k.Buckets) > 0 {
				// Restrict to specified subset
				allowed = make(map[string]bool, len(k.Buckets))
				for _, b := range k.Buckets {
					if groupBucketSet[b] {
						allowed[b] = true
					}
				}
			}
			result[k.Key] = KeyScope{
				Name:    k.Name,
				Buckets: allowed,
			}
		}

		// Subgroup keys: inherit parent group buckets as the superset,
		// but restrict to subgroup's own bucket list
		for _, sub := range group.Subgroups {
			subBucketSet := make(map[string]bool, len(sub.Buckets))
			for _, b := range sub.Buckets {
				if groupBucketSet[b] {
					subBucketSet[b] = true
				}
			}

			for _, k := range sub.Keys {
				allowed := subBucketSet
				if len(k.Buckets) > 0 {
					allowed = make(map[string]bool, len(k.Buckets))
					for _, b := range k.Buckets {
						if subBucketSet[b] {
							allowed[b] = true
						}
					}
				}
				result[k.Key] = KeyScope{
					Name:    k.Name,
					Buckets: allowed,
				}
			}
		}
	}

	return result
}

// KeyScope represents the resolved access scope for an API key.
type KeyScope struct {
	Name    string
	Buckets map[string]bool
}
