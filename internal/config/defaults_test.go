package config

import (
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
)

func TestDefaultsProvider_SetDefaults(t *testing.T) {
	dp := NewDefaultsProvider()
	v := viper.New()
	dp.SetDefaults(v)

	// Server defaults
	assert.Equal(t, 8080, v.GetInt("server.port"))
	assert.Equal(t, "0.0.0.0", v.GetString("server.host"))

	// Storage defaults
	assert.Equal(t, "default", v.GetString("storage.default"))
	assert.Equal(t, "filesystem", v.GetString("storage.buckets.default.type"))
	assert.Equal(t, "./data/images", v.GetString("storage.buckets.default.path"))

	// Cache defaults
	assert.Equal(t, 256, v.GetInt("cache.size_mb"))
	assert.Equal(t, 24, v.GetInt("cache.ttl_hours"))

	// Processing defaults
	assert.Equal(t, 10, v.GetInt("processing.max_file_size_mb"))
	assert.Equal(t, 85, v.GetInt("processing.default_quality"))
	assert.Equal(t, "webp", v.GetString("processing.default_format"))
	assert.Equal(t, 4, v.GetInt("processing.concurrent_workers"))

	// Security defaults
	assert.Equal(t, false, v.GetBool("security.api_key_required"))
	assert.Equal(t, 1000, v.GetInt("security.rate_limit.requests_per_minute"))
	assert.Equal(t, 100, v.GetInt("security.rate_limit.burst"))
	assert.Equal(t, 32, v.GetInt("security.hmac_signature_size"))

	// Logging defaults
	assert.Equal(t, "info", v.GetString("logging.level"))
	assert.Equal(t, "json", v.GetString("logging.format"))
	assert.Equal(t, "stdout", v.GetString("logging.output"))

	// Development defaults
	assert.Equal(t, false, v.GetBool("development.debug"))
	assert.Equal(t, false, v.GetBool("development.enable_pprof"))
	assert.Equal(t, false, v.GetBool("development.enable_metrics"))
}
