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
			Default: "local",
			Buckets: map[string]BucketConfig{
				"local": {Type: "filesystem", Path: "./data/images"},
			},
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

func TestValidator_NoBuckets(t *testing.T) {
	v := NewValidator()
	cfg := validConfig()
	cfg.Storage.Buckets = map[string]BucketConfig{}
	err := v.Validate(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at least one bucket")
}

func TestValidator_DefaultBucketMissing(t *testing.T) {
	v := NewValidator()
	cfg := validConfig()
	cfg.Storage.Default = "nonexistent"
	err := v.Validate(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "default bucket")
}

func TestValidator_InvalidBucketType(t *testing.T) {
	v := NewValidator()
	cfg := validConfig()
	cfg.Storage.Buckets["local"] = BucketConfig{Type: "invalid"}
	err := v.Validate(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid type")
}

func TestValidator_ValidBucketTypes(t *testing.T) {
	v := NewValidator()
	for _, bucketType := range []string{"filesystem", "s3", "minio", "r2"} {
		t.Run(bucketType, func(t *testing.T) {
			cfg := validConfig()
			cfg.Storage.Buckets["local"] = BucketConfig{Type: bucketType}
			err := v.Validate(cfg)
			assert.NoError(t, err)
		})
	}
}

func TestValidator_BackupTargetNotFound(t *testing.T) {
	v := NewValidator()
	cfg := validConfig()
	cfg.Storage.Buckets["local"] = BucketConfig{
		Type: "filesystem",
		Backups: []BackupRef{
			{Target: "nonexistent", Mode: "sync"},
		},
	}
	err := v.Validate(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestValidator_BackupSelfReference(t *testing.T) {
	v := NewValidator()
	cfg := validConfig()
	cfg.Storage.Buckets["local"] = BucketConfig{
		Type: "filesystem",
		Backups: []BackupRef{
			{Target: "local", Mode: "sync"},
		},
	}
	err := v.Validate(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot reference itself")
}

func TestValidator_ValidBackup(t *testing.T) {
	v := NewValidator()
	cfg := validConfig()
	cfg.Storage.Buckets["backup"] = BucketConfig{Type: "s3"}
	cfg.Storage.Buckets["local"] = BucketConfig{
		Type: "filesystem",
		Backups: []BackupRef{
			{Target: "backup", Mode: "sync"},
			{Target: "backup", Mode: "async"},
		},
	}
	err := v.Validate(cfg)
	assert.NoError(t, err)
}

func TestValidator_GroupBucketNotFound(t *testing.T) {
	v := NewValidator()
	cfg := validConfig()
	cfg.Storage.Groups = map[string]GroupConfig{
		"g": {Buckets: []string{"nonexistent"}},
	}
	err := v.Validate(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-existent bucket")
}

func TestValidator_SubgroupBucketNotInParent(t *testing.T) {
	v := NewValidator()
	cfg := validConfig()
	cfg.Storage.Buckets["other"] = BucketConfig{Type: "s3"}
	cfg.Storage.Groups = map[string]GroupConfig{
		"g": {
			Buckets: []string{"local"},
			Subgroups: map[string]SubgroupConfig{
				"sub": {Buckets: []string{"other"}},
			},
		},
	}
	err := v.Validate(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not in parent group")
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
