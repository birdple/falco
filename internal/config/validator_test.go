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
	buckets := map[string]BucketConfig{
		"filesystem": {Type: "filesystem", Path: "/tmp/images"},
		"s3":         {Type: "s3", Bucket: "my-bucket", Region: "us-east-1"},
		"r2":         {Type: "r2", Bucket: "my-bucket", AccountID: "abc123"},
	}
	for bucketType, bucketCfg := range buckets {
		t.Run(bucketType, func(t *testing.T) {
			cfg := validConfig()
			cfg.Storage.Buckets["local"] = bucketCfg
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
		Path: "/tmp/images",
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
		Path: "/tmp/images",
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
	cfg.Storage.Buckets["backup"] = BucketConfig{Type: "s3", Bucket: "backup-bucket", Region: "us-east-1"}
	cfg.Storage.Buckets["local"] = BucketConfig{
		Type: "filesystem",
		Path: "/tmp/images",
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
	cfg.Storage.Buckets["other"] = BucketConfig{Type: "s3", Bucket: "other-bucket", Region: "us-east-1"}
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

func TestValidator_HMACRequiresAPIKey(t *testing.T) {
	cfg := validConfig()
	cfg.Security.HMACRequired = true
	cfg.Security.HMACKey = "aa"
	cfg.Security.HMACKeySalt = "bb"
	err := NewValidator().Validate(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "api_key_required")
}

func TestValidator_HMACMaterial(t *testing.T) {
	secure := func() *Config {
		cfg := validConfig()
		cfg.Security.APIKeyRequired = true
		cfg.Security.APIKey = "admin-key"
		cfg.Security.HMACRequired = true
		cfg.Security.HMACKey = "00112233"
		cfg.Security.HMACKeySalt = "44556677"
		return cfg
	}
	require.NoError(t, NewValidator().Validate(secure()))

	cases := map[string]func(*Config){
		"key not hex":      func(c *Config) { c.Security.HMACKey = "not-hex" },
		"salt not hex":     func(c *Config) { c.Security.HMACKeySalt = "zz" },
		"signature 1 byte": func(c *Config) { c.Security.HMACSignatureSize = 1 },
		"signature 15":     func(c *Config) { c.Security.HMACSignatureSize = 15 },
		"signature 33":     func(c *Config) { c.Security.HMACSignatureSize = 33 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := secure()
			mutate(cfg)
			assert.Error(t, NewValidator().Validate(cfg))
		})
	}
	for _, size := range []int{0, 16, 32} {
		cfg := secure()
		cfg.Security.HMACSignatureSize = size
		assert.NoError(t, NewValidator().Validate(cfg), "size %d", size)
	}
}

// groupConfig has a group over {a, b} with a subgroup over {a}.
func groupConfig(subKeyBuckets []string) *Config {
	cfg := validConfig()
	cfg.Storage.Buckets["a"] = BucketConfig{Type: "filesystem", Path: "/tmp/a"}
	cfg.Storage.Buckets["b"] = BucketConfig{Type: "filesystem", Path: "/tmp/b"}
	cfg.Storage.Groups = map[string]GroupConfig{
		"g": {
			Buckets: []string{"a", "b"},
			Subgroups: map[string]SubgroupConfig{
				"s": {
					Buckets: []string{"a"},
					Keys:    []GroupKeyConfig{{Name: "narrow", Key: "k-narrow", Buckets: subKeyBuckets}},
				},
			},
		},
	}
	return cfg
}

func TestValidator_SubgroupKeyBucketOutsideSubgroup(t *testing.T) {
	require.NoError(t, NewValidator().Validate(groupConfig([]string{"a"})))

	// "b" is in the group but not in the subgroup: before this check the key
	// resolved to an empty set, which used to mean "every bucket".
	err := NewValidator().Validate(groupConfig([]string{"b"}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not in subgroup")
}

func TestValidator_DuplicateScopedKeyValue(t *testing.T) {
	cfg := validConfig()
	cfg.Storage.Buckets["local"] = BucketConfig{
		Type: "filesystem", Path: "./data/images",
		Keys: []BucketKeyConfig{{Name: "one", Key: "same"}},
	}
	cfg.Storage.Buckets["other"] = BucketConfig{
		Type: "filesystem", Path: "./data/other",
		Keys: []BucketKeyConfig{{Name: "two", Key: "same"}},
	}
	err := NewValidator().Validate(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "same key value")
}

func TestValidator_ScopedKeyReusesAdminKey(t *testing.T) {
	cfg := validConfig()
	cfg.Security.APIKey = "admin"
	cfg.Storage.Buckets["local"] = BucketConfig{
		Type: "filesystem", Path: "./data/images",
		Keys: []BucketKeyConfig{{Name: "one", Key: "admin"}},
	}
	err := NewValidator().Validate(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "admin API key")
}
