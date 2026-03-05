package server

import (
	"context"
	"log/slog"
	"net/http"
	"path"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"gomodel/internal/admin"
	"gomodel/internal/admin/dashboard"
	"gomodel/internal/auditlog"
	batchstore "gomodel/internal/batch"
	"gomodel/internal/core"
	"gomodel/internal/responsecache"
	"gomodel/internal/usage"

	echoswagger "github.com/swaggo/echo-swagger"
)

// Server wraps the Echo server
type Server struct {
	echo                    *echo.Echo
	handler                 *Handler
	responseCacheMiddleware *responsecache.ResponseCacheMiddleware
}

// Config holds server configuration options
type Config struct {
	MasterKey                string                   // Optional: Master key for authentication
	MetricsEnabled           bool                     // Whether to expose Prometheus metrics endpoint
	MetricsEndpoint          string                   // HTTP path for metrics endpoint (default: /metrics)
	BodySizeLimit            string                   // Max request body size (e.g., "10M", "1024K")
	AuditLogger              auditlog.LoggerInterface // Optional: Audit logger for request/response logging
	UsageLogger              usage.LoggerInterface    // Optional: Usage logger for token tracking
	PricingResolver          usage.PricingResolver    // Optional: Resolves pricing for cost calculation
	BatchStore               batchstore.Store         // Optional: Batch lifecycle persistence store
	LogOnlyModelInteractions bool                     // Only log AI model endpoints (default: true)
	AdminEndpointsEnabled    bool                     // Whether admin API endpoints are enabled
	AdminUIEnabled           bool                     // Whether admin dashboard UI is enabled
	AdminHandler             *admin.Handler           // Admin API handler (nil if disabled)
	DashboardHandler         *dashboard.Handler       // Dashboard UI handler (nil if disabled)
	SwaggerEnabled           bool                     // Whether to expose the Swagger UI at /swagger/index.html
	ResponseCacheMiddleware  *responsecache.ResponseCacheMiddleware
}

// New creates a new HTTP server
func New(provider core.RoutableProvider, cfg *Config) *Server {
	e := echo.New()
	e.HideBanner = true

	// Get loggers from config (may be nil)
	var auditLogger auditlog.LoggerInterface
	var usageLogger usage.LoggerInterface
	var pricingResolver usage.PricingResolver
	if cfg != nil {
		auditLogger = cfg.AuditLogger
		usageLogger = cfg.UsageLogger
		pricingResolver = cfg.PricingResolver
	}

	handler := NewHandler(provider, auditLogger, usageLogger, pricingResolver)
	if cfg != nil && cfg.BatchStore != nil {
		handler.SetBatchStore(cfg.BatchStore)
	}

	// Build list of paths that skip authentication
	authSkipPaths := []string{"/health"}

	// Determine metrics path
	metricsPath := "/metrics"
	if cfg != nil && cfg.MetricsEnabled {
		if cfg.MetricsEndpoint != "" {
			// Normalize path to prevent traversal attacks
			metricsPath = path.Clean(cfg.MetricsEndpoint)
		}
		// Prevent metrics endpoint from shadowing API routes (security: auth bypass)
		if metricsPath == "/v1" || strings.HasPrefix(metricsPath, "/v1/") {
			slog.Warn("metrics endpoint conflicts with API routes, using /metrics instead",
				"configured", cfg.MetricsEndpoint,
				"normalized", metricsPath)
			metricsPath = "/metrics"
		}
		authSkipPaths = append(authSkipPaths, metricsPath)
	}

	// Admin dashboard pages and static assets skip auth (/* enables prefix matching)
	if cfg != nil && cfg.AdminUIEnabled && cfg.DashboardHandler != nil {
		authSkipPaths = append(authSkipPaths, "/admin/dashboard", "/admin/dashboard/*", "/admin/static/*")
	}
	if cfg != nil && cfg.SwaggerEnabled {
		authSkipPaths = append(authSkipPaths, "/swagger/*")
	}

	// Global middleware stack (order matters)
	// Request logger with optional filtering for model-only interactions
	if cfg != nil && cfg.LogOnlyModelInteractions {
		e.Use(middleware.RequestLoggerWithConfig(middleware.RequestLoggerConfig{
			Skipper: func(c echo.Context) bool {
				return !auditlog.IsModelInteractionPath(c.Request().URL.Path)
			},
			LogStatus:   true,
			LogURI:      true,
			LogError:    true,
			LogMethod:   true,
			LogLatency:  true,
			LogProtocol: true,
			LogRemoteIP: true,
			LogHost:     true,
			LogURIPath:  true,
			LogValuesFunc: func(c echo.Context, v middleware.RequestLoggerValues) error {
				slog.Info("REQUEST",
					"method", v.Method,
					"uri", v.URI,
					"status", v.Status,
					"latency", v.Latency.String(),
					"host", v.Host,
					"bytes_in", c.Request().ContentLength,
					"bytes_out", c.Response().Size,
					"user_agent", c.Request().UserAgent(),
					"remote_ip", v.RemoteIP,
					"request_id", c.Request().Header.Get("X-Request-ID"),
				)
				return nil
			},
		}))
	} else {
		e.Use(middleware.RequestLogger())
	}
	e.Use(middleware.Recover())

	// Body size limit (default: 10MB)
	bodySizeLimit := "10M"
	if cfg != nil && cfg.BodySizeLimit != "" {
		bodySizeLimit = cfg.BodySizeLimit
	}
	e.Use(middleware.BodyLimit(bodySizeLimit))

	// Request ID middleware (always active — ensures every request has a unique ID
	// for usage tracking, audit logging, and response correlation)
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			id := c.Request().Header.Get("X-Request-ID")
			if id == "" {
				id = uuid.NewString()
				c.Request().Header.Set("X-Request-ID", id)
			}
			c.Response().Header().Set("X-Request-ID", id)
			return next(c)
		}
	})

	// Audit logging middleware (before authentication to capture all requests)
	if cfg != nil && cfg.AuditLogger != nil && cfg.AuditLogger.Config().Enabled {
		e.Use(auditlog.Middleware(cfg.AuditLogger))
	}

	// Authentication (skips public paths)
	if cfg != nil && cfg.MasterKey != "" {
		e.Use(AuthMiddleware(cfg.MasterKey, authSkipPaths))
	}

	// Model validation (skips non-model paths via IsModelInteractionPath)
	e.Use(ModelValidation(provider))

	if cfg != nil && cfg.ResponseCacheMiddleware != nil {
		e.Use(cfg.ResponseCacheMiddleware.Middleware())
	}

	// Public routes
	e.GET("/health", handler.Health)
	if cfg != nil && cfg.SwaggerEnabled {
		e.GET("/swagger/*", echoswagger.WrapHandler)
	}
	if cfg != nil && cfg.MetricsEnabled {
		e.GET(metricsPath, echo.WrapHandler(promhttp.Handler()))
	}

	// API routes
	e.GET("/v1/models", handler.ListModels)
	e.POST("/v1/chat/completions", handler.ChatCompletion)
	e.POST("/v1/responses", handler.Responses)
	e.POST("/v1/embeddings", handler.Embeddings)
	e.POST("/v1/files", handler.CreateFile)
	e.GET("/v1/files", handler.ListFiles)
	e.GET("/v1/files/:id", handler.GetFile)
	e.DELETE("/v1/files/:id", handler.DeleteFile)
	e.GET("/v1/files/:id/content", handler.GetFileContent)
	e.POST("/v1/batches", handler.Batches)
	e.GET("/v1/batches", handler.ListBatches)
	e.GET("/v1/batches/:id", handler.GetBatch)
	e.POST("/v1/batches/:id/cancel", handler.CancelBatch)
	e.GET("/v1/batches/:id/results", handler.BatchResults)

	// Admin API routes (behind ADMIN_ENDPOINTS_ENABLED flag)
	if cfg != nil && cfg.AdminEndpointsEnabled && cfg.AdminHandler != nil {
		adminAPI := e.Group("/admin/api/v1")
		adminAPI.GET("/usage/summary", cfg.AdminHandler.UsageSummary)
		adminAPI.GET("/usage/daily", cfg.AdminHandler.DailyUsage)
		adminAPI.GET("/usage/models", cfg.AdminHandler.UsageByModel)
		adminAPI.GET("/usage/log", cfg.AdminHandler.UsageLog)
		adminAPI.GET("/audit/log", cfg.AdminHandler.AuditLog)
		adminAPI.GET("/audit/conversation", cfg.AdminHandler.AuditConversation)
		adminAPI.GET("/models", cfg.AdminHandler.ListModels)
		adminAPI.GET("/models/categories", cfg.AdminHandler.ListCategories)
	}

	// Admin dashboard UI routes (behind ADMIN_UI_ENABLED flag)
	if cfg != nil && cfg.AdminUIEnabled && cfg.DashboardHandler != nil {
		e.GET("/admin/dashboard", cfg.DashboardHandler.Index)
		e.GET("/admin/dashboard/*", cfg.DashboardHandler.Index)
		e.GET("/admin/static/*", cfg.DashboardHandler.Static)
	}

	var rcm *responsecache.ResponseCacheMiddleware
	if cfg != nil {
		rcm = cfg.ResponseCacheMiddleware
	}
	return &Server{
		echo:                    e,
		handler:                 handler,
		responseCacheMiddleware: rcm,
	}
}

// Start starts the HTTP server on the given address
func (s *Server) Start(addr string) error {
	return s.echo.Start(addr)
}

// Shutdown gracefully shuts down the HTTP server and releases resources.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.responseCacheMiddleware != nil {
		_ = s.responseCacheMiddleware.Close()
	}
	return s.echo.Shutdown(ctx)
}

// ServeHTTP implements the http.Handler interface, allowing Server to be used with httptest
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.echo.ServeHTTP(w, r)
}
