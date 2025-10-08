package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/ivangsm/imagine/internal/api"
	apimw "github.com/ivangsm/imagine/internal/api/middleware"
	"github.com/ivangsm/imagine/internal/cache"
	"github.com/ivangsm/imagine/internal/config"
	"github.com/ivangsm/imagine/internal/database"
	"github.com/ivangsm/imagine/internal/processor"
	"github.com/ivangsm/imagine/internal/storage"
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
	storageConfig := &storage.StorageConfig{
		Type:       storage.StorageType(cfg.Storage.Primary),
		LocalPath:  cfg.GetLocalStoragePath(),
		S3Bucket:   cfg.Storage.S3.Bucket,
		S3Region:   cfg.Storage.S3.Region,
		S3Endpoint: cfg.Storage.S3.Endpoint,
		AccessKey:  cfg.Storage.S3.AccessKey,
		SecretKey:  cfg.Storage.S3.SecretKey,
	}

	return storage.NewStorageBackend(storageConfig)
}
