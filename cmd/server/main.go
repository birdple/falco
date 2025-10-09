package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/birdple/imagine/internal/api"
	apimw "github.com/birdple/imagine/internal/api/middleware"
	"github.com/birdple/imagine/internal/cache"
	"github.com/birdple/imagine/internal/config"
	"github.com/birdple/imagine/internal/database"
	"github.com/birdple/imagine/internal/processor"
	"github.com/birdple/imagine/internal/storage"
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
	var lruCache *cache.LRUCache
	cacheSize := cfg.GetCacheSizeBytes()
	if cacheSize > 0 {
		lruCache = cache.NewLRUCache(cacheSize, cfg.GetCacheTTL())
		imageProcessor.SetCache(lruCache)
		logger.WithField("cache_size_mb", cfg.Cache.SizeMB).Info("Cache initialized")
	}

	// Initialize Honeypot
	honeypotDB, err := database.NewHoneypotDB(cfg.Security.Honeypot.DBPath, logger)
	if err != nil {
		logger.WithError(err).Fatal("Failed to initialize honeypot database")
	}
	honeypot := apimw.NewHoneypot(honeypotDB, logger)

	// Initialize API server
	server := api.NewServer(&api.ServerConfig{
		Config:         cfg,
		Logger:         logger,
		Storage:        storageBackend,
		ImageProcessor: imageProcessor,
		Honeypot:       honeypot,
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
	cleanupResources(ctx, storageBackend, lruCache, honeypotDB, logger)

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

		// Wait for signal
		sig := <-sigChan
		logger.WithField("signal", sig.String()).Info("Received shutdown signal")

		// Cancel context to signal shutdown to all goroutines
		cancel()

		// Give a brief moment for cleanup
		time.Sleep(50 * time.Millisecond)
	}()

	return shutdown
}

// cleanupResources performs cleanup of application resources
func cleanupResources(ctx context.Context, storageBackend storage.StorageBackend, lruCache *cache.LRUCache, honeypotDB *database.HoneypotDB, logger *logrus.Logger) {
	// Clean up honeypot database
	if honeypotDB != nil {
		logger.Info("Closing honeypot database...")
		if err := honeypotDB.Close(); err != nil {
			logger.WithError(err).Error("Failed to close honeypot database")
		}
		logger.Info("Honeypot database closed")
	}

	// Clean up cache
	if lruCache != nil {
		logger.Info("Cleaning up cache...")
		lruCache.Clear()
		logger.Info("Cache cleanup completed")
	}

	// Clean up storage connections (if applicable)
	if storageBackend != nil {
		logger.Info("Cleaning up storage connections...")
		// Note: Filesystem storage doesn't need explicit cleanup
		// S3 storage would close connections here if needed
		logger.Info("Storage cleanup completed")
	}

	// Clean up temporary files
	logger.Info("Cleaning up temporary files...")
	// Add cleanup for any temporary files if needed
	logger.Info("Temporary file cleanup completed")
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
