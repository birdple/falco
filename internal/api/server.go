package api

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/birdple/falco/internal/api/handlers"
	"github.com/birdple/falco/internal/api/handlers/ui"
	apimw "github.com/birdple/falco/internal/api/middleware"
	"github.com/birdple/falco/internal/api/views"
	"github.com/birdple/falco/internal/config"
	"github.com/birdple/falco/internal/pkg/logger"
	"github.com/birdple/falco/internal/pkg/metrics"
	"github.com/birdple/falco/internal/processor"
	"github.com/birdple/falco/internal/storage"
)

// ServerConfig holds configuration for the API server
type ServerConfig struct {
	Config         *config.Config
	Storage        storage.StorageBackend
	ImageProcessor processor.ImageProcessor
}

// Server represents the HTTP server
type Server struct {
	config    *config.Config
	router    *chi.Mux
	server    *http.Server
	handler   *handlers.Handler
	uiHandler *ui.Handler
	metrics   *metrics.Metrics
}

// NewServer creates a new API server
func NewServer(cfg *ServerConfig) *Server {
	h := handlers.NewHandler(
		cfg.Config,
		cfg.Storage,
		cfg.ImageProcessor,
		time.Now(),
	)

	m := metrics.New()

	renderer, err := views.NewRenderer()
	if err != nil {
		logger.Error().Err(err).Msg("Failed to initialize UI renderer")
	}

	s := &Server{
		config:    cfg.Config,
		handler:   h,
		uiHandler: ui.NewHandler(renderer, cfg.Storage),
		metrics:   m,
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
	r.Use(middleware.RealIP)
	r.Use(apimw.ZerologRequestLogger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(30 * time.Second))

	// Request size limiting
	maxRequestSize := s.config.GetMaxFileSizeBytes() * 2
	sizeLimiter := apimw.NewRequestSizeLimiter(maxRequestSize)
	r.Use(sizeLimiter.Handler)

	// Compression middleware
	r.Use(middleware.Compress(5))

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

	// UI Routes
	r.Get("/", s.uiHandler.Index)

	// Static files - restrict to known extensions
	r.Handle("/static/*", http.StripPrefix("/static/", apimw.RestrictedFileServer(http.Dir("web/static"))))

	// Health check endpoint (no auth required)
	r.Get("/health", s.handler.HandleHealth)

	// Docs endpoint
	r.Get("/docs", s.handler.HandleDocs)
	r.Get("/docs/openapi.yaml", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "./docs/openapi.yaml")
	})

	// Prometheus metrics endpoint
	if s.config.Development.EnableMetrics {
		r.Handle("/metrics", promhttp.Handler())
	}

	// API routes
	r.Route("/api/v1", func(r chi.Router) {
		// Public endpoint
		r.Get("/images/*", s.handler.HandleDelivery)

		// Protected endpoints
		r.Group(func(r chi.Router) {
			if s.config.Security.APIKeyRequired {
				apiKeyAuth := apimw.NewAPIKeyAuth(s.config.Security.APIKey)
				r.Use(apiKeyAuth.Handler)
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
		Addr:         s.config.GetServerAddress(),
		Handler:      s.router,
		ReadTimeout:  s.config.Server.ReadTimeout,
		WriteTimeout: s.config.Server.WriteTimeout,
		IdleTimeout:  s.config.Server.IdleTimeout,
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
