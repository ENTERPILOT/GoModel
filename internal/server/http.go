package server

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	httppprof "net/http/pprof"
	"path"
	"strings"
	"time"

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

const (
	inboundServerReadTimeout       = 30 * time.Second
	inboundServerReadHeaderTimeout = 10 * time.Second
	inboundServerWriteTimeout      = 30 * time.Second
)

// Config holds server configuration options
type Config struct {
	MasterKey                       string                                 // Optional: Master key for authentication
	Authenticator                   BearerTokenAuthenticator               // Optional: managed API key authenticator
	MetricsEnabled                  bool                                   // Whether to expose Prometheus metrics endpoint
	MetricsEndpoint                 string                                 // HTTP path for metrics endpoint (default: /metrics)
	BodySizeLimit                   string                                 // Max request body size (e.g., "10M", "1024K")
	PprofEnabled                    bool                                   // Whether to expose debug profiling routes at /debug/pprof/*
	AuditLogger                     auditlog.LoggerInterface               // Optional: Audit logger for request/response logging
	UsageLogger                     usage.LoggerInterface                  // Optional: Usage logger for token tracking
	PricingResolver                 usage.PricingResolver                  // Optional: Resolves pricing for cost calculation
	ModelResolver                   RequestModelResolver                   // Optional: explicit model resolver used during workflow resolution
	ModelAuthorizer                 RequestModelAuthorizer                 // Optional: request-scoped concrete model access controller
	WorkflowPolicyResolver          RequestWorkflowPolicyResolver          // Optional: persisted workflow resolver used during workflow resolution
	FallbackResolver                RequestFallbackResolver                // Optional: translated-route fallback resolver
	TranslatedRequestPatcher        TranslatedRequestPatcher               // Optional: request patcher for translated routes after workflow resolution
	BatchRequestPreparer            BatchRequestPreparer                   // Optional: batch request preparer before native provider submission
	ExposedModelLister              ExposedModelLister                     // Optional: additional public models to merge into GET /v1/models
	KeepOnlyAliasesAtModelsEndpoint bool                                   // Whether GET /v1/models should hide concrete provider models
	PassthroughSemanticEnrichers    []core.PassthroughSemanticEnricher     // Optional: provider-owned passthrough semantic enrichers before workflow resolution
	BatchStore                      batchstore.Store                       // Optional: Batch lifecycle persistence store
	LogOnlyModelInteractions        bool                                   // Only log AI model endpoints (default: true)
	DisablePassthroughRoutes        bool                                   // Disable /p/{provider}/{endpoint} route registration
	EnabledPassthroughProviders     []string                               // Provider types enabled on /p/{provider}/... passthrough routes
	AllowPassthroughV1Alias         *bool                                  // Allow /p/{provider}/v1/... aliases; nil defaults to true
	AdminEndpointsEnabled           bool                                   // Whether admin API endpoints are enabled
	AdminUIEnabled                  bool                                   // Whether admin dashboard UI is enabled
	AdminHandler                    *admin.Handler                         // Admin API handler (nil if disabled)
	DashboardHandler                *dashboard.Handler                     // Dashboard UI handler (nil if disabled)
	SwaggerEnabled                  bool                                   // Whether to expose the Swagger UI at /swagger/index.html
	ResponseCacheMiddleware         *responsecache.ResponseCacheMiddleware // Optional: response cache middleware for cacheable endpoints
	GuardrailsHash                  string                                 // Optional: SHA-256 hash of active guardrail rules; stored in context post-patch for semantic cache
	IPExtractor                     echo.IPExtractor                       // Optional: trusted client IP extraction strategy for proxied deployments
}

// New creates a new HTTP server
func New(provider core.RoutableProvider, cfg *Config) *Server {
	e := echo.New()
	e.Logger = slog.Default()
	// Keep client IP handling explicit after Echo v5.1.0 changed RealIP defaults.
	// Direct extraction is the safe baseline unless a caller opts into trusted
	// proxy header handling via Config.IPExtractor.
	e.IPExtractor = echo.ExtractIPDirect()
	if cfg != nil && cfg.IPExtractor != nil {
		e.IPExtractor = cfg.IPExtractor
	}

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
	var modelAuthorizer RequestModelAuthorizer
	var workflowPolicyResolver RequestWorkflowPolicyResolver
	var fallbackResolver RequestFallbackResolver
	var translatedRequestPatcher TranslatedRequestPatcher
	if cfg != nil {
		modelResolver = cfg.ModelResolver
		modelAuthorizer = cfg.ModelAuthorizer
		workflowPolicyResolver = cfg.WorkflowPolicyResolver
		fallbackResolver = cfg.FallbackResolver
		translatedRequestPatcher = cfg.TranslatedRequestPatcher
	}

	handler := newHandlerWithAuthorizer(provider, auditLogger, usageLogger, pricingResolver, modelResolver, modelAuthorizer, workflowPolicyResolver, fallbackResolver, translatedRequestPatcher)
	if cfg != nil {
		handler.batchRequestPreparer = cfg.BatchRequestPreparer
		handler.exposedModelLister = cfg.ExposedModelLister
		handler.keepOnlyAliasesAtModelsEndpoint = cfg.KeepOnlyAliasesAtModelsEndpoint
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
	// When no bootstrap master key is configured, keep admin APIs reachable so
	// the dashboard can recover managed-key access instead of locking itself out.
	if cfg != nil && cfg.MasterKey == "" && cfg.AdminEndpointsEnabled && cfg.AdminHandler != nil {
		authSkipPaths = append(authSkipPaths, "/admin/api/v1/*")
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
	e.Use(modelInteractionWriteDeadlineMiddleware())

	// Ingress capture (before auth/audit/model validation so they can consume shared raw request state)
	e.Use(RequestSnapshotCapture())

	if cfg != nil && len(cfg.PassthroughSemanticEnrichers) > 0 {
		e.Use(PassthroughSemanticEnrichment(provider, cfg.PassthroughSemanticEnrichers, passthroughV1PrefixNormalizationEnabled(cfg)))
	}

	// Audit logging runs before workflow resolution so early workflow resolution/validation
	// failures are still logged. The middleware defers request capture and
	// dynamically gates response capture on the final resolved workflow, so
	// Audit=false still suppresses per-request capture work.
	if cfg != nil && cfg.AuditLogger != nil && cfg.AuditLogger.Config().Enabled {
		e.Use(auditlog.Middleware(cfg.AuditLogger))
	}

	// Authentication (skips public paths)
	if cfg != nil && (cfg.MasterKey != "" || cfg.Authenticator != nil) {
		e.Use(AuthMiddlewareWithAuthenticator(cfg.MasterKey, cfg.Authenticator, authSkipPaths))
	}

	// Workflow resolution resolves the request-scoped workflow after auth so
	// managed auth key user-path overrides are visible to policy resolution while
	// still keeping workflow resolution failures loggable through the audit middleware.
	e.Use(WorkflowResolutionWithResolverAndPolicy(provider, modelResolver, workflowPolicyResolver))

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
		adminAPI.GET("/dashboard/config", cfg.AdminHandler.DashboardConfig)
		adminAPI.GET("/cache/overview", cfg.AdminHandler.CacheOverview)
		adminAPI.GET("/usage/summary", cfg.AdminHandler.UsageSummary)
		adminAPI.GET("/usage/daily", cfg.AdminHandler.DailyUsage)
		adminAPI.GET("/usage/models", cfg.AdminHandler.UsageByModel)
		adminAPI.GET("/usage/user-paths", cfg.AdminHandler.UsageByUserPath)
		adminAPI.GET("/usage/log", cfg.AdminHandler.UsageLog)
		adminAPI.GET("/audit/log", cfg.AdminHandler.AuditLog)
		adminAPI.GET("/audit/conversation", cfg.AdminHandler.AuditConversation)
		adminAPI.GET("/providers/status", cfg.AdminHandler.ProviderStatus)
		adminAPI.POST("/runtime/refresh", cfg.AdminHandler.RefreshRuntime)
		adminAPI.GET("/models", cfg.AdminHandler.ListModels)
		adminAPI.GET("/models/categories", cfg.AdminHandler.ListCategories)
		adminAPI.GET("/model-overrides", cfg.AdminHandler.ListModelOverrides)
		adminAPI.PUT("/model-overrides/:selector", cfg.AdminHandler.UpsertModelOverride)
		adminAPI.DELETE("/model-overrides/:selector", cfg.AdminHandler.DeleteModelOverride)
		adminAPI.GET("/auth-keys", cfg.AdminHandler.ListAuthKeys)
		adminAPI.POST("/auth-keys", cfg.AdminHandler.CreateAuthKey)
		adminAPI.POST("/auth-keys/:id/deactivate", cfg.AdminHandler.DeactivateAuthKey)
		adminAPI.GET("/aliases", cfg.AdminHandler.ListAliases)
		adminAPI.PUT("/aliases/:name", cfg.AdminHandler.UpsertAlias)
		adminAPI.DELETE("/aliases/:name", cfg.AdminHandler.DeleteAlias)
		adminAPI.GET("/guardrails/types", cfg.AdminHandler.ListGuardrailTypes)
		adminAPI.GET("/guardrails", cfg.AdminHandler.ListGuardrails)
		adminAPI.PUT("/guardrails/:name", cfg.AdminHandler.UpsertGuardrail)
		adminAPI.DELETE("/guardrails/:name", cfg.AdminHandler.DeleteGuardrail)
		adminAPI.GET("/workflows", cfg.AdminHandler.ListWorkflows)
		adminAPI.GET("/workflows/guardrails", cfg.AdminHandler.ListWorkflowGuardrails)
		adminAPI.GET("/workflows/:id", cfg.AdminHandler.GetWorkflow)
		adminAPI.POST("/workflows", cfg.AdminHandler.CreateWorkflow)
		adminAPI.POST("/workflows/:id/deactivate", cfg.AdminHandler.DeactivateWorkflow)
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
	return newGatewayStartConfig(addr).Start(ctx, s.echo)
}

// StartWithListener starts the HTTP server using a pre-bound listener.
// This is useful in tests that need an already-reserved loopback port.
func (s *Server) StartWithListener(ctx context.Context, listener net.Listener) error {
	sc := echo.StartConfig{
		HideBanner: true,
		Listener:   listener,
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

func newGatewayStartConfig(addr string) echo.StartConfig {
	return echo.StartConfig{
		Address:    addr,
		HideBanner: true,
		BeforeServeFunc: func(server *http.Server) error {
			return configureGatewayHTTPServer(server)
		},
	}
}

func configureGatewayHTTPServer(server *http.Server) error {
	if server == nil {
		return nil
	}

	// Keep an explicit server-wide write timeout for ordinary routes. Long-lived
	// model interaction routes clear it per request before provider work begins.
	server.ReadTimeout = inboundServerReadTimeout
	server.ReadHeaderTimeout = inboundServerReadHeaderTimeout
	server.WriteTimeout = inboundServerWriteTimeout
	return nil
}

func modelInteractionWriteDeadlineMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if !core.IsModelInteractionPath(c.Request().URL.Path) {
				return next(c)
			}
			if err := http.NewResponseController(c.Response()).SetWriteDeadline(time.Time{}); err != nil && !errors.Is(err, http.ErrNotSupported) {
				slog.Warn("failed to clear write deadline for model interaction",
					"path", c.Request().URL.Path,
					"request_id", requestIDFromContextOrHeader(c.Request()),
					"error", err,
				)
			}
			return next(c)
		}
	}
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
