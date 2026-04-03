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

	l.setEnvValue(v, "server.port", "9090")
	assert.Equal(t, 9090, v.GetInt("server.port"))

	l.setEnvValue(v, "cache.size_mb", "512")
	assert.Equal(t, 512, v.GetInt("cache.size_mb"))

	// Invalid int should not be set
	l.setEnvValue(v, "server.port", "not-a-number")
	assert.Equal(t, 9090, v.GetInt("server.port")) // unchanged
}

func TestSetEnvValue_BoolKeys(t *testing.T) {
	l := &loader{defaults: NewDefaultsProvider(), validator: NewValidator()}
	v := viper.New()

	l.setEnvValue(v, "security.api_key_required", "true")
	assert.True(t, v.GetBool("security.api_key_required"))

	l.setEnvValue(v, "development.debug", "false")
	assert.False(t, v.GetBool("development.debug"))

	// Invalid bool should not be set
	l.setEnvValue(v, "development.debug", "not-a-bool")
	assert.False(t, v.GetBool("development.debug"))
}

func TestSetEnvValue_CORSOrigins(t *testing.T) {
	l := &loader{defaults: NewDefaultsProvider(), validator: NewValidator()}
	v := viper.New()

	l.setEnvValue(v, "security.cors.origins", "http://localhost:3000,https://example.com")
	origins := v.GetStringSlice("security.cors.origins")
	assert.Len(t, origins, 2)
	assert.Equal(t, "http://localhost:3000", origins[0])
	assert.Equal(t, "https://example.com", origins[1])
}

func TestSetEnvValue_StringKeys(t *testing.T) {
	l := &loader{defaults: NewDefaultsProvider(), validator: NewValidator()}
	v := viper.New()

	l.setEnvValue(v, "storage.default", "images")
	assert.Equal(t, "images", v.GetString("storage.default"))

	l.setEnvValue(v, "logging.level", "debug")
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
