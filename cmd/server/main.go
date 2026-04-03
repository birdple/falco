package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/birdple/falco/internal/api"
	"github.com/birdple/falco/internal/cache"
	"github.com/birdple/falco/internal/config"
	"github.com/birdple/falco/internal/processor"
	"github.com/birdple/falco/internal/storage"
	"github.com/cshum/vipsgen/vips"
)

func main() {
	// Initialize logger
	logger := logrus.New()
	logger.SetFormatter(&logrus.JSONFormatter{})
	logger.SetLevel(logrus.InfoLevel)

	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		logger.WithError(err).Fatal("Failed to load configuration")
	}

	// Set log level from configuration
	if level, err := logrus.ParseLevel(cfg.Logging.Level); err == nil {
		logger.SetLevel(level)
	}

	logger.Info("Starting Imagine Image Processing Service")

	// Initialize VIPS
	vips.Startup(nil)
	defer vips.Shutdown()

	// Debug: Show storage configuration
	logger.WithFields(logrus.Fields{
		"storage_primary":   cfg.Storage.Primary,
		"storage_secondary": cfg.Storage.Secondary,
		"s3_bucket":         cfg.Storage.S3.Bucket,
		"minio_bucket":      cfg.Storage.MinIO.Bucket,
		"minio_endpoint":    cfg.Storage.MinIO.Endpoint,
		"local_path":        cfg.GetLocalStoragePath(),
	}).Info("Storage configuration loaded")

	// Create application context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Initialize components
	storageBackend, err := initializeStorage(cfg)
	if err != nil {
		logger.WithError(err).Fatal("Failed to initialize storage backend")
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
			logger.WithError(err).Warn("Failed to initialize Redis cache, falling back to LRU cache")
		} else {
			appCache = redisCache
			logger.WithField("redis_url", cfg.Cache.RedisURL).Info("Redis cache initialized")
		}
	}

	if appCache == nil {
		cacheSize := cfg.GetCacheSizeBytes()
		if cacheSize > 0 {
			lruCache := cache.NewLRUCache(cacheSize, cfg.GetCacheTTL())
			appCache = lruCache
			logger.WithField("cache_size_mb", cfg.Cache.SizeMB).Info("LRU cache initialized")
		}
	}

	if appCache != nil {
		imageProcessor.SetCache(appCache)
	}

	// Initialize API server
	server := api.NewServer(&api.ServerConfig{
		Config:         cfg,
		Logger:         logger,
		Storage:        storageBackend,
		ImageProcessor: imageProcessor,
	})

	// Start server in a goroutine
	serverErr := make(chan error, 1)
	go func() {
		logger.WithField("address", cfg.GetServerAddress()).Info("Server starting")
		if err := server.Start(); err != nil && err != http.ErrServerClosed {
			serverErr <- err
		}
	}()

	// Set up graceful shutdown
	shutdown := setupGracefulShutdown(ctx, cancel, logger)

	// Wait for either server error or shutdown signal
	select {
	case err := <-serverErr:
		logger.WithError(err).Error("Server error")
	case <-shutdown:
		logger.Info("Shutdown signal received")
	}

	// Perform graceful shutdown
	shutdownTimeout := cfg.Server.ShutdownTimeout
	if shutdownTimeout == 0 {
		shutdownTimeout = 30 * time.Second
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer shutdownCancel()

	logger.Info("Initiating graceful shutdown...")

	// Phase 1: Stop accepting new requests
	logger.Info("Phase 1: Stopping server (no new requests)")
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.WithError(err).Error("Server shutdown error")
	}

	// Phase 2: Clean up resources
	logger.Info("Phase 2: Cleaning up resources")
	cleanupResources(ctx, storageBackend, appCache, logger)

	// Phase 3: Final cleanup
	logger.Info("Phase 3: Final cleanup")
	time.Sleep(100 * time.Millisecond) // Brief pause for cleanup

	logger.Info("Server shutdown complete")
}

// setupGracefulShutdown sets up signal handling for graceful shutdown
func setupGracefulShutdown(ctx context.Context, cancel context.CancelFunc, logger *logrus.Logger) <-chan struct{} {
	shutdown := make(chan struct{})

	go func() {
		defer close(shutdown)

		// Create signal channel
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan,
			syscall.SIGINT,  // Ctrl+C
			syscall.SIGTERM, // Termination signal
			syscall.SIGHUP,  // Terminal closed
		)

		// Wait for signal or context cancellation
		select {
		case sig := <-sigChan:
			logger.WithField("signal", sig.String()).Info("Received shutdown signal")
		case <-ctx.Done():
			logger.Info("Context cancelled, initiating shutdown")
		}

		// Cancel context to signal shutdown to all goroutines
		cancel()

		// Give a brief moment for cleanup
		time.Sleep(50 * time.Millisecond)
	}()

	return shutdown
}

// cleanupResources performs cleanup of application resources
func cleanupResources(ctx context.Context, storageBackend storage.StorageBackend, appCache processor.Cache, logger *logrus.Logger) {
	// Clean up cache
	if appCache != nil {
		logger.Info("Stopping cache...")
		if lru, ok := appCache.(*cache.LRUCache); ok {
			lru.Stop()
		} else if redis, ok := appCache.(*cache.RedisCache); ok {
			redis.Stop()
		}
		logger.Info("Clearing cache...")
		appCache.Clear()
		logger.Info("Cache cleanup completed")
	}

	// Clean up storage connections (if applicable)
	if storageBackend != nil {
		logger.Info("Cleaning up storage connections...")
		// Note: Filesystem storage doesn't need explicit cleanup
		// S3 storage would close connections here if needed
		logger.Info("Storage cleanup completed")
	}

	// Clean up temporary files with timeout
	logger.Info("Cleaning up temporary files...")
	cleanupDone := make(chan struct{})
	go func() {
		defer close(cleanupDone)
		// Add cleanup for any temporary files if needed
		// This could include removing temp files created during image processing
	}()

	select {
	case <-cleanupDone:
		logger.Info("Temporary file cleanup completed")
	case <-ctx.Done():
		logger.Warn("Temporary file cleanup interrupted by shutdown timeout")
	}
}

// initializeStorage initializes the storage backend based on configuration
func initializeStorage(cfg *config.Config) (storage.StorageBackend, error) {
	storageType := storage.StorageType(cfg.Storage.Primary)

	// Debug: Show what storage type we're trying to initialize
	logrus.WithFields(logrus.Fields{
		"requested_type": storageType,
		"primary_config": cfg.Storage.Primary,
		"minio_endpoint": cfg.Storage.MinIO.Endpoint,
		"minio_bucket":   cfg.Storage.MinIO.Bucket,
		"s3_bucket":      cfg.Storage.S3.Bucket,
	}).Info("Initializing storage backend")

	// Determine which credentials to use based on storage type
	accessKey := cfg.Storage.S3.AccessKey
	secretKey := cfg.Storage.S3.SecretKey
	if storageType == storage.StorageTypeMinIO {
		accessKey = cfg.Storage.MinIO.AccessKey
		secretKey = cfg.Storage.MinIO.SecretKey
	}

	storageConfig := &storage.StorageConfig{
		Type:       storageType,
		LocalPath:  cfg.GetLocalStoragePath(),
		S3Bucket:   cfg.Storage.S3.Bucket,
		S3Region:   cfg.Storage.S3.Region,
		S3Endpoint: cfg.Storage.S3.Endpoint,
		AccessKey:  accessKey,
		SecretKey:  secretKey,
		// MinIO configuration
		MinIOBucket:   cfg.Storage.MinIO.Bucket,
		MinIOEndpoint: cfg.Storage.MinIO.Endpoint,
		MinIORegion:   cfg.Storage.MinIO.Region,
		MinIOSecure:   cfg.Storage.MinIO.Secure,
	}

	backend, err := storage.NewStorageBackend(storageConfig)
	if err != nil {
		logrus.WithError(err).Error("Failed to initialize storage backend")
		return nil, err
	}

	// Show what type of storage we actually created
	switch storageType {
	case storage.StorageTypeMinIO:
		logrus.Info("Using MinIO storage backend")
	case storage.StorageTypeS3:
		logrus.Info("Using S3 storage backend")
	case storage.StorageTypeFilesystem:
		logrus.Info("Using filesystem storage backend")
	default:
		logrus.WithField("type", storageType).Warn("Unknown storage backend type")
	}

	return backend, nil
}
