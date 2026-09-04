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

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/ext"

	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/enterpilot/gomodel/internal/admin"
	"github.com/enterpilot/gomodel/internal/admin/dashboard"
	"github.com/enterpilot/gomodel/internal/auditlog"
	batchstore "github.com/enterpilot/gomodel/internal/batch"
	"github.com/enterpilot/gomodel/internal/conversationstore"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/filestore"
	"github.com/enterpilot/gomodel/internal/gateway"
	"github.com/enterpilot/gomodel/internal/mcpgateway"
	"github.com/enterpilot/gomodel/internal/responsecache"
	"github.com/enterpilot/gomodel/internal/responsestore"
	"github.com/enterpilot/gomodel/internal/session"
	"github.com/enterpilot/gomodel/internal/tagging"
	"github.com/enterpilot/gomodel/internal/usage"
	"github.com/enterpilot/gomodel/internal/versioncheck"
)

// Server wraps the Echo server
type Server struct {
	echo                    *echo.Echo
	handler                 *Handler
	responseCacheMiddleware *responsecache.ResponseCacheMiddleware
	responseStore           responsestore.Store
	conversationStore       conversationstore.Store
}

const (
	inboundServerReadTimeout       = 30 * time.Second
	inboundServerReadHeaderTimeout = 10 * time.Second
	inboundServerWriteTimeout      = 30 * time.Second

	// GracefulDrainTimeout bounds how long the HTTP server waits for in-flight
	// requests to finish once shutdown begins. Streamed model responses run far
	// longer than any drain window worth waiting for, so this is deliberately a
	// cutoff rather than a promise: past it the remaining connections are cut
	// and shutdown moves on to flushing usage and audit records. It is sized
	// for the short requests that can actually finish — a dashboard fetch, a
	// health check — not for model traffic.
	//
	// It must stay below run.shutdownTimeout, which covers this drain plus
	// those flushes and the database close that follow it. Exported so that
	// relationship is checked rather than merely asserted here — see
	// TestGracefulDrainFitsInsideTheShutdownBudget in the run package.
	GracefulDrainTimeout = 10 * time.Second
)

// Config holds server configuration options
type Config struct {
	BasePath                        string                                 // URL path prefix where the app is mounted (default: /)
	MasterKey                       string                                 // Optional: Master key for authentication
	Authenticator                   BearerTokenAuthenticator               // Optional: managed API key authenticator
	MetricsEnabled                  bool                                   // Whether to expose Prometheus metrics endpoint
	MetricsEndpoint                 string                                 // HTTP path for metrics endpoint (default: /metrics)
	BodySizeLimit                   string                                 // Max request body size (e.g., "10M", "1024K")
	PprofEnabled                    bool                                   // Whether to expose debug profiling routes at /debug/pprof/*
	AuditLogger                     auditlog.LoggerInterface               // Optional: Audit logger for request/response logging
	AuditReader                     auditlog.Reader                        // Optional: audit lookup used for dashboard interaction continuations
	UsageLogger                     usage.LoggerInterface                  // Optional: Usage logger for token tracking
	BudgetChecker                   BudgetChecker                          // Optional: per-user-path budget checker
	RateLimiter                     RateLimiter                            // Optional: per-user-path rate limiter
	UsageSummarizer                 UsageSummarizer                        // Optional: usage aggregates for the self-service GET /v1/usage endpoint
	PricingResolver                 usage.PricingResolver                  // Optional: Resolves pricing for cost calculation
	ModelResolver                   RequestModelResolver                   // Optional: explicit model resolver used during workflow resolution
	ModelAuthorizer                 RequestModelAuthorizer                 // Optional: request-scoped concrete model access controller
	WorkflowPolicyResolver          RequestWorkflowPolicyResolver          // Optional: persisted workflow resolver used during workflow resolution
	FailoverResolver                RequestFailoverResolver                // Optional: translated-route failover resolver
	FailoverPolicy                  *gateway.FailoverPolicy                // Optional: which errors trigger failover and how many targets to try; nil applies the defaults
	TranslatedRequestPatcher        TranslatedRequestPatcher               // Optional: request patcher for translated routes after workflow resolution
	BatchRequestPreparer            BatchRequestPreparer                   // Optional: batch request preparer before native provider submission
	ExposedModelLister              ExposedModelLister                     // Optional: additional public models to merge into GET /v1/models
	KeepOnlyAliasesAtModelsEndpoint bool                                   // Whether GET /v1/models should hide concrete provider models
	PassthroughSemanticEnrichers    []core.PassthroughSemanticEnricher     // Optional: provider-owned passthrough semantic enrichers before workflow resolution
	BatchStore                      batchstore.Store                       // Optional: Batch lifecycle persistence store
	FileStore                       filestore.Store                        // Optional: File provider mapping persistence store
	ResponseStore                   responsestore.Store                    // Optional: Responses lifecycle persistence store
	ConversationStore               conversationstore.Store                // Optional: Conversations lifecycle persistence store
	LogOnlyModelInteractions        bool                                   // Only log AI model endpoints (default: true)
	DisablePassthroughRoutes        bool                                   // Disable /p/{provider}/{endpoint} route registration
	RealtimeEnabled                 bool                                   // Enable the realtime websocket routes (/v1/realtime, /v1/realtime/translations) and passthrough upgrades
	MCPEnabled                      bool                                   // Enable the MCP gateway routes /mcp and /mcp/{server}
	MCPGateway                      *mcpgateway.Service                    // MCP gateway service (nil if disabled or not wired)
	EnabledPassthroughProviders     []string                               // Provider types enabled on /p/{provider}/... passthrough routes
	AllowPassthroughV1Alias         *bool                                  // Allow /p/{provider}/v1/... aliases; nil defaults to true
	UserPathHeader                  string                                 // Header carrying the request user path (default: X-GoModel-User-Path)
	AdminEndpointsEnabled           bool                                   // Whether admin API endpoints are enabled
	AdminUIEnabled                  bool                                   // Whether admin dashboard UI is enabled
	AdminHandler                    *admin.Handler                         // Admin API handler (nil if disabled)
	DashboardHandler                *dashboard.Handler                     // Dashboard UI handler (nil if disabled)
	SwaggerEnabled                  bool                                   // Whether to expose the Swagger UI at /swagger/index.html
	ResponseCacheMiddleware         *responsecache.ResponseCacheMiddleware // Optional: response cache middleware for cacheable endpoints
	GuardrailsHash                  string                                 // Optional: SHA-256 hash of active guardrail rules; stored in context post-patch for semantic cache
	IPExtractor                     echo.IPExtractor                       // Optional: trusted client IP extraction strategy for proxied deployments
	StorageProbe                    ReadinessProbe                         // Optional: primary storage connectivity check; failure makes /health/ready report not_ready (503)
	CacheProbe                      ReadinessProbe                         // Optional: Redis cache connectivity check; failure makes /health/ready report degraded (200, non-blocking)
	RequestRewriters                []ext.RequestRewriter                  // Optional: raw-body rewriters invoked on inference ingress (post-auth, pre-workflow-resolution)
	OuterMiddleware                 []echo.MiddlewareFunc                  // Optional: extension middleware after sensitive URI redaction, before logging/recovery/limits
	ExtraMiddleware                 []echo.MiddlewareFunc                  // Optional: extension middleware registered after audit, before gateway auth
	ExtraRoutes                     []func(*echo.Echo)                     // Optional: extension route registration callbacks invoked after core routes
	ExtraAuthSkipPaths              []string                               // Optional: extension paths appended to the auth skip list ("/*" suffix matches a prefix)
	RequestAuthenticators           []ext.RequestAuthenticator             // Optional extension-provided request authentication mechanisms
	Tagging                         *tagging.Service                       // Optional: request labelling based on configured tagging headers
	SessionDetector                 *session.Detector                      // Optional: client session identification for sticky routing and audit grouping
	VersionChecker                  *versioncheck.Checker                  // Optional: daily update check backing GET /version
}

// ReadinessProbe verifies that a dependency the gateway owns is reachable.
// It is deliberately narrow so the server stays decoupled from concrete storage
// and cache types. Upstream provider reachability is intentionally NOT a probe:
// an external provider outage must not pull a healthy gateway out of rotation.
type ReadinessProbe interface {
	Ping(ctx context.Context) error
}

// New creates a new HTTP server
func New(provider core.RoutableProvider, cfg *Config) *Server {
	// The router-level NotFoundHandler fires only when no route matches the
	// path at all, so unknown routes get a dialect-aware canonical error
	// envelope while echo's 405 handling for known paths stays intact (a
	// wildcard RouteNotFound route would shadow it and turn 405s into 404s).
	e := echo.NewWithConfig(echo.Config{
		Router: echo.NewRouter(echo.RouterConfig{
			AllowOverwritingRoute: true,
			NotFoundHandler:       handleRouteNotFound,
		}),
		JSONSerializer: goJSONSerializer{},
		// Errors no handler reported (recovered panics, responses the JSON
		// serializer could not encode) get the canonical envelope and a log
		// line instead of echo's silent, bare "Internal Server Error".
		HTTPErrorHandler: gatewayErrorHandler,
	})
	e.Logger = slog.Default()
	basePath := configuredBasePath(cfg)
	if basePath != "/" {
		e.Pre(stripBasePathMiddleware(basePath))
	}
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
	var budgetChecker BudgetChecker
	var pricingResolver usage.PricingResolver
	if cfg != nil {
		auditLogger = cfg.AuditLogger
		usageLogger = cfg.UsageLogger
		budgetChecker = cfg.BudgetChecker
		pricingResolver = cfg.PricingResolver
	}

	var modelResolver RequestModelResolver
	var modelAuthorizer RequestModelAuthorizer
	var workflowPolicyResolver RequestWorkflowPolicyResolver
	var failoverResolver RequestFailoverResolver
	var translatedRequestPatcher TranslatedRequestPatcher
	if cfg != nil {
		modelResolver = cfg.ModelResolver
		modelAuthorizer = cfg.ModelAuthorizer
		workflowPolicyResolver = cfg.WorkflowPolicyResolver
		failoverResolver = cfg.FailoverResolver
		translatedRequestPatcher = cfg.TranslatedRequestPatcher
	}

	handler := newHandlerWithAuthorizer(provider, auditLogger, usageLogger, pricingResolver, modelResolver, modelAuthorizer, workflowPolicyResolver, failoverResolver, translatedRequestPatcher)
	handler.budgetChecker = budgetChecker
	if cfg != nil {
		handler.failoverPolicy = cfg.FailoverPolicy
		handler.rateLimiter = cfg.RateLimiter
		handler.usageSummarizer = cfg.UsageSummarizer
	}
	if cfg != nil {
		handler.batchRequestPreparer = cfg.BatchRequestPreparer
		handler.exposedModelLister = cfg.ExposedModelLister
		handler.keepOnlyAliasesAtModelsEndpoint = cfg.KeepOnlyAliasesAtModelsEndpoint
		handler.responseCache = cfg.ResponseCacheMiddleware
		handler.guardrailsHash = cfg.GuardrailsHash
		handler.storageProbe = cfg.StorageProbe
		handler.cacheProbe = cfg.CacheProbe
	}
	if cfg != nil && cfg.EnabledPassthroughProviders != nil {
		handler.setEnabledPassthroughProviders(cfg.EnabledPassthroughProviders)
	}
	// Mirror the route-registration default below: a nil config enables realtime
	// so the documented default and the registered route stay consistent.
	handler.realtimeEnabled = cfg == nil || cfg.RealtimeEnabled
	if cfg != nil {
		handler.versionChecker = cfg.VersionChecker
	}
	if cfg != nil {
		handler.mcpEnabled = cfg.MCPEnabled
		handler.mcpGateway = cfg.MCPGateway
	}
	if cfg != nil && !passthroughV1PrefixNormalizationEnabled(cfg) {
		handler.normalizePassthroughV1Prefix = false
	}
	if cfg != nil && cfg.BatchStore != nil {
		handler.SetBatchStore(cfg.BatchStore)
	}
	if cfg != nil && cfg.FileStore != nil {
		handler.SetFileStore(cfg.FileStore)
	}
	if cfg != nil && cfg.ResponseStore != nil {
		handler.SetResponseStore(cfg.ResponseStore)
	}
	if cfg != nil && cfg.ConversationStore != nil {
		handler.SetConversationStore(cfg.ConversationStore)
	}

	// Build list of paths that skip authentication
	authSkipPaths := []string{"/health", "/health/ready", "/version"}

	// Determine metrics path
	metricsPath := config.ResolveMetricsEndpoint("")
	if cfg != nil && cfg.MetricsEnabled {
		configuredPath := path.Clean("/" + cfg.MetricsEndpoint)
		metricsPath = config.ResolveMetricsEndpointWithPprof(cfg.MetricsEndpoint, cfg.PprofEnabled)
		// Prevent metrics endpoint from shadowing API routes (security: auth bypass)
		if metricsPath != configuredPath && cfg.MetricsEndpoint != "" {
			slog.Warn("metrics endpoint conflicts with API routes, using /metrics instead",
				"configured", cfg.MetricsEndpoint,
				"normalized", configuredPath)
		}
		authSkipPaths = append(authSkipPaths, metricsPath)
	}

	// Admin dashboard pages and static assets skip auth (/* enables prefix matching)
	if cfg != nil && cfg.AdminUIEnabled && cfg.DashboardHandler != nil {
		authSkipPaths = append(authSkipPaths, "/admin/dashboard", "/admin/dashboard/*", "/admin/static/*")
	}
	// When no bootstrap master key is configured, keep admin APIs reachable so
	// the dashboard can recover managed-key access instead of locking itself out.
	if cfg != nil && cfg.MasterKey == "" && !hasRequestAuthenticators(cfg.RequestAuthenticators) && cfg.AdminEndpointsEnabled && cfg.AdminHandler != nil {
		authSkipPaths = append(authSkipPaths, "/admin/*")
	}
	if cfg != nil && cfg.SwaggerEnabled && SwaggerAvailable() {
		authSkipPaths = append(authSkipPaths, "/swagger/*")
	}
	if cfg != nil && cfg.PprofEnabled {
		authSkipPaths = append(authSkipPaths, "/debug/pprof", "/debug/pprof/*")
	}
	if cfg != nil {
		authSkipPaths = append(authSkipPaths, cfg.ExtraAuthSkipPaths...)
	}

	// Global middleware stack (order matters)
	// Scrub credential-like query values before the outer request logger
	// snapshots RequestURI. URL.RawQuery remains intact for handlers.
	e.Use(redactSensitiveRequestURI())
	e.Use(middleware.Recover())
	// Outer extension middleware covers the complete HTTP request while still
	// seeing the credential-redacted URI. It runs before request logging,
	// limits, audit, and auth, so it must not depend on identity.
	if cfg != nil {
		for _, m := range cfg.OuterMiddleware {
			e.Use(m)
		}
	}
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
			// LogAttrs with typed attrs: slog.Info's variadic ...any would box
			// every value (an allocation each) on every model request.
			LogValuesFunc: func(c *echo.Context, v middleware.RequestLoggerValues) error {
				slog.LogAttrs(context.Background(), slog.LevelInfo, "REQUEST",
					slog.String("method", v.Method),
					slog.String("uri", v.URI),
					slog.Int("status", v.Status),
					slog.String("latency", v.Latency.String()),
					slog.String("host", v.Host),
					slog.String("bytes_in", v.ContentLength),
					slog.Int64("bytes_out", v.ResponseSize),
					slog.String("user_agent", v.UserAgent),
					slog.String("remote_ip", v.RemoteIP),
					slog.String("request_id", v.RequestID),
				)
				return nil
			},
		}))
	} else {
		e.Use(middleware.RequestLogger())
	}
	// Body size limit (default: 10MB)
	bodySizeLimit := "10M"
	if cfg != nil && cfg.BodySizeLimit != "" {
		bodySizeLimit = cfg.BodySizeLimit
	}
	e.Use(middleware.BodyLimit(parseBodySizeLimitBytes(bodySizeLimit)))

	e.Use(modelInteractionWriteDeadlineMiddleware())

	// Ingress capture (before auth/audit/model validation so they can consume
	// shared raw request state). Also assigns the per-request ID: the snapshot
	// middleware runs unconditionally and calls ensureRequestID first thing, so
	// a separate request-ID middleware would just repeat that work (a second
	// context wrap + request copy) on every request.
	userPathHeaderName := configuredUserPathHeader(cfg)
	handler.userPathHeaderName = userPathHeaderName
	e.Use(RequestSnapshotCapture(userPathHeaderName))

	// Request labelling from configured tagging headers (after snapshot capture so
	// audit logging still sees the original headers, before audit logging so
	// entries can record the labels)
	if cfg != nil && cfg.Tagging != nil {
		e.Use(TaggingCapture(cfg.Tagging))
	}

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

	// Extension middleware runs after audit capture and before gateway auth so
	// extensions (e.g. SSO sessions) can normalize credentials for the auth check.
	if cfg != nil {
		for _, m := range cfg.ExtraMiddleware {
			e.Use(m)
		}
	}

	// Authentication (skips public paths)
	// Register by authenticator presence; its Enabled state can change at runtime.
	authMiddlewareRegistered := cfg != nil && (cfg.MasterKey != "" || cfg.Authenticator != nil || hasRequestAuthenticators(cfg.RequestAuthenticators))
	if authMiddlewareRegistered {
		e.Use(AuthMiddlewareWithRequestAuthenticators(cfg.MasterKey, cfg.Authenticator, cfg.RequestAuthenticators, authSkipPaths, userPathHeaderName))
	}

	// Session identification runs after auth so session ids are scoped by the
	// EFFECTIVE user path (a managed key's bound path, not the ingress header)
	// and before workflow resolution, which consumes the id for sticky
	// virtual-model routing. The audit middleware re-reads the id after the
	// handler returns, so persisted entries carry it even though they are
	// created earlier in the chain.
	if cfg != nil && cfg.SessionDetector != nil {
		e.Use(sessionCapture(cfg.SessionDetector, cfg.AuditReader, !authMiddlewareRegistered))
	}

	// Request rewriters run post-auth (rewriters only see authenticated
	// traffic) and pre-workflow-resolution (body rewrites, including "model",
	// affect routing, failover, guardrails, budgets, and caching). Not
	// registered when no rewriters exist, so the default build pays nothing.
	if cfg != nil && len(cfg.RequestRewriters) > 0 {
		e.Use(RequestRewriteMiddleware(cfg.RequestRewriters, auditLogger))
	}

	// Workflow resolution resolves the request-scoped workflow after auth so
	// managed auth key user-path overrides are visible to policy resolution while
	// still keeping workflow resolution failures loggable through the audit middleware.
	e.Use(WorkflowResolutionWithResolverAndPolicy(provider, modelResolver, workflowPolicyResolver))

	// Public routes
	e.GET("/health", handler.Health)
	e.GET("/health/ready", handler.Ready)
	e.GET("/version", handler.Version)
	registerSwagger(e, cfg)
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
	e.GET("/v1/usage", handler.UsageStatus)
	e.POST("/v1/chat/completions", handler.ChatCompletion)
	e.POST("/v1/messages", handler.Messages)
	e.POST("/v1/messages/count_tokens", handler.CountMessageTokens)
	e.POST("/v1/messages/batches", handler.MessagesBatches)
	e.GET("/v1/messages/batches", handler.ListMessagesBatches)
	e.GET("/v1/messages/batches/:id", handler.GetMessagesBatch)
	e.POST("/v1/messages/batches/:id/cancel", handler.CancelMessagesBatch)
	e.DELETE("/v1/messages/batches/:id", handler.DeleteMessagesBatch)
	e.GET("/v1/messages/batches/:id/results", handler.MessagesBatchResults)
	e.POST("/v1/responses/input_tokens", handler.ResponseInputTokens)
	e.POST("/v1/responses/compact", handler.CompactResponse)
	e.GET("/v1/responses/:id/input_items", handler.ListResponseInputItems)
	e.POST("/v1/responses/:id/cancel", handler.CancelResponse)
	e.GET("/v1/responses/:id", handler.GetResponse)
	e.DELETE("/v1/responses/:id", handler.DeleteResponse)
	e.POST("/v1/responses", handler.Responses)
	e.POST("/v1/conversations", handler.CreateConversation)
	e.POST("/v1/conversations/:id/items", handler.CreateConversationItems)
	e.GET("/v1/conversations/:id/items", handler.ListConversationItems)
	e.GET("/v1/conversations/:id/items/:item_id", handler.GetConversationItem)
	e.DELETE("/v1/conversations/:id/items/:item_id", handler.DeleteConversationItem)
	e.GET("/v1/conversations/:id", handler.GetConversation)
	e.POST("/v1/conversations/:id", handler.UpdateConversation)
	e.DELETE("/v1/conversations/:id", handler.DeleteConversation)
	e.POST("/v1/embeddings", handler.Embeddings)
	e.POST("/v1/audio/speech", handler.AudioSpeech)
	e.POST("/v1/audio/transcriptions", handler.AudioTranscriptions)
	e.POST("/v1/audio/translations", handler.AudioTranslations)
	e.POST("/v1/images/generations", handler.ImageGenerations)
	e.POST("/v1/images/edits", handler.ImageEdits)
	if cfg == nil || cfg.RealtimeEnabled {
		e.GET("/v1/realtime", handler.Realtime)
		e.POST("/v1/realtime/calls", handler.RealtimeCalls)
		e.POST("/v1/realtime/client_secrets", handler.RealtimeClientSecrets)
		// Speech translation sessions have their own provider surface; the
		// gateway mirrors it so OpenAI clients need no rewriting.
		e.GET("/v1/realtime/translations", handler.RealtimeTranslations)
		e.POST("/v1/realtime/translations/calls", handler.RealtimeTranslationCalls)
		e.POST("/v1/realtime/translations/client_secrets", handler.RealtimeTranslationClientSecrets)
	}
	if cfg != nil && cfg.MCPEnabled && cfg.MCPGateway != nil {
		e.POST("/mcp", handler.MCP)
		e.GET("/mcp", handler.MCP)
		e.DELETE("/mcp", handler.MCP)
		e.POST("/mcp/:server", handler.MCPServer)
		e.GET("/mcp/:server", handler.MCPServer)
		e.DELETE("/mcp/:server", handler.MCPServer)
	}
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

	// Admin API routes (behind ADMIN_ENDPOINTS_ENABLED flag). Managed keys
	// need dashboard access to pass the gate; the master key always does.
	if cfg != nil && cfg.AdminEndpointsEnabled && cfg.AdminHandler != nil {
		adminGate := AdminAccessMiddleware()
		// Admin responses are large, highly compressible JSON (audit entries
		// carrying request/response bodies, usage aggregates), so gzip cuts
		// the wire size roughly an order of magnitude. The live-log SSE
		// stream is exempt: each event must flush to the client immediately,
		// and buffering it behind a compressor delays delivery.
		adminGzip := middleware.GzipWithConfig(middleware.GzipConfig{
			Skipper: func(c *echo.Context) bool {
				return strings.HasSuffix(c.Request().URL.Path, "/live/logs")
			},
		})
		cfg.AdminHandler.RegisterRoutes(e.Group("/admin", adminGate, adminGzip))

		// Legacy alias under /admin/api/v1/* — accepted until adminLegacySunset
		// to give operators a window to migrate. Responses carry Deprecation,
		// Sunset, and Link headers per RFC 8594 / draft-ietf-httpapi-deprecation-header.
		legacy := e.Group("/admin/api/v1", adminLegacyDeprecationMiddleware, adminGate, adminGzip)
		cfg.AdminHandler.RegisterRoutes(legacy)
		// DashboardConfig moved within /admin from /dashboard/config to
		// /runtime/config; preserve the historical legacy path explicitly.
		legacy.GET("/dashboard/config", cfg.AdminHandler.DashboardConfig)
	}

	// Admin dashboard UI routes (behind ADMIN_UI_ENABLED flag)
	if cfg != nil && cfg.AdminUIEnabled && cfg.DashboardHandler != nil {
		e.GET("/admin/dashboard", cfg.DashboardHandler.Index)
		e.GET("/admin/dashboard/*", cfg.DashboardHandler.Index)
		e.GET("/admin/static/*", cfg.DashboardHandler.Static)
	}

	// Extension routes register after all core routes.
	if cfg != nil {
		for _, register := range cfg.ExtraRoutes {
			register(e)
		}
	}

	var rcm *responsecache.ResponseCacheMiddleware
	if cfg != nil {
		rcm = cfg.ResponseCacheMiddleware
	}
	return &Server{
		echo:                    e,
		handler:                 handler,
		responseCacheMiddleware: rcm,
		responseStore:           handler.currentResponseStore(),
		conversationStore:       handler.conversationStore,
	}
}

// adminLegacySunset is the sunset date advertised on responses served from the
// deprecated /admin/api/v1/* alias. Format follows RFC 7231 HTTP-date.
const adminLegacySunset = "Sun, 09 Aug 2026 00:00:00 GMT"

// adminLegacyDeprecationMiddleware tags responses on the legacy /admin/api/v1/*
// alias with deprecation signals so clients can detect the move to /admin/*.
func adminLegacyDeprecationMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c *echo.Context) error {
		h := c.Response().Header()
		h.Set("Deprecation", "true")
		h.Set("Sunset", adminLegacySunset)
		h.Set("Link", `</admin/>; rel="successor-version"`)
		return next(c)
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

// StartWithListener starts the HTTP server using a pre-bound listener. The
// gateway serves this way in production — the listening socket outlives each
// configuration a reload installs — and tests use it to reserve a loopback
// port up front, so it configures the server exactly like Start does: same
// inbound timeouts, same drain window.
func (s *Server) StartWithListener(ctx context.Context, listener net.Listener) error {
	if listener == nil {
		return errors.New("listener is required")
	}
	return newGatewayStartConfigForListener(listener).Start(ctx, s.echo)
}

// Shutdown releases server resources. The HTTP server itself is stopped by
// cancelling the context passed to Start; this method drains any in-flight
// response cache and snapshot writes, closes the cache store, and closes the
// response and conversation stores.
func (s *Server) Shutdown(_ context.Context) error {
	var firstErr error
	if s.responseCacheMiddleware != nil {
		if err := s.responseCacheMiddleware.Close(); err != nil {
			firstErr = err
		}
	}
	s.handler.drainSnapshotWrites()
	if s.responseStore != nil {
		if err := s.responseStore.Close(); err != nil {
			if firstErr == nil {
				firstErr = err
			} else {
				slog.Warn("response store close failed during shutdown", "error", err)
			}
		}
	}
	if s.conversationStore != nil {
		if err := s.conversationStore.Close(); err != nil {
			if firstErr == nil {
				firstErr = err
			} else {
				slog.Warn("conversation store close failed during shutdown", "error", err)
			}
		}
	}
	return firstErr
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
		// Echo's own default is an implicit 10s that reports the cutoff as an
		// unexplained error. Both are set here so the drain window is sized
		// against the application's shutdown budget and so cutting a stream
		// short on Ctrl+C reads as the routine event it is.
		GracefulTimeout: GracefulDrainTimeout,
		OnShutdownError: func(err error) {
			slog.Warn("closing requests still in flight at the shutdown deadline",
				"graceful_timeout", GracefulDrainTimeout,
				"error", err,
			)
		},
	}
}

// newGatewayStartConfig with a pre-bound listener. Echo ignores Address once
// Listener is set; it is filled in anyway so the two describe the same server.
func newGatewayStartConfigForListener(listener net.Listener) echo.StartConfig {
	sc := newGatewayStartConfig(listener.Addr().String())
	sc.Listener = listener
	return sc
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
