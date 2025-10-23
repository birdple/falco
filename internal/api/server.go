package api

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/sirupsen/logrus"

	apimw "github.com/birdple/imagine/internal/api/middleware"
	"github.com/birdple/imagine/internal/config"
	"github.com/birdple/imagine/internal/processor"
	"github.com/birdple/imagine/internal/storage"
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
	config         *config.Config
	logger         *logrus.Logger
	router         *chi.Mux
	server         *http.Server
	storage        storage.StorageBackend
	imageProcessor processor.ImageProcessor
	startTime      time.Time
}

// NewServer creates a new API server
func NewServer(cfg *ServerConfig) *Server {
	s := &Server{
		config:         cfg.Config,
		logger:         cfg.Logger,
		storage:        cfg.Storage,
		imageProcessor: cfg.ImageProcessor,
		startTime:      time.Now(),
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
	r.Use(middleware.Timeout(60 * time.Second))

	// Request size limiting
	maxRequestSize := s.config.GetMaxFileSizeBytes() * 2 // Allow some overhead
	sizeLimiter := apimw.NewRequestSizeLimiter(maxRequestSize, s.logger)
	r.Use(sizeLimiter.Handler)

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

	// Health check endpoint (no auth required)
	r.Get("/health", s.handleHealth)

	// API routes
	r.Route("/api/v1", func(r chi.Router) {
		// Public endpoint - no auth required (image delivery)
		r.Get("/images/{id}", s.handleDelivery)

		// Protected endpoints - require API key when enabled
		r.Group(func(r chi.Router) {
			// API key authentication for protected endpoints
			if s.config.Security.APIKeyRequired {
				apiKeyAuth := apimw.NewAPIKeyAuth(s.config.Security.APIKey, s.logger)
				r.Use(apiKeyAuth.Handler)
			}

			r.Post("/upload", s.handleUpload)
			r.Post("/update", s.handleUpdate)
			r.Get("/list", s.handleList)
			r.Delete("/delete", s.handleDelete)
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
