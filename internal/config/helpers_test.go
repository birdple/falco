package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestGetServerAddress(t *testing.T) {
	cfg := &Config{
		Server: ServerConfig{Host: "0.0.0.0", Port: 8080},
	}
	assert.Equal(t, "0.0.0.0:8080", cfg.GetServerAddress())

	cfg.Server.Host = "127.0.0.1"
	cfg.Server.Port = 3000
	assert.Equal(t, "127.0.0.1:3000", cfg.GetServerAddress())
}

func TestGetMaxFileSizeBytes(t *testing.T) {
	cfg := &Config{
		Processing: ProcessingConfig{MaxFileSizeMB: 10},
	}
	assert.Equal(t, int64(10*1024*1024), cfg.GetMaxFileSizeBytes())

	cfg.Processing.MaxFileSizeMB = 1
	assert.Equal(t, int64(1024*1024), cfg.GetMaxFileSizeBytes())
}

func TestGetCacheSizeBytes(t *testing.T) {
	cfg := &Config{
		Cache: CacheConfig{SizeMB: 256},
	}
	assert.Equal(t, int64(256*1024*1024), cfg.GetCacheSizeBytes())
}

func TestGetCacheTTL(t *testing.T) {
	cfg := &Config{
		Cache: CacheConfig{TTLHrs: 24},
	}
	assert.Equal(t, 24*time.Hour, cfg.GetCacheTTL())

	cfg.Cache.TTLHrs = 1
	assert.Equal(t, time.Hour, cfg.GetCacheTTL())
}

func TestIsDevelopment(t *testing.T) {
	cfg := &Config{}
	assert.False(t, cfg.IsDevelopment())

	cfg.Development.Debug = true
	assert.True(t, cfg.IsDevelopment())
}

func TestGetBucketConfig(t *testing.T) {
	cfg := &Config{
		Storage: StorageConfig{
			Default: "local",
			Buckets: map[string]BucketConfig{
				"local": {Type: "filesystem", Path: "./data/images"},
				"s3":    {Type: "s3", Bucket: "my-bucket"},
			},
		},
	}

	// Get by name
	bc, err := cfg.GetBucketConfig("local")
	assert.NoError(t, err)
	assert.Equal(t, "filesystem", bc.Type)

	// Get default (empty name)
	bc, err = cfg.GetBucketConfig("")
	assert.NoError(t, err)
	assert.Equal(t, "filesystem", bc.Type)

	// Not found
	_, err = cfg.GetBucketConfig("nonexistent")
	assert.Error(t, err)
}

func TestCollectAllKeys(t *testing.T) {
	cfg := &Config{
		Storage: StorageConfig{
			Default: "images",
			Buckets: map[string]BucketConfig{
				"images": {
					Type: "s3",
					Keys: []BucketKeyConfig{
						{Name: "client-a", Key: "sk-a"},
					},
				},
				"backups": {Type: "minio"},
			},
			Groups: map[string]GroupConfig{
				"media": {
					Buckets: []string{"images", "backups"},
					Keys: []GroupKeyConfig{
						{Name: "media-team", Key: "sk-media"},
					},
					Subgroups: map[string]SubgroupConfig{
						"thumbs": {
							Buckets: []string{"images"},
							Keys: []GroupKeyConfig{
								{Name: "thumb-svc", Key: "sk-thumb"},
							},
						},
					},
				},
			},
		},
	}

	keys := cfg.CollectAllKeys()

	// Bucket-level key: access to "images" only
	assert.Contains(t, keys, "sk-a")
	assert.True(t, keys["sk-a"].Buckets["images"])
	assert.False(t, keys["sk-a"].Buckets["backups"])

	// Group-level key: access to all group buckets
	assert.Contains(t, keys, "sk-media")
	assert.True(t, keys["sk-media"].Buckets["images"])
	assert.True(t, keys["sk-media"].Buckets["backups"])

	// Subgroup-level key: access to subgroup buckets only
	assert.Contains(t, keys, "sk-thumb")
	assert.True(t, keys["sk-thumb"].Buckets["images"])
	assert.False(t, keys["sk-thumb"].Buckets["backups"])
}

func TestCollectAllKeys_GroupKeyWithBucketRestriction(t *testing.T) {
	cfg := &Config{
		Storage: StorageConfig{
			Default: "images",
			Buckets: map[string]BucketConfig{
				"images":  {Type: "s3"},
				"backups": {Type: "minio"},
			},
			Groups: map[string]GroupConfig{
				"media": {
					Buckets: []string{"images", "backups"},
					Keys: []GroupKeyConfig{
						{Name: "restricted", Key: "sk-restricted", Buckets: []string{"images"}},
					},
				},
			},
		},
	}

	keys := cfg.CollectAllKeys()
	assert.True(t, keys["sk-restricted"].Buckets["images"])
	assert.False(t, keys["sk-restricted"].Buckets["backups"])
}
