package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func validConfig() *Config {
	return &Config{
		Server: ServerConfig{
			Port: 8080,
			Host: "0.0.0.0",
		},
		Storage: StorageConfig{
			Primary:   "filesystem",
			Secondary: "none",
		},
		Cache: CacheConfig{
			SizeMB: 256,
		},
		Processing: ProcessingConfig{
			MaxFileSizeMB:    10,
			DefaultQuality:   85,
			SupportedFormats: []string{"jpeg", "png", "webp"},
		},
	}
}

func TestValidator_ValidConfig(t *testing.T) {
	v := NewValidator()
	cfg := validConfig()
	err := v.Validate(cfg)
	assert.NoError(t, err)
}

func TestValidator_InvalidPort(t *testing.T) {
	v := NewValidator()

	tests := []struct {
		name string
		port int
	}{
		{"zero", 0},
		{"negative", -1},
		{"too high", 70000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.Server.Port = tt.port
			err := v.Validate(cfg)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "invalid port")
		})
	}
}

func TestValidator_EmptyHost(t *testing.T) {
	v := NewValidator()
	cfg := validConfig()
	cfg.Server.Host = ""
	err := v.Validate(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "host cannot be empty")
}

func TestValidator_InvalidPrimaryStorage(t *testing.T) {
	v := NewValidator()
	cfg := validConfig()
	cfg.Storage.Primary = "invalid"
	err := v.Validate(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid primary storage")
}

func TestValidator_ValidPrimaryStorageTypes(t *testing.T) {
	v := NewValidator()
	for _, storageType := range []string{"filesystem", "s3", "minio"} {
		t.Run(storageType, func(t *testing.T) {
			cfg := validConfig()
			cfg.Storage.Primary = storageType
			err := v.Validate(cfg)
			assert.NoError(t, err)
		})
	}
}

func TestValidator_InvalidSecondaryStorage(t *testing.T) {
	v := NewValidator()
	cfg := validConfig()
	cfg.Storage.Secondary = "invalid"
	err := v.Validate(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid secondary storage")
}

func TestValidator_ValidSecondaryStorageTypes(t *testing.T) {
	v := NewValidator()
	for _, storageType := range []string{"none", "filesystem", "s3", "minio"} {
		t.Run(storageType, func(t *testing.T) {
			cfg := validConfig()
			cfg.Storage.Secondary = storageType
			err := v.Validate(cfg)
			assert.NoError(t, err)
		})
	}
}

func TestValidator_InvalidCacheSize(t *testing.T) {
	v := NewValidator()
	cfg := validConfig()
	cfg.Cache.SizeMB = 0
	err := v.Validate(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid cache size")
}

func TestValidator_InvalidMaxFileSize(t *testing.T) {
	v := NewValidator()
	cfg := validConfig()
	cfg.Processing.MaxFileSizeMB = 0
	err := v.Validate(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid max file size")
}

func TestValidator_InvalidQuality(t *testing.T) {
	v := NewValidator()

	tests := []struct {
		name    string
		quality int
	}{
		{"zero", 0},
		{"too high", 101},
		{"negative", -5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.Processing.DefaultQuality = tt.quality
			err := v.Validate(cfg)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "invalid quality")
		})
	}
}

func TestValidator_InvalidFormat(t *testing.T) {
	v := NewValidator()
	cfg := validConfig()
	cfg.Processing.SupportedFormats = []string{"jpeg", "bmp"}
	err := v.Validate(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported format")
}

func BenchmarkValidator_ValidConfig(b *testing.B) {
	v := NewValidator()
	cfg := validConfig()
	b.ResetTimer()
	for range b.N {
		v.Validate(cfg)
	}
}

func BenchmarkValidator_InvalidPort(b *testing.B) {
	v := NewValidator()
	cfg := validConfig()
	cfg.Server.Port = 0
	b.ResetTimer()
	for range b.N {
		v.Validate(cfg)
	}
}

func TestValidator_BoundaryQuality(t *testing.T) {
	v := NewValidator()

	for _, q := range []int{1, 50, 100} {
		t.Run("valid", func(t *testing.T) {
			cfg := validConfig()
			cfg.Processing.DefaultQuality = q
			assert.NoError(t, v.Validate(cfg))
		})
	}
}
