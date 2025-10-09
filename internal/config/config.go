package config

// Load loads configuration from various sources using the default loader
func Load() (*Config, error) {
	loader := NewLoader()
	return loader.Load()
}
