package config

import "time"

// Config holds all configuration for the application
type Config struct {
	Server      ServerConfig      `mapstructure:"server"`
	Storage     StorageConfig     `mapstructure:"storage"`
	Cache       CacheConfig       `mapstructure:"cache"`
	Processing  ProcessingConfig  `mapstructure:"processing"`
	Security    SecurityConfig    `mapstructure:"security"`
	Logging     LoggingConfig     `mapstructure:"logging"`
	Development DevelopmentConfig `mapstructure:"development"`
}

// ServerConfig holds server-related configuration
type ServerConfig struct {
	Port            int           `mapstructure:"port"`
	Host            string        `mapstructure:"host"`
	ReadTimeout     time.Duration `mapstructure:"read_timeout"`
	WriteTimeout    time.Duration `mapstructure:"write_timeout"`
	IdleTimeout     time.Duration `mapstructure:"idle_timeout"`
	ShutdownTimeout time.Duration `mapstructure:"shutdown_timeout"`
	// MaxHeaderBytes caps the header bytes accepted per request. Left unset
	// the stdlib allows 1 MB, which for a public image CDN is far more than
	// any legitimate client needs.
	MaxHeaderBytes int `mapstructure:"max_header_bytes"`
	// MaxHeaderValueCount caps the NUMBER of header values
	// (net/http, Go 1.27). Complementa a MaxHeaderBytes: miles de cabeceras
	// tiny headers weigh almost nothing in bytes but are expensive to parse.
	MaxHeaderValueCount int `mapstructure:"max_header_value_count"`
}

// StorageConfig holds the unified storage configuration based on bucket groups.
type StorageConfig struct {
	Default string                  `mapstructure:"default"`
	Buckets map[string]BucketConfig `mapstructure:"buckets"`
	Groups  map[string]GroupConfig  `mapstructure:"groups"`
}

// BucketConfig holds configuration for a named storage bucket.
type BucketConfig struct {
	Type      string            `mapstructure:"type"`       // s3, minio, r2, filesystem, jay
	Path      string            `mapstructure:"path"`       // filesystem only
	Bucket    string            `mapstructure:"bucket"`     // s3/minio/r2/jay bucket name
	Region    string            `mapstructure:"region"`     // s3/minio
	Endpoint  string            `mapstructure:"endpoint"`   // s3/minio
	AccountID string            `mapstructure:"account_id"` // r2
	AccessKey string            `mapstructure:"access_key"`
	SecretKey string            `mapstructure:"secret_key"`
	Secure    bool              `mapstructure:"secure"`  // minio
	Backups   []BackupRef       `mapstructure:"backups"` // backup targets
	Keys      []BucketKeyConfig `mapstructure:"keys"`    // bucket-level API keys
	// Jay-specific fields
	JayAddr      string `mapstructure:"addr"`         // native protocol address, e.g. "jay:4012"
	JayAdminAddr string `mapstructure:"admin_addr"`   // HTTP address for GetStats, e.g. "jay:4010"
	JayTokenID   string `mapstructure:"token_id"`     // jay token id
	JayTokenSec  string `mapstructure:"token_secret"` // jay token secret
	JayPoolSize  int    `mapstructure:"pool_size"`    // connection pool size
}

// BucketKeyConfig defines an API key scoped to a specific bucket.
type BucketKeyConfig struct {
	Name string `mapstructure:"name"`
	Key  string `mapstructure:"key"`
}

// BackupRef references another bucket as a backup target.
type BackupRef struct {
	Target string `mapstructure:"target"` // name of another bucket
	Mode   string `mapstructure:"mode"`   // sync, async, read-fallback
}

// GroupConfig defines a logical group of buckets with shared keys.
type GroupConfig struct {
	Buckets   []string                  `mapstructure:"buckets"`
	Keys      []GroupKeyConfig          `mapstructure:"keys"`
	Subgroups map[string]SubgroupConfig `mapstructure:"subgroups"`
}

// SubgroupConfig defines a subgroup within a group (max 1 level deep).
// Inherits keys from the parent group.
type SubgroupConfig struct {
	Buckets []string         `mapstructure:"buckets"`
	Keys    []GroupKeyConfig `mapstructure:"keys"`
}

// GroupKeyConfig defines an API key scoped to a group or subgroup.
type GroupKeyConfig struct {
	Name    string   `mapstructure:"name"`
	Key     string   `mapstructure:"key"`
	Buckets []string `mapstructure:"buckets"` // optional: restrict to subset of group buckets
}

// CacheConfig holds cache-related configuration
type CacheConfig struct {
	SizeMB          int           `mapstructure:"size_mb"`
	TTLHrs          int           `mapstructure:"ttl_hours"`
	CleanupInterval time.Duration `mapstructure:"cleanup_interval"`
	DefaultMaxAge   int           `mapstructure:"default_max_age"`
	DefaultSMaxAge  int           `mapstructure:"default_smax_age"`
	EnableRedis     bool          `mapstructure:"enable_redis"`
	RedisURL        string        `mapstructure:"redis_url"`
}

// ProcessingConfig holds image processing configuration
type ProcessingConfig struct {
	MaxFileSizeMB     int           `mapstructure:"max_file_size_mb"`
	DefaultQuality    int           `mapstructure:"default_quality"`
	DefaultFormat     string        `mapstructure:"default_format"`
	ConcurrentWorkers int           `mapstructure:"concurrent_workers"`
	WebPEffort        int           `mapstructure:"webp_effort"`
	SupportedFormats  []string      `mapstructure:"supported_formats"`
	MaxDimensions     MaxDimensions `mapstructure:"max_dimensions"`
}

// MaxDimensions caps the output size. It is a named type rather than an
// anonymous struct so it can be built in a literal: anonymous, the only way to
// populate it was field-by-field assignment after creating the Config.
type MaxDimensions struct {
	Width  int `mapstructure:"width"`
	Height int `mapstructure:"height"`
}

// SecurityConfig holds security-related configuration
type SecurityConfig struct {
	APIKeyRequired bool            `mapstructure:"api_key_required"`
	APIKey         string          `mapstructure:"api_key"`
	CORS           CORSConfig      `mapstructure:"cors"`
	RateLimit      RateLimitConfig `mapstructure:"rate_limit"`
	TrustedProxies []string        `mapstructure:"trusted_proxies"`
	// HMAC URL signing
	HMACKey           string `mapstructure:"hmac_key"`
	HMACKeySalt       string `mapstructure:"hmac_salt"`
	HMACSignatureSize int    `mapstructure:"hmac_signature_size"`
	HMACRequired      bool   `mapstructure:"hmac_required"`
}

// CORSConfig and RateLimitConfig are named types for the same reason as
// MaxDimensions: como structs anónimos no se podían escribir en un literal.

// CORSConfig holds the CORS policy.
type CORSConfig struct {
	Origins []string `mapstructure:"origins"`
	Methods []string `mapstructure:"methods"`
	Headers []string `mapstructure:"headers"`
}

// RateLimitConfig holds the per-IP rate limit.
type RateLimitConfig struct {
	RequestsPerMinute int `mapstructure:"requests_per_minute"`
	Burst             int `mapstructure:"burst"`
}

// LoggingConfig holds logging configuration
type LoggingConfig struct {
	Level        string `mapstructure:"level"`
	Format       string `mapstructure:"format"`
	Output       string `mapstructure:"output"`
	EnableCaller bool   `mapstructure:"enable_caller"`
}

// DevelopmentConfig holds development-specific configuration
type DevelopmentConfig struct {
	Debug         bool `mapstructure:"debug"`
	EnablePprof   bool `mapstructure:"enable_pprof"`
	EnableMetrics bool `mapstructure:"enable_metrics"`
}
