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

func TestGetLocalStoragePath_Absolute(t *testing.T) {
	cfg := &Config{
		Storage: StorageConfig{
			Local: LocalStorageConfig{Path: "/var/data/images"},
		},
	}
	assert.Equal(t, "/var/data/images", cfg.GetLocalStoragePath())
}

func TestGetLocalStoragePath_Relative(t *testing.T) {
	cfg := &Config{
		Storage: StorageConfig{
			Local: LocalStorageConfig{Path: "./data/images"},
		},
	}
	path := cfg.GetLocalStoragePath()
	// Should return an absolute path
	assert.NotEqual(t, "./data/images", path)
	assert.True(t, len(path) > 0)
}
