// Package app provides the main application struct for centralized dependency management
// and lifecycle control of the GoModel server.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/admin"
	"github.com/enterpilot/gomodel/internal/admin/dashboard"
	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/authkeys"
	"github.com/enterpilot/gomodel/internal/batch"
	"github.com/enterpilot/gomodel/internal/budget"
	"github.com/enterpilot/gomodel/internal/conversationstore"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/failover"
	"github.com/enterpilot/gomodel/internal/filestore"
	"github.com/enterpilot/gomodel/internal/guardrails"
	"github.com/enterpilot/gomodel/internal/httpclient"
	"github.com/enterpilot/gomodel/internal/live"
	"github.com/enterpilot/gomodel/internal/mcpgateway"
	"github.com/enterpilot/gomodel/internal/pricingoverrides"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/health"
	"github.com/enterpilot/gomodel/internal/ratelimit"
	"github.com/enterpilot/gomodel/internal/responsecache"
	"github.com/enterpilot/gomodel/internal/responsestore"
	"github.com/enterpilot/gomodel/internal/server"
	"github.com/enterpilot/gomodel/internal/storage"
	"github.com/enterpilot/gomodel/internal/tagging"
	"github.com/enterpilot/gomodel/internal/usage"
	"github.com/enterpilot/gomodel/internal/virtualmodels"
	"github.com/enterpilot/gomodel/internal/workflows"
)

// App represents the main application with all its dependencies.
// It provides centralized lifecycle management for all components.
type App struct {
	config           *config.Config
	providers        *providers.InitResult
	audit            *auditlog.Result
	usage            *usage.Result
	budgets          *budget.Result
	rateLimits       *ratelimit.Result
	batch            *batch.Result
	fileStore        *filestore.Result
	responseStore    *responsestore.Result
	conversations    *conversationstore.Result
	virtualModels    *virtualmodels.Result
	failover         *failover.Result
	tagging          *tagging.Result
	mcpGateway       *mcpgateway.Result
	pricingOverrides *pricingoverrides.Result
	authKeys         *authkeys.Result
	guardrails       *guardrails.Result
	workflows        *workflows.Result
	live             *live.Broker
	server           *server.Server

	shutdownMu  sync.Mutex
	shutdown    bool
	serverMu    sync.Mutex
	serverStop  context.CancelFunc
	serverDone  chan error
	refreshCh   chan struct{}
	refreshOnce sync.Once
}

// Config holds the configuration options for creating an App.
type Config struct {
	// AppConfig holds the loaded application configuration and raw provider data
	// produced by config.Load.
	AppConfig *config.LoadResult

	// Factory provides the ProviderFactory used to construct provider instances.
	Factory *providers.ProviderFactory

	// Extensions optionally carries registered gateway extensions (request
	// rewriters, middleware, routes). The registry is snapshotted here; later
	// registrations have no effect.
	Extensions *ext.Registry
}

// applyExtensions snapshots a registered extension set into the server
// configuration. A nil registry leaves the config untouched.
func applyExtensions(serverCfg *server.Config, extensions *ext.Registry) {
	if extensions == nil {
		return
	}
	serverCfg.RequestRewriters = extensions.Rewriters()
	serverCfg.ExtraMiddleware = extensions.Middleware()
	serverCfg.ExtraRoutes = extensions.Routes()
	serverCfg.ExtraAuthSkipPaths = extensions.PublicPaths()
}

// New creates a new App with all dependencies initialized.
// The caller must call Shutdown to release resources.
func New(ctx context.Context, cfg Config) (*App, error) {
	if cfg.AppConfig == nil {
		return nil, fmt.Errorf("app config is required")
	}

	if cfg.AppConfig.Config == nil {
		return nil, fmt.Errorf("app config contains nil Config")
	}

	if cfg.Factory == nil {
		return nil, fmt.Errorf("factory is required")
	}

	appCfg := cfg.AppConfig.Config
	// Install config-file HTTP timeouts before any provider constructs a
	// transport; env vars still take precedence inside httpclient.
	httpclient.SetConfiguredTimeouts(appCfg.HTTP.Timeout, appCfg.HTTP.ResponseHeaderTimeout)
	if appCfg.Budgets.Enabled && !appCfg.Usage.Enabled {
		appCfg.Budgets.Enabled = false
		slog.Warn("budget management disabled because usage tracking is disabled",
			"usage_enabled", false,
			"budgets_enabled", false,
			"hint", "enable usage tracking to use budgets, or set BUDGETS_ENABLED=false to silence this warning",
		)
	}

	app := &App{
		config: appCfg,
	}
	app.live = live.NewBroker(live.Config{
		Enabled:     appCfg.Admin.LiveLogsEnabled,
		BufferSize:  appCfg.Admin.LiveLogsBufferSize,
		ReplayLimit: appCfg.Admin.LiveLogsReplayLimit,
		Heartbeat:   time.Duration(appCfg.Admin.LiveLogsHeartbeatSeconds) * time.Second,
	})

	// closers collects the Close functions of successfully initialized
	// components; fail unwinds them in reverse order before returning an
	// initialization error. Appending here is the single source of truth
	// for the cleanup order on startup failure. The live broker is created
	// above, so it is the first entry.
	closers := []func() error{func() error {
		app.live.Close()
		return nil
	}}
	fail := func(msg string, cause error) (*App, error) {
		var closeErrs []error
		for i := len(closers) - 1; i >= 0; i-- {
			closeErrs = append(closeErrs, closers[i]())
		}
		closeErr := errors.Join(closeErrs...)
		switch {
		case cause != nil && closeErr != nil:
			return nil, fmt.Errorf("%s: %w (also: close error: %v)", msg, cause, closeErr)
		case cause != nil:
			return nil, fmt.Errorf("%s: %w", msg, cause)
		case closeErr != nil:
			return nil, fmt.Errorf("%s (also: close error: %v)", msg, closeErr)
		default:
			return nil, errors.New(msg)
		}
	}

	// sharedStorage is the first non-nil storage backend among initialized
	// components; later stores reuse it instead of opening their own.
	var sharedStorage storage.Storage
	claimSharedStorage := func(s storage.Storage) {
		if sharedStorage == nil {
			sharedStorage = s
		}
	}

	// Track real-traffic outcomes per provider/model for the dashboard's
	// provider status; hooks must be composed before any provider is created.
	requestHealth := health.NewTracker()
	cfg.Factory.AddHooks(requestHealth.Hooks())

	providerResult, err := providers.Init(ctx, cfg.AppConfig, cfg.Factory)
	if err != nil {
		return fail("failed to initialize providers", err)
	}
	app.providers = providerResult
	closers = append(closers, app.providers.Close)

	// Initialize audit logging
	auditResult, err := auditlog.New(ctx, appCfg)
	if err != nil {
		return fail("failed to initialize audit logging", err)
	}
	app.audit = auditResult
	closers = append(closers, app.audit.Close)
	claimSharedStorage(auditResult.Storage)

	// Initialize usage tracking
	// Use shared storage if both audit logging and usage tracking use the same backend
	var usageResult *usage.Result
	if auditResult.Storage != nil && appCfg.Usage.Enabled {
		// Share storage connection with audit logging
		usageResult, err = usage.NewWithSharedStorage(ctx, appCfg, auditResult.Storage)
	} else {
		// Create separate storage or return noop logger
		usageResult, err = usage.New(ctx, appCfg)
	}
	if err != nil {
		return fail("failed to initialize usage tracking", err)
	}
	if usageResult == nil || usageResult.Logger == nil {
		if usageResult != nil {
			closers = append(closers, usageResult.Close)
		}
		return fail("usage tracking initialization returned nil result", nil)
	}
	app.usage = usageResult
	closers = append(closers, app.usage.Close)
	claimSharedStorage(usageResult.Storage)

	var budgetResult *budget.Result
	if appCfg.Budgets.Enabled {
		if sharedStorage != nil {
			budgetResult, err = budget.NewWithSharedStorage(ctx, appCfg, sharedStorage)
		} else {
			budgetResult, err = budget.New(ctx, appCfg)
		}
		if err != nil {
			return fail("failed to initialize budgets", err)
		}
	} else {
		budgetResult = &budget.Result{}
		slog.Info("budgets disabled")
	}
	app.budgets = budgetResult
	closers = append(closers, app.budgets.Close)

	var rateLimitResult *ratelimit.Result
	if appCfg.RateLimits.Enabled {
		if sharedStorage != nil {
			rateLimitResult, err = ratelimit.NewWithSharedStorage(ctx, appCfg, sharedStorage)
		} else {
			rateLimitResult, err = ratelimit.New(ctx, appCfg)
		}
		if err != nil {
			return fail("failed to initialize rate limits", err)
		}
		if rateLimitResult.Service.HasTokenRules() && !appCfg.Usage.Enabled {
			slog.Warn("token rate limits configured but usage tracking is disabled; max_tokens limits will not be enforced",
				"usage_enabled", false,
				"hint", "enable usage tracking to enforce token rate limits, or remove max_tokens from rate limit rules",
			)
		}
	} else {
		rateLimitResult = &ratelimit.Result{}
		slog.Info("rate limits disabled")
	}
	app.rateLimits = rateLimitResult
	closers = append(closers, app.rateLimits.Close)

	// Initialize batch lifecycle storage.
	var batchResult *batch.Result
	if sharedStorage != nil {
		batchResult, err = batch.NewWithSharedStorage(ctx, sharedStorage)
	} else {
		batchResult, err = batch.New(ctx, appCfg)
	}
	if err != nil {
		return fail("failed to initialize batch storage", err)
	}
	app.batch = batchResult
	closers = append(closers, app.batch.Close)
	claimSharedStorage(batchResult.Storage)

	// Initialize file provider mapping storage for OpenAI-compatible Files/Batches workflows.
	var fileStoreResult *filestore.Result
	if sharedStorage != nil {
		fileStoreResult, err = filestore.NewWithSharedStorage(ctx, sharedStorage)
	} else {
		fileStoreResult, err = filestore.New(ctx, appCfg)
	}
	if err != nil {
		return fail("failed to initialize file mapping storage", err)
	}
	app.fileStore = fileStoreResult
	closers = append(closers, app.fileStore.Close)
	claimSharedStorage(fileStoreResult.Storage)

	// Initialize Responses/Conversations lifecycle persistence so agentic
	// response chains and conversation history land in storage instead of
	// accumulating in process memory.
	var responseStoreResult *responsestore.Result
	if sharedStorage != nil {
		responseStoreResult, err = responsestore.NewWithSharedStorage(ctx, sharedStorage)
	} else {
		responseStoreResult, err = responsestore.New(ctx, appCfg)
	}
	if err != nil {
		return fail("failed to initialize response snapshot storage", err)
	}
	app.responseStore = responseStoreResult
	closers = append(closers, app.responseStore.Close)
	claimSharedStorage(responseStoreResult.Storage)

	var conversationStoreResult *conversationstore.Result
	if sharedStorage != nil {
		conversationStoreResult, err = conversationstore.NewWithSharedStorage(ctx, sharedStorage)
	} else {
		conversationStoreResult, err = conversationstore.New(ctx, appCfg)
	}
	if err != nil {
		return fail("failed to initialize conversation storage", err)
	}
	app.conversations = conversationStoreResult
	closers = append(closers, app.conversations.Close)
	claimSharedStorage(conversationStoreResult.Storage)

	// Initialize virtual models (unified aliases + access overrides) using
	// shared storage when already available. Provider names declared in YAML —
	// including entries whose credentials did not resolve, which never register —
	// let validation tell a misspelled target provider (abort startup) from a
	// declared-but-inactive one (warn, target stays unavailable).
	declaredProviders := make([]string, 0, len(cfg.AppConfig.RawProviders))
	for name := range cfg.AppConfig.RawProviders {
		declaredProviders = append(declaredProviders, name)
	}
	var virtualModelsResult *virtualmodels.Result
	if sharedStorage != nil {
		virtualModelsResult, err = virtualmodels.NewWithSharedStorage(ctx, appCfg, sharedStorage, providerResult.Registry, declaredProviders)
	} else {
		virtualModelsResult, err = virtualmodels.New(ctx, appCfg, providerResult.Registry, declaredProviders)
	}
	if err != nil {
		return fail("failed to initialize virtual models", err)
	}
	app.virtualModels = virtualModelsResult
	closers = append(closers, app.virtualModels.Close)
	claimSharedStorage(virtualModelsResult.Storage)

	// The unified virtual models service is the single engine: it serves model
	// resolution (redirects), access authorization (policies), and exposed-model
	// listing.
	vm := app.virtualModels.Service

	// Load balancing prefers targets with live rate-limit capacity and falls
	// back to the first declared target when every target is saturated, so
	// the request reaches admission and receives an honest 429 (or defers to
	// failover) instead of the all-targets-down error. Capacity deliberately
	// steers target choice only: a saturated target stays in the catalog,
	// listed and valid.
	if rateLimitResult.Service != nil {
		registry := providerResult.Registry
		limiter := rateLimitResult.Service
		vm.SetTargetCapacity(func(qualifiedModel string) bool {
			return limiter.RouteAvailable(registry.GetProviderName(qualifiedModel), qualifiedModel)
		})
	}

	var failoverResult *failover.Result
	if sharedStorage != nil {
		failoverResult, err = failover.NewWithSharedStorage(ctx, appCfg, sharedStorage)
	} else {
		failoverResult, err = failover.New(ctx, appCfg)
	}
	if err != nil {
		return fail("failed to initialize failover rules", err)
	}
	app.failover = failoverResult
	closers = append(closers, app.failover.Close)
	claimSharedStorage(failoverResult.Storage)

	var taggingResult *tagging.Result
	if sharedStorage != nil {
		taggingResult, err = tagging.NewWithSharedStorage(ctx, appCfg, sharedStorage)
	} else {
		taggingResult, err = tagging.New(ctx, appCfg)
	}
	if err != nil {
		return fail("failed to initialize tagging", err)
	}
	app.tagging = taggingResult
	closers = append(closers, app.tagging.Close)
	claimSharedStorage(taggingResult.Storage)

	var pricingOverrideResult *pricingoverrides.Result
	if sharedStorage != nil {
		pricingOverrideResult, err = pricingoverrides.NewWithSharedStorage(ctx, appCfg, sharedStorage, providerResult.Registry, providerResult.Registry)
	} else {
		pricingOverrideResult, err = pricingoverrides.New(ctx, appCfg, providerResult.Registry, providerResult.Registry)
	}
	if err != nil {
		return fail("failed to initialize model pricing overrides", err)
	}
	app.pricingOverrides = pricingOverrideResult
	closers = append(closers, app.pricingOverrides.Close)
	claimSharedStorage(pricingOverrideResult.Storage)
	pricingResolver := usage.PricingResolver(providerResult.Registry)
	if app.pricingOverrides != nil && app.pricingOverrides.Service != nil {
		pricingResolver = app.pricingOverrides.Service
	}

	refreshInterval := workflowRefreshInterval(appCfg)
	var guardrailExecutor guardrails.ChatCompletionExecutor = app.providers.Router
	if vm != nil {
		guardrailExecutor = virtualmodels.NewChatExecutor(app.providers.Router, vm)
	}

	// Initialize reusable guardrail definitions using shared storage when already available.
	var guardrailResult *guardrails.Result
	if sharedStorage != nil {
		guardrailResult, err = guardrails.NewWithSharedStorage(ctx, sharedStorage, refreshInterval, guardrailExecutor)
	} else {
		guardrailResult, err = guardrails.New(ctx, appCfg, refreshInterval, guardrailExecutor)
	}
	if err != nil {
		return fail("failed to initialize guardrails", err)
	}
	app.guardrails = guardrailResult
	closers = append(closers, app.guardrails.Close)
	claimSharedStorage(guardrailResult.Storage)

	seedGuardrails, err := configGuardrailDefinitions(appCfg.Guardrails)
	if err != nil {
		return fail("failed to prepare guardrail definitions", err)
	}
	if err := guardrailResult.Service.UpsertDefinitions(ctx, seedGuardrails); err != nil {
		return fail("failed to upsert guardrails", err)
	}

	// Build runtime execution dependencies. Policy is passed explicitly into the
	// server; the live provider dependency remains the bare router.
	var provider core.RoutableProvider = app.providers.Router
	var translatedRequestPatcher server.TranslatedRequestPatcher
	var batchRequestPreparers []server.BatchRequestPreparer
	featureCaps := runtimeWorkflowFeatureCaps(appCfg)

	var workflowResult *workflows.Result
	workflowCompiler := workflows.NewCompilerWithFeatureCaps(guardrailResult.Service, featureCaps)
	if sharedStorage != nil {
		workflowResult, err = workflows.NewWithSharedStorage(ctx, sharedStorage, workflowCompiler, refreshInterval)
	} else {
		workflowResult, err = workflows.New(ctx, appCfg, workflowCompiler, refreshInterval)
	}
	if err != nil {
		return fail("failed to initialize workflows", err)
	}
	closers = append(closers, workflowResult.Close)
	claimSharedStorage(workflowResult.Storage)
	defaultWorkflow := defaultWorkflowInput(appCfg, guardrailResult.Service.Names(), seedGuardrails)
	if err := workflowResult.Service.EnsureDefaultGlobal(ctx, defaultWorkflow); err != nil {
		return fail("failed to seed workflows", err)
	}
	if err := workflowResult.Service.Refresh(ctx); err != nil {
		return fail("failed to load workflows", err)
	}
	app.workflows = workflowResult

	var authKeyResult *authkeys.Result
	if sharedStorage != nil {
		authKeyResult, err = authkeys.NewWithSharedStorage(ctx, sharedStorage)
	} else {
		authKeyResult, err = authkeys.New(ctx, appCfg)
	}
	if err != nil {
		return fail("failed to initialize auth keys", err)
	}
	app.authKeys = authKeyResult
	closers = append(closers, app.authKeys.Close)

	// Log configuration status after auth has been initialized so the startup
	// message reflects both bootstrap and managed auth modes.
	app.logStartupInfo()

	if featureCaps.Guardrails {
		if app.guardrails != nil && app.guardrails.Service != nil {
			translatedRequestPatcher = guardrails.NewWorkflowRequestPatcher(workflowResult.Service)
			if appCfg.Guardrails.EnableForBatchProcessing {
				batchRequestPreparers = append(batchRequestPreparers, guardrails.NewWorkflowBatchPreparer(provider, workflowResult.Service))
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
			virtualmodels.NewBatchPreparer(provider, vm),
		}, batchRequestPreparers...)
	}
	batchRequestPreparer := server.ComposeBatchRequestPreparers(providerAsNativeFileRouter(provider), batchRequestPreparers...)

	// Create server
	allowPassthroughV1Alias := appCfg.Server.AllowPassthroughV1Alias
	swaggerEnabled := appCfg.Server.SwaggerEnabled && server.SwaggerAvailable()
	if appCfg.Server.SwaggerEnabled && !server.SwaggerAvailable() {
		slog.Warn("swagger UI requested but not available in this build",
			"recommendation", "rebuild with -tags=swagger")
	}

	// The usage tap feeds recorded token counts into rate limit token windows
	// before delegating to the real logger; it is transparent when no rate
	// limit service exists.
	serverUsageLogger := usage.LoggerInterface(usageResult.Logger)
	if rateLimitResult.Service != nil {
		serverUsageLogger = ratelimit.NewUsageTap(serverUsageLogger, rateLimitResult.Service)
	}

	// Initialize the MCP gateway (aggregated upstream MCP servers behind /mcp).
	var mcpResult *mcpgateway.Result
	if appCfg.MCP.Enabled {
		if sharedStorage != nil {
			mcpResult, err = mcpgateway.NewWithSharedStorage(ctx, appCfg, sharedStorage, nil, serverUsageLogger)
		} else {
			mcpResult, err = mcpgateway.New(ctx, appCfg, nil, serverUsageLogger)
		}
		if err != nil {
			return fail("failed to initialize mcp gateway", err)
		}
		app.mcpGateway = mcpResult
		closers = append(closers, app.mcpGateway.Close)
		claimSharedStorage(mcpResult.Storage)
		slog.Info("mcp gateway enabled",
			"path", config.JoinBasePath(appCfg.Server.BasePath, "/mcp"),
			"configured_servers", len(appCfg.MCP.Servers))
	} else {
		slog.Info("mcp gateway disabled")
	}

	// The self-service GET /v1/usage endpoint and the admin dashboard read
	// usage aggregates through one shared reader. Audit storage is preferred
	// because its schema always includes the usage tables.
	usageReadStorage := auditResult.Storage
	if usageReadStorage == nil {
		usageReadStorage = usageResult.Storage
	}
	var usageReader usage.UsageReader
	if usageReadStorage != nil {
		var readerErr error
		usageReader, readerErr = usage.NewReader(usageReadStorage)
		if readerErr != nil {
			slog.Warn("usage reader unavailable; usage endpoints will omit usage data", "error", readerErr)
			// Explicit reset so a typed-nil reader never reaches the nil checks
			// downstream (same guard as pricingRecalculator).
			usageReader = nil
		}
	}

	serverCfg := &server.Config{
		BasePath:                        appCfg.Server.BasePath,
		MasterKey:                       appCfg.Server.MasterKey,
		Authenticator:                   authKeyResult.Service,
		MetricsEnabled:                  appCfg.Metrics.Enabled,
		MetricsEndpoint:                 appCfg.Metrics.Endpoint,
		BodySizeLimit:                   appCfg.Server.BodySizeLimit,
		PprofEnabled:                    appCfg.Server.PprofEnabled,
		AuditLogger:                     auditResult.Logger,
		UsageLogger:                     serverUsageLogger,
		BudgetChecker:                   budgetResult.Service,
		PricingResolver:                 pricingResolver,
		ModelResolver:                   vm,
		ModelAuthorizer:                 vm,
		FailoverResolver:                failover.NewResolverWithRuleProvider(appCfg.Failover, providerResult.Registry, failoverResult.Service),
		WorkflowPolicyResolver:          workflowResult.Service,
		TranslatedRequestPatcher:        translatedRequestPatcher,
		BatchRequestPreparer:            batchRequestPreparer,
		ExposedModelLister:              vm,
		KeepOnlyAliasesAtModelsEndpoint: appCfg.Models.KeepOnlyAliasesAtModelsEndpoint,
		PassthroughSemanticEnrichers:    cfg.Factory.PassthroughSemanticEnrichers(),
		BatchStore:                      batchResult.Store,
		FileStore:                       fileStoreResult.Store,
		ResponseStore:                   responseStoreResult.Store,
		ConversationStore:               conversationStoreResult.Store,
		LogOnlyModelInteractions:        appCfg.Logging.OnlyModelInteractions,
		DisablePassthroughRoutes:        !appCfg.Server.EnablePassthroughRoutes,
		EnabledPassthroughProviders:     appCfg.Server.EnabledPassthroughProviders,
		RealtimeEnabled:                 appCfg.Server.RealtimeEnabled,
		AllowPassthroughV1Alias:         &allowPassthroughV1Alias,
		UserPathHeader:                  appCfg.Server.UserPathHeader,
		SwaggerEnabled:                  swaggerEnabled,
		Tagging:                         taggingResult.Service,
		MCPEnabled:                      appCfg.MCP.Enabled,
	}
	if mcpResult != nil {
		serverCfg.MCPGateway = mcpResult.Service
	}

	// Assigned conditionally so a disabled feature leaves the interface nil
	// (a typed-nil *ratelimit.Service would defeat the fast nil check).
	if rateLimitResult.Service != nil {
		serverCfg.RateLimiter = rateLimitResult.Service
	}
	if usageReader != nil {
		serverCfg.UsageSummarizer = usageReader
	}

	applyExtensions(serverCfg, cfg.Extensions)

	// Wire the readiness storage probe. Storage is a required dependency, so a
	// failed ping makes /health/ready report not_ready (503). When no storage
	// backend is active, readiness simply collapses to liveness.
	if hc, ok := sharedStorage.(storage.HealthChecker); ok {
		serverCfg.StorageProbe = hc
	}

	// Initialize admin API and dashboard (behind separate feature flags)
	adminCfg := appCfg.Admin
	if !adminCfg.EndpointsEnabled && adminCfg.UIEnabled {
		slog.Warn("ADMIN_UI_ENABLED=true requires ADMIN_ENDPOINTS_ENABLED=true — forcing UI to disabled")
		adminCfg.UIEnabled = false
	}
	livePublishersEnabled := false
	usageEnabledForDashboard := usageResult.Logger.Config().Enabled
	if adminCfg.EndpointsEnabled {
		adminHandler, dashHandler, adminErr := initAdmin(
			usageReader,
			usageReadStorage,
			auditResult.Storage,
			providerResult.Registry,
			providerResult.ConfiguredProviders,
			authKeyResult.Service,
			vm,
			failoverResult.Service,
			app.pricingOverrides.Service,
			workflowResult.Service,
			app.guardrails.Service,
			budgetResult.Service,
			rateLimitResult.Service,
			taggingResult.Service,
			mcpResult,
			app,
			dashboardRuntimeConfig(appCfg, usageEnabledForDashboard),
			app.live,
			requestHealth,
			usagePricingRecalculationConfigured(appCfg),
			appCfg.Server.BasePath,
			adminCfg.UIEnabled,
		)
		if adminErr != nil {
			slog.Warn("failed to initialize admin", "error", adminErr)
		} else {
			serverCfg.AdminEndpointsEnabled = true
			serverCfg.AdminHandler = adminHandler
			livePublishersEnabled = true
			slog.Info("admin API enabled",
				"api", config.JoinBasePath(appCfg.Server.BasePath, "/admin"),
				"legacy_alias", config.JoinBasePath(appCfg.Server.BasePath, "/admin/api/v1"),
				"legacy_sunset", "2026-08-09")
			if adminCfg.UIEnabled {
				serverCfg.AdminUIEnabled = true
				serverCfg.DashboardHandler = dashHandler
				slog.Info("admin UI enabled", "url", fmt.Sprintf("http://localhost:%s%s", appCfg.Server.Port, config.JoinBasePath(appCfg.Server.BasePath, "/admin/dashboard")))
			}
		}
	} else {
		slog.Info("admin API disabled")
	}

	if swaggerEnabled {
		slog.Info("swagger UI enabled", "path", config.JoinBasePath(appCfg.Server.BasePath, "/swagger/index.html"))
	}
	if appCfg.Server.PprofEnabled {
		slog.Info("pprof enabled", "path", config.JoinBasePath(appCfg.Server.BasePath, "/debug/pprof/"))
	}
	if appCfg.Server.EnablePassthroughRoutes {
		slog.Info("provider passthrough enabled", "path", config.JoinBasePath(appCfg.Server.BasePath, "/p/{provider}/{endpoint}"))
	} else {
		slog.Info("provider passthrough disabled")
	}

	rcm, err := responsecache.NewResponseCacheMiddleware(appCfg.Cache.Response, providerResult.CredentialResolvedProviders, usageResult.Logger, pricingResolver)
	if err != nil {
		return fail("failed to initialize response cache", err)
	}
	closers = append(closers, rcm.Close)
	serverCfg.ResponseCacheMiddleware = rcm

	// Wire the readiness cache probe only when a Redis-backed exact cache is
	// configured. The cache is a performance optimization, so a failed ping
	// reports degraded (200) rather than blocking traffic.
	if rcm.UsesRedis() {
		serverCfg.CacheProbe = rcm
	}

	internalGuardrailExecutor := server.NewInternalChatCompletionExecutor(provider, server.InternalChatCompletionExecutorConfig{
		ModelResolver:          vm,
		ModelAuthorizer:        vm,
		WorkflowPolicyResolver: workflowResult.Service,
		FailoverResolver:       serverCfg.FailoverResolver,
		AuditLogger:            auditResult.Logger,
		// The tapped logger, so guardrail LLM calls count toward the
		// request's rate limit token windows like any other completion.
		UsageLogger:     serverUsageLogger,
		PricingResolver: pricingResolver,
		ResponseCache:   rcm,
	})
	if err := guardrailResult.Service.SetExecutor(ctx, internalGuardrailExecutor); err != nil {
		return fail("failed to wire internal guardrail executor", err)
	}
	if err := workflowResult.Service.Refresh(ctx); err != nil {
		return fail("failed to refresh workflows after wiring internal guardrail executor", err)
	}

	if livePublishersEnabled {
		app.attachLivePublishers()
	}
	app.server = server.New(provider, serverCfg)

	return app, nil
}

// Router returns the core.RoutableProvider for request routing.
func (a *App) Router() core.RoutableProvider {
	if a.providers == nil {
		return nil
	}
	return a.providers.Router
}

// AuditLogger returns the audit logger interface.
func (a *App) AuditLogger() auditlog.LoggerInterface {
	if a.audit == nil {
		return nil
	}
	return a.audit.Logger
}

// UsageLogger returns the usage logger interface.
func (a *App) UsageLogger() usage.LoggerInterface {
	if a.usage == nil {
		return nil
	}
	return a.usage.Logger
}

func (a *App) attachLivePublishers() {
	if a == nil || a.live == nil || !a.live.Enabled() {
		return
	}
	if a.audit != nil {
		if logger, ok := a.audit.Logger.(interface {
			SetLivePublisher(auditlog.LiveEventPublisher)
		}); ok {
			logger.SetLivePublisher(a.live)
		}
	}
	if a.usage != nil {
		if logger, ok := a.usage.Logger.(interface {
			SetLivePublisher(usage.LiveEventPublisher)
		}); ok {
			logger.SetLivePublisher(a.live)
		}
	}
}

func providerAsNativeFileRouter(provider core.RoutableProvider) core.NativeFileRoutableProvider {
	if fileRouter, ok := provider.(core.NativeFileRoutableProvider); ok {
		return fileRouter
	}
	return nil
}

// Start starts the HTTP server on the given address.
// This is a blocking call that returns when the server stops.
func (a *App) Start(ctx context.Context, addr string) error {
	return a.startServer(ctx, addr, func(serverCtx context.Context) error {
		return a.server.Start(serverCtx, addr)
	})
}

// StartWithListener starts the HTTP server on a pre-bound listener.
// This is primarily useful for tests that need to reserve a loopback port
// before handing control to the server.
func (a *App) StartWithListener(ctx context.Context, listener net.Listener) error {
	if listener == nil {
		return fmt.Errorf("listener is required")
	}
	return a.startServer(ctx, listener.Addr().String(), func(serverCtx context.Context) error {
		return a.server.StartWithListener(serverCtx, listener)
	})
}

func (a *App) startServer(ctx context.Context, address string, start func(context.Context) error) error {
	if a.server == nil {
		return fmt.Errorf("server is not initialized")
	}

	a.serverMu.Lock()
	if a.serverDone != nil {
		a.serverMu.Unlock()
		return fmt.Errorf("server is already running")
	}
	serverCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	a.serverStop = cancel
	a.serverDone = done
	a.serverMu.Unlock()

	slog.Info("starting server", "address", address)
	err := start(serverCtx)

	a.serverMu.Lock()
	if a.serverDone == done {
		done <- err
		close(done)
		a.serverDone = nil
		a.serverStop = nil
	}
	a.serverMu.Unlock()

	if err != nil {
		if errors.Is(err, http.ErrServerClosed) {
			slog.Info("server stopped gracefully")
			return nil
		}
		return fmt.Errorf("server failed to start: %w", err)
	}
	return nil
}

// Shutdown gracefully tears down app components in dependency order.
// Order:
// 1. Close long-lived streams, cancel the HTTP server context, and wait for it to stop.
// 2. Provider subsystem close (stops model refresh loop and cache resources).
// 3. Batch store close.
// 4. Usage logger close (flushes pending usage records).
// 5. Audit logger close (flushes pending audit records).
//
// Shutdown is idempotent and safe for repeated calls; after the first call, subsequent calls are no-ops.
// It attempts every close step, aggregates failures, and returns a joined error if any step fails.
func (a *App) Shutdown(ctx context.Context) error {
	a.shutdownMu.Lock()
	if a.shutdown {
		a.shutdownMu.Unlock()
		return nil
	}
	a.shutdown = true
	a.shutdownMu.Unlock()

	slog.Info("shutting down application...")

	var errs []error

	// 1. End long-lived streams before asking the HTTP server to drain. MCP
	// Streamable HTTP clients intentionally keep a GET request open; leaving it
	// alive here makes Echo wait until its graceful-shutdown timeout.
	if a.mcpGateway != nil && a.mcpGateway.Service != nil {
		a.mcpGateway.Service.Close()
	}
	if a.live != nil {
		a.live.Close()
	}

	// Stop accepting new requests and wait for in-flight requests to finish.
	a.serverMu.Lock()
	serverStop := a.serverStop
	serverDone := a.serverDone
	a.serverMu.Unlock()
	if serverStop != nil {
		serverStop()
	}
	if serverDone != nil {
		select {
		case err := <-serverDone:
			a.serverMu.Lock()
			a.serverDone = nil
			a.serverStop = nil
			a.serverMu.Unlock()
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				slog.Error("server shutdown error", "error", err)
				errs = append(errs, fmt.Errorf("server shutdown: %w", err))
			}
		case <-ctx.Done():
			slog.Error("server shutdown timed out", "error", ctx.Err())
			errs = append(errs, fmt.Errorf("server shutdown: %w", ctx.Err()))
		}
	}

	// 2. Release server-owned resources now that no requests are in flight
	// (drains response cache writes, closes response/conversation stores).
	if a.server != nil {
		if err := a.server.Shutdown(ctx); err != nil {
			slog.Error("server resources close error", "error", err)
			errs = append(errs, fmt.Errorf("server resources close: %w", err))
		}
	}

	// 3. Close providers (stops model refresh and provider-owned resources)
	if a.providers != nil {
		if err := a.providers.Close(); err != nil {
			slog.Error("providers close error", "error", err)
			errs = append(errs, fmt.Errorf("providers close: %w", err))
		}
	}

	// 3b. Close the MCP gateway (terminates upstream MCP sessions).
	if a.mcpGateway != nil {
		if err := a.mcpGateway.Close(); err != nil {
			slog.Error("mcp gateway close error", "error", err)
			errs = append(errs, fmt.Errorf("mcp gateway close: %w", err))
		}
	}

	// 4. Close virtual models subsystem (aliases + access overrides).
	if a.virtualModels != nil {
		if err := a.virtualModels.Close(); err != nil {
			slog.Error("virtual models close error", "error", err)
			errs = append(errs, fmt.Errorf("virtual models close: %w", err))
		}
	}

	// 5. Close failover rules subsystem.
	if a.failover != nil {
		if err := a.failover.Close(); err != nil {
			slog.Error("failover rules close error", "error", err)
			errs = append(errs, fmt.Errorf("failover close: %w", err))
		}
	}

	// 6. Close tagging subsystem.
	if a.tagging != nil {
		if err := a.tagging.Close(); err != nil {
			slog.Error("tagging close error", "error", err)
			errs = append(errs, fmt.Errorf("tagging close: %w", err))
		}
	}

	// 7. Close workflows subsystem.
	if a.workflows != nil {
		if err := a.workflows.Close(); err != nil {
			slog.Error("workflows close error", "error", err)
			errs = append(errs, fmt.Errorf("workflows close: %w", err))
		}
	}

	// 8. Close model pricing overrides subsystem.
	if a.pricingOverrides != nil {
		if err := a.pricingOverrides.Close(); err != nil {
			slog.Error("model pricing overrides close error", "error", err)
			errs = append(errs, fmt.Errorf("model pricing overrides close: %w", err))
		}
	}

	// 9. Close reusable guardrails subsystem.
	if a.guardrails != nil {
		if err := a.guardrails.Close(); err != nil {
			slog.Error("guardrails close error", "error", err)
			errs = append(errs, fmt.Errorf("guardrails close: %w", err))
		}
	}

	// 10. Close managed auth keys subsystem.
	if a.authKeys != nil {
		if err := a.authKeys.Close(); err != nil {
			slog.Error("auth keys close error", "error", err)
			errs = append(errs, fmt.Errorf("auth keys close: %w", err))
		}
	}

	// 11. Close file mapping store.
	if a.fileStore != nil {
		if err := a.fileStore.Close(); err != nil {
			slog.Error("file mapping store close error", "error", err)
			errs = append(errs, fmt.Errorf("file store close: %w", err))
		}
	}

	// 12. Close batch store (flushes pending entries)
	if a.batch != nil {
		if err := a.batch.Close(); err != nil {
			slog.Error("batch store close error", "error", err)
			errs = append(errs, fmt.Errorf("batch close: %w", err))
		}
	}

	// 13. Close budget subsystem.
	if a.budgets != nil {
		if err := a.budgets.Close(); err != nil {
			slog.Error("budgets close error", "error", err)
			errs = append(errs, fmt.Errorf("budgets close: %w", err))
		}
	}

	// 13b. Close rate limit subsystem.
	if a.rateLimits != nil {
		if err := a.rateLimits.Close(); err != nil {
			slog.Error("rate limits close error", "error", err)
			errs = append(errs, fmt.Errorf("rate limits close: %w", err))
		}
	}

	// 14. Close usage tracking (flushes pending entries)
	if a.usage != nil {
		if err := a.usage.Close(); err != nil {
			slog.Error("usage logger close error", "error", err)
			errs = append(errs, fmt.Errorf("usage close: %w", err))
		}
	}

	// 15. Close audit logging (flushes pending logs)
	if a.audit != nil {
		if err := a.audit.Close(); err != nil {
			slog.Error("audit logger close error", "error", err)
			errs = append(errs, fmt.Errorf("audit close: %w", err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("shutdown errors: %w", errors.Join(errs...))
	}

	slog.Info("application shutdown complete")
	return nil
}

// logStartupInfo logs the application configuration on startup.
func (a *App) logStartupInfo() {
	cfg := a.config

	// Security warnings
	managedKeysConfigured := a.authKeys != nil && a.authKeys.Service != nil && a.authKeys.Service.Enabled()
	switch {
	case cfg.Server.MasterKey != "" && managedKeysConfigured:
		slog.Info("authentication enabled", "mode", "master_key+managed_keys", "managed_key_total", a.authKeys.Service.Total(), "managed_key_active", a.authKeys.Service.ActiveCount())
	case managedKeysConfigured:
		slog.Info("authentication enabled", "mode", "managed_keys", "managed_key_total", a.authKeys.Service.Total(), "managed_key_active", a.authKeys.Service.ActiveCount())
	case cfg.Server.MasterKey == "":
		slog.Warn("SECURITY WARNING: GOMODEL_MASTER_KEY not set - server running in UNSAFE MODE",
			"security_risk", "unauthenticated access allowed",
			"recommendation", "set GOMODEL_MASTER_KEY environment variable to secure this gateway")
	default:
		slog.Info("authentication enabled", "mode", "master_key")
	}

	// Metrics configuration
	if cfg.Metrics.Enabled {
		slog.Info("prometheus metrics enabled", "endpoint", cfg.Metrics.Endpoint)
	} else {
		slog.Info("prometheus metrics disabled")
	}

	// Storage configuration (shared by audit logging and usage tracking)
	if backend := cfg.Storage.BackendConfig(); backend.Type == storage.TypeSQLite {
		slog.Info("storage configured", "type", backend.Type, "path", backend.SQLite.Path)
	} else {
		slog.Info("storage configured", "type", backend.Type)
	}

	// Audit logging configuration
	if cfg.Logging.Enabled {
		slog.Info("audit logging enabled",
			"log_bodies", cfg.Logging.LogBodies,
			"log_audio_bodies", cfg.Logging.LogAudioBodies,
			"log_headers", cfg.Logging.LogHeaders,
			"retention_days", cfg.Logging.RetentionDays,
		)
	} else {
		slog.Info("audit logging disabled")
	}

	// Usage tracking configuration
	if cfg.Usage.Enabled {
		slog.Info("usage tracking enabled",
			"buffer_size", cfg.Usage.BufferSize,
			"flush_interval", cfg.Usage.FlushInterval,
			"retention_days", cfg.Usage.RetentionDays,
		)
	} else {
		slog.Info("usage tracking disabled")
	}

}

// initAdmin creates the admin API handler and optionally the dashboard handler.
// Returns nil dashboard handler if uiEnabled is false.
func initAdmin(
	reader usage.UsageReader,
	usageReadStorage storage.Storage,
	auditStorage storage.Storage,
	registry *providers.ModelRegistry,
	configuredProviders []providers.SanitizedProviderConfig,
	authKeyService *authkeys.Service,
	virtualModelService *virtualmodels.Service,
	failoverService *failover.Service,
	pricingOverrideService *pricingoverrides.Service,
	workflowService *workflows.Service,
	guardrailService *guardrails.Service,
	budgetService *budget.Service,
	rateLimitService *ratelimit.Service,
	taggingService *tagging.Service,
	mcpResult *mcpgateway.Result,
	runtimeRefresher admin.RuntimeRefresher,
	runtimeConfig admin.DashboardConfigResponse,
	liveBroker *live.Broker,
	requestHealth admin.RequestHealthSource,
	usagePricingRecalculationEnabled bool,
	basePath string,
	uiEnabled bool,
) (*admin.Handler, *dashboard.Handler, error) {
	// Pricing recalculation writes through the same storage the reader uses.
	var pricingRecalculator usage.PricingRecalculator
	if usageReadStorage != nil && usagePricingRecalculationEnabled {
		var err error
		pricingRecalculator, err = usage.NewPricingRecalculator(usageReadStorage)
		if err != nil {
			slog.Warn("usage pricing recalculation unavailable", "error", err)
			pricingRecalculator = nil
		}
	}
	runtimeConfig.PricingRecalculation = dashboardEnabledValue(usagePricingRecalculationEnabled && pricingRecalculator != nil)

	// Create audit reader (only from audit storage, because the usage-only storage
	// schema may not include the audit_logs table/collection).
	var auditReader auditlog.Reader
	if auditStorage != nil {
		var err error
		auditReader, err = auditlog.NewReader(auditStorage)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to create audit reader: %w", err)
		}
	}

	// Assigned conditionally so a disabled MCP gateway leaves the option nil
	// (a typed-nil *mcpgateway.Service stored in the interface field would
	// defeat the handlers' feature-unavailable check).
	var mcpOption admin.Option
	if mcpResult != nil && mcpResult.Service != nil {
		mcpOption = admin.WithMCPServers(mcpResult.Service)
	}

	adminHandler := admin.NewHandler(
		reader,
		registry,
		admin.WithConfiguredProviders(configuredProviders),
		admin.WithUsagePricingRecalculator(pricingRecalculator),
		admin.WithPricingResolver(pricingOverrideService),
		admin.WithAuditReader(auditReader),
		admin.WithAuthKeys(authKeyService),
		admin.WithVirtualModels(virtualModelService),
		admin.WithFailover(failoverService),
		admin.WithPricingOverrides(pricingOverrideService),
		admin.WithWorkflows(workflowService),
		admin.WithGuardrailService(guardrailService),
		admin.WithBudgets(budgetService),
		admin.WithRateLimits(rateLimitService),
		admin.WithTagging(taggingService),
		mcpOption,
		admin.WithRuntimeRefresher(runtimeRefresher),
		admin.WithDashboardRuntimeConfig(runtimeConfig),
		admin.WithLiveBroker(liveBroker),
		admin.WithRequestHealth(requestHealth),
	)

	var dashHandler *dashboard.Handler
	if uiEnabled {
		var err error
		dashHandler, err = dashboard.NewWithBasePath(basePath)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to initialize dashboard: %w", err)
		}
	}

	return adminHandler, dashHandler, nil
}

func configGuardrailDefinitions(cfg config.GuardrailsConfig) ([]guardrails.Definition, error) {
	if !cfg.Enabled {
		return nil, nil
	}

	definitions := make([]guardrails.Definition, 0, len(cfg.Rules))
	for i, rule := range cfg.Rules {
		name := strings.TrimSpace(rule.Name)
		ruleType := strings.ToLower(strings.TrimSpace(rule.Type))
		switch ruleType {
		case "llm-based-altering":
			ruleType = "llm_based_altering"
		}
		if name == "" {
			return nil, fmt.Errorf("guardrail rule #%d: name is required", i)
		}
		if ruleType == "" {
			return nil, fmt.Errorf("guardrail rule #%d (%q): type is required", i, name)
		}

		var rawConfig []byte
		var err error
		switch ruleType {
		case "system_prompt":
			rawConfig, err = json.Marshal(map[string]any{
				"mode":    rule.SystemPrompt.Mode,
				"content": rule.SystemPrompt.Content,
			})
		case "llm_based_altering":
			rawConfig, err = json.Marshal(map[string]any{
				"model":               rule.LLMBasedAltering.Model,
				"provider":            rule.LLMBasedAltering.Provider,
				"prompt":              rule.LLMBasedAltering.Prompt,
				"roles":               rule.LLMBasedAltering.Roles,
				"skip_content_prefix": rule.LLMBasedAltering.SkipContentPrefix,
				"max_tokens":          rule.LLMBasedAltering.MaxTokens,
			})
		default:
			return nil, fmt.Errorf("guardrail rule #%d (%q): unsupported type %q", i, name, ruleType)
		}
		if err != nil {
			return nil, fmt.Errorf("guardrail rule #%d (%q): marshal config: %w", i, name, err)
		}
		definitions = append(definitions, guardrails.Definition{
			Name:     name,
			Type:     ruleType,
			UserPath: strings.TrimSpace(rule.UserPath),
			Config:   rawConfig,
		})
	}
	return definitions, nil
}

func defaultWorkflowInput(cfg *config.Config, availableGuardrails []string, configuredGuardrails []guardrails.Definition) workflows.CreateInput {
	failoverEnabled := failoverFeatureEnabledGlobally(cfg)
	budgetEnabled := cfg.Budgets.Enabled
	payload := workflows.Payload{
		SchemaVersion: 1,
		Features: workflows.FeatureFlags{
			Cache:    responseCacheConfigured(cfg.Cache.Response),
			Audit:    cfg.Logging.Enabled,
			Usage:    cfg.Usage.Enabled,
			Budget:   &budgetEnabled,
			Failover: &failoverEnabled,
		},
	}
	available := make(map[string]struct{}, len(availableGuardrails))
	for _, name := range availableGuardrails {
		available[strings.TrimSpace(name)] = struct{}{}
	}
	for _, definition := range configuredGuardrails {
		name := strings.TrimSpace(definition.Name)
		if name == "" {
			continue
		}
		available[name] = struct{}{}
	}
	if cfg.Guardrails.Enabled && len(cfg.Guardrails.Rules) > 0 {
		payload.Guardrails = make([]workflows.GuardrailStep, 0, len(cfg.Guardrails.Rules))
		for _, rule := range cfg.Guardrails.Rules {
			name := strings.TrimSpace(rule.Name)
			if name == "" {
				continue
			}
			if len(available) > 0 {
				if _, ok := available[name]; !ok {
					continue
				}
			}
			payload.Guardrails = append(payload.Guardrails, workflows.GuardrailStep{
				Ref:  name,
				Step: rule.Order,
			})
		}
	}
	payload.Features.Guardrails = len(payload.Guardrails) > 0

	return workflows.CreateInput{
		Scope:       workflows.Scope{},
		Activate:    true,
		Name:        workflows.ManagedDefaultGlobalName,
		Description: workflows.ManagedDefaultGlobalDescription,
		Payload:     payload,
	}
}

func dashboardRuntimeConfig(cfg *config.Config, usageEnabled bool) admin.DashboardConfigResponse {
	return admin.DashboardConfigResponse{
		FailoverEnabled:      dashboardEnabledValue(failoverFeatureEnabledGlobally(cfg)),
		LoggingEnabled:       dashboardEnabledValue(cfg != nil && cfg.Logging.Enabled),
		UsageEnabled:         dashboardEnabledValue(cfg != nil && cfg.Usage.Enabled),
		BudgetsEnabled:       dashboardEnabledValue(cfg != nil && cfg.Budgets.Enabled),
		RateLimitsEnabled:    dashboardEnabledValue(cfg != nil && cfg.RateLimits.Enabled),
		GuardrailsEnabled:    dashboardEnabledValue(cfg != nil && cfg.Guardrails.Enabled),
		CacheEnabled:         dashboardEnabledValue(cacheAnalyticsConfigured(cfg, usageEnabled)),
		RedisURL:             dashboardEnabledValue(simpleResponseCacheConfigured(cfg)),
		SemanticCacheEnabled: dashboardEnabledValue(semanticResponseCacheConfigured(cfg)),
		LiveLogsEnabled:      dashboardEnabledValue(cfg != nil && cfg.Admin.LiveLogsEnabled),
	}
}

func usagePricingRecalculationConfigured(cfg *config.Config) bool {
	return cfg != nil && cfg.Usage.Enabled && cfg.Usage.PricingRecalculationEnabled
}

func cacheAnalyticsConfigured(cfg *config.Config, usageEnabled bool) bool {
	return cfg != nil && usageEnabled && responseCacheConfigured(cfg.Cache.Response)
}

func dashboardEnabledValue(enabled bool) string {
	if enabled {
		return "on"
	}
	return "off"
}

func runtimeWorkflowFeatureCaps(cfg *config.Config) core.WorkflowFeatures {
	if cfg == nil {
		return core.WorkflowFeatures{}
	}
	return core.WorkflowFeatures{
		Cache:      responseCacheConfigured(cfg.Cache.Response),
		Audit:      cfg.Logging.Enabled,
		Usage:      cfg.Usage.Enabled,
		Budget:     cfg.Budgets.Enabled,
		Guardrails: cfg.Guardrails.Enabled,
		Failover:   failoverFeatureEnabledGlobally(cfg),
	}
}

func workflowRefreshInterval(cfg *config.Config) time.Duration {
	if cfg == nil || cfg.Workflows.RefreshInterval <= 0 {
		return time.Minute
	}
	return cfg.Workflows.RefreshInterval
}

func responseCacheConfigured(cfg config.ResponseCacheConfig) bool {
	return simpleResponseCacheConfiguredFromResponse(cfg) || semanticResponseCacheConfiguredFromResponse(cfg)
}

func simpleResponseCacheConfigured(cfg *config.Config) bool {
	if cfg == nil {
		return false
	}
	return simpleResponseCacheConfiguredFromResponse(cfg.Cache.Response)
}

func simpleResponseCacheConfiguredFromResponse(cfg config.ResponseCacheConfig) bool {
	return cfg.Simple != nil && config.SimpleCacheEnabled(cfg.Simple) &&
		cfg.Simple.Redis != nil && strings.TrimSpace(cfg.Simple.Redis.URL) != ""
}

func semanticResponseCacheConfigured(cfg *config.Config) bool {
	if cfg == nil {
		return false
	}
	return semanticResponseCacheConfiguredFromResponse(cfg.Cache.Response)
}

func semanticResponseCacheConfiguredFromResponse(cfg config.ResponseCacheConfig) bool {
	return cfg.Semantic != nil && config.SemanticCacheActive(cfg.Semantic)
}

func failoverFeatureEnabledGlobally(cfg *config.Config) bool {
	return cfg != nil && cfg.Failover.Enabled
}
