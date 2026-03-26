package server

import (
	"context"
	"log/slog"
	"net/http"
	httppprof "net/http/pprof"
	"path"
	"strings"

	"gomodel/config"

	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"
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
	MasterKey                    string                                 // Optional: Master key for authentication
	MetricsEnabled               bool                                   // Whether to expose Prometheus metrics endpoint
	MetricsEndpoint              string                                 // HTTP path for metrics endpoint (default: /metrics)
	BodySizeLimit                string                                 // Max request body size (e.g., "10M", "1024K")
	PprofEnabled                 bool                                   // Whether to expose debug profiling routes at /debug/pprof/*
	AuditLogger                  auditlog.LoggerInterface               // Optional: Audit logger for request/response logging
	UsageLogger                  usage.LoggerInterface                  // Optional: Usage logger for token tracking
	PricingResolver              usage.PricingResolver                  // Optional: Resolves pricing for cost calculation
	ModelResolver                RequestModelResolver                   // Optional: explicit model resolver used during request planning
	ExecutionPolicyResolver      RequestExecutionPolicyResolver         // Optional: persisted execution-plan resolver used during request planning
	TranslatedRequestPatcher     TranslatedRequestPatcher               // Optional: request patcher for translated routes after planning
	BatchRequestPreparer         BatchRequestPreparer                   // Optional: batch request preparer before native provider submission
	ExposedModelLister           ExposedModelLister                     // Optional: additional public models to merge into GET /v1/models
	PassthroughSemanticEnrichers []core.PassthroughSemanticEnricher     // Optional: provider-owned passthrough semantic enrichers before planning
	BatchStore                   batchstore.Store                       // Optional: Batch lifecycle persistence store
	LogOnlyModelInteractions     bool                                   // Only log AI model endpoints (default: true)
	DisablePassthroughRoutes     bool                                   // Disable /p/{provider}/{endpoint} route registration
	EnabledPassthroughProviders  []string                               // Provider types enabled on /p/{provider}/... passthrough routes
	AllowPassthroughV1Alias      *bool                                  // Allow /p/{provider}/v1/... aliases; nil defaults to true
	AdminEndpointsEnabled        bool                                   // Whether admin API endpoints are enabled
	AdminUIEnabled               bool                                   // Whether admin dashboard UI is enabled
	AdminHandler                 *admin.Handler                         // Admin API handler (nil if disabled)
	DashboardHandler             *dashboard.Handler                     // Dashboard UI handler (nil if disabled)
	SwaggerEnabled               bool                                   // Whether to expose the Swagger UI at /swagger/index.html
	ResponseCacheMiddleware      *responsecache.ResponseCacheMiddleware // Optional: response cache middleware for cacheable endpoints
	GuardrailsHash               string                                 // Optional: SHA-256 hash of active guardrail rules; stored in context post-patch for semantic cache
}

// New creates a new HTTP server
func New(provider core.RoutableProvider, cfg *Config) *Server {
	e := echo.New()
	e.Logger = slog.Default()

	// Get loggers from config (may be nil)
	var auditLogger auditlog.LoggerInterface
	var usageLogger usage.LoggerInterface
	var pricingResolver usage.PricingResolver
	if cfg != nil {
		auditLogger = cfg.AuditLogger
		usageLogger = cfg.UsageLogger
		pricingResolver = cfg.PricingResolver
	}

	var modelResolver RequestModelResolver
	var executionPolicyResolver RequestExecutionPolicyResolver
	var translatedRequestPatcher TranslatedRequestPatcher
	if cfg != nil {
		modelResolver = cfg.ModelResolver
		executionPolicyResolver = cfg.ExecutionPolicyResolver
		translatedRequestPatcher = cfg.TranslatedRequestPatcher
	}

	handler := newHandler(provider, auditLogger, usageLogger, pricingResolver, modelResolver, executionPolicyResolver, translatedRequestPatcher)
	if cfg != nil {
		handler.batchRequestPreparer = cfg.BatchRequestPreparer
		handler.exposedModelLister = cfg.ExposedModelLister
		handler.responseCache = cfg.ResponseCacheMiddleware
		handler.guardrailsHash = cfg.GuardrailsHash
	}
	if cfg != nil && cfg.EnabledPassthroughProviders != nil {
		handler.setEnabledPassthroughProviders(cfg.EnabledPassthroughProviders)
	}
	if cfg != nil && !passthroughV1PrefixNormalizationEnabled(cfg) {
		handler.normalizePassthroughV1Prefix = false
	}
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
		if metricsPath == "/v1" || strings.HasPrefix(metricsPath, "/v1/") ||
			metricsPath == "/p" || strings.HasPrefix(metricsPath, "/p/") {
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
	if cfg != nil && cfg.PprofEnabled {
		authSkipPaths = append(authSkipPaths, "/debug/pprof", "/debug/pprof/*")
	}

	// Global middleware stack (order matters)
	// Request logger with optional filtering for model-only interactions
	if cfg != nil && cfg.LogOnlyModelInteractions {
		e.Use(middleware.RequestLoggerWithConfig(middleware.RequestLoggerConfig{
			Skipper: func(c *echo.Context) bool {
				return !core.IsModelInteractionPath(c.Request().URL.Path)
			},
			LogStatus:        true,
			LogURI:           true,
			LogMethod:        true,
			LogLatency:       true,
			LogProtocol:      true,
			LogRemoteIP:      true,
			LogHost:          true,
			LogURIPath:       true,
			LogUserAgent:     true,
			LogRequestID:     true,
			LogContentLength: true,
			LogResponseSize:  true,
			LogValuesFunc: func(c *echo.Context, v middleware.RequestLoggerValues) error {
				slog.Info("REQUEST",
					"method", v.Method,
					"uri", v.URI,
					"status", v.Status,
					"latency", v.Latency.String(),
					"host", v.Host,
					"bytes_in", v.ContentLength,
					"bytes_out", v.ResponseSize,
					"user_agent", v.UserAgent,
					"remote_ip", v.RemoteIP,
					"request_id", v.RequestID,
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
	e.Use(middleware.BodyLimit(parseBodySizeLimitBytes(bodySizeLimit)))

	// Request ID middleware (always active — ensures every request has a unique ID
	// for usage tracking, audit logging, and response correlation)
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			req, id := ensureRequestID(c.Request())
			c.SetRequest(req)
			c.Response().Header().Set("X-Request-ID", id)
			return next(c)
		}
	})

	// Ingress capture (before auth/audit/model validation so they can consume shared raw request state)
	e.Use(RequestSnapshotCapture())

	if cfg != nil && len(cfg.PassthroughSemanticEnrichers) > 0 {
		e.Use(PassthroughSemanticEnrichment(cfg.PassthroughSemanticEnrichers, passthroughV1PrefixNormalizationEnabled(cfg)))
	}

	// Audit logging runs before request planning so early planning/validation
	// failures are still logged. The middleware defers request capture and
	// dynamically gates response capture on the final resolved execution plan, so
	// Audit=false still suppresses per-request capture work.
	if cfg != nil && cfg.AuditLogger != nil && cfg.AuditLogger.Config().Enabled {
		e.Use(auditlog.Middleware(cfg.AuditLogger))
	}

	// Request planning resolves the request-scoped execution plan before auth and
	// handler execution. This keeps rejected model requests loggable and lets
	// downstream stages consume a shared policy decision.
	e.Use(ExecutionPlanningWithResolverAndPolicy(provider, modelResolver, executionPolicyResolver))

	// Authentication (skips public paths)
	if cfg != nil && cfg.MasterKey != "" {
		e.Use(AuthMiddleware(cfg.MasterKey, authSkipPaths))
	}

	// Public routes
	e.GET("/health", handler.Health)
	if cfg != nil && cfg.SwaggerEnabled {
		e.GET("/swagger/*", echoswagger.WrapHandler)
	}
	if cfg != nil && cfg.MetricsEnabled {
		e.GET(metricsPath, echo.WrapHandler(promhttp.Handler()))
	}
	if cfg != nil && cfg.PprofEnabled {
		e.GET("/debug/pprof", echo.WrapHandler(http.HandlerFunc(httppprof.Index)))
		e.GET("/debug/pprof/", echo.WrapHandler(http.HandlerFunc(httppprof.Index)))
		e.GET("/debug/pprof/cmdline", echo.WrapHandler(http.HandlerFunc(httppprof.Cmdline)))
		e.GET("/debug/pprof/profile", echo.WrapHandler(http.HandlerFunc(httppprof.Profile)))
		e.GET("/debug/pprof/symbol", echo.WrapHandler(http.HandlerFunc(httppprof.Symbol)))
		e.GET("/debug/pprof/trace", echo.WrapHandler(http.HandlerFunc(httppprof.Trace)))
		e.GET("/debug/pprof/:profile", func(c *echo.Context) error {
			httppprof.Handler(c.Param("profile")).ServeHTTP(c.Response(), c.Request())
			return nil
		})
	}

	// API routes
	if cfg == nil || !cfg.DisablePassthroughRoutes {
		e.GET("/p/:provider/*", handler.ProviderPassthrough)
		e.POST("/p/:provider/*", handler.ProviderPassthrough)
		e.PUT("/p/:provider/*", handler.ProviderPassthrough)
		e.PATCH("/p/:provider/*", handler.ProviderPassthrough)
		e.DELETE("/p/:provider/*", handler.ProviderPassthrough)
		e.HEAD("/p/:provider/*", handler.ProviderPassthrough)
		e.OPTIONS("/p/:provider/*", handler.ProviderPassthrough)
	}
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
		adminAPI.GET("/aliases", cfg.AdminHandler.ListAliases)
		adminAPI.PUT("/aliases/:name", cfg.AdminHandler.UpsertAlias)
		adminAPI.DELETE("/aliases/:name", cfg.AdminHandler.DeleteAlias)
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

func passthroughV1PrefixNormalizationEnabled(cfg *Config) bool {
	if cfg == nil || cfg.AllowPassthroughV1Alias == nil {
		return true
	}
	return *cfg.AllowPassthroughV1Alias
}

// Start starts the HTTP server on the given address and exits when ctx is canceled.
func (s *Server) Start(ctx context.Context, addr string) error {
	sc := echo.StartConfig{
		Address:    addr,
		HideBanner: true,
	}
	return sc.Start(ctx, s.echo)
}

// Shutdown releases server resources. The HTTP server itself is stopped by
// cancelling the context passed to Start; this method drains any in-flight
// response cache writes and closes the cache store.
func (s *Server) Shutdown(_ context.Context) error {
	if s.responseCacheMiddleware != nil {
		return s.responseCacheMiddleware.Close()
	}
	return nil
}

// ServeHTTP implements the http.Handler interface, allowing Server to be used with httptest
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.echo.ServeHTTP(w, r)
}

func parseBodySizeLimitBytes(limit string) int64 {
	limit = strings.TrimSpace(limit)
	if limit == "" {
		return config.DefaultBodySizeLimit
	}

	value, err := config.ParseBodySizeLimitBytes(limit)
	if err != nil {
		slog.Warn("invalid body size limit, falling back to default", "configured", limit)
		return config.DefaultBodySizeLimit
	}

	return value
}
