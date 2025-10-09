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
	validPrimary := map[string]bool{
		"filesystem": true,
		"s3":         true,
		"minio":      true,
	}

	if !validPrimary[config.Storage.Primary] {
		return fmt.Errorf("invalid primary storage: %s (must be filesystem, s3, or minio)", config.Storage.Primary)
	}

	validSecondary := map[string]bool{
		"none":       true,
		"filesystem": true,
		"s3":         true,
		"minio":      true,
	}

	if !validSecondary[config.Storage.Secondary] {
		return fmt.Errorf("invalid secondary storage: %s", config.Storage.Secondary)
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
