package main

import (
	"context"
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
		Str("storage_primary", cfg.Storage.Primary).
		Str("storage_secondary", cfg.Storage.Secondary).
		Str("s3_bucket", cfg.Storage.S3.Bucket).
		Str("minio_bucket", cfg.Storage.MinIO.Bucket).
		Str("minio_endpoint", cfg.Storage.MinIO.Endpoint).
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
	storageBackend, err := initializeStorage(cfg)
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
		Config:         cfg,
		Storage:        storageBackend,
		ImageProcessor: imageProcessor,
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

// initializeStorage initializes the storage backend based on configuration
func initializeStorage(cfg *config.Config) (storage.StorageBackend, error) {
	storageType := storage.StorageType(cfg.Storage.Primary)

	logger.Info().
		Str("requested_type", string(storageType)).
		Str("primary_config", cfg.Storage.Primary).
		Str("minio_endpoint", cfg.Storage.MinIO.Endpoint).
		Str("minio_bucket", cfg.Storage.MinIO.Bucket).
		Str("s3_bucket", cfg.Storage.S3.Bucket).
		Msg("Initializing storage backend")

	accessKey := cfg.Storage.S3.AccessKey
	secretKey := cfg.Storage.S3.SecretKey
	if storageType == storage.StorageTypeMinIO {
		accessKey = cfg.Storage.MinIO.AccessKey
		secretKey = cfg.Storage.MinIO.SecretKey
	}

	storageConfig := &storage.StorageConfig{
		Type:          storageType,
		LocalPath:     cfg.GetLocalStoragePath(),
		S3Bucket:      cfg.Storage.S3.Bucket,
		S3Region:      cfg.Storage.S3.Region,
		S3Endpoint:    cfg.Storage.S3.Endpoint,
		AccessKey:     accessKey,
		SecretKey:     secretKey,
		MinIOBucket:   cfg.Storage.MinIO.Bucket,
		MinIOEndpoint: cfg.Storage.MinIO.Endpoint,
		MinIORegion:   cfg.Storage.MinIO.Region,
		MinIOSecure:   cfg.Storage.MinIO.Secure,
	}

	backend, err := storage.NewStorageBackend(storageConfig)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to initialize storage backend")
		return nil, err
	}

	switch storageType {
	case storage.StorageTypeMinIO:
		logger.Info().Msg("Using MinIO storage backend")
	case storage.StorageTypeS3:
		logger.Info().Msg("Using S3 storage backend")
	case storage.StorageTypeFilesystem:
		logger.Info().Msg("Using filesystem storage backend")
	default:
		logger.Warn().Str("type", string(storageType)).Msg("Unknown storage backend type")
	}

	return backend, nil
}
