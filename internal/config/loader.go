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
	// Load .env file if present (optional)
	_ = godotenv.Load()

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

	// Auto-discover named backends from STORAGE_BACKEND_<NAME>_TYPE env vars
	l.discoverBackendsFromEnv(v)

	// Auto-discover scoped API keys from SCOPED_KEY_<NAME>_KEY env vars
	l.discoverScopedKeysFromEnv(v)
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
		"STORAGE_R2_BUCKET":        "storage.r2.bucket",
		"STORAGE_R2_ACCOUNT_ID":    "storage.r2.account_id",
		"STORAGE_R2_ACCESS_KEY":    "storage.r2.access_key",
		"STORAGE_R2_SECRET_KEY":    "storage.r2.secret_key",
		"STORAGE_REPLICATION":      "storage.replication",
		"STORAGE_MODE":             "storage.mode",
		"STORAGE_DEFAULT":          "storage.default",
		"CACHE_SIZE_MB":            "cache.size_mb",
		"CACHE_TTL_HOURS":          "cache.ttl_hours",
		"CACHE_DEFAULT_MAX_AGE":    "cache.default_max_age",
		"CACHE_DEFAULT_SMAX_AGE":   "cache.default_smax_age",
		"ENABLE_REDIS":             "cache.enable_redis",
		"REDIS_URL":                "cache.redis_url",
		"MAX_FILE_SIZE_MB":         "processing.max_file_size_mb",
		"DEFAULT_QUALITY":          "processing.default_quality",
		"DEFAULT_FORMAT":           "processing.default_format",
		"CONCURRENT_WORKERS":       "processing.concurrent_workers",
		"API_KEY_REQUIRED":         "security.api_key_required",
		"API_KEY":                  "security.api_key",
		"CORS_ORIGINS":             "security.cors.origins",
		"RATE_LIMIT_RPM":           "security.rate_limit.requests_per_minute",
		"HMAC_KEY":                 "security.hmac_key",
		"HMAC_SALT":                "security.hmac_salt",
		"HMAC_SIGNATURE_SIZE":      "security.hmac_signature_size",
		"HMAC_REQUIRED":            "security.hmac_required",
		"LOG_LEVEL":                "logging.level",
		"LOG_FORMAT":               "logging.format",
		"LOG_OUTPUT":               "logging.output",
		"DEBUG":                    "development.debug",
		"ENABLE_PPROF":             "development.enable_pprof",
		"ENABLE_METRICS":           "development.enable_metrics",
	}
}

// discoverBackendsFromEnv scans environment variables for the pattern
// STORAGE_BACKEND_<NAME>_TYPE and builds named backend configurations.
// Example: STORAGE_BACKEND_IMAGES_TYPE=minio, STORAGE_BACKEND_IMAGES_BUCKET=prod-images
func (l *loader) discoverBackendsFromEnv(v *viper.Viper) {
	const prefix = "STORAGE_BACKEND_"
	const typeSuffix = "_TYPE"

	discovered := make(map[string]bool)

	// First pass: find all backend names via _TYPE vars
	for _, env := range os.Environ() {
		parts := strings.SplitN(env, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := parts[0]
		if !strings.HasPrefix(key, prefix) || !strings.HasSuffix(key, typeSuffix) {
			continue
		}
		name := strings.ToLower(key[len(prefix) : len(key)-len(typeSuffix)])
		if name != "" {
			discovered[name] = true
		}
	}

	if len(discovered) == 0 {
		return
	}

	// Automatically switch to multi mode when backends are discovered
	v.Set("storage.mode", "multi")

	// Second pass: read all fields for each discovered backend
	suffixes := map[string]string{
		"_TYPE":       "type",
		"_BUCKET":     "bucket",
		"_PATH":       "path",
		"_REGION":     "region",
		"_ENDPOINT":   "endpoint",
		"_ACCOUNT_ID": "account_id",
		"_ACCESS_KEY": "access_key",
		"_SECRET_KEY": "secret_key",
		"_SECURE":     "secure",
	}

	for name := range discovered {
		envPrefix := prefix + strings.ToUpper(name)
		for suffix, field := range suffixes {
			if val := os.Getenv(envPrefix + suffix); val != "" {
				configKey := fmt.Sprintf("storage.backends.%s.%s", name, field)
				if field == "secure" {
					if boolVal, err := strconv.ParseBool(val); err == nil {
						v.Set(configKey, boolVal)
					}
				} else {
					v.Set(configKey, val)
				}
			}
		}
	}
}

// discoverScopedKeysFromEnv scans environment variables for the pattern
// SCOPED_KEY_<NAME>_KEY and builds scoped API key configurations.
// Additional vars: SCOPED_KEY_<NAME>_STORAGES (comma-separated), SCOPED_KEY_<NAME>_BUCKETS (comma-separated).
func (l *loader) discoverScopedKeysFromEnv(v *viper.Viper) {
	const prefix = "SCOPED_KEY_"
	const keySuffix = "_KEY"

	discovered := make(map[string]bool)

	for _, env := range os.Environ() {
		parts := strings.SplitN(env, "=", 2)
		if len(parts) != 2 {
			continue
		}
		varName := parts[0]
		if !strings.HasPrefix(varName, prefix) || !strings.HasSuffix(varName, keySuffix) {
			continue
		}
		// Exclude SCOPED_KEY_KEY (empty name)
		name := strings.ToLower(varName[len(prefix) : len(varName)-len(keySuffix)])
		if name != "" {
			discovered[name] = true
		}
	}

	if len(discovered) == 0 {
		return
	}

	// Build scoped key configs
	var scopedKeys []map[string]interface{}
	for name := range discovered {
		envPrefix := prefix + strings.ToUpper(name)

		key := os.Getenv(envPrefix + "_KEY")
		if key == "" {
			continue
		}

		entry := map[string]interface{}{
			"name": name,
			"key":  key,
		}

		if storages := os.Getenv(envPrefix + "_STORAGES"); storages != "" {
			entry["storages"] = strings.Split(storages, ",")
		}
		if buckets := os.Getenv(envPrefix + "_BUCKETS"); buckets != "" {
			entry["buckets"] = strings.Split(buckets, ",")
		}

		scopedKeys = append(scopedKeys, entry)
	}

	if len(scopedKeys) > 0 {
		v.Set("security.scoped_keys", scopedKeys)
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
		"cache.default_max_age":                   true,
		"cache.default_smax_age":                  true,
		"security.hmac_signature_size":            true,
	}

	boolKeys := map[string]bool{
		"security.api_key_required":  true,
		"development.debug":          true,
		"development.enable_pprof":   true,
		"development.enable_metrics": true,
		"storage.minio.secure":       true,
		"cache.enable_redis":         true,
		"security.hmac_required":     true,
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
