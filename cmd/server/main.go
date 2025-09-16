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
	"github.com/ivangsm/imagine/internal/cache"
	"github.com/ivangsm/imagine/internal/config"
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

	// Initialize storage backend
	storageBackend, err := initializeStorage(cfg)
	if err != nil {
		logger.WithError(err).Fatal("Failed to initialize storage backend")
	}

	// Initialize image processor
	imageProcessor := processor.NewImageProcessor(
		cfg.Processing.MaxFileSizeMB,
		cfg.Processing.DefaultQuality,
		processor.ImageFormat(cfg.Processing.DefaultFormat),
		cfg.Processing.MaxDimensions.Width,
		cfg.Processing.MaxDimensions.Height,
	)

	// Initialize cache
	cacheSize := cfg.GetCacheSizeBytes()
	if cacheSize > 0 {
		lruCache := cache.NewLRUCache(cacheSize, 10*time.Minute)
		imageProcessor.SetCache(lruCache)
		logger.WithField("cache_size_mb", cfg.Cache.SizeMB).Info("Cache initialized")
	}

	// Initialize API server
	server := api.NewServer(&api.ServerConfig{
		Config:         cfg,
		Logger:         logger,
		Storage:        storageBackend,
		ImageProcessor: imageProcessor,
	})

	// Start server
	go func() {
		logger.WithField("address", cfg.GetServerAddress()).Info("Server starting")
		if err := server.Start(); err != nil && err != http.ErrServerClosed {
			logger.WithError(err).Fatal("Server failed to start")
		}
	}()

	// Wait for interrupt signal to gracefully shutdown the server
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("Shutting down server...")

	// Give outstanding requests a deadline for completion
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
	defer cancel()

	// Shutdown server
	if err := server.Shutdown(ctx); err != nil {
		logger.WithError(err).Error("Server forced to shutdown")
	}

	logger.Info("Server exited")
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
