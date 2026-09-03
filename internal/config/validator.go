package config

import (
	"errors"
	"fmt"
	"strings"
)

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

	if err := v.validateSecurity(config); err != nil {
		return fmt.Errorf("security validation failed: %w", err)
	}

	return nil
}

// validateSecurity enforces the "no silent insecure default" rule: if
// API_KEY_REQUIRED is true an API key must be set; if HMAC_REQUIRED is true
// both the HMAC key and salt must be set. The delivery route depends on HMAC
// for access control because browsers cannot carry an API key, so if
// API_KEY_REQUIRED is true we also require HMAC_REQUIRED to avoid leaving
// /api/v1/images/* unauthenticated.
func (v *validator) validateSecurity(config *Config) error {
	if config.Security.APIKeyRequired && config.Security.APIKey == "" {
		return errors.New("security.api_key_required=true but security.api_key is empty")
	}
	if config.Security.HMACRequired {
		if config.Security.HMACKey == "" {
			return errors.New("security.hmac_required=true but security.hmac_key is empty")
		}
		if config.Security.HMACKeySalt == "" {
			return errors.New("security.hmac_required=true but security.hmac_salt is empty")
		}
	}
	if config.Security.APIKeyRequired && !config.Security.HMACRequired {
		return errors.New("security.api_key_required=true requires security.hmac_required=true " +
			"because the image delivery route cannot be protected by API key alone " +
			"(browsers cannot carry API keys on image URLs)")
	}
	return nil
}

// validateServer validates server configuration
func (v *validator) validateServer(config *Config) error {
	if config.Server.Port < 1 || config.Server.Port > 65535 {
		return fmt.Errorf("invalid port: %d (must be 1-65535)", config.Server.Port)
	}

	if config.Server.Host == "" {
		return errors.New("host cannot be empty")
	}

	return nil
}

// validateStorage validates the unified storage configuration
func (v *validator) validateStorage(config *Config) error {
	validTypes := map[string]bool{
		"filesystem": true,
		"s3":         true,
		"r2":         true,
		"jay":        true,
	}

	validModes := map[string]bool{
		"sync":          true,
		"async":         true,
		"read-fallback": true,
	}

	// Must have at least one bucket
	if len(config.Storage.Buckets) == 0 {
		return errors.New("at least one bucket must be configured")
	}

	// Default bucket must exist
	if config.Storage.Default == "" {
		return errors.New("storage.default is required")
	}
	if _, ok := config.Storage.Buckets[config.Storage.Default]; !ok {
		return fmt.Errorf("default bucket %q not found in storage.buckets", config.Storage.Default)
	}

	// Validate each bucket
	for name, bucket := range config.Storage.Buckets {
		if !validTypes[bucket.Type] {
			return fmt.Errorf("invalid type %q for bucket %q (must be filesystem, s3, r2, or jay)", bucket.Type, name)
		}

		// Validate type-specific required fields
		if err := v.validateBucketFields(name, bucket); err != nil {
			return err
		}

		// Validate backup refs
		for i, backup := range bucket.Backups {
			if backup.Target == "" {
				return fmt.Errorf("bucket %q: backup[%d] has no target", name, i)
			}
			if backup.Target == name {
				return fmt.Errorf("bucket %q: backup[%d] cannot reference itself", name, i)
			}
			if _, ok := config.Storage.Buckets[backup.Target]; !ok {
				return fmt.Errorf("bucket %q: backup[%d] target %q not found in storage.buckets", name, i, backup.Target)
			}
			if backup.Mode != "" && !validModes[backup.Mode] {
				return fmt.Errorf("bucket %q: backup[%d] invalid mode %q (must be sync, async, or read-fallback)", name, i, backup.Mode)
			}
		}

		// Validate bucket-level keys
		seenKeys := make(map[string]bool)
		for i, key := range bucket.Keys {
			if key.Key == "" {
				return fmt.Errorf("bucket %q: key[%d] has no key value", name, i)
			}
			if key.Name == "" {
				return fmt.Errorf("bucket %q: key[%d] has no name", name, i)
			}
			if seenKeys[key.Name] {
				return fmt.Errorf("bucket %q: duplicate key name %q", name, key.Name)
			}
			seenKeys[key.Name] = true
		}
	}

	// Validate groups
	for groupName, group := range config.Storage.Groups {
		if err := v.validateGroup(config, groupName, group); err != nil {
			return err
		}
	}

	return nil
}

// validateGroup validates a group configuration
func (v *validator) validateGroup(config *Config, groupName string, group GroupConfig) error {
	if len(group.Buckets) == 0 {
		return fmt.Errorf("group %q has no buckets", groupName)
	}

	// All group buckets must exist
	groupBucketSet := make(map[string]bool)
	for _, b := range group.Buckets {
		if _, ok := config.Storage.Buckets[b]; !ok {
			return fmt.Errorf("group %q references non-existent bucket %q", groupName, b)
		}
		groupBucketSet[b] = true
	}

	// Validate group keys
	seenKeys := make(map[string]bool)
	for i, key := range group.Keys {
		if key.Key == "" {
			return fmt.Errorf("group %q: key[%d] has no key value", groupName, i)
		}
		if key.Name == "" {
			return fmt.Errorf("group %q: key[%d] has no name", groupName, i)
		}
		if seenKeys[key.Name] {
			return fmt.Errorf("group %q: duplicate key name %q", groupName, key.Name)
		}
		seenKeys[key.Name] = true

		// If key restricts to specific buckets, they must be in the group
		for _, b := range key.Buckets {
			if !groupBucketSet[b] {
				return fmt.Errorf("group %q: key %q references bucket %q not in group", groupName, key.Name, b)
			}
		}
	}

	// Validate subgroups
	for subName, sub := range group.Subgroups {
		if len(sub.Buckets) == 0 {
			return fmt.Errorf("group %q: subgroup %q has no buckets", groupName, subName)
		}

		// Subgroup buckets must be a subset of the parent group's buckets
		for _, b := range sub.Buckets {
			if !groupBucketSet[b] {
				return fmt.Errorf("group %q: subgroup %q references bucket %q not in parent group", groupName, subName, b)
			}
		}

		// Validate subgroup keys
		subSeenKeys := make(map[string]bool)
		for i, key := range sub.Keys {
			if key.Key == "" {
				return fmt.Errorf("group %q: subgroup %q: key[%d] has no key value", groupName, subName, i)
			}
			if key.Name == "" {
				return fmt.Errorf("group %q: subgroup %q: key[%d] has no name", groupName, subName, i)
			}
			if subSeenKeys[key.Name] {
				return fmt.Errorf("group %q: subgroup %q: duplicate key name %q", groupName, subName, key.Name)
			}
			subSeenKeys[key.Name] = true
		}
	}

	return nil
}

// validateBucketFields validates type-specific required fields for a bucket.
// Without this, missing env vars like STORAGE_BUCKET_JAY_ADDR result in empty
// strings that pass generic validation but cause confusing TCP dial errors at
// runtime instead of a clear startup failure.
func (v *validator) validateBucketFields(name string, bucket BucketConfig) error {
	switch bucket.Type {
	case "jay":
		if bucket.JayAddr == "" {
			return fmt.Errorf("bucket %q (type jay): addr is required (set STORAGE_BUCKET_%s_ADDR)", name, strings.ToUpper(name))
		}
		if bucket.JayAdminAddr == "" {
			return fmt.Errorf("bucket %q (type jay): admin_addr is required (set STORAGE_BUCKET_%s_ADMIN_ADDR)", name, strings.ToUpper(name))
		}
		if bucket.Bucket == "" {
			return fmt.Errorf("bucket %q (type jay): bucket name is required (set STORAGE_BUCKET_%s_BUCKET)", name, strings.ToUpper(name))
		}
		if bucket.JayTokenID == "" {
			return fmt.Errorf("bucket %q (type jay): token_id is required (set STORAGE_BUCKET_%s_TOKEN_ID)", name, strings.ToUpper(name))
		}
		if bucket.JayTokenSec == "" {
			return fmt.Errorf("bucket %q (type jay): token_secret is required (set STORAGE_BUCKET_%s_TOKEN_SECRET)", name, strings.ToUpper(name))
		}
	case "s3":
		if bucket.Bucket == "" {
			return fmt.Errorf("bucket %q (type s3): bucket name is required", name)
		}
		if bucket.Region == "" {
			return fmt.Errorf("bucket %q (type s3): region is required", name)
		}
	case "r2":
		if bucket.Bucket == "" {
			return fmt.Errorf("bucket %q (type r2): bucket name is required", name)
		}
		if bucket.AccountID == "" {
			return fmt.Errorf("bucket %q (type r2): account_id is required", name)
		}
	case "filesystem":
		if bucket.Path == "" {
			return fmt.Errorf("bucket %q (type filesystem): path is required", name)
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
