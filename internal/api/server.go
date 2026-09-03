package api

import (
	"context"
	"io/fs"
	"net/http"
	"net/http/pprof"
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
	"github.com/birdple/falco/internal/telemetry"
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
// setupRouter builds the chi router: middleware stack first, then routes.
// Router tuning knobs.
const (
	// requestTimeout caps how long any single request may run. Generous
	// because a cold cache miss on a large original means a Jay fetch plus a
	// libvips decode and encode.
	requestTimeout = 30 * time.Second

	// compressionLevel is gzip's default: past it the CPU cost climbs faster
	// than the bytes saved, and this only ever applies to text responses.
	compressionLevel = 5

	// corsMaxAgeSeconds is how long a browser may cache a preflight result.
	corsMaxAgeSeconds = 300
)

func (s *Server) setupRouter() {
	r := chi.NewRouter()
	s.useMiddleware(r)
	s.mountUIRoutes(r)
	s.mountOperationalRoutes(r)
	s.mountAPIRoutes(r)
	r.NotFound(s.handleNotFound)
	s.router = r
}

// useMiddleware installs the middleware stack. Order matters: RequestID and
// RealIP have to run before the logger so every line carries them, and the
// size limiter before anything that reads a body.
func (s *Server) useMiddleware(r chi.Router) {
	r.Use(apimw.SecurityHeaders)
	r.Use(middleware.RequestID)
	// apimw.RealIP replaces chi's middleware.RealIP, which trusts
	// X-Forwarded-For / X-Real-IP unconditionally. See realip.go.
	r.Use(apimw.RealIP)
	r.Use(apimw.ZerologRequestLogger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(requestTimeout))

	sizeLimiter := apimw.NewRequestSizeLimiter(s.config.GetMaxFileSizeBytes() * 2)
	r.Use(sizeLimiter.Handler)

	// Compression is restricted to text-like responses on purpose. Do NOT
	// compress image/* bodies: webp, jpeg, png and avif are already compressed,
	// so gzipping them on the fly burns CPU and usually grows the payload.
	// Anything outside this allowlist streams through untouched.
	r.Use(middleware.Compress(
		compressionLevel,
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

	if s.config.Development.EnableMetrics {
		r.Use(apimw.NewMetricsMiddleware(s.metrics).Handler)
	}

	if s.config.Security.RateLimit.RequestsPerMinute > 0 {
		rateLimiter := apimw.NewRateLimiter(
			s.config.Security.RateLimit.RequestsPerMinute,
			s.config.Security.RateLimit.Burst,
		)
		r.Use(rateLimiter.Handler)
	}

	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   s.config.Security.CORS.Origins,
		AllowedMethods:   s.config.Security.CORS.Methods,
		AllowedHeaders:   append(s.config.Security.CORS.Headers, "X-API-Key", "Authorization"),
		ExposedHeaders:   []string{"Link", "X-RateLimit-Limit", "X-RateLimit-Remaining"},
		AllowCredentials: false,
		MaxAge:           corsMaxAgeSeconds,
	}))
}

// mountUIRoutes mounts the admin panel and its static assets. These are public
// at the router level; the handlers authenticate internally via cookie or key.
func (s *Server) mountUIRoutes(r chi.Router) {
	r.Get("/", s.uiHandler.Login)
	r.Get("/dashboard", s.uiHandler.Dashboard)
	r.Post("/ui/auth", s.uiHandler.AuthPost)
	r.Post("/ui/logout", s.uiHandler.LogoutPost)
	r.Get("/ui/content", s.uiHandler.Content)

	staticFS, _ := fs.Sub(web.StaticFS, "static")
	r.Handle("/static/*", http.StripPrefix("/static/", apimw.RestrictedFileServer(http.FS(staticFS))))
}

// mountOperationalRoutes mounts health, docs, metrics and pprof.
func (s *Server) mountOperationalRoutes(r chi.Router) {
	// Health needs no auth: it is what the orchestrator polls.
	r.Get("/health", s.handler.HandleHealth)
	r.Head("/health", s.handler.HandleHealth)

	// Disallow all crawlers. Falco is a CDN origin for images, not indexable
	// content.
	r.Get("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		_, _ = w.Write([]byte("User-agent: *\nDisallow: /\n"))
	})

	r.Get("/docs", s.handler.HandleDocs)
	r.Get("/docs/openapi.yaml", func(w http.ResponseWriter, r *http.Request) {
		data, err := docs.FS.ReadFile("openapi.yaml")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write(data)
	})

	if s.config.Development.EnableMetrics {
		r.Group(func(r chi.Router) {
			s.useAPIKeyAuth(r)
			r.Handle("/metrics", promhttp.Handler())
		})
	}

	if s.config.Development.EnablePprof {
		s.mountPprof(r)
	}
}

// mountPprof mounts the runtime profiles, behind the same API key as /metrics:
// a heap or goroutine profile exposes code paths and internal process state.
//
// The routes are mounted by hand because this router is an explicit chi one and
// never falls through to the DefaultServeMux, so the blank import of
// net/http/pprof would register nothing reachable.
//
// The profile worth having here is goroutineleak (new in Go 1.27): it is what
// catches the fire-and-forget goroutines in ReplicatedStorage, which start on
// context.Background(), are awaited by no WaitGroup and are not considered by
// shutdown.
func (s *Server) mountPprof(r chi.Router) {
	r.Group(func(r chi.Router) {
		s.useAPIKeyAuth(r)
		r.Get("/debug/pprof/", pprof.Index)
		r.Get("/debug/pprof/cmdline", pprof.Cmdline)
		r.Get("/debug/pprof/profile", pprof.Profile)
		r.Get("/debug/pprof/symbol", pprof.Symbol)
		r.Post("/debug/pprof/symbol", pprof.Symbol)
		r.Get("/debug/pprof/trace", pprof.Trace)
		// pprof.Index already serves any profile registered by name —
		// goroutineleak included — once the route hangs off /debug/pprof/.
		r.Get("/debug/pprof/{profile}", pprof.Index)
	})
	logger.Warn().Msg("pprof endpoints enabled at /debug/pprof/ — do not enable in production without an API key")
}

// mountAPIRoutes mounts /api/v1.
//
// Delivery and proxy are deliberately outside the protected group: they gate
// themselves, by HMAC signature or by API key depending on deployment, because
// a browser cannot attach an API key to an <img> URL.
func (s *Server) mountAPIRoutes(r chi.Router) {
	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/images/*", s.handler.HandleDelivery)
		r.Get("/proxy/*", s.handler.HandleProxy)

		r.Group(func(r chi.Router) {
			s.useScopedAuth(r)
			r.Post("/upload", s.handler.HandleUpload)
			r.Post("/update", s.handler.HandleUpdate)
			r.Get("/list", s.handler.HandleList)
			r.Delete("/delete", s.handler.HandleDelete)
			r.Post("/sign", s.handler.HandleSignURL)
		})
	})
}

// useAPIKeyAuth guards a route group with the plain admin API key, when auth is
// enabled at all.
func (s *Server) useAPIKeyAuth(r chi.Router) {
	if s.config.Security.APIKeyRequired {
		r.Use(apimw.NewAPIKeyAuth(s.config.Security.APIKey).Handler)
	}
}

// useScopedAuth guards a route group with scoped keys when any are configured,
// falling back to the single admin key otherwise.
//
// Both branches exist because falco serves two deployment shapes: multi-tenant
// installs give each bucket or group its own key, while birdple-v2 runs with
// one API_KEY and no scopes at all.
func (s *Server) useScopedAuth(r chi.Router) {
	if !s.config.Security.APIKeyRequired {
		return
	}
	if scopedAuth := apimw.NewScopedAPIKeyAuth(s.config.Security.APIKey, s.config); scopedAuth.HasScopedKeys() {
		r.Use(scopedAuth.Handler)
		return
	}
	r.Use(apimw.NewAPIKeyAuth(s.config.Security.APIKey).Handler)
}

// handleNotFound handles requests for non-existent routes
func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	http.NotFound(w, r)
}

// setupServer configures the HTTP server
func (s *Server) setupServer() {
	// Wrap the chi router with OTel HTTP instrumentation so every request
	// gets a span with method/route/status. No-op when OTEL_EXPORTER_OTLP_ENDPOINT
	// is unset (the global tracer provider stays at the default no-op).
	s.server = &http.Server{
		Addr:              s.config.GetServerAddress(),
		Handler:           telemetry.WrapHandler(s.router, "falco"),
		ReadTimeout:       s.config.Server.ReadTimeout,
		WriteTimeout:      s.config.Server.WriteTimeout,
		IdleTimeout:       s.config.Server.IdleTimeout,
		ReadHeaderTimeout: 10 * time.Second,
		// Topes de cabecera: falco es un CDN de imágenes de cara pública detrás
		// from Cloudflare. MaxHeaderBytes caps the weight and
		// MaxHeaderValueCount (Go 1.27) the count; without the latter, thousands
		// of tiny headers stay under the byte cap and still cost parsing time.
		MaxHeaderBytes:      s.config.Server.MaxHeaderBytes,
		MaxHeaderValueCount: s.config.Server.MaxHeaderValueCount,
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
