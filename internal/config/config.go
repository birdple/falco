package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/viper"
)

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

// CacheConfig holds cache-related configuration
type CacheConfig struct {
	SizeMB          int           `mapstructure:"size_mb"`
	TTLHrs          int           `mapstructure:"ttl_hours"`
	CleanupInterval time.Duration `mapstructure:"cleanup_interval"`
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
	Honeypot HoneypotConfig `mapstructure:"honeypot"`
}

// HoneypotConfig holds honeypot-specific configuration
type HoneypotConfig struct {
	DBPath       string `mapstructure:"db_path"`
	BanThreshold int    `mapstructure:"ban_threshold"`
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

// Load loads configuration from various sources
func Load() (*Config, error) {
	v := viper.New()

	// Set defaults
	setDefaults(v)

	// Load from config file
	if err := loadFromFile(v); err != nil {
		return nil, fmt.Errorf("failed to load config file: %w", err)
	}

	// Load from environment variables
	loadFromEnv(v)

	// Unmarshal into config struct
	var config Config
	if err := v.Unmarshal(&config); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	// Validate configuration
	if err := validateConfig(&config); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	return &config, nil
}

// setDefaults sets default configuration values
func setDefaults(v *viper.Viper) {
	// Server defaults
	v.SetDefault("server.port", 8080)
	v.SetDefault("server.host", "0.0.0.0")
	v.SetDefault("server.read_timeout", "30s")
	v.SetDefault("server.write_timeout", "30s")
	v.SetDefault("server.idle_timeout", "60s")
	v.SetDefault("server.shutdown_timeout", "30s")

	// Storage defaults
	v.SetDefault("storage.primary", "filesystem")
	v.SetDefault("storage.secondary", "none")
	v.SetDefault("storage.local.path", "./data/images")
	v.SetDefault("storage.local.create_dirs", true)

	// Cache defaults
	v.SetDefault("cache.size_mb", 256)
	v.SetDefault("cache.ttl_hours", 24)
	v.SetDefault("cache.cleanup_interval", "10m")

	// Processing defaults
	v.SetDefault("processing.max_file_size_mb", 10)
	v.SetDefault("processing.default_quality", 85)
	v.SetDefault("processing.default_format", "webp")
	v.SetDefault("processing.concurrent_workers", 4)
	v.SetDefault("processing.supported_formats", []string{"jpeg", "png", "webp"})
	v.SetDefault("processing.max_dimensions.width", 4000)
	v.SetDefault("processing.max_dimensions.height", 4000)

	// Security defaults
	v.SetDefault("security.api_key_required", false)
	v.SetDefault("security.cors.origins", []string{"*"})
	v.SetDefault("security.cors.methods", []string{"GET", "POST", "OPTIONS"})
	v.SetDefault("security.cors.headers", []string{"Content-Type", "Authorization"})
	v.SetDefault("security.rate_limit.requests_per_minute", 1000)
	v.SetDefault("security.rate_limit.burst", 100)
	v.SetDefault("security.honeypot.db_path", "data/honeypot.db")
	v.SetDefault("security.honeypot.ban_threshold", 3)
	// Logging defaults
	v.SetDefault("logging.level", "info")
	v.SetDefault("logging.format", "json")
	v.SetDefault("logging.output", "stdout")
	v.SetDefault("logging.enable_caller", false)

	// Development defaults
	v.SetDefault("development.debug", false)
	v.SetDefault("development.enable_pprof", false)
	v.SetDefault("development.enable_metrics", false)
}

// loadFromFile loads configuration from YAML file
func loadFromFile(v *viper.Viper) error {
	v.SetConfigName("config")
	v.SetConfigType("yaml")
	v.AddConfigPath("./configs")
	v.AddConfigPath(".")

	// Try to read config file
	if err := v.ReadInConfig(); err != nil {
		// Config file is optional, so ignore if not found
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return err
		}
	}

	return nil
}

// loadFromEnv loads configuration from environment variables
func loadFromEnv(v *viper.Viper) {
	// Environment variable mappings
	envMappings := map[string]string{
		"PORT":                  "server.port",
		"HOST":                  "server.host",
		"ENV":                   "development.debug",
		"STORAGE_PRIMARY":       "storage.primary",
		"STORAGE_SECONDARY":     "storage.secondary",
		"STORAGE_LOCAL_PATH":    "storage.local.path",
		"STORAGE_S3_BUCKET":     "storage.s3.bucket",
		"STORAGE_S3_REGION":     "storage.s3.region",
		"STORAGE_S3_ACCESS_KEY": "storage.s3.access_key",
		"STORAGE_S3_SECRET_KEY": "storage.s3.secret_key",
		"CACHE_SIZE_MB":         "cache.size_mb",
		"CACHE_TTL_HOURS":       "cache.ttl_hours",
		"MAX_FILE_SIZE_MB":      "processing.max_file_size_mb",
		"DEFAULT_QUALITY":       "processing.default_quality",
		"DEFAULT_FORMAT":        "processing.default_format",
		"CONCURRENT_WORKERS":    "processing.concurrent_workers",
		"API_KEY_REQUIRED":      "security.api_key_required",
		"API_KEY":               "security.api_key",
		"CORS_ORIGINS":          "security.cors.origins",
		"RATE_LIMIT_RPM":        "security.rate_limit.requests_per_minute",
		"LOG_LEVEL":             "logging.level",
		"LOG_FORMAT":            "logging.format",
		"LOG_OUTPUT":            "logging.output",
		"DEBUG":                 "development.debug",
		"ENABLE_PPROF":          "development.enable_pprof",
	}

	for envVar, configKey := range envMappings {
		if value := os.Getenv(envVar); value != "" {
			setEnvValue(v, configKey, value)
		}
	}
}

// setEnvValue sets a configuration value from environment variable
func setEnvValue(v *viper.Viper, key, value string) {
	switch key {
	case "server.port", "cache.size_mb", "cache.ttl_hours",
		"processing.max_file_size_mb", "processing.default_quality",
		"processing.concurrent_workers", "security.rate_limit.requests_per_minute",
		"processing.max_dimensions.width", "processing.max_dimensions.height":
		if intVal, err := strconv.Atoi(value); err == nil {
			v.Set(key, intVal)
		}
	case "security.api_key_required", "development.debug", "development.enable_pprof", "development.enable_metrics":
		if boolVal, err := strconv.ParseBool(value); err == nil {
			v.Set(key, boolVal)
		}
	case "security.cors.origins":
		// Handle comma-separated values
		v.Set(key, strings.Split(value, ","))
	default:
		v.Set(key, value)
	}
}

// validateConfig validates the loaded configuration
func validateConfig(config *Config) error {
	// Validate server configuration
	if config.Server.Port < 1 || config.Server.Port > 65535 {
		return fmt.Errorf("invalid server port: %d", config.Server.Port)
	}

	// Validate storage configuration
	if config.Storage.Primary != "filesystem" && config.Storage.Primary != "s3" {
		return fmt.Errorf("invalid storage primary: %s", config.Storage.Primary)
	}

	if config.Storage.Secondary != "none" && config.Storage.Secondary != "filesystem" && config.Storage.Secondary != "s3" {
		return fmt.Errorf("invalid storage secondary: %s", config.Storage.Secondary)
	}

	// Validate cache configuration
	if config.Cache.SizeMB < 1 {
		return fmt.Errorf("invalid cache size: %d MB", config.Cache.SizeMB)
	}

	// Validate processing configuration
	if config.Processing.MaxFileSizeMB < 1 {
		return fmt.Errorf("invalid max file size: %d MB", config.Processing.MaxFileSizeMB)
	}

	if config.Processing.DefaultQuality < 1 || config.Processing.DefaultQuality > 100 {
		return fmt.Errorf("invalid default quality: %d", config.Processing.DefaultQuality)
	}

	// Validate supported formats
	validFormats := map[string]bool{"jpeg": true, "png": true, "webp": true}
	for _, format := range config.Processing.SupportedFormats {
		if !validFormats[format] {
			return fmt.Errorf("unsupported format: %s", format)
		}
	}

	return nil
}

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
