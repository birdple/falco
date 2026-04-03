package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/birdple/falco/internal/api"
	"github.com/birdple/falco/internal/cache"
	"github.com/birdple/falco/internal/config"
	"github.com/birdple/falco/internal/pkg/httputil"
	"github.com/birdple/falco/internal/pkg/logger"
	"github.com/birdple/falco/internal/processor"
	"github.com/birdple/falco/internal/storage"
	"github.com/cshum/vipsgen/vips"
)

func main() {
	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		logger.Fatal().Err(err).Msg("Failed to load configuration")
	}

	// Setup logger from config
	logger.Setup(logger.Config{
		Level:        cfg.Logging.Level,
		Format:       cfg.Logging.Format,
		Output:       cfg.Logging.Output,
		EnableCaller: cfg.Logging.EnableCaller,
	})

	logger.Info().Msg("Starting Falco Image Processing Service")

	// Configure trusted proxies for X-Forwarded-For / X-Real-IP header trust
	if len(cfg.Security.TrustedProxies) > 0 {
		httputil.SetTrustedProxies(cfg.Security.TrustedProxies)
		logger.Info().Strs("trusted_proxies", cfg.Security.TrustedProxies).Msg("Trusted proxies configured")
	}

	// Initialize VIPS
	vips.Startup(nil)
	defer vips.Shutdown()

	// Log sanitized configuration (no secrets)
	logger.Info().
		Str("storage_mode", cfg.Storage.Mode).
		Str("storage_primary", cfg.Storage.Primary).
		Str("storage_secondary", cfg.Storage.Secondary).
		Str("storage_replication", cfg.Storage.Replication).
		Str("s3_bucket", cfg.Storage.S3.Bucket).
		Str("minio_bucket", cfg.Storage.MinIO.Bucket).
		Str("minio_endpoint", cfg.Storage.MinIO.Endpoint).
		Str("r2_bucket", cfg.Storage.R2.Bucket).
		Str("local_path", cfg.GetLocalStoragePath()).
		Int("port", cfg.Server.Port).
		Str("host", cfg.Server.Host).
		Int("cache_size_mb", cfg.Cache.SizeMB).
		Int("cache_ttl_hours", cfg.Cache.TTLHrs).
		Bool("redis_enabled", cfg.Cache.EnableRedis).
		Int("max_file_size_mb", cfg.Processing.MaxFileSizeMB).
		Int("default_quality", cfg.Processing.DefaultQuality).
		Str("default_format", cfg.Processing.DefaultFormat).
		Int("concurrent_workers", cfg.Processing.ConcurrentWorkers).
		Bool("api_key_required", cfg.Security.APIKeyRequired).
		Strs("cors_origins", cfg.Security.CORS.Origins).
		Int("rate_limit_rpm", cfg.Security.RateLimit.RequestsPerMinute).
		Bool("hmac_required", cfg.Security.HMACRequired).
		Bool("metrics_enabled", cfg.Development.EnableMetrics).
		Bool("debug", cfg.Development.Debug).
		Msg("Configuration loaded")

	// Create application context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Initialize components
	storageBackend, storageReg, err := initializeStorage(cfg)
	if err != nil {
		logger.Fatal().Err(err).Msg("Failed to initialize storage backend")
	}

	imageProcessor := processor.NewImageProcessor(
		cfg.Processing.MaxFileSizeMB,
		cfg.Processing.DefaultQuality,
		processor.ImageFormat(cfg.Processing.DefaultFormat),
		cfg.Processing.MaxDimensions.Width,
		cfg.Processing.MaxDimensions.Height,
	)

	// Limit concurrent image processing to avoid CPU/memory exhaustion
	if cfg.Processing.ConcurrentWorkers > 0 {
		if vp, ok := imageProcessor.(*processor.VipsProcessor); ok {
			vp.SetMaxConcurrency(cfg.Processing.ConcurrentWorkers)
			logger.Info().Int("max_concurrent", cfg.Processing.ConcurrentWorkers).Msg("Processing concurrency limit set")
		}
	}

	// Initialize cache
	var appCache processor.Cache
	if cfg.Cache.EnableRedis && cfg.Cache.RedisURL != "" {
		redisCache, err := cache.NewRedisCache(cfg.Cache.RedisURL, cfg.GetCacheTTL())
		if err != nil {
			logger.Warn().Err(err).Msg("Failed to initialize Redis cache, falling back to LRU cache")
		} else {
			appCache = redisCache
			logger.Info().Str("redis_url", cfg.Cache.RedisURL).Msg("Redis cache initialized")
		}
	}

	if appCache == nil {
		cacheSize := cfg.GetCacheSizeBytes()
		if cacheSize > 0 {
			lruCache := cache.NewLRUCache(cacheSize, cfg.GetCacheTTL())
			appCache = lruCache
			logger.Info().Int("cache_size_mb", cfg.Cache.SizeMB).Msg("LRU cache initialized")
		}
	}

	if appCache != nil {
		imageProcessor.SetCache(appCache)
	}

	// Initialize API server
	server := api.NewServer(&api.ServerConfig{
		Config:          cfg,
		Storage:         storageBackend,
		StorageRegistry: storageReg,
		ImageProcessor:  imageProcessor,
	})

	// Start server in a goroutine
	serverErr := make(chan error, 1)
	go func() {
		logger.Info().Str("address", cfg.GetServerAddress()).Msg("Server starting")
		if err := server.Start(); err != nil && err != http.ErrServerClosed {
			serverErr <- err
		}
	}()

	// Set up graceful shutdown
	shutdown := setupGracefulShutdown(ctx, cancel)

	// Wait for either server error or shutdown signal
	select {
	case err := <-serverErr:
		logger.Error().Err(err).Msg("Server error")
	case <-shutdown:
		logger.Info().Msg("Shutdown signal received")
	}

	// Perform graceful shutdown
	shutdownTimeout := cfg.Server.ShutdownTimeout
	if shutdownTimeout == 0 {
		shutdownTimeout = 30 * time.Second
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer shutdownCancel()

	logger.Info().Msg("Initiating graceful shutdown...")

	// Phase 1: Stop accepting new requests
	logger.Info().Msg("Phase 1: Stopping server (no new requests)")
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error().Err(err).Msg("Server shutdown error")
	}

	// Phase 2: Clean up resources
	logger.Info().Msg("Phase 2: Cleaning up resources")
	cleanupResources(ctx, storageBackend, appCache)

	// Phase 3: Final cleanup
	logger.Info().Msg("Phase 3: Final cleanup")
	time.Sleep(100 * time.Millisecond)

	logger.Info().Msg("Server shutdown complete")
}

// setupGracefulShutdown sets up signal handling for graceful shutdown
func setupGracefulShutdown(ctx context.Context, cancel context.CancelFunc) <-chan struct{} {
	shutdown := make(chan struct{})

	go func() {
		defer close(shutdown)

		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan,
			syscall.SIGINT,
			syscall.SIGTERM,
			syscall.SIGHUP,
		)

		select {
		case sig := <-sigChan:
			logger.Info().Str("signal", sig.String()).Msg("Received shutdown signal")
		case <-ctx.Done():
			logger.Info().Msg("Context cancelled, initiating shutdown")
		}

		cancel()
		time.Sleep(50 * time.Millisecond)
	}()

	return shutdown
}

// cleanupResources performs cleanup of application resources
func cleanupResources(ctx context.Context, storageBackend storage.StorageBackend, appCache processor.Cache) {
	if appCache != nil {
		logger.Info().Msg("Stopping cache...")
		if lru, ok := appCache.(*cache.LRUCache); ok {
			lru.Stop()
		} else if redis, ok := appCache.(*cache.RedisCache); ok {
			redis.Stop()
		}
		logger.Info().Msg("Clearing cache...")
		appCache.Clear()
		logger.Info().Msg("Cache cleanup completed")
	}

	// Storage backends (S3, MinIO, filesystem) use pooled HTTP clients
	// that are cleaned up by the Go runtime. No explicit close needed.
	if storageBackend != nil {
		logger.Info().Msg("Storage connections released")
	}
}

// initializeStorage initializes the storage backend(s) based on configuration.
// Returns the default backend and an optional registry (non-nil in multi mode).
func initializeStorage(cfg *config.Config) (storage.StorageBackend, *storage.Registry, error) {
	// Multi mode: build named backends from config
	if cfg.Storage.Mode == "multi" {
		return initializeMultiStorage(cfg)
	}

	// Single mode: primary + optional secondary with replication
	primary, err := buildBackend(cfg, cfg.Storage.Primary)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to initialize primary storage: %w", err)
	}
	logger.Info().Str("type", cfg.Storage.Primary).Msg("Primary storage initialized")

	// If secondary is configured, wrap with ReplicatedStorage
	if cfg.Storage.Secondary != "" && cfg.Storage.Secondary != "none" {
		secondary, err := buildBackend(cfg, cfg.Storage.Secondary)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to initialize secondary storage: %w", err)
		}

		mode := storage.ReplicationMode(cfg.Storage.Replication)
		if mode == "" {
			mode = storage.ReplicationSync
		}

		logger.Info().
			Str("secondary_type", cfg.Storage.Secondary).
			Str("replication_mode", string(mode)).
			Msg("Secondary storage initialized with replication")

		replicated := storage.NewReplicatedStorage(primary, secondary, mode)
		return replicated, nil, nil
	}

	return primary, nil, nil
}

// initializeMultiStorage builds a registry of named backends from config.
func initializeMultiStorage(cfg *config.Config) (storage.StorageBackend, *storage.Registry, error) {
	if len(cfg.Storage.Backends) == 0 {
		return nil, nil, fmt.Errorf("multi mode requires at least one named backend in storage.backends")
	}

	// Build the first backend to use as initial default
	var firstBackend storage.StorageBackend
	var firstName string
	for name := range cfg.Storage.Backends {
		firstName = name
		break
	}

	firstBackend, err := buildNamedBackend(cfg.Storage.Backends[firstName])
	if err != nil {
		return nil, nil, fmt.Errorf("failed to initialize backend %q: %w", firstName, err)
	}

	reg := storage.NewRegistry(firstBackend)
	// Re-register under its actual name (NewRegistry puts it under "default")
	reg.Register(firstName, firstBackend)
	logger.Info().Str("name", firstName).Str("type", cfg.Storage.Backends[firstName].Type).Msg("Storage backend registered")

	// Build remaining backends
	for name, backendCfg := range cfg.Storage.Backends {
		if name == firstName {
			continue
		}
		backend, err := buildNamedBackend(backendCfg)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to initialize backend %q: %w", name, err)
		}
		reg.Register(name, backend)
		logger.Info().Str("name", name).Str("type", backendCfg.Type).Msg("Storage backend registered")
	}

	// Set the configured default if specified
	if cfg.Storage.Default != "" {
		if err := reg.SetDefault(cfg.Storage.Default); err != nil {
			return nil, nil, fmt.Errorf("failed to set default backend: %w", err)
		}
	}

	logger.Info().
		Int("backend_count", reg.Len()).
		Str("default", reg.DefaultName()).
		Strs("backends", reg.Names()).
		Msg("Multi-storage mode initialized")

	return reg.Default(), reg, nil
}

// buildBackend creates a storage backend from the global config for a given type string.
func buildBackend(cfg *config.Config, storageType string) (storage.StorageBackend, error) {
	st := storage.StorageType(storageType)

	accessKey := cfg.Storage.S3.AccessKey
	secretKey := cfg.Storage.S3.SecretKey
	if st == storage.StorageTypeMinIO {
		accessKey = cfg.Storage.MinIO.AccessKey
		secretKey = cfg.Storage.MinIO.SecretKey
	}

	storageConfig := &storage.StorageConfig{
		Type:        st,
		LocalPath:   cfg.GetLocalStoragePath(),
		S3Bucket:    cfg.Storage.S3.Bucket,
		S3Region:    cfg.Storage.S3.Region,
		S3Endpoint:  cfg.Storage.S3.Endpoint,
		AccessKey:   accessKey,
		SecretKey:   secretKey,
		MinIOBucket:   cfg.Storage.MinIO.Bucket,
		MinIOEndpoint: cfg.Storage.MinIO.Endpoint,
		MinIORegion:   cfg.Storage.MinIO.Region,
		MinIOSecure:   cfg.Storage.MinIO.Secure,
		R2Bucket:    cfg.Storage.R2.Bucket,
		R2AccountID: cfg.Storage.R2.AccountID,
		R2AccessKey: cfg.Storage.R2.AccessKey,
		R2SecretKey: cfg.Storage.R2.SecretKey,
	}

	return storage.NewStorageBackend(storageConfig)
}

// buildNamedBackend creates a storage backend from a named BackendConfig.
func buildNamedBackend(bcfg config.BackendConfig) (storage.StorageBackend, error) {
	st := storage.StorageType(bcfg.Type)

	storageConfig := &storage.StorageConfig{
		Type:      st,
		LocalPath: bcfg.Path,
		// S3 fields
		S3Bucket:   bcfg.Bucket,
		S3Region:   bcfg.Region,
		S3Endpoint: bcfg.Endpoint,
		AccessKey:  bcfg.AccessKey,
		SecretKey:  bcfg.SecretKey,
		// MinIO fields
		MinIOBucket:   bcfg.Bucket,
		MinIOEndpoint: bcfg.Endpoint,
		MinIORegion:   bcfg.Region,
		MinIOSecure:   bcfg.Secure,
		// R2 fields
		R2Bucket:    bcfg.Bucket,
		R2AccountID: bcfg.AccountID,
		R2AccessKey: bcfg.AccessKey,
		R2SecretKey: bcfg.SecretKey,
	}

	return storage.NewStorageBackend(storageConfig)
}
