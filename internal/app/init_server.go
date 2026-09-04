package app

import (
	"fmt"
	"log/slog"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/gateway"
	"github.com/enterpilot/gomodel/internal/guardrails"
	"github.com/enterpilot/gomodel/internal/mcpgateway"
	"github.com/enterpilot/gomodel/internal/ratelimit"
	"github.com/enterpilot/gomodel/internal/responsecache"
	"github.com/enterpilot/gomodel/internal/server"
	"github.com/enterpilot/gomodel/internal/session"
	"github.com/enterpilot/gomodel/internal/storage"
	"github.com/enterpilot/gomodel/internal/usage"
	"github.com/enterpilot/gomodel/internal/virtualmodels"
)

// initServerDependencies builds the request-path collaborators the server
// config needs: guardrail patchers and batch preparers, the rate-limit usage
// tap, the MCP gateway, the usage reader, and the version checker.
func (b *bootstrap) initServerDependencies() error {
	app := b.app
	appCfg := b.appCfg
	vm := app.virtualModels.Service

	// Build runtime execution dependencies. Policy is passed explicitly into the
	// server; the live provider dependency remains the bare router.
	b.provider = app.providers.Router
	var batchRequestPreparers []server.BatchRequestPreparer
	if b.featureCaps.Guardrails {
		if app.guardrails != nil && app.guardrails.Service != nil {
			b.translatedRequestPatcher = guardrails.NewWorkflowRequestPatcher(app.workflows.Service)
			b.pluginChains = app.workflows.Service
			if appCfg.Guardrails.EnableForBatchProcessing {
				batchRequestPreparers = append(batchRequestPreparers, guardrails.NewWorkflowBatchPreparer(b.provider, app.workflows.Service))
			}
			slog.Info(
				"guardrails enabled",
				"count", app.guardrails.Service.Len(),
				"enable_for_batch_processing", appCfg.Guardrails.EnableForBatchProcessing,
			)
		}
	}
	if vm != nil {
		// One combined preparer rewrites redirect sources and validates access,
		// replacing the previous two-preparer pipeline.
		batchRequestPreparers = append([]server.BatchRequestPreparer{
			virtualmodels.NewBatchPreparer(b.provider, vm),
		}, batchRequestPreparers...)
	}
	b.batchRequestPreparer = server.ComposeBatchRequestPreparers(providerAsNativeFileRouter(b.provider), batchRequestPreparers...)

	b.swaggerEnabled = appCfg.Server.SwaggerEnabled && server.SwaggerAvailable()
	if appCfg.Server.SwaggerEnabled && !server.SwaggerAvailable() {
		slog.Warn("swagger UI requested but not available in this build",
			"recommendation", "rebuild with -tags=swagger")
	}

	// The usage tap feeds recorded token counts into rate limit token windows
	// before delegating to the real logger; it is transparent when no rate
	// limit service exists.
	b.serverUsageLogger = usage.LoggerInterface(app.usage.Logger)
	if app.rateLimits.Service != nil {
		b.serverUsageLogger = ratelimit.NewUsageTap(b.serverUsageLogger, app.rateLimits.Service)
	}

	// Initialize the MCP gateway (aggregated upstream MCP servers behind /mcp).
	if appCfg.MCP.Enabled {
		mcpResult, err := mcpgateway.New(b.ctx, appCfg, app.storage, nil, b.serverUsageLogger)
		if err != nil {
			return fmt.Errorf("failed to initialize mcp gateway: %w", err)
		}
		app.mcpGateway = mcpResult
		app.register(subsystemMCPGateway, ownedByShutdown, app.mcpGateway.Close)
		slog.Info("mcp gateway enabled",
			"path", config.JoinBasePath(appCfg.Server.BasePath, "/mcp"),
			"configured_servers", len(appCfg.MCP.Servers))
	} else {
		slog.Info("mcp gateway disabled")
	}

	// The self-service GET /v1/usage endpoint and the admin dashboard read
	// usage aggregates through one shared reader.
	if app.storage != nil {
		usageReader, err := usage.NewReader(app.storage)
		if err != nil {
			slog.Warn("usage reader unavailable; usage endpoints will omit usage data", "error", err)
		} else {
			// Assigned only on success so a typed-nil reader never reaches the
			// nil checks downstream (same guard as pricingRecalculator).
			b.usageReader = usageReader
		}
	}

	// The update check owns the only outbound connection core makes that is
	// not a provider call. It is constructed even when disabled so GET
	// /version keeps reporting the local build.
	app.versionCheck = newVersionChecker(b.ctx, appCfg.VersionCheck, app.storage, appCfg.Server.MasterKey)
	warnIfDataDirEphemeral(appCfg.Storage.BackendConfig())
	return nil
}

// initServerConfig assembles the server configuration from every subsystem
// built so far. Later phases (admin, response cache) add to it before
// initServer hands it to server.New.
func (b *bootstrap) initServerConfig() error {
	app := b.app
	appCfg := b.appCfg
	vm := app.virtualModels.Service

	allowPassthroughV1Alias := appCfg.Server.AllowPassthroughV1Alias
	serverCfg := &server.Config{
		BasePath:                        appCfg.Server.BasePath,
		MasterKey:                       appCfg.Server.MasterKey,
		Authenticator:                   app.authKeys.Service,
		MetricsEnabled:                  appCfg.Metrics.Enabled,
		MetricsEndpoint:                 appCfg.Metrics.Endpoint,
		BodySizeLimit:                   appCfg.Server.BodySizeLimit,
		PprofEnabled:                    appCfg.Server.PprofEnabled,
		AuditLogger:                     app.audit.Logger,
		UsageLogger:                     b.serverUsageLogger,
		BudgetChecker:                   app.budgets.Service,
		PricingResolver:                 b.pricingResolver,
		ModelResolver:                   vm,
		ModelAuthorizer:                 vm,
		FailoverResolver:                failoverResolver(appCfg, vm),
		FailoverPolicy:                  gateway.NewFailoverPolicy(appCfg.Failover),
		WorkflowPolicyResolver:          app.workflows.Service,
		TranslatedRequestPatcher:        b.translatedRequestPatcher,
		PluginChainsResolver:            b.pluginChains,
		BatchRequestPreparer:            b.batchRequestPreparer,
		ExposedModelLister:              vm,
		KeepOnlyAliasesAtModelsEndpoint: appCfg.Models.KeepOnlyAliasesAtModelsEndpoint,
		PassthroughSemanticEnrichers:    b.cfg.Factory.PassthroughSemanticEnrichers(),
		BatchStore:                      app.batch.Store,
		FileStore:                       app.fileStore.Store,
		ResponseStore:                   app.responseStore.Store,
		ConversationStore:               app.conversations.Store,
		LogOnlyModelInteractions:        appCfg.Logging.OnlyModelInteractions,
		DisablePassthroughRoutes:        !appCfg.Server.EnablePassthroughRoutes,
		EnabledPassthroughProviders:     appCfg.Server.EnabledPassthroughProviders,
		RealtimeEnabled:                 appCfg.Server.RealtimeEnabled,
		AllowPassthroughV1Alias:         &allowPassthroughV1Alias,
		UserPathHeader:                  appCfg.Server.UserPathHeader,
		SwaggerEnabled:                  b.swaggerEnabled,
		Tagging:                         app.tagging.Service,
		SessionDetector:                 session.NewDetectorFromConfig(appCfg.Session),
		MCPEnabled:                      appCfg.MCP.Enabled,
		VersionChecker:                  app.versionCheck,
	}
	if app.mcpGateway != nil {
		serverCfg.MCPGateway = app.mcpGateway.Service
	}

	// Assigned conditionally so a disabled feature leaves the interface nil
	// (a typed-nil *ratelimit.Service would defeat the fast nil check).
	if app.rateLimits.Service != nil {
		serverCfg.RateLimiter = app.rateLimits.Service
	}
	if b.usageReader != nil {
		serverCfg.UsageSummarizer = b.usageReader
	}

	applyExtensions(serverCfg, b.cfg.Extensions)
	if app.telemetry != nil {
		// Outermost, so the HTTP server span also covers extension middleware.
		serverCfg.OuterMiddleware = append([]echo.MiddlewareFunc{app.telemetry.Middleware()}, serverCfg.OuterMiddleware...)
	}

	// Wire the readiness storage probe. Storage is a required dependency, so a
	// failed ping makes /health/ready report not_ready (503). When no storage
	// backend is active, readiness simply collapses to liveness.
	if hc, ok := app.storage.(storage.HealthChecker); ok {
		serverCfg.StorageProbe = hc
	}
	b.serverCfg = serverCfg
	return nil
}

// initResponseCache builds the response cache middleware and the internal
// chat executor guardrails use for LLM-based rules. The executor needs the
// finished server config (failover policy, usage tap, cache), so this is the
// last fallible phase before the server is created.
func (b *bootstrap) initResponseCache() error {
	app := b.app
	appCfg := b.appCfg
	serverCfg := b.serverCfg

	rcm, err := responsecache.NewResponseCacheMiddleware(appCfg.Cache.Response, app.providers.CredentialResolvedProviders, app.usage.Logger, b.pricingResolver)
	if err != nil {
		return fmt.Errorf("failed to initialize response cache: %w", err)
	}
	app.register(subsystemResponseCache, ownedByServer, rcm.Close)
	b.responseCache = rcm
	serverCfg.ResponseCacheMiddleware = rcm

	// Wire the readiness cache probe only when a Redis-backed exact cache is
	// configured. The cache is a performance optimization, so a failed ping
	// reports degraded (200) rather than blocking traffic.
	if rcm.UsesRedis() {
		serverCfg.CacheProbe = rcm
	}

	vm := app.virtualModels.Service
	internalGuardrailExecutor := server.NewInternalChatCompletionExecutor(b.provider, server.InternalChatCompletionExecutorConfig{
		ModelResolver:          vm,
		ModelAuthorizer:        vm,
		WorkflowPolicyResolver: app.workflows.Service,
		FailoverResolver:       serverCfg.FailoverResolver,
		FailoverPolicy:         serverCfg.FailoverPolicy,
		AuditLogger:            app.audit.Logger,
		// The tapped logger, so guardrail LLM calls count toward the
		// request's rate limit token windows like any other completion.
		UsageLogger:     b.serverUsageLogger,
		PricingResolver: b.pricingResolver,
		ResponseCache:   rcm,
	})
	// Instances pick the executor up on their next call; no rebuild needed.
	app.guardrails.Service.SetChatCompleter(internalGuardrailExecutor)
	return nil
}

// initServer creates the HTTP server and binds the extension authenticators.
// It is infallible and runs last: everything before it can fail, and a
// failed replacement generation must not have rebound shared registries.
func (b *bootstrap) initServer() error {
	app := b.app

	if b.livePublishersEnabled {
		app.attachLivePublishers()
	}
	app.server = server.New(b.provider, b.serverCfg)

	// Registries are reused across reload generations, so their authenticators
	// are shared too. Rebind only after every fallible construction step has
	// succeeded: otherwise a failed replacement would leave the still-serving
	// generation pointing at the replacement's already-closed audit logger.
	bindAuthenticationEventRecorders(b.cfg.Extensions, auditlog.NewAuthenticationEventRecorder(app.audit.Logger))
	return nil
}

// applyExtensions snapshots a registered extension set into the server
// configuration. A nil registry leaves the config untouched.
func applyExtensions(serverCfg *server.Config, extensions *ext.Registry) {
	if extensions == nil {
		return
	}
	serverCfg.RequestRewriters = extensions.Rewriters()
	serverCfg.OuterMiddleware = extensions.OuterMiddleware()
	serverCfg.ExtraMiddleware = extensions.Middleware()
	serverCfg.ExtraRoutes = extensions.Routes()
	serverCfg.ExtraAuthSkipPaths = extensions.PublicPaths()
	serverCfg.RequestAuthenticators = extensions.Authenticators()
}

func providerAsNativeFileRouter(provider core.RoutableProvider) core.NativeFileRoutableProvider {
	if fileRouter, ok := provider.(core.NativeFileRoutableProvider); ok {
		return fileRouter
	}
	return nil
}
