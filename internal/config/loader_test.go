package config

import (
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewLoader(t *testing.T) {
	l := NewLoader()
	require.NotNil(t, l)
}

func TestSetEnvValue_IntKeys(t *testing.T) {
	l := &loader{defaults: NewDefaultsProvider(), validator: NewValidator()}
	v := viper.New()

	require.NoError(t, l.setEnvValue(v, "server.port", "9090"))
	assert.Equal(t, 9090, v.GetInt("server.port"))

	require.NoError(t, l.setEnvValue(v, "cache.size_mb", "512"))
	assert.Equal(t, 512, v.GetInt("cache.size_mb"))

	// An invalid int is an error, and leaves the previous value alone.
	assert.Error(t, l.setEnvValue(v, "server.port", "not-a-number"))
	assert.Equal(t, 9090, v.GetInt("server.port"))
}

func TestSetEnvValue_BoolKeys(t *testing.T) {
	l := &loader{defaults: NewDefaultsProvider(), validator: NewValidator()}
	v := viper.New()

	require.NoError(t, l.setEnvValue(v, "security.api_key_required", "true"))
	assert.True(t, v.GetBool("security.api_key_required"))

	require.NoError(t, l.setEnvValue(v, "development.debug", "false"))
	assert.False(t, v.GetBool("development.debug"))

	// An invalid bool is an error: API_KEY_REQUIRED=yes must not boot with
	// auth silently off.
	assert.Error(t, l.setEnvValue(v, "security.api_key_required", "yes"))
	assert.True(t, v.GetBool("security.api_key_required"))
}

func TestSetEnvValue_CORSOrigins(t *testing.T) {
	l := &loader{defaults: NewDefaultsProvider(), validator: NewValidator()}
	v := viper.New()

	require.NoError(t, l.setEnvValue(v, "security.cors.origins", "http://localhost:3000,https://example.com"))
	origins := v.GetStringSlice("security.cors.origins")
	assert.Len(t, origins, 2)
	assert.Equal(t, "http://localhost:3000", origins[0])
	assert.Equal(t, "https://example.com", origins[1])
}

func TestSetEnvValue_StringKeys(t *testing.T) {
	l := &loader{defaults: NewDefaultsProvider(), validator: NewValidator()}
	v := viper.New()

	require.NoError(t, l.setEnvValue(v, "storage.default", "images"))
	assert.Equal(t, "images", v.GetString("storage.default"))

	require.NoError(t, l.setEnvValue(v, "logging.level", "debug"))
	assert.Equal(t, "debug", v.GetString("logging.level"))
}

func TestGetEnvMappings(t *testing.T) {
	l := &loader{defaults: NewDefaultsProvider(), validator: NewValidator()}
	mappings := l.getEnvMappings()

	// Verify some key mappings exist
	assert.Equal(t, "server.port", mappings["PORT"])
	assert.Equal(t, "server.host", mappings["HOST"])
	assert.Equal(t, "storage.default", mappings["STORAGE_DEFAULT"])
	assert.Equal(t, "security.api_key", mappings["API_KEY"])
	assert.Equal(t, "logging.level", mappings["LOG_LEVEL"])
	assert.Equal(t, "security.hmac_key", mappings["HMAC_KEY"])
}

func TestLoadFromFile_NoConfigFile(t *testing.T) {
	l := &loader{defaults: NewDefaultsProvider(), validator: NewValidator()}
	v := viper.New()
	// Should not error when no config file is found
	err := l.loadFromFile(v)
	assert.NoError(t, err)
}

func TestLoadFromEnv_InvalidValuesAreReported(t *testing.T) {
	t.Setenv("API_KEY_REQUIRED", "yes")
	t.Setenv("RATE_LIMIT_RPM", "1k")

	l := &loader{defaults: NewDefaultsProvider(), validator: NewValidator()}
	err := l.loadFromEnv(viper.New())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "API_KEY_REQUIRED")
	assert.Contains(t, err.Error(), "RATE_LIMIT_RPM")
}

func TestLoadFromEnv_InvalidPoolSizeIsReported(t *testing.T) {
	t.Setenv("STORAGE_BUCKET_MAIN_TYPE", "jay")
	t.Setenv("STORAGE_BUCKET_MAIN_POOL_SIZE", "four")

	l := &loader{defaults: NewDefaultsProvider(), validator: NewValidator()}
	err := l.loadFromEnv(viper.New())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "STORAGE_BUCKET_MAIN_POOL_SIZE")
}
