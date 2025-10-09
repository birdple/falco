package config

import (
	"fmt"
	"path/filepath"
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

// GetLocalStoragePath returns the absolute local storage path
func (c *Config) GetLocalStoragePath() string {
	if filepath.IsAbs(c.Storage.Local.Path) {
		return c.Storage.Local.Path
	}

	// Convert relative path to absolute
	absPath, _ := filepath.Abs(c.Storage.Local.Path)
	return absPath
}
