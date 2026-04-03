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
}

// StorageConfig holds storage-related configuration
type StorageConfig struct {
	Primary   string             `mapstructure:"primary"`
	Secondary string             `mapstructure:"secondary"`
	Local     LocalStorageConfig `mapstructure:"local"`
	S3        S3StorageConfig    `mapstructure:"s3"`
	MinIO     MinIOStorageConfig `mapstructure:"minio"`
}

// LocalStorageConfig holds local filesystem storage configuration
type LocalStorageConfig struct {
	Path       string `mapstructure:"path"`
	CreateDirs bool   `mapstructure:"create_dirs"`
}

// S3StorageConfig holds S3 storage configuration
type S3StorageConfig struct {
	Bucket    string `mapstructure:"bucket"`
	Region    string `mapstructure:"region"`
	Endpoint  string `mapstructure:"endpoint"`
	AccessKey string `mapstructure:"access_key"`
	SecretKey string `mapstructure:"secret_key"`
}

// MinIOStorageConfig holds MinIO storage configuration
type MinIOStorageConfig struct {
	Bucket    string `mapstructure:"bucket"`
	Endpoint  string `mapstructure:"endpoint"`
	Region    string `mapstructure:"region"`
	AccessKey string `mapstructure:"access_key"`
	SecretKey string `mapstructure:"secret_key"`
	Secure    bool   `mapstructure:"secure"`
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
	MaxFileSizeMB     int      `mapstructure:"max_file_size_mb"`
	DefaultQuality    int      `mapstructure:"default_quality"`
	DefaultFormat     string   `mapstructure:"default_format"`
	ConcurrentWorkers int      `mapstructure:"concurrent_workers"`
	SupportedFormats  []string `mapstructure:"supported_formats"`
	MaxDimensions     struct {
		Width  int `mapstructure:"width"`
		Height int `mapstructure:"height"`
	} `mapstructure:"max_dimensions"`
}

// SecurityConfig holds security-related configuration
type SecurityConfig struct {
	APIKeyRequired bool   `mapstructure:"api_key_required"`
	APIKey         string `mapstructure:"api_key"`
	CORS           struct {
		Origins []string `mapstructure:"origins"`
		Methods []string `mapstructure:"methods"`
		Headers []string `mapstructure:"headers"`
	} `mapstructure:"cors"`
	RateLimit struct {
		RequestsPerMinute int `mapstructure:"requests_per_minute"`
		Burst             int `mapstructure:"burst"`
	} `mapstructure:"rate_limit"`
	TrustedProxies []string `mapstructure:"trusted_proxies"`
	// HMAC URL signing
	HMACKey           string `mapstructure:"hmac_key"`
	HMACKeySalt       string `mapstructure:"hmac_salt"`
	HMACSignatureSize int    `mapstructure:"hmac_signature_size"` // bytes, default 32
	HMACRequired      bool   `mapstructure:"hmac_required"`       // if false, signature is optional
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
