// Command falco serves the image processing service: upload, delivery, proxying
// of external images, and the admin panel.
//
// Object bytes are not stored here — they are delegated to a storage backend,
// which in birdple-v2 is jay over its native TCP protocol.
package main

import (
	"context"
	"errors"
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

// defaultShutdownTimeout is used when SERVER_SHUTDOWN_TIMEOUT is unset.
const defaultShutdownTimeout = 30 * time.Second

func main() {
	cfg, err := config.Load()
	if err != nil {
		logger.Fatal().Err(err).Msg("Failed to load configuration")
	}

	logger.Setup(logger.Config{
		Level:        cfg.Logging.Level,
		Format:       cfg.Logging.Format,
		Output:       cfg.Logging.Output,
		EnableCaller: cfg.Logging.EnableCaller,
	})
	logger.Info().Msg("Starting Falco Image Processing Service")

	// Non-fatal: with OTEL_EXPORTER_OTLP_ENDPOINT unset, telemetry is skipped
	// rather than failing — otherwise local dev drowns in "connection refused".
	otelShutdown, err := telemetry.Init(context.Background(), "falco")
	if err != nil {
		logger.Warn().Err(err).Msg("telemetry init failed, continuing without")
	}

	configureTrustedProxies(cfg)
	startVips()
	defer vips.Shutdown()

	logConfiguration(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	storageReg, err := initializeStorage(cfg)
	if err != nil {
		logger.Fatal().Err(err).Msg("Failed to initialize storage")
	}

	imageProcessor := buildImageProcessor(cfg)
	appCache := buildCache(cfg)
	if appCache != nil {
		imageProcessor.SetCache(appCache)
	}

	server := api.NewServer(&api.ServerConfig{
		Config:          cfg,
		Storage:         storageReg.Default(),
		StorageRegistry: storageReg,
		ImageProcessor:  imageProcessor,
	})

	serverErr := make(chan error, 1)
	go func() {
		logger.Info().Str("address", cfg.GetServerAddress()).Msg("Server starting")
		if err := server.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	select {
	case err := <-serverErr:
		logger.Error().Err(err).Msg("Server error")
	case <-setupGracefulShutdown(ctx, cancel):
		logger.Info().Msg("Shutdown signal received")
	}

	shutdownEverything(cfg, otelShutdown, server, storageReg, appCache)
}

// configureTrustedProxies decides whose X-Forwarded-For / X-Real-IP headers are
// believed.
//
// Loopback is always trusted. An empty TRUSTED_PROXIES means no external proxy
// is — fail-closed, so an operator has to opt their load balancer's subnet in
// explicitly rather than inherit a spoofable client IP by accident.
func configureTrustedProxies(cfg *config.Config) {
	httputil.SetTrustedProxies(cfg.Security.TrustedProxies)
	if len(cfg.Security.TrustedProxies) > 0 {
		logger.Info().Strs("trusted_proxies", cfg.Security.TrustedProxies).Msg("Trusted proxies configured")
		return
	}
	logger.Info().Msg("No TRUSTED_PROXIES set; only loopback is trusted to forward client IPs")
}

// startVips boots libvips.
//
// vips.Startup(nil) looks harmless but is not: vipsgen reads a nil config as
// "vector (SIMD) disabled", which measurably slows every encode. The explicit
// Config below matches nil's other defaults — the operation cache stays off,
// which is right here because every proxied image is distinct — and flips
// VectorEnabled on, which is the whole point of passing one.
func startVips() {
	vips.Startup(&vips.Config{
		ConcurrencyLevel: 1,    // 1 thread per pipeline; CONCURRENT_WORKERS governs real parallelism
		MaxCacheFiles:    0,    // operation cache off (unchanged from nil default)
		MaxCacheMem:      0,    // unchanged from nil default
		MaxCacheSize:     0,    // unchanged from nil default
		VectorEnabled:    true, // enable libvips' SIMD paths — silently off under Startup(nil)
	})
}

// logConfiguration emits the effective configuration at startup. Bucket and
// group names only, never their credentials.
func logConfiguration(cfg *config.Config) {
	bucketNames := make([]string, 0, len(cfg.Storage.Buckets))
	for name := range cfg.Storage.Buckets {
		bucketNames = append(bucketNames, name)
	}
	groupNames := make([]string, 0, len(cfg.Storage.Groups))
	for name := range cfg.Storage.Groups {
		groupNames = append(groupNames, name)
	}

	logger.Info().
		Str("default_bucket", cfg.Storage.Default).
		Strs("buckets", bucketNames).
		Strs("groups", groupNames).
		Int("port", cfg.Server.Port).
		Str("host", cfg.Server.Host).
		Int("cache_size_mb", cfg.Cache.SizeMB).
		Int("cache_ttl_hours", cfg.Cache.TTLHrs).
		Bool("redis_enabled", cfg.Cache.EnableRedis).
		Int("max_file_size_mb", cfg.Processing.MaxFileSizeMB).
		Int("default_quality", cfg.Processing.DefaultQuality).
		Str("default_format", cfg.Processing.DefaultFormat).
		Int("concurrent_workers", cfg.Processing.ConcurrentWorkers).
		Int("webp_effort", cfg.Processing.WebPEffort).
		Bool("api_key_required", cfg.Security.APIKeyRequired).
		Strs("cors_origins", cfg.Security.CORS.Origins).
		Int("rate_limit_rpm", cfg.Security.RateLimit.RequestsPerMinute).
		Bool("hmac_required", cfg.Security.HMACRequired).
		Bool("metrics_enabled", cfg.Development.EnableMetrics).
		Bool("debug", cfg.Development.Debug).
		Msg("Configuration loaded")
}

// buildImageProcessor creates the processor and applies the runtime knobs that
// only exist on the vips implementation.
func buildImageProcessor(cfg *config.Config) processor.ImageProcessor {
	imageProcessor := processor.NewImageProcessor(
		cfg.Processing.MaxFileSizeMB,
		cfg.Processing.DefaultQuality,
		processor.ImageFormat(cfg.Processing.DefaultFormat),
		cfg.Processing.MaxDimensions.Width,
		cfg.Processing.MaxDimensions.Height,
	)

	vp, ok := imageProcessor.(*processor.VipsProcessor)
	if !ok {
		return imageProcessor
	}

	// Bounding concurrency is what keeps a burst of large uploads from
	// exhausting CPU and memory: every decode holds the whole raster.
	if cfg.Processing.ConcurrentWorkers > 0 {
		vp.SetMaxConcurrency(cfg.Processing.ConcurrentWorkers)
		logger.Info().Int("max_concurrent", cfg.Processing.ConcurrentWorkers).Msg("Processing concurrency limit set")
	}
	vp.SetWebPEffort(cfg.Processing.WebPEffort)
	logger.Info().Int("webp_effort", cfg.Processing.WebPEffort).Msg("WebP encode effort set")
	vp.SetCacheTTL(cfg.GetCacheTTL())
	logger.Info().Dur("cache_ttl", cfg.GetCacheTTL()).Msg("Cache entry TTL set")

	return imageProcessor
}

// buildCache picks the cache backend: Redis when configured and reachable,
// otherwise an in-process sharded LRU. Returns nil when caching is disabled.
//
// A Redis that fails to connect degrades to the LRU rather than aborting: the
// cache is an optimisation, and falco still serves correctly without it.
func buildCache(cfg *config.Config) processor.Cache {
	if cfg.Cache.EnableRedis && cfg.Cache.RedisURL != "" {
		redisCache, err := cache.NewRedisCache(cfg.Cache.RedisURL, cfg.GetCacheTTL())
		if err != nil {
			logger.Warn().Err(err).Msg("Failed to initialize Redis cache, falling back to LRU cache")
		} else {
			logger.Info().Str("redis_url", cfg.Cache.RedisURL).Msg("Redis cache initialized")
			return redisCache
		}
	}

	cacheSize := cfg.GetCacheSizeBytes()
	if cacheSize <= 0 {
		return nil
	}

	// The second argument is the background sweep frequency, NOT the per-entry
	// TTL — those are different knobs, and conflating them here is what once
	// made CACHE_TTL_HOURS a no-op: it only ever reached this parameter and
	// never the expiry actually used when writing an entry. The per-entry TTL
	// is set by VipsProcessor.SetCacheTTL.
	shardedCache := cache.NewShardedCache(cacheSize, cfg.Cache.CleanupInterval)
	logger.Info().
		Int("cache_size_mb", cfg.Cache.SizeMB).
		Dur("cleanup_interval", cfg.Cache.CleanupInterval).
		Msg("Sharded LRU cache initialized")
	return shardedCache
}

// shutdownEverything tears the process down in order.
//
// Telemetry flushes first, so the spans describing the shutdown itself make it
// out; then the server stops accepting requests; only then are the resources
// those requests were using released.
func shutdownEverything(
	cfg *config.Config,
	otelShutdown func(context.Context) error,
	server *api.Server,
	storageReg *storage.Registry,
	appCache processor.Cache,
) {
	timeout := cfg.Server.ShutdownTimeout
	if timeout == 0 {
		timeout = defaultShutdownTimeout
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	logger.Info().Msg("Initiating graceful shutdown...")

	if otelShutdown != nil {
		if err := otelShutdown(shutdownCtx); err != nil {
			logger.Warn().Err(err).Msg("Telemetry shutdown error")
		}
	}

	logger.Info().Msg("Phase 1: Stopping server (no new requests)")
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error().Err(err).Msg("Server shutdown error")
	}

	logger.Info().Msg("Phase 2: Cleaning up resources")
	cleanupResources(shutdownCtx, storageReg, appCache)

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

// cleanupResources performs cleanup of application resources.
//
// Actually waits on in-flight async replications via Registry.CloseAll, instead
// of the fixed time.Sleep(100ms) this replaced: a sleep does not know whether
// the work finished, it only pretends it did.
func cleanupResources(ctx context.Context, storageReg *storage.Registry, appCache processor.Cache) {
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

	if storageReg != nil {
		logger.Info().Msg("Waiting for in-flight storage replication...")
		if failures := storageReg.CloseAll(ctx); len(failures) > 0 {
			for name, err := range failures {
				logger.Error().Err(err).Str("bucket", name).
					Msg("Storage backend did not finish cleanly — replicas may be stale")
			}
		} else {
			logger.Info().Msg("Storage connections released")
		}
	}
}

// initializeStorage builds all bucket backends from config, wraps them with
// ReplicatedStorage if they have backups, and registers them in a Registry.
func initializeStorage(cfg *config.Config) (*storage.Registry, error) {
	if len(cfg.Storage.Buckets) == 0 {
		return nil, errors.New("no storage buckets configured")
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
