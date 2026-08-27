// Package config loads and validates falco's configuration.
//
// Buckets, groups and scoped keys are auto-discovered from STORAGE_BUCKET_<NAME>_*
// environment variables, which is what lets a multi-tenant deployment add a
// bucket without a code change.
//
// Secrets have no defaults: a missing one refuses to start rather than degrading
// into an open deployment.
package config

// Load loads configuration from various sources using the default loader
func Load() (*Config, error) {
	loader := NewLoader()
	return loader.Load()
}
