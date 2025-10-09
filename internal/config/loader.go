package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
	"github.com/spf13/viper"
)

// Loader defines the interface for configuration loading
type Loader interface {
	Load() (*Config, error)
}

// loader implements configuration loading
type loader struct {
	defaults  DefaultSetter
	validator Validator
}

// NewLoader creates a new configuration loader
func NewLoader() Loader {
	return &loader{
		defaults:  NewDefaultsProvider(),
		validator: NewValidator(),
	}
}

// Load loads configuration from various sources
func (l *loader) Load() (*Config, error) {
	// Load .env file if present
	if err := godotenv.Load(); err != nil {
		// .env file is optional
		fmt.Println("No .env file found, using system environment variables")
	}

	v := viper.New()

	// Set defaults
	l.defaults.SetDefaults(v)

	// Load from config file
	if err := l.loadFromFile(v); err != nil {
		return nil, fmt.Errorf("failed to load config file: %w", err)
	}

	// Load from environment variables (overrides config file)
	l.loadFromEnv(v)

	// Unmarshal into config struct
	var config Config
	if err := v.Unmarshal(&config); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	// Validate configuration
	if err := l.validator.Validate(&config); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	return &config, nil
}

// loadFromFile loads configuration from YAML file
func (l *loader) loadFromFile(v *viper.Viper) error {
	v.SetConfigName("config")
	v.SetConfigType("yaml")
	v.AddConfigPath("./configs")
	v.AddConfigPath(".")

	// Config file is optional
	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return err
		}
	}

	return nil
}

// loadFromEnv loads configuration from environment variables
func (l *loader) loadFromEnv(v *viper.Viper) {
	envMappings := l.getEnvMappings()

	for envVar, configKey := range envMappings {
		if value := os.Getenv(envVar); value != "" {
			l.setEnvValue(v, configKey, value)
		}
	}
}

// getEnvMappings returns environment variable to config key mappings
func (l *loader) getEnvMappings() map[string]string {
	return map[string]string{
		"PORT":                     "server.port",
		"HOST":                     "server.host",
		"ENV":                      "development.debug",
		"STORAGE_PRIMARY":          "storage.primary",
		"STORAGE_SECONDARY":        "storage.secondary",
		"STORAGE_LOCAL_PATH":       "storage.local.path",
		"STORAGE_S3_BUCKET":        "storage.s3.bucket",
		"STORAGE_S3_REGION":        "storage.s3.region",
		"STORAGE_S3_ACCESS_KEY":    "storage.s3.access_key",
		"STORAGE_S3_SECRET_KEY":    "storage.s3.secret_key",
		"STORAGE_MINIO_BUCKET":     "storage.minio.bucket",
		"STORAGE_MINIO_ENDPOINT":   "storage.minio.endpoint",
		"STORAGE_MINIO_REGION":     "storage.minio.region",
		"STORAGE_MINIO_ACCESS_KEY": "storage.minio.access_key",
		"STORAGE_MINIO_SECRET_KEY": "storage.minio.secret_key",
		"STORAGE_MINIO_SECURE":     "storage.minio.secure",
		"CACHE_SIZE_MB":            "cache.size_mb",
		"CACHE_TTL_HOURS":          "cache.ttl_hours",
		"MAX_FILE_SIZE_MB":         "processing.max_file_size_mb",
		"DEFAULT_QUALITY":          "processing.default_quality",
		"DEFAULT_FORMAT":           "processing.default_format",
		"CONCURRENT_WORKERS":       "processing.concurrent_workers",
		"API_KEY_REQUIRED":         "security.api_key_required",
		"API_KEY":                  "security.api_key",
		"CORS_ORIGINS":             "security.cors.origins",
		"RATE_LIMIT_RPM":           "security.rate_limit.requests_per_minute",
		"LOG_LEVEL":                "logging.level",
		"LOG_FORMAT":               "logging.format",
		"LOG_OUTPUT":               "logging.output",
		"DEBUG":                    "development.debug",
		"ENABLE_PPROF":             "development.enable_pprof",
	}
}

// setEnvValue sets a configuration value from environment variable
func (l *loader) setEnvValue(v *viper.Viper, key, value string) {
	intKeys := map[string]bool{
		"server.port":                             true,
		"cache.size_mb":                           true,
		"cache.ttl_hours":                         true,
		"processing.max_file_size_mb":             true,
		"processing.default_quality":              true,
		"processing.concurrent_workers":           true,
		"security.rate_limit.requests_per_minute": true,
		"processing.max_dimensions.width":         true,
		"processing.max_dimensions.height":        true,
	}

	boolKeys := map[string]bool{
		"security.api_key_required":  true,
		"development.debug":          true,
		"development.enable_pprof":   true,
		"development.enable_metrics": true,
		"storage.minio.secure":       true,
	}

	switch {
	case intKeys[key]:
		if intVal, err := strconv.Atoi(value); err == nil {
			v.Set(key, intVal)
		}
	case boolKeys[key]:
		if boolVal, err := strconv.ParseBool(value); err == nil {
			v.Set(key, boolVal)
		}
	case key == "security.cors.origins":
		v.Set(key, strings.Split(value, ","))
	default:
		v.Set(key, value)
	}
}
