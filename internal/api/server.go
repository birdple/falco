package api

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sirupsen/logrus"

	"github.com/birdple/falco/internal/api/handlers"
	"github.com/birdple/falco/internal/api/handlers/ui"
	apimw "github.com/birdple/falco/internal/api/middleware"
	"github.com/birdple/falco/internal/api/views"
	"github.com/birdple/falco/internal/config"
	"github.com/birdple/falco/internal/pkg/metrics"
	"github.com/birdple/falco/internal/processor"
	"github.com/birdple/falco/internal/storage"
)

// ServerConfig holds configuration for the API server
type ServerConfig struct {
	Config         *config.Config
	Logger         *logrus.Logger
	Storage        storage.StorageBackend
	ImageProcessor processor.ImageProcessor
}

// Server represents the HTTP server
type Server struct {
	config    *config.Config
	logger    *logrus.Logger
	router    *chi.Mux
	server    *http.Server
	handler   *handlers.Handler
	uiHandler *ui.Handler
	metrics   *metrics.Metrics
}

// NewServer creates a new API server
func NewServer(cfg *ServerConfig) *Server {
	// Create handler with dependencies
	h := handlers.NewHandler(
		cfg.Config,
		cfg.Logger,
		cfg.Storage,
		cfg.ImageProcessor,
		time.Now(),
	)

	// Create metrics instance
	m := metrics.New()

	// Initialize UI renderer
	renderer, err := views.NewRenderer()
	if err != nil {
		cfg.Logger.WithError(err).Error("Failed to initialize UI renderer")
	}

	s := &Server{
		config:    cfg.Config,
		logger:    cfg.Logger,
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
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(30 * time.Second)) // Reduced from 60s to prevent slowloris attacks

	// Request size limiting
	maxRequestSize := s.config.GetMaxFileSizeBytes() * 2 // Allow some overhead
	sizeLimiter := apimw.NewRequestSizeLimiter(maxRequestSize, s.logger)
	r.Use(sizeLimiter.Handler)

	// Compression middleware
	r.Use(middleware.Compress(5))

	// Metrics middleware (when metrics are enabled)
	if s.config.Development.EnableMetrics {
		metricsMiddleware := apimw.NewMetricsMiddleware(s.metrics)
		r.Use(metricsMiddleware.Handler)
	}

	// Rate limiting
	if s.config.Security.RateLimit.RequestsPerMinute > 0 {
		rateLimiter := apimw.NewRateLimiter(
			s.config.Security.RateLimit.RequestsPerMinute,
			s.config.Security.RateLimit.Burst,
			s.logger,
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

	// Static files
	staticDir := http.Dir("web/static")
	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(staticDir)))

	// Health check endpoint (no auth required)
	r.Get("/health", s.handler.HandleHealth)

	// Docs endpoint
	r.Get("/docs", s.handler.HandleDocs)
	r.Get("/docs/openapi.yaml", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "./docs/openapi.yaml")
	})

	// Prometheus metrics endpoint (enabled via development.enable_metrics)
	if s.config.Development.EnableMetrics {
		r.Handle("/metrics", promhttp.Handler())
	}

	// API routes
	r.Route("/api/v1", func(r chi.Router) {
		// Public endpoint - no auth required (image delivery)
		r.Get("/images/*", s.handler.HandleDelivery)

		// Protected endpoints - require API key when enabled
		r.Group(func(r chi.Router) {
			// API key authentication for protected endpoints
			if s.config.Security.APIKeyRequired {
				apiKeyAuth := apimw.NewAPIKeyAuth(s.config.Security.APIKey, s.logger)
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
	s.logger.WithField("address", s.config.GetServerAddress()).Info("Starting HTTP server")
	return s.server.ListenAndServe()
}

// Shutdown gracefully shuts down the server with enhanced resource management
func (s *Server) Shutdown(ctx context.Context) error {
	s.logger.Info("Initiating HTTP server shutdown")

	// Set a reasonable timeout if none provided
	shutdownCtx := ctx
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 30*time.Second {
		var cancel context.CancelFunc
		shutdownCtx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}

	// Attempt graceful shutdown
	err := s.server.Shutdown(shutdownCtx)
	if err != nil {
		s.logger.WithError(err).Warn("HTTP server shutdown completed with errors")
		return err
	}

	s.logger.Info("HTTP server shutdown completed successfully")
	return nil
}

// Router returns the Chi router (useful for testing)
func (s *Server) Router() *chi.Mux {
	return s.router
}
