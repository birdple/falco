package config

import "github.com/spf13/viper"

// DefaultSetter defines the interface for setting default configurations
type DefaultSetter interface {
	SetDefaults(v *viper.Viper)
}

// defaultsProvider implements DefaultSetter
type defaultsProvider struct{}

// NewDefaultsProvider creates a new defaults provider
func NewDefaultsProvider() DefaultSetter {
	return &defaultsProvider{}
}

// SetDefaults sets default configuration values
func (d *defaultsProvider) SetDefaults(v *viper.Viper) {
	d.setServerDefaults(v)
	d.setStorageDefaults(v)
	d.setCacheDefaults(v)
	d.setProcessingDefaults(v)
	d.setSecurityDefaults(v)
	d.setLoggingDefaults(v)
	d.setDevelopmentDefaults(v)
}

// setServerDefaults sets server default values
func (d *defaultsProvider) setServerDefaults(v *viper.Viper) {
	v.SetDefault("server.port", 8080)
	v.SetDefault("server.host", "0.0.0.0")
	v.SetDefault("server.read_timeout", "30s")
	v.SetDefault("server.write_timeout", "30s")
	v.SetDefault("server.idle_timeout", "60s")
	v.SetDefault("server.shutdown_timeout", "30s")
}

// setStorageDefaults sets storage default values.
// No default bucket is registered: if the operator does not configure a bucket,
// validation must fail at startup. This matches the birdple-v2 monorepo rule
// that sensitive configuration has no implicit defaults.
func (d *defaultsProvider) setStorageDefaults(v *viper.Viper) {
	// Intentionally empty. See validator.validateStorage.
}

// setCacheDefaults sets cache default values
func (d *defaultsProvider) setCacheDefaults(v *viper.Viper) {
	v.SetDefault("cache.size_mb", 256)
	v.SetDefault("cache.ttl_hours", 24)
	v.SetDefault("cache.cleanup_interval", "10m")
	v.SetDefault("cache.default_max_age", 31536000)
	v.SetDefault("cache.default_smax_age", 31536000)
}

// setProcessingDefaults sets processing default values
func (d *defaultsProvider) setProcessingDefaults(v *viper.Viper) {
	// 10 MB, alineado con `config.yaml`, los dos docker-compose y el límite que
	// validan birdple-api y la app. El default compilado era 5 y nadie lo veía
	// porque todos los ambientes fijan la variable: un deploy que la olvidara
	// habría rechazado con 413 imágenes que el resto del stack da por buenas.
	v.SetDefault("processing.max_file_size_mb", 10)
	v.SetDefault("processing.default_quality", 85)
	v.SetDefault("processing.default_format", "webp")
	v.SetDefault("processing.concurrent_workers", 4)
	v.SetDefault("processing.webp_effort", 4)
	v.SetDefault("processing.supported_formats", []string{"jpeg", "png", "webp"})
	v.SetDefault("processing.max_dimensions.width", 2048)
	v.SetDefault("processing.max_dimensions.height", 2048)
}

// setSecurityDefaults sets security default values.
//
// IMPORTANT: api_key_required and hmac_required have NO defaults. Both must be
// set explicitly in the environment. This prevents a misconfigured deployment
// from silently booting with no auth and no signing — previously the cause of
// the "/api/v1/images/* is publicly readable" CRITICAL finding.
//
// Only values that are safe to default (format tweaks, rate limit knobs, CORS
// development fallbacks) are set here.
func (d *defaultsProvider) setSecurityDefaults(v *viper.Viper) {
	v.SetDefault("security.cors.origins", []string{"http://localhost:*", "https://localhost:*"})
	v.SetDefault("security.cors.methods", []string{"GET", "POST", "OPTIONS"})
	v.SetDefault("security.cors.headers", []string{"Content-Type", "Authorization"})
	v.SetDefault("security.rate_limit.requests_per_minute", 1000)
	v.SetDefault("security.rate_limit.burst", 100)
	v.SetDefault("security.hmac_signature_size", 32)
	// api_key_required: NO default — must be explicit
	// hmac_required: NO default — must be explicit
}

// setLoggingDefaults sets logging default values
func (d *defaultsProvider) setLoggingDefaults(v *viper.Viper) {
	v.SetDefault("logging.level", "info")
	v.SetDefault("logging.format", "json")
	v.SetDefault("logging.output", "stdout")
	v.SetDefault("logging.enable_caller", false)
}

// setDevelopmentDefaults sets development default values
func (d *defaultsProvider) setDevelopmentDefaults(v *viper.Viper) {
	v.SetDefault("development.debug", false)
	v.SetDefault("development.enable_pprof", false)
	v.SetDefault("development.enable_metrics", false)
}
