package config

import (
	"fmt"
	"os"
	"sort"
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

	// Auto-discover buckets from STORAGE_BUCKET_<NAME>_TYPE env vars
	l.discoverBucketsFromEnv(v)

	// Auto-discover groups from STORAGE_GROUP_<NAME>_BUCKETS env vars
	l.discoverGroupsFromEnv(v)
}

// getEnvMappings returns environment variable to config key mappings
func (l *loader) getEnvMappings() map[string]string {
	return map[string]string{
		"PORT":              "server.port",
		"HOST":              "server.host",
		"ENV":               "development.debug",
		"STORAGE_DEFAULT":   "storage.default",
		"CACHE_SIZE_MB":     "cache.size_mb",
		"CACHE_TTL_HOURS":   "cache.ttl_hours",
		"CACHE_DEFAULT_MAX_AGE":  "cache.default_max_age",
		"CACHE_DEFAULT_SMAX_AGE": "cache.default_smax_age",
		"ENABLE_REDIS":      "cache.enable_redis",
		"REDIS_URL":         "cache.redis_url",
		"MAX_FILE_SIZE_MB":  "processing.max_file_size_mb",
		"DEFAULT_QUALITY":   "processing.default_quality",
		"DEFAULT_FORMAT":    "processing.default_format",
		"CONCURRENT_WORKERS": "processing.concurrent_workers",
		"API_KEY_REQUIRED":  "security.api_key_required",
		"API_KEY":           "security.api_key",
		"CORS_ORIGINS":      "security.cors.origins",
		"RATE_LIMIT_RPM":    "security.rate_limit.requests_per_minute",
		"HMAC_KEY":          "security.hmac_key",
		"HMAC_SALT":         "security.hmac_salt",
		"HMAC_SIGNATURE_SIZE": "security.hmac_signature_size",
		"HMAC_REQUIRED":     "security.hmac_required",
		"LOG_LEVEL":         "logging.level",
		"LOG_FORMAT":        "logging.format",
		"LOG_OUTPUT":        "logging.output",
		"DEBUG":             "development.debug",
		"ENABLE_PPROF":      "development.enable_pprof",
		"ENABLE_METRICS":    "development.enable_metrics",
	}
}

// discoverBucketsFromEnv scans environment variables for the pattern
// STORAGE_BUCKET_<NAME>_TYPE and builds bucket configurations.
// Also discovers backups (STORAGE_BUCKET_<NAME>_BACKUP_<N>_TARGET/MODE)
// and bucket-level keys (STORAGE_BUCKET_<NAME>_KEY_<KEYNAME>_KEY).
func (l *loader) discoverBucketsFromEnv(v *viper.Viper) {
	const prefix = "STORAGE_BUCKET_"
	const typeSuffix = "_TYPE"

	discovered := make(map[string]bool)

	// First pass: find all bucket names via _TYPE vars
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

	// Second pass: read all fields for each discovered bucket
	fieldSuffixes := map[string]string{
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
		for suffix, field := range fieldSuffixes {
			if val := os.Getenv(envPrefix + suffix); val != "" {
				configKey := fmt.Sprintf("storage.buckets.%s.%s", name, field)
				if field == "secure" {
					if boolVal, err := strconv.ParseBool(val); err == nil {
						v.Set(configKey, boolVal)
					}
				} else {
					v.Set(configKey, val)
				}
			}
		}

		// Discover backups: STORAGE_BUCKET_<NAME>_BACKUP_<N>_TARGET / _MODE
		l.discoverBucketBackupsFromEnv(v, name, envPrefix)

		// Discover bucket-level keys: STORAGE_BUCKET_<NAME>_KEY_<KEYNAME>_KEY
		l.discoverBucketKeysFromEnv(v, name, envPrefix)
	}
}

// discoverBucketBackupsFromEnv discovers backup refs for a bucket from env vars.
// Pattern: STORAGE_BUCKET_<NAME>_BACKUP_<N>_TARGET and _MODE where N is 1,2,3...
func (l *loader) discoverBucketBackupsFromEnv(v *viper.Viper, bucketName, envPrefix string) {
	backupPrefix := envPrefix + "_BACKUP_"

	// Find all backup indices
	indices := make(map[int]bool)
	for _, env := range os.Environ() {
		parts := strings.SplitN(env, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := parts[0]
		if !strings.HasPrefix(key, backupPrefix) {
			continue
		}
		rest := key[len(backupPrefix):]
		// Extract index: rest is like "1_TARGET" or "2_MODE"
		idx := strings.IndexByte(rest, '_')
		if idx < 1 {
			continue
		}
		if n, err := strconv.Atoi(rest[:idx]); err == nil {
			indices[n] = true
		}
	}

	if len(indices) == 0 {
		return
	}

	// Sort indices for deterministic ordering
	sorted := make([]int, 0, len(indices))
	for n := range indices {
		sorted = append(sorted, n)
	}
	sort.Ints(sorted)

	var backups []map[string]interface{}
	for _, n := range sorted {
		target := os.Getenv(fmt.Sprintf("%s%d_TARGET", backupPrefix, n))
		mode := os.Getenv(fmt.Sprintf("%s%d_MODE", backupPrefix, n))
		if target == "" {
			continue
		}
		backup := map[string]interface{}{
			"target": strings.ToLower(target),
			"mode":   strings.ToLower(mode),
		}
		backups = append(backups, backup)
	}

	if len(backups) > 0 {
		v.Set(fmt.Sprintf("storage.buckets.%s.backups", bucketName), backups)
	}
}

// discoverBucketKeysFromEnv discovers API keys for a bucket from env vars.
// Pattern: STORAGE_BUCKET_<NAME>_KEY_<KEYNAME>_KEY
func (l *loader) discoverBucketKeysFromEnv(v *viper.Viper, bucketName, envPrefix string) {
	keyPrefix := envPrefix + "_KEY_"
	const keySuffix = "_KEY"

	discovered := make(map[string]bool)
	for _, env := range os.Environ() {
		parts := strings.SplitN(env, "=", 2)
		if len(parts) != 2 {
			continue
		}
		varName := parts[0]
		if !strings.HasPrefix(varName, keyPrefix) || !strings.HasSuffix(varName, keySuffix) {
			continue
		}
		keyName := strings.ToLower(varName[len(keyPrefix) : len(varName)-len(keySuffix)])
		if keyName != "" {
			discovered[keyName] = true
		}
	}

	if len(discovered) == 0 {
		return
	}

	var keys []map[string]interface{}
	for keyName := range discovered {
		keyVal := os.Getenv(keyPrefix + strings.ToUpper(keyName) + keySuffix)
		if keyVal == "" {
			continue
		}
		keys = append(keys, map[string]interface{}{
			"name": keyName,
			"key":  keyVal,
		})
	}

	if len(keys) > 0 {
		v.Set(fmt.Sprintf("storage.buckets.%s.keys", bucketName), keys)
	}
}

// discoverGroupsFromEnv scans environment variables for the pattern
// STORAGE_GROUP_<NAME>_BUCKETS and builds group configurations.
// Also discovers group keys and subgroups.
func (l *loader) discoverGroupsFromEnv(v *viper.Viper) {
	const prefix = "STORAGE_GROUP_"
	const bucketsSuffix = "_BUCKETS"

	discovered := make(map[string]bool)

	for _, env := range os.Environ() {
		parts := strings.SplitN(env, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := parts[0]
		if !strings.HasPrefix(key, prefix) || !strings.HasSuffix(key, bucketsSuffix) {
			continue
		}
		// Exclude subgroup vars: STORAGE_GROUP_X_SUBGROUP_Y_BUCKETS
		middle := key[len(prefix) : len(key)-len(bucketsSuffix)]
		if strings.Contains(middle, "_SUBGROUP_") {
			continue
		}
		name := strings.ToLower(middle)
		if name != "" {
			discovered[name] = true
		}
	}

	if len(discovered) == 0 {
		return
	}

	for name := range discovered {
		envPrefix := prefix + strings.ToUpper(name)

		// Set buckets
		if buckets := os.Getenv(envPrefix + "_BUCKETS"); buckets != "" {
			v.Set(fmt.Sprintf("storage.groups.%s.buckets", name), strings.Split(buckets, ","))
		}

		// Discover group keys: STORAGE_GROUP_<NAME>_KEY_<KEYNAME>_KEY
		l.discoverGroupKeysFromEnv(v, name, envPrefix)

		// Discover subgroups: STORAGE_GROUP_<NAME>_SUBGROUP_<SUBNAME>_BUCKETS
		l.discoverSubgroupsFromEnv(v, name, envPrefix)
	}
}

// discoverGroupKeysFromEnv discovers keys for a group from env vars.
// Pattern: STORAGE_GROUP_<NAME>_KEY_<KEYNAME>_KEY
// Optional: STORAGE_GROUP_<NAME>_KEY_<KEYNAME>_BUCKETS (comma-separated)
func (l *loader) discoverGroupKeysFromEnv(v *viper.Viper, groupName, envPrefix string) {
	keyPrefix := envPrefix + "_KEY_"
	const keySuffix = "_KEY"

	discovered := make(map[string]bool)
	for _, env := range os.Environ() {
		parts := strings.SplitN(env, "=", 2)
		if len(parts) != 2 {
			continue
		}
		varName := parts[0]
		if !strings.HasPrefix(varName, keyPrefix) || !strings.HasSuffix(varName, keySuffix) {
			continue
		}
		keyName := strings.ToLower(varName[len(keyPrefix) : len(varName)-len(keySuffix)])
		if keyName != "" {
			discovered[keyName] = true
		}
	}

	if len(discovered) == 0 {
		return
	}

	var keys []map[string]interface{}
	for keyName := range discovered {
		keyEnvPrefix := keyPrefix + strings.ToUpper(keyName)
		keyVal := os.Getenv(keyEnvPrefix + keySuffix)
		if keyVal == "" {
			continue
		}
		entry := map[string]interface{}{
			"name": keyName,
			"key":  keyVal,
		}
		if buckets := os.Getenv(keyEnvPrefix + "_BUCKETS"); buckets != "" {
			entry["buckets"] = strings.Split(buckets, ",")
		}
		keys = append(keys, entry)
	}

	if len(keys) > 0 {
		v.Set(fmt.Sprintf("storage.groups.%s.keys", groupName), keys)
	}
}

// discoverSubgroupsFromEnv discovers subgroups for a group from env vars.
// Pattern: STORAGE_GROUP_<NAME>_SUBGROUP_<SUBNAME>_BUCKETS
func (l *loader) discoverSubgroupsFromEnv(v *viper.Viper, groupName, envPrefix string) {
	subPrefix := envPrefix + "_SUBGROUP_"
	const bucketsSuffix = "_BUCKETS"

	discovered := make(map[string]bool)
	for _, env := range os.Environ() {
		parts := strings.SplitN(env, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := parts[0]
		if !strings.HasPrefix(key, subPrefix) || !strings.HasSuffix(key, bucketsSuffix) {
			continue
		}
		subName := strings.ToLower(key[len(subPrefix) : len(key)-len(bucketsSuffix)])
		// Exclude key vars like _SUBGROUP_X_KEY_Y_BUCKETS
		if strings.Contains(subName, "_KEY_") {
			continue
		}
		if subName != "" {
			discovered[subName] = true
		}
	}

	for subName := range discovered {
		subEnvPrefix := subPrefix + strings.ToUpper(subName)

		if buckets := os.Getenv(subEnvPrefix + "_BUCKETS"); buckets != "" {
			v.Set(fmt.Sprintf("storage.groups.%s.subgroups.%s.buckets", groupName, subName), strings.Split(buckets, ","))
		}

		// Subgroup keys: STORAGE_GROUP_<NAME>_SUBGROUP_<SUBNAME>_KEY_<KEYNAME>_KEY
		l.discoverSubgroupKeysFromEnv(v, groupName, subName, subEnvPrefix)
	}
}

// discoverSubgroupKeysFromEnv discovers keys for a subgroup from env vars.
func (l *loader) discoverSubgroupKeysFromEnv(v *viper.Viper, groupName, subName, subEnvPrefix string) {
	keyPrefix := subEnvPrefix + "_KEY_"
	const keySuffix = "_KEY"

	discovered := make(map[string]bool)
	for _, env := range os.Environ() {
		parts := strings.SplitN(env, "=", 2)
		if len(parts) != 2 {
			continue
		}
		varName := parts[0]
		if !strings.HasPrefix(varName, keyPrefix) || !strings.HasSuffix(varName, keySuffix) {
			continue
		}
		keyName := strings.ToLower(varName[len(keyPrefix) : len(varName)-len(keySuffix)])
		if keyName != "" {
			discovered[keyName] = true
		}
	}

	if len(discovered) == 0 {
		return
	}

	var keys []map[string]interface{}
	for keyName := range discovered {
		keyVal := os.Getenv(keyPrefix + strings.ToUpper(keyName) + keySuffix)
		if keyVal == "" {
			continue
		}
		entry := map[string]interface{}{
			"name": keyName,
			"key":  keyVal,
		}
		if buckets := os.Getenv(keyPrefix + strings.ToUpper(keyName) + "_BUCKETS"); buckets != "" {
			entry["buckets"] = strings.Split(buckets, ",")
		}
		keys = append(keys, entry)
	}

	if len(keys) > 0 {
		v.Set(fmt.Sprintf("storage.groups.%s.subgroups.%s.keys", groupName, subName), keys)
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
