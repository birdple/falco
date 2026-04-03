package config

import "fmt"

// Validator defines the interface for configuration validation
type Validator interface {
	Validate(config *Config) error
}

// validator implements configuration validation
type validator struct{}

// NewValidator creates a new configuration validator
func NewValidator() Validator {
	return &validator{}
}

// Validate validates the loaded configuration
func (v *validator) Validate(config *Config) error {
	if err := v.validateServer(config); err != nil {
		return fmt.Errorf("server validation failed: %w", err)
	}

	if err := v.validateStorage(config); err != nil {
		return fmt.Errorf("storage validation failed: %w", err)
	}

	if err := v.validateCache(config); err != nil {
		return fmt.Errorf("cache validation failed: %w", err)
	}

	if err := v.validateProcessing(config); err != nil {
		return fmt.Errorf("processing validation failed: %w", err)
	}

	if err := v.validateScopedKeys(config); err != nil {
		return fmt.Errorf("scoped keys validation failed: %w", err)
	}

	return nil
}

// validateServer validates server configuration
func (v *validator) validateServer(config *Config) error {
	if config.Server.Port < 1 || config.Server.Port > 65535 {
		return fmt.Errorf("invalid port: %d (must be 1-65535)", config.Server.Port)
	}

	if config.Server.Host == "" {
		return fmt.Errorf("host cannot be empty")
	}

	return nil
}

// validateStorage validates storage configuration
func (v *validator) validateStorage(config *Config) error {
	validTypes := map[string]bool{
		"filesystem": true,
		"s3":         true,
		"minio":      true,
		"r2":         true,
	}

	if !validTypes[config.Storage.Primary] {
		return fmt.Errorf("invalid primary storage: %s (must be filesystem, s3, minio, or r2)", config.Storage.Primary)
	}

	validSecondary := map[string]bool{
		"none":       true,
		"filesystem": true,
		"s3":         true,
		"minio":      true,
		"r2":         true,
	}

	if !validSecondary[config.Storage.Secondary] {
		return fmt.Errorf("invalid secondary storage: %s", config.Storage.Secondary)
	}

	// Validate replication mode
	validReplication := map[string]bool{
		"sync":          true,
		"async":         true,
		"read-fallback": true,
	}
	if config.Storage.Replication != "" && !validReplication[config.Storage.Replication] {
		return fmt.Errorf("invalid replication mode: %s (must be sync, async, or read-fallback)", config.Storage.Replication)
	}

	// Validate storage mode
	validMode := map[string]bool{
		"single": true,
		"multi":  true,
	}
	if config.Storage.Mode != "" && !validMode[config.Storage.Mode] {
		return fmt.Errorf("invalid storage mode: %s (must be single or multi)", config.Storage.Mode)
	}

	// Validate named backends in multi mode
	if config.Storage.Mode == "multi" {
		for name, backend := range config.Storage.Backends {
			if !validTypes[backend.Type] {
				return fmt.Errorf("invalid type %q for backend %q", backend.Type, name)
			}
		}
		// Validate default backend reference
		if config.Storage.Default != "" {
			if _, ok := config.Storage.Backends[config.Storage.Default]; !ok {
				return fmt.Errorf("default backend %q not found in backends", config.Storage.Default)
			}
		}
	}

	return nil
}

// validateCache validates cache configuration
func (v *validator) validateCache(config *Config) error {
	if config.Cache.SizeMB < 1 {
		return fmt.Errorf("invalid cache size: %d MB (must be >= 1)", config.Cache.SizeMB)
	}

	return nil
}

// validateProcessing validates processing configuration
func (v *validator) validateProcessing(config *Config) error {
	if config.Processing.MaxFileSizeMB < 1 {
		return fmt.Errorf("invalid max file size: %d MB (must be >= 1)", config.Processing.MaxFileSizeMB)
	}

	if config.Processing.DefaultQuality < 1 || config.Processing.DefaultQuality > 100 {
		return fmt.Errorf("invalid quality: %d (must be 1-100)", config.Processing.DefaultQuality)
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

// validateScopedKeys validates scoped API key configurations
func (v *validator) validateScopedKeys(config *Config) error {
	seen := make(map[string]bool)
	for i, sk := range config.Security.ScopedKeys {
		if sk.Key == "" {
			return fmt.Errorf("scoped key at index %d has no key value", i)
		}
		if sk.Name == "" {
			return fmt.Errorf("scoped key at index %d has no name", i)
		}
		if seen[sk.Name] {
			return fmt.Errorf("duplicate scoped key name: %s", sk.Name)
		}
		seen[sk.Name] = true
		if len(sk.Storages) == 0 && len(sk.Buckets) == 0 {
			return fmt.Errorf("scoped key %q must have at least one storage or bucket", sk.Name)
		}
	}
	return nil
}
