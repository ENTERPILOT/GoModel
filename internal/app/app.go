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
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/goccy/go-json"

	"github.com/labstack/echo/v5"

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
	"github.com/enterpilot/gomodel/internal/filestore"
	"github.com/enterpilot/gomodel/internal/gateway"
	"github.com/enterpilot/gomodel/internal/guardrails"
	"github.com/enterpilot/gomodel/internal/httpclient"
	"github.com/enterpilot/gomodel/internal/live"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/mcpgateway"
	"github.com/enterpilot/gomodel/internal/pricingoverrides"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/health"
	"github.com/enterpilot/gomodel/internal/ratelimit"
	"github.com/enterpilot/gomodel/internal/responsecache"
	"github.com/enterpilot/gomodel/internal/responsestore"
	"github.com/enterpilot/gomodel/internal/runtimesettings"
	"github.com/enterpilot/gomodel/internal/server"
	"github.com/enterpilot/gomodel/internal/session"
	"github.com/enterpilot/gomodel/internal/storage"
	"github.com/enterpilot/gomodel/internal/tagging"
	"github.com/enterpilot/gomodel/internal/telemetry"
	"github.com/enterpilot/gomodel/internal/usage"
	"github.com/enterpilot/gomodel/internal/users"
	"github.com/enterpilot/gomodel/internal/versioncheck"
	"github.com/enterpilot/gomodel/internal/virtualmodels"
	"github.com/enterpilot/gomodel/internal/workflows"
)

// App represents the main application with all its dependencies.
// It provides centralized lifecycle management for all components.
type App struct {
	config              *config.Config
	telemetry           *telemetry.Service // nil unless OpenTelemetry export is enabled
	providers           *providers.InitResult
	audit               *auditlog.Result
	usage               *usage.Result
	budgets             *budget.Result
	rateLimits          *ratelimit.Result
	batch               *batch.Result
	fileStore           *filestore.Result
	responseStore       *responsestore.Result
	conversations       *conversationstore.Result
	virtualModels       *virtualmodels.Result
	tagging             *tagging.Result
	mcpGateway          *mcpgateway.Result
	providerCredentials *providers.CredentialsResult
	pricingOverrides    *pricingoverrides.Result
	authKeys            *authkeys.Result
	users               *users.Result
	guardrails          *guardrails.Result
	workflows           *workflows.Result
	live                *live.Broker
	server              *server.Server
	storage             storage.Storage
	runtimeSettings     *runtimesettings.Service
	versionCheck        *versioncheck.Checker
	extensionAuth       bool

	// registered records every successfully initialized subsystem in
	// construction order, together with the teardown path that owns it. It is
	// the single source of truth for what must be closed: startup failure
	// unwinds it in reverse, and shutdownOrder is checked against it.
	registered []registeredSubsystem

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

	// DemoMode exposes a prominent dashboard warning for public demo instances.
	// It does not change persistence or security behavior.
	DemoMode bool

	// ProductName names the running distribution (for example "gomodel-pro")
	// and becomes the default OpenTelemetry service.name. Empty means
	// "gomodel".
	ProductName string
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

// routeSelectorHooks adapts upstream client lifecycle events into route
// selector observations. Selector callbacks are extension code running on
// the request path, so panics are contained rather than failing the request.
// The selector's name is captured once, panic-safe, and the recovery path
// logs only fixed metadata: it never calls back into extension code
// mid-panic, and never logs the recovered value, which the extension
// controls and could fill with request data.
func routeSelectorHooks(selector ext.RouteSelector) llmclient.Hooks {
	name := selectorLabel(selector)
	observe := func(event string, fn func()) {
		defer func() {
			if recover() != nil {
				slog.Error("route selector panicked during observation",
					"selector", name, "event", event)
			}
		}()
		fn()
	}
	return llmclient.Hooks{
		OnRequestStart: func(ctx context.Context, info llmclient.RequestInfo) context.Context {
			observe("attempt_start", func() {
				selector.OnAttemptStart(ext.RouteTarget{Provider: info.Provider, Model: info.Model})
			})
			return ctx
		},
		OnRequestEnd: func(ctx context.Context, info llmclient.ResponseInfo) {
			observe("attempt_end", func() {
				source, sessionID := routeAffinityContext(ctx)
				selector.OnAttemptEnd(ext.RouteOutcome{
					Provider: info.Provider, Model: info.Model,
					Source:     source,
					SessionID:  sessionID,
					Endpoint:   info.Endpoint,
					StatusCode: info.StatusCode,
					Duration:   info.Duration,
					Stream:     info.Stream,
					Err:        info.Error,
				})
			})
		},
	}
}

func routeAffinityContext(ctx context.Context) (source, sessionID string) {
	sessionID = core.SessionIDFromContext(ctx)
	workflow := core.GetWorkflow(ctx)
	if workflow == nil || workflow.Resolution == nil || !workflow.Resolution.AliasApplied {
		return "", sessionID
	}
	return workflow.Resolution.RequestedQualifiedModel(), sessionID
}

// selectorLabel returns the selector's name for logs, tolerating a panicking
// Name implementation, so recovery paths never re-enter extension code.
func selectorLabel(selector ext.RouteSelector) (name string) {
	if selector == nil {
		return ""
	}
	defer func() {
		if recover() != nil {
			name = "unknown"
		}
	}()
	return selector.Name()
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
	quotaTemplatesEnabled := cfg.Extensions != nil && cfg.Extensions.HasCapability(ext.CapabilityQuotaTemplates)
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
		config:        appCfg,
		extensionAuth: hasUsableRequestAuthenticator(cfg.Extensions),
	}
	app.live = live.NewBroker(live.Config{
		Enabled:     appCfg.Admin.LiveLogsEnabled,
		BufferSize:  appCfg.Admin.LiveLogsBufferSize,
		ReplayLimit: appCfg.Admin.LiveLogsReplayLimit,
		Heartbeat:   time.Duration(appCfg.Admin.LiveLogsHeartbeatSeconds) * time.Second,
	})

	// Every subsystem registers as it initializes (see subsystems.go): fail
	// unwinds the registry in reverse construction order before returning an
	// initialization error, and Shutdown releases the same set in its own
	// hand-maintained runtime order. The live broker is created above, so it
	// is the first entry.
	app.register(subsystemLive, ownedByPrologue, func() error {
		app.live.Close()
		return nil
	})
	fail := func(msg string, cause error) (*App, error) {
		closeErr := app.unwind()
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

	// One storage connection serves every subsystem. Each used to be able to
	// open its own, which meant a deployment with audit logging and usage
	// tracking both disabled opened a separate connection per subsystem to the
	// same database.
	sharedStorage, err := storage.New(ctx, appCfg.Storage.BackendConfig())
	if err != nil {
		return fail("failed to create storage", err)
	}
	app.storage = sharedStorage
	app.register(subsystemStorage, ownedByShutdown, sharedStorage.Close)

	var registeredSettings []ext.RuntimeSetting
	if cfg.Extensions != nil {
		registeredSettings = cfg.Extensions.Settings()
	}
	app.runtimeSettings, err = runtimesettings.New(ctx, sharedStorage, registeredSettings)
	if err != nil {
		return fail("failed to initialize runtime settings", err)
	}
	if app.runtimeSettings != nil {
		app.register(subsystemRuntimeSettings, ownedByShutdown, app.runtimeSettings.Close)
	}

	// Track real-traffic outcomes per provider/model for the dashboard's
	// provider status; hooks must be composed before any provider is created.
	requestHealth := health.NewTracker()
	cfg.Factory.AddHooks(requestHealth.Hooks())

	// An extension route selector observes every upstream attempt — primaries,
	// retries, and failovers — to steer adaptive load balancing. Like the
	// health tracker, its hooks must be attached before any provider exists.
	var routeSelector ext.RouteSelector
	if cfg.Extensions != nil {
		routeSelector = cfg.Extensions.RouteSelector()
	}
	if routeSelector != nil {
		cfg.Factory.AddHooks(routeSelectorHooks(routeSelector))
	}
	// OpenTelemetry instruments provider calls through the same hooks, so it
	// too must exist before the first provider is constructed.
	if appCfg.OpenTelemetry.Enabled {
		metricsEndpoint := config.ResolveMetricsEndpointWithPprof(appCfg.Metrics.Endpoint, appCfg.Server.PprofEnabled)
		app.telemetry, err = telemetry.New(ctx, appCfg.OpenTelemetry, metricsEndpoint, cfg.ProductName)
		if err != nil {
			return fail("failed to initialize opentelemetry", err)
		}
		app.register(subsystemTelemetry, ownedByShutdown, app.telemetry.Close)
		cfg.Factory.AddHooks(app.telemetry.Hooks())
	}

	providerResult, err := providers.Init(ctx, cfg.AppConfig, cfg.Factory)
	if err != nil {
		return fail("failed to initialize providers", err)
	}
	app.providers = providerResult
	app.register(subsystemProviders, ownedByShutdown, app.providers.Close)

	// Initialize audit logging
	auditResult, err := auditlog.New(ctx, appCfg, sharedStorage)
	if err != nil {
		return fail("failed to initialize audit logging", err)
	}
	app.audit = auditResult
	app.register(subsystemAudit, ownedByShutdown, app.audit.Close)

	// Initialize usage tracking. Disabled tracking yields a noop logger.
	usageResult, err := usage.New(ctx, appCfg, sharedStorage)
	if err != nil {
		return fail("failed to initialize usage tracking", err)
	}
	if usageResult == nil || usageResult.Logger == nil {
		if usageResult != nil {
			app.register(subsystemUsage, ownedByShutdown, usageResult.Close)
		}
		return fail("usage tracking initialization returned nil result", nil)
	}
	app.usage = usageResult
	app.register(subsystemUsage, ownedByShutdown, app.usage.Close)

	var budgetResult *budget.Result
	if appCfg.Budgets.Enabled {
		budgetResult, err = budget.New(ctx, appCfg, sharedStorage, quotaTemplatesEnabled)
		if err != nil {
			return fail("failed to initialize budgets", err)
		}
	} else {
		budgetResult = &budget.Result{}
		slog.Info("budgets disabled")
	}
	app.budgets = budgetResult
	app.register(subsystemBudgets, ownedByShutdown, app.budgets.Close)

	var rateLimitResult *ratelimit.Result
	if appCfg.RateLimits.Enabled {
		rateLimitResult, err = ratelimit.New(ctx, appCfg, sharedStorage, quotaTemplatesEnabled)
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
	app.register(subsystemRateLimits, ownedByShutdown, app.rateLimits.Close)

	// Initialize batch lifecycle storage.
	var batchResult *batch.Result
	batchResult, err = batch.New(ctx, sharedStorage)
	if err != nil {
		return fail("failed to initialize batch storage", err)
	}
	app.batch = batchResult
	app.register(subsystemBatch, ownedByShutdown, app.batch.Close)

	// Initialize file provider mapping storage for OpenAI-compatible Files/Batches workflows.
	var fileStoreResult *filestore.Result
	fileStoreResult, err = filestore.New(ctx, sharedStorage)
	if err != nil {
		return fail("failed to initialize file mapping storage", err)
	}
	app.fileStore = fileStoreResult
	app.register(subsystemFileStore, ownedByShutdown, app.fileStore.Close)

	// Initialize Responses/Conversations lifecycle persistence so agentic
	// response chains and conversation history land in storage instead of
	// accumulating in process memory.
	var responseStoreResult *responsestore.Result
	responseStoreResult, err = responsestore.New(ctx, sharedStorage)
	if err != nil {
		return fail("failed to initialize response snapshot storage", err)
	}
	app.responseStore = responseStoreResult
	app.register(subsystemResponseStore, ownedByServer, app.responseStore.Close)

	var conversationStoreResult *conversationstore.Result
	conversationStoreResult, err = conversationstore.New(ctx, sharedStorage)
	if err != nil {
		return fail("failed to initialize conversation storage", err)
	}
	app.conversations = conversationStoreResult
	app.register(subsystemConversationStore, ownedByServer, app.conversations.Close)

	// Initialize virtual models (unified aliases + access overrides) using
	// shared storage when already available. Provider names declared in YAML —
	// including entries whose credentials did not resolve, which never register —
	// let validation tell a misspelled target provider (abort startup) from a
	// declared-but-inactive one (warn, target stays unavailable).
	declaredProviders := make([]string, 0, len(cfg.AppConfig.RawProviders))
	for name := range cfg.AppConfig.RawProviders {
		declaredProviders = append(declaredProviders, name)
	}

	// Provider credentials store: the dashboard alternative to setting
	// provider API keys as env vars. Declared (config.yaml/env) provider
	// names are read-only here; admin-managed rows are hot-registered into
	// the same registry/factory providers.Init already built, so a provider
	// added from the dashboard routes traffic without a restart.
	//
	// The "managed" (read-only) name set must be broader than declaredProviders
	// above: that slice only covers YAML `providers:` keys, but a provider can
	// also be declared purely through env vars with no config.yaml entry at
	// all (e.g. OLLAMA_BASE_URL alone registers "ollama"). Every name
	// providers.Init actually resolved and registered -- from either source --
	// must be read-only here, or the dashboard could unregister and replace a
	// live env-only provider out from under the operator.
	managedProviderNames := make([]string, 0, len(declaredProviders)+len(providerResult.ConfiguredProviders))
	managedProviderNames = append(managedProviderNames, declaredProviders...)
	for _, resolved := range providerResult.ConfiguredProviders {
		managedProviderNames = append(managedProviderNames, resolved.Name)
	}

	var providerCredentialsResult *providers.CredentialsResult
	providerCredentialsResult, err = providers.NewCredentialsStore(ctx, sharedStorage, providerResult.Factory, providerResult.Registry, managedProviderNames, appCfg.Resilience)
	if err != nil {
		return fail("failed to initialize provider credentials store", err)
	}
	app.providerCredentials = providerCredentialsResult
	app.register(subsystemProviderCredentials, ownedByShutdown, app.providerCredentials.Close)

	var virtualModelsResult *virtualmodels.Result
	virtualModelsResult, err = virtualmodels.New(ctx, appCfg, sharedStorage, providerResult.Registry, declaredProviders)
	if err != nil {
		return fail("failed to initialize virtual models", err)
	}
	app.virtualModels = virtualModelsResult
	app.register(subsystemVirtualModels, ownedByShutdown, app.virtualModels.Close)

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

	// Redirects with the adaptive strategy delegate target choice to the
	// extension route selector; without one they fall back to round robin
	// inside the balancer, so the strategy stays valid in plain core builds.
	if routeSelector != nil {
		vm.SetRouteSelector(routeSelector)
	}

	// Subject-side model access: auth key and user-path allowlists narrow what
	// the model-side policy rows expose, so one authorizer answers both.
	var usersResult *users.Result
	usersResult, err = users.New(ctx, appCfg, sharedStorage, providerResult.Registry, managedProviderNames)
	if err != nil {
		return fail("failed to initialize users", err)
	}
	app.users = usersResult
	app.register(subsystemUsers, ownedByShutdown, app.users.Close)
	vm.SetAccessPolicy(usersResult.Service)

	var taggingResult *tagging.Result
	taggingResult, err = tagging.New(ctx, appCfg, sharedStorage)
	if err != nil {
		return fail("failed to initialize tagging", err)
	}
	app.tagging = taggingResult
	app.register(subsystemTagging, ownedByShutdown, app.tagging.Close)

	var pricingOverrideResult *pricingoverrides.Result
	pricingOverrideResult, err = pricingoverrides.New(ctx, appCfg, sharedStorage, providerResult.Registry, providerResult.Registry)
	if err != nil {
		return fail("failed to initialize model pricing overrides", err)
	}
	app.pricingOverrides = pricingOverrideResult
	app.register(subsystemPricingOverrides, ownedByShutdown, app.pricingOverrides.Close)
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
	guardrailResult, err = guardrails.New(ctx, sharedStorage, refreshInterval, guardrailExecutor)
	if err != nil {
		return fail("failed to initialize guardrails", err)
	}
	app.guardrails = guardrailResult
	app.register(subsystemGuardrails, ownedByShutdown, app.guardrails.Close)

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
	workflowResult, err = workflows.New(ctx, sharedStorage, workflowCompiler, refreshInterval)
	if err != nil {
		return fail("failed to initialize workflows", err)
	}
	app.register(subsystemWorkflows, ownedByShutdown, workflowResult.Close)
	defaultWorkflow := defaultWorkflowInput(appCfg, guardrailResult.Service.Names(), seedGuardrails)
	if err := workflowResult.Service.EnsureDefaultGlobal(ctx, defaultWorkflow); err != nil {
		return fail("failed to seed workflows", err)
	}
	if err := workflowResult.Service.Refresh(ctx); err != nil {
		return fail("failed to load workflows", err)
	}
	app.workflows = workflowResult

	var authKeyResult *authkeys.Result
	authKeyResult, err = authkeys.New(ctx, sharedStorage)
	if err != nil {
		return fail("failed to initialize auth keys", err)
	}
	app.authKeys = authKeyResult
	app.register(subsystemAuthKeys, ownedByShutdown, app.authKeys.Close)

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
		mcpResult, err = mcpgateway.New(ctx, appCfg, sharedStorage, nil, serverUsageLogger)
		if err != nil {
			return fail("failed to initialize mcp gateway", err)
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
	usageReadStorage := sharedStorage
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

	// The update check owns the only outbound connection core makes that is
	// not a provider call. It is constructed even when disabled so GET
	// /version keeps reporting the local build.
	versionChecker := newVersionChecker(appCfg.VersionCheck)
	app.versionCheck = versionChecker

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
		FailoverResolver:                failoverResolver(appCfg, vm),
		FailoverPolicy:                  gateway.NewFailoverPolicy(appCfg.Failover),
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
		SessionDetector:                 session.NewDetectorFromConfig(appCfg.Session),
		MCPEnabled:                      appCfg.MCP.Enabled,
		VersionChecker:                  versionChecker,
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
	if app.telemetry != nil {
		// Outermost, so the HTTP server span also covers extension middleware.
		serverCfg.OuterMiddleware = append([]echo.MiddlewareFunc{app.telemetry.Middleware()}, serverCfg.OuterMiddleware...)
	}

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
		adminRuntimeConfig := dashboardRuntimeConfig(appCfg, usageEnabledForDashboard, cfg.DemoMode, routeSelector != nil)
		adminRuntimeConfig.QuotaTemplatesEnabled = dashboardEnabledValue(quotaTemplatesEnabled)
		adminHandler, dashHandler, auditReader, adminErr := initAdmin(
			usageReader,
			usageReadStorage,
			sharedStorage,
			providerResult.Registry,
			providerResult.ConfiguredProviders,
			authKeyResult.Service,
			usersResult.Service,
			vm,
			app.pricingOverrides.Service,
			workflowResult.Service,
			app.guardrails.Service,
			budgetResult.Service,
			rateLimitResult.Service,
			taggingResult.Service,
			app.runtimeSettings,
			mcpResult,
			app.providerCredentials,
			app,
			adminRuntimeConfig,
			quotaTemplatesEnabled,
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
			serverCfg.AuditReader = auditReader
			livePublishersEnabled = true
			slog.Info("admin API enabled",
				"api", config.JoinBasePath(appCfg.Server.BasePath, "/admin"),
				"legacy_alias", config.JoinBasePath(appCfg.Server.BasePath, "/admin/api/v1"),
				"legacy_sunset", "2026-08-09")
			if adminCfg.UIEnabled && dashHandler != nil {
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
	app.register(subsystemResponseCache, ownedByServer, rcm.Close)
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
		FailoverPolicy:         serverCfg.FailoverPolicy,
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

	// Registries are reused across reload generations, so their authenticators
	// are shared too. Rebind only after every fallible construction step has
	// succeeded: otherwise a failed replacement would leave the still-serving
	// generation pointing at the replacement's already-closed audit logger.
	bindAuthenticationEventRecorders(cfg.Extensions, auditlog.NewAuthenticationEventRecorder(auditResult.Logger))

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
	// Cancelled on every exit path, not just Shutdown: the server can also
	// stop by returning an error, and background workers started on this
	// context (the update check) would otherwise outlive it.
	defer cancel()
	done := make(chan error, 1)
	a.serverStop = cancel
	a.serverDone = done
	a.serverMu.Unlock()

	if a.rateLimits != nil && a.rateLimits.Service != nil {
		a.rateLimits.Service.Start(ctx)
	}
	if a.versionCheck.Enabled() {
		go a.versionCheck.Run(serverCtx)
	}

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

// Shutdown gracefully tears down app components in dependency order:
//  1. Close long-lived streams (ownedByPrologue), so they do not hold the HTTP
//     drain open, then cancel the server context and wait for it to stop.
//  2. Close server-owned resources (ownedByServer) now that no request is in
//     flight.
//  3. Close the remaining subsystems in the order given by shutdownOrder.
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

	// Remaining subsystems close in dependency order (see shutdownOrder).
	for _, subsystem := range a.shutdownOrder() {
		if subsystem.close == nil {
			continue
		}
		if err := subsystem.close(); err != nil {
			slog.Error(subsystem.name+" close error", "error", err)
			errs = append(errs, fmt.Errorf("%s close: %w", subsystem.name, err))
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
	case a.extensionAuth && cfg.Server.MasterKey != "" && managedKeysConfigured:
		slog.Info("authentication enabled", "mode", "master_key+managed_keys+extension")
	case a.extensionAuth && (cfg.Server.MasterKey != "" || managedKeysConfigured):
		slog.Info("authentication enabled", "mode", "extension+bearer")
	case a.extensionAuth:
		slog.Info("authentication enabled", "mode", "extension")
	case cfg.Server.MasterKey != "" && managedKeysConfigured:
		slog.Info("authentication enabled", "mode", "master_key+managed_keys", "managed_key_total", a.authKeys.Service.Total(), "managed_key_active", a.authKeys.Service.ActiveCount())
	case managedKeysConfigured:
		slog.Info("authentication enabled", "mode", "managed_keys", "managed_key_total", a.authKeys.Service.Total(), "managed_key_active", a.authKeys.Service.ActiveCount())
	case cfg.Server.MasterKey == "":
		slog.Warn("SECURITY WARNING: GOMODEL_MASTER_KEY not set - server running in UNSAFE MODE",
			"security_risk", "unauthenticated access allowed",
			"recommendation", "set GOMODEL_MASTER_KEY environment variable to secure this gateway")
		if cfg.MCP.Enabled && len(cfg.MCP.Servers) > 0 {
			// Worth calling out separately: an unauthenticated /mcp hands any
			// caller that can reach the port every aggregated tool, together
			// with the upstream credentials configured behind them.
			slog.Warn("SECURITY WARNING: the MCP gateway is serving aggregated tools without authentication",
				"security_risk", "any caller that can reach this port can invoke every configured MCP tool",
				"configured_servers", len(cfg.MCP.Servers),
				"recommendation", "set GOMODEL_MASTER_KEY, or set MCP_ENABLED=false")
		}
	default:
		slog.Info("authentication enabled", "mode", "master_key")
	}

	// A wildcard origin allowlist turns off the MCP gateway's DNS-rebinding
	// defense, so it is never silent.
	if cfg.MCP.Enabled && slices.Contains(cfg.MCP.AllowedOrigins, config.TrustAnyOrigin) {
		slog.Warn("SECURITY WARNING: mcp.allowed_origins trusts every browser origin",
			"security_risk", "browser-based DNS rebinding attacks against the MCP gateway are not blocked",
			"recommendation", "list the specific origins you serve an MCP web client from instead of \"*\"")
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
			"log_image_bodies", cfg.Logging.LogImageBodies,
			"log_image_bodies_scope", cfg.Logging.LogImageBodiesScope,
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

func hasUsableRequestAuthenticator(registry *ext.Registry) bool {
	if registry == nil {
		return false
	}
	for _, authenticator := range registry.Authenticators() {
		if !nilInterface(authenticator) {
			return true
		}
	}
	return false
}

func bindAuthenticationEventRecorders(registry *ext.Registry, recorder ext.AuthenticationEventRecorder) {
	if registry == nil || recorder == nil {
		return
	}
	for _, authenticator := range registry.Authenticators() {
		if nilInterface(authenticator) {
			continue
		}
		if aware, ok := authenticator.(ext.AuthenticationEventRecorderAware); ok && !nilInterface(aware) {
			aware.SetAuthenticationEventRecorder(recorder)
		}
	}
}

func nilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

// initAdmin creates the admin API handler and optionally the dashboard handler.
// Returns nil dashboard handler if uiEnabled is false or the dashboard build
// is missing from the binary.
func initAdmin(
	reader usage.UsageReader,
	usageReadStorage storage.Storage,
	auditStorage storage.Storage,
	registry *providers.ModelRegistry,
	configuredProviders []providers.SanitizedProviderConfig,
	authKeyService *authkeys.Service,
	userService *users.Service,
	virtualModelService *virtualmodels.Service,
	pricingOverrideService *pricingoverrides.Service,
	workflowService *workflows.Service,
	guardrailService *guardrails.Service,
	budgetService *budget.Service,
	rateLimitService *ratelimit.Service,
	taggingService *tagging.Service,
	runtimeSettingsService *runtimesettings.Service,
	mcpResult *mcpgateway.Result,
	providerCredentialsResult *providers.CredentialsResult,
	runtimeRefresher admin.RuntimeRefresher,
	runtimeConfig admin.DashboardConfigResponse,
	quotaTemplatesEnabled bool,
	liveBroker *live.Broker,
	requestHealth admin.RequestHealthSource,
	usagePricingRecalculationEnabled bool,
	basePath string,
	uiEnabled bool,
) (*admin.Handler, *dashboard.Handler, auditlog.Reader, error) {
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
			return nil, nil, nil, fmt.Errorf("failed to create audit reader: %w", err)
		}
	}

	// Assigned conditionally so a disabled MCP gateway leaves the option nil
	// (a typed-nil *mcpgateway.Service stored in the interface field would
	// defeat the handlers' feature-unavailable check).
	var mcpOption admin.Option
	if mcpResult != nil && mcpResult.Service != nil {
		mcpOption = admin.WithMCPServers(mcpResult.Service)
	}
	var providerCredentialsOption admin.Option
	if providerCredentialsResult != nil && providerCredentialsResult.Service != nil {
		providerCredentialsOption = admin.WithProviderCredentials(providerCredentialsResult.Service)
	}

	adminHandler := admin.NewHandler(
		reader,
		registry,
		admin.WithConfiguredProviders(configuredProviders),
		admin.WithUsagePricingRecalculator(pricingRecalculator),
		admin.WithPricingResolver(pricingOverrideService),
		admin.WithAuditReader(auditReader),
		admin.WithAuthKeys(authKeyService),
		admin.WithUsers(userService),
		admin.WithVirtualModels(virtualModelService),
		admin.WithPricingOverrides(pricingOverrideService),
		admin.WithWorkflows(workflowService),
		admin.WithGuardrailService(guardrailService),
		admin.WithBudgets(budgetService),
		admin.WithRateLimits(rateLimitService),
		admin.WithQuotaTemplatesEnabled(quotaTemplatesEnabled),
		admin.WithTagging(taggingService),
		admin.WithRuntimeSettings(runtimeSettingsService),
		mcpOption,
		providerCredentialsOption,
		admin.WithRuntimeRefresher(runtimeRefresher),
		admin.WithDashboardRuntimeConfig(runtimeConfig),
		admin.WithLiveBroker(liveBroker),
		admin.WithRequestHealth(requestHealth),
	)

	var dashHandler *dashboard.Handler
	if uiEnabled {
		h, err := dashboard.NewWithDemoMode(basePath, runtimeConfig.DemoMode == "on")
		if err != nil {
			// The dashboard build is generated, not committed (see
			// docs/adr/0010-dashboard-built-in-ci.md). A binary built without
			// it keeps the gateway and admin API; only the UI is unavailable.
			slog.Error("admin UI disabled: dashboard assets missing from this build", "error", err)
		} else {
			dashHandler = h
		}
	}

	return adminHandler, dashHandler, auditReader, nil
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

func dashboardRuntimeConfig(cfg *config.Config, usageEnabled, demoMode, adaptiveRouting bool) admin.DashboardConfigResponse {
	return admin.DashboardConfigResponse{
		DemoMode:               dashboardEnabledValue(demoMode),
		FailoverEnabled:        dashboardEnabledValue(failoverFeatureEnabledGlobally(cfg)),
		LoggingEnabled:         dashboardEnabledValue(cfg != nil && cfg.Logging.Enabled),
		LoggingRetentionDays:   dashboardLoggingRetentionDays(cfg),
		UsageEnabled:           dashboardEnabledValue(cfg != nil && cfg.Usage.Enabled),
		BudgetsEnabled:         dashboardEnabledValue(cfg != nil && cfg.Budgets.Enabled),
		RateLimitsEnabled:      dashboardEnabledValue(cfg != nil && cfg.RateLimits.Enabled),
		GuardrailsEnabled:      dashboardEnabledValue(cfg != nil && cfg.Guardrails.Enabled),
		CacheEnabled:           dashboardEnabledValue(cacheAnalyticsConfigured(cfg, usageEnabled)),
		RedisURL:               dashboardEnabledValue(simpleResponseCacheConfigured(cfg)),
		SemanticCacheEnabled:   dashboardEnabledValue(semanticResponseCacheConfigured(cfg)),
		LiveLogsEnabled:        dashboardEnabledValue(cfg != nil && cfg.Admin.LiveLogsEnabled),
		MCPEnabled:             dashboardEnabledValue(cfg != nil && cfg.MCP.Enabled),
		VirtualModelStrategies: dashboardVirtualModelStrategies(adaptiveRouting),
	}
}

// dashboardVirtualModelStrategies lists the load-balancing strategies the
// dashboard should offer. Core accepts "adaptive" regardless (it falls back
// to round robin without a selector), but the UI only advertises it when a
// route-selector extension is actually registered.
func dashboardVirtualModelStrategies(adaptiveRouting bool) string {
	strategies := []string{virtualmodels.StrategyRoundRobin, virtualmodels.StrategyCost, virtualmodels.StrategyFailover}
	if adaptiveRouting {
		strategies = append(strategies, virtualmodels.StrategyAdaptive)
	}
	return strings.Join(strategies, ",")
}

func dashboardLoggingRetentionDays(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	return fmt.Sprintf("%d", cfg.Logging.RetentionDays)
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

// failoverResolver returns the virtual models service as the translated-route
// failover resolver: a redirect's remaining targets are its failover chain.
// FAILOVER_ENABLED=false switches the sweep off globally.
func failoverResolver(cfg *config.Config, vm *virtualmodels.Service) server.RequestFailoverResolver {
	if !failoverFeatureEnabledGlobally(cfg) || vm == nil {
		return nil
	}
	return vm
}
