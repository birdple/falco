package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sony/gobreaker"

	"github.com/birdple/falco/internal/api"
	"github.com/birdple/falco/internal/cache"
	"github.com/birdple/falco/internal/config"
	"github.com/birdple/falco/internal/pkg/circuitbreaker"
	"github.com/birdple/falco/internal/pkg/httputil"
	"github.com/birdple/falco/internal/pkg/logger"
	"github.com/birdple/falco/internal/processor"
	"github.com/birdple/falco/internal/storage"
	"github.com/birdple/falco/internal/telemetry"
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

	// Initialize OpenTelemetry. Non-fatal: if OTEL_EXPORTER_OTLP_ENDPOINT is
	// unset, telemetry is silently skipped (avoids "connection refused" spam
	// in local dev). Shutdown runs before HTTP shutdown below to flush spans.
	otelShutdown, err := telemetry.Init(context.Background(), "falco")
	if err != nil {
		logger.Warn().Err(err).Msg("telemetry init failed, continuing without")
	}

	// Configure trusted proxies for X-Forwarded-For / X-Real-IP header trust.
	// Loopback is always trusted; an empty TRUSTED_PROXIES env var means no
	// external proxy is trusted (fail-closed) — operators must opt into
	// trusting their reverse-proxy/load-balancer subnet explicitly.
	httputil.SetTrustedProxies(cfg.Security.TrustedProxies)
	if len(cfg.Security.TrustedProxies) > 0 {
		logger.Info().Strs("trusted_proxies", cfg.Security.TrustedProxies).Msg("Trusted proxies configured")
	} else {
		logger.Info().Msg("No TRUSTED_PROXIES set; only loopback is trusted to forward client IPs")
	}

	// Initialize VIPS
	vips.Startup(nil)
	defer vips.Shutdown()

	// Log sanitized configuration (no secrets)
	logBucketNames := make([]string, 0, len(cfg.Storage.Buckets))
	for name := range cfg.Storage.Buckets {
		logBucketNames = append(logBucketNames, name)
	}
	logGroupNames := make([]string, 0, len(cfg.Storage.Groups))
	for name := range cfg.Storage.Groups {
		logGroupNames = append(logGroupNames, name)
	}

	logger.Info().
		Str("default_bucket", cfg.Storage.Default).
		Strs("buckets", logBucketNames).
		Strs("groups", logGroupNames).
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
	storageReg, err := initializeStorage(cfg)
	if err != nil {
		logger.Fatal().Err(err).Msg("Failed to initialize storage")
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
			shardedCache := cache.NewShardedCache(cacheSize, cfg.GetCacheTTL())
			appCache = shardedCache
			logger.Info().Int("cache_size_mb", cfg.Cache.SizeMB).Msg("Sharded LRU cache initialized")
		}
	}

	if appCache != nil {
		imageProcessor.SetCache(appCache)
	}

	// Initialize API server
	server := api.NewServer(&api.ServerConfig{
		Config:          cfg,
		Storage:         storageReg.Default(),
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

	// Phase 0: Flush in-flight OTel spans/metrics before tearing down servers.
	if otelShutdown != nil {
		if err := otelShutdown(shutdownCtx); err != nil {
			logger.Warn().Err(err).Msg("Telemetry shutdown error")
		}
	}

	// Phase 1: Stop accepting new requests
	logger.Info().Msg("Phase 1: Stopping server (no new requests)")
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error().Err(err).Msg("Server shutdown error")
	}

	// Phase 2: Clean up resources
	logger.Info().Msg("Phase 2: Cleaning up resources")
	cleanupResources(ctx, storageReg.Default(), appCache)

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
		if sharded, ok := appCache.(*cache.ShardedCache); ok {
			sharded.Stop()
		} else if lru, ok := appCache.(*cache.LRUCache); ok {
			lru.Stop()
		} else if redis, ok := appCache.(*cache.RedisCache); ok {
			redis.Stop()
		}
		logger.Info().Msg("Clearing cache...")
		appCache.Clear()
		logger.Info().Msg("Cache cleanup completed")
	}

	if storageBackend != nil {
		logger.Info().Msg("Storage connections released")
	}
}

// initializeStorage builds all bucket backends from config, wraps them with
// ReplicatedStorage if they have backups, and registers them in a Registry.
func initializeStorage(cfg *config.Config) (*storage.Registry, error) {
	if len(cfg.Storage.Buckets) == 0 {
		return nil, fmt.Errorf("no storage buckets configured")
	}

	// First pass: build raw backends (without backup wrappers)
	rawBackends := make(map[string]storage.StorageBackend, len(cfg.Storage.Buckets))
	for name, bucketCfg := range cfg.Storage.Buckets {
		backend, err := buildBucketBackend(bucketCfg)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize bucket %q: %w", name, err)
		}
		rawBackends[name] = backend
		logger.Info().Str("name", name).Str("type", bucketCfg.Type).Msg("Storage bucket initialized")
	}

	// Second pass: wrap buckets that have backups with ReplicatedStorage
	finalBackends := make(map[string]storage.StorageBackend, len(cfg.Storage.Buckets))
	for name, bucketCfg := range cfg.Storage.Buckets {
		primary := rawBackends[name]

		if len(bucketCfg.Backups) > 0 {
			targets := make([]storage.BackupTarget, 0, len(bucketCfg.Backups))
			for _, ref := range bucketCfg.Backups {
				targetBackend, ok := rawBackends[ref.Target]
				if !ok {
					return nil, fmt.Errorf("bucket %q: backup target %q not found", name, ref.Target)
				}
				mode := storage.ReplicationMode(ref.Mode)
				if mode == "" {
					mode = storage.ReplicationSync
				}
				targets = append(targets, storage.BackupTarget{
					Backend: targetBackend,
					Mode:    mode,
				})
				logger.Info().
					Str("bucket", name).
					Str("target", ref.Target).
					Str("mode", string(mode)).
					Msg("Backup target configured")
			}
			primary = storage.NewReplicatedStorage(primary, targets)
		}

		// Wrap with circuit breaker for fault tolerance
		cbSettings := circuitbreaker.DefaultSettings(name)
		cbSettings.OnStateChange = func(cbName string, from, to gobreaker.State) {
			logger.Warn().
				Str("bucket", cbName).
				Str("from", from.String()).
				Str("to", to.String()).
				Msg("Circuit breaker state change")
		}
		finalBackends[name] = circuitbreaker.NewStorageBackend(primary, cbSettings)
	}

	// Build registry
	defaultBackend := finalBackends[cfg.Storage.Default]
	reg := storage.NewRegistry(defaultBackend)

	for name, backend := range finalBackends {
		reg.Register(name, backend)
	}

	if err := reg.SetDefault(cfg.Storage.Default); err != nil {
		return nil, fmt.Errorf("failed to set default bucket: %w", err)
	}

	logger.Info().
		Int("bucket_count", reg.Len()).
		Str("default", reg.DefaultName()).
		Strs("buckets", reg.Names()).
		Msg("Storage initialized")

	return reg, nil
}

// buildBucketBackend creates a storage backend from a BucketConfig.
func buildBucketBackend(bcfg config.BucketConfig) (storage.StorageBackend, error) {
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
		// Jay fields
		JayAddr:      bcfg.JayAddr,
		JayAdminAddr: bcfg.JayAdminAddr,
		JayTokenID:   bcfg.JayTokenID,
		JayTokenSec:  bcfg.JayTokenSec,
		JayBucket:    bcfg.Bucket,
		JayPoolSize:  bcfg.JayPoolSize,
	}

	return storage.NewStorageBackend(storageConfig)
}
