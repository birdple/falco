package api

import (
	"context"
	"io/fs"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/birdple/falco/docs"
	"github.com/birdple/falco/internal/api/handlers"
	"github.com/birdple/falco/internal/api/handlers/ui"
	apimw "github.com/birdple/falco/internal/api/middleware"
	"github.com/birdple/falco/internal/config"
	"github.com/birdple/falco/internal/pkg/logger"
	"github.com/birdple/falco/internal/pkg/metrics"
	"github.com/birdple/falco/internal/processor"
	"github.com/birdple/falco/internal/storage"
	"github.com/birdple/falco/web"
)

// ServerConfig holds configuration for the API server
type ServerConfig struct {
	Config          *config.Config
	Storage         storage.StorageBackend
	StorageRegistry *storage.Registry
	ImageProcessor  processor.ImageProcessor
}

// Server represents the HTTP server
type Server struct {
	config    *config.Config
	router    *chi.Mux
	server    *http.Server
	handler   *handlers.Handler
	uiHandler *ui.Handler
	metrics   *metrics.Metrics
	registry  *storage.Registry
}

// NewServer creates a new API server
func NewServer(cfg *ServerConfig) *Server {
	h := handlers.NewHandler(
		cfg.Config,
		cfg.Storage,
		cfg.ImageProcessor,
		time.Now(),
	)

	if cfg.StorageRegistry != nil {
		h.SetRegistry(cfg.StorageRegistry)
	}

	m := metrics.New()

	s := &Server{
		config:    cfg.Config,
		handler:   h,
		uiHandler: ui.NewHandler(cfg.Config, cfg.StorageRegistry),
		metrics:   m,
		registry:  cfg.StorageRegistry,
	}

	s.setupRouter()
	s.setupServer()

	return s
}

// setupRouter configures the Chi router with middleware and routes
func (s *Server) setupRouter() {
	r := chi.NewRouter()

	// Security middleware
	r.Use(apimw.SecurityHeaders)
	r.Use(middleware.RequestID)
	// apimw.RealIP replaces chi's middleware.RealIP, which trusts
	// X-Forwarded-For / X-Real-IP unconditionally. See realip.go.
	r.Use(apimw.RealIP)
	r.Use(apimw.ZerologRequestLogger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(30 * time.Second))

	// Request size limiting
	maxRequestSize := s.config.GetMaxFileSizeBytes() * 2
	sizeLimiter := apimw.NewRequestSizeLimiter(maxRequestSize)
	r.Use(sizeLimiter.Handler)

	// Compression middleware — only for text-like responses. Do NOT compress
	// image/* bodies: webp/jpeg/png/avif are already compressed, so gzipping
	// them on the fly wastes CPU and usually grows the payload. chi's Compress
	// accepts a variadic allowlist of content types; anything not in the list
	// is streamed through untouched.
	r.Use(middleware.Compress(
		5,
		"text/html",
		"text/plain",
		"text/css",
		"text/javascript",
		"application/javascript",
		"application/json",
		"application/yaml",
		"application/xml",
		"image/svg+xml",
	))

	// Metrics middleware
	if s.config.Development.EnableMetrics {
		metricsMiddleware := apimw.NewMetricsMiddleware(s.metrics)
		r.Use(metricsMiddleware.Handler)
	}

	// Rate limiting
	if s.config.Security.RateLimit.RequestsPerMinute > 0 {
		rateLimiter := apimw.NewRateLimiter(
			s.config.Security.RateLimit.RequestsPerMinute,
			s.config.Security.RateLimit.Burst,
		)
		r.Use(rateLimiter.Handler)
	}

	// CORS middleware
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   s.config.Security.CORS.Origins,
		AllowedMethods:   s.config.Security.CORS.Methods,
		AllowedHeaders:   append(s.config.Security.CORS.Headers, "X-API-Key", "Authorization"),
		ExposedHeaders:   []string{"Link", "X-RateLimit-Limit", "X-RateLimit-Remaining"},
		AllowCredentials: false,
		MaxAge:           300,
	}))

	// UI Routes (public - auth handled internally via cookie/key)
	r.Get("/", s.uiHandler.Login)
	r.Get("/dashboard", s.uiHandler.Dashboard)
	r.Post("/ui/auth", s.uiHandler.AuthPost)
	r.Post("/ui/logout", s.uiHandler.LogoutPost)
	r.Get("/ui/content", s.uiHandler.Content)

	// Static files (embedded in binary)
	staticFS, _ := fs.Sub(web.StaticFS, "static")
	r.Handle("/static/*", http.StripPrefix("/static/", apimw.RestrictedFileServer(http.FS(staticFS))))

	// Health check endpoint (no auth required)
	r.Get("/health", s.handler.HandleHealth)
	r.Head("/health", s.handler.HandleHealth)

	// Docs endpoint
	r.Get("/docs", s.handler.HandleDocs)
	r.Get("/docs/openapi.yaml", func(w http.ResponseWriter, r *http.Request) {
		data, err := docs.FS.ReadFile("openapi.yaml")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/yaml")
		w.Write(data)
	})

	// Prometheus metrics endpoint (protected by API key when auth is enabled)
	if s.config.Development.EnableMetrics {
		r.Group(func(r chi.Router) {
			if s.config.Security.APIKeyRequired {
				apiKeyAuth := apimw.NewAPIKeyAuth(s.config.Security.APIKey)
				r.Use(apiKeyAuth.Handler)
			}
			r.Handle("/metrics", promhttp.Handler())
		})
	}

	// API routes
	r.Route("/api/v1", func(r chi.Router) {
		// Public endpoint
		r.Get("/images/*", s.handler.HandleDelivery)

		// Protected endpoints
		r.Group(func(r chi.Router) {
			if s.config.Security.APIKeyRequired {
				scopedAuth := apimw.NewScopedAPIKeyAuth(s.config.Security.APIKey, s.config)
				if scopedAuth.HasScopedKeys() {
					r.Use(scopedAuth.Handler)
				} else {
					apiKeyAuth := apimw.NewAPIKeyAuth(s.config.Security.APIKey)
					r.Use(apiKeyAuth.Handler)
				}
			}

			r.Post("/upload", s.handler.HandleUpload)
			r.Post("/update", s.handler.HandleUpdate)
			r.Get("/list", s.handler.HandleList)
			r.Delete("/delete", s.handler.HandleDelete)
			r.Post("/sign", s.handler.HandleSignURL)
		})
	})

	r.NotFound(s.handleNotFound)

	s.router = r
}

// handleNotFound handles requests for non-existent routes
func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	http.NotFound(w, r)
}

// setupServer configures the HTTP server
func (s *Server) setupServer() {
	s.server = &http.Server{
		Addr:              s.config.GetServerAddress(),
		Handler:           s.router,
		ReadTimeout:       s.config.Server.ReadTimeout,
		WriteTimeout:      s.config.Server.WriteTimeout,
		IdleTimeout:       s.config.Server.IdleTimeout,
		ReadHeaderTimeout: 10 * time.Second,
	}
}

// Start starts the HTTP server
func (s *Server) Start() error {
	logger.Info().Str("address", s.config.GetServerAddress()).Msg("Starting HTTP server")
	return s.server.ListenAndServe()
}

// Shutdown gracefully shuts down the server
func (s *Server) Shutdown(ctx context.Context) error {
	logger.Info().Msg("Initiating HTTP server shutdown")

	shutdownCtx := ctx
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 30*time.Second {
		var cancel context.CancelFunc
		shutdownCtx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}

	err := s.server.Shutdown(shutdownCtx)
	if err != nil {
		logger.Warn().Err(err).Msg("HTTP server shutdown completed with errors")
		return err
	}

	logger.Info().Msg("HTTP server shutdown completed successfully")
	return nil
}

// Router returns the Chi router (useful for testing)
func (s *Server) Router() *chi.Mux {
	return s.router
}
