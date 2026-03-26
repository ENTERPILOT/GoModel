// Package app provides the main application struct for centralized dependency management
// and lifecycle control of the GOModel server.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"gomodel/config"
	"gomodel/internal/admin"
	"gomodel/internal/admin/dashboard"
	"gomodel/internal/aliases"
	"gomodel/internal/auditlog"
	"gomodel/internal/batch"
	"gomodel/internal/core"
	"gomodel/internal/executionplans"
	"gomodel/internal/guardrails"
	"gomodel/internal/providers"
	"gomodel/internal/responsecache"
	"gomodel/internal/server"
	"gomodel/internal/storage"
	"gomodel/internal/usage"
)

// App represents the main application with all its dependencies.
// It provides centralized lifecycle management for all components.
type App struct {
	config         *config.Config
	providers      *providers.InitResult
	audit          *auditlog.Result
	usage          *usage.Result
	batch          *batch.Result
	aliases        *aliases.Result
	executionPlans *executionplans.Result
	server         *server.Server

	shutdownMu sync.Mutex
	shutdown   bool
	serverMu   sync.Mutex
	serverStop context.CancelFunc
	serverDone chan error
}

// Config holds the configuration options for creating an App.
type Config struct {
	// AppConfig holds the loaded application configuration and raw provider data
	// produced by config.Load.
	AppConfig *config.LoadResult

	// Factory provides the ProviderFactory used to construct provider instances.
	Factory *providers.ProviderFactory
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

	app := &App{
		config: appCfg,
	}

	providerResult, err := providers.Init(ctx, cfg.AppConfig, cfg.Factory)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize providers: %w", err)
	}
	app.providers = providerResult

	// Initialize audit logging
	auditResult, err := auditlog.New(ctx, appCfg)
	if err != nil {
		closeErr := app.providers.Close()
		if closeErr != nil {
			return nil, fmt.Errorf("failed to initialize audit logging: %w (also: providers close error: %v)", err, closeErr)
		}
		return nil, fmt.Errorf("failed to initialize audit logging: %w", err)
	}
	app.audit = auditResult

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
		closeErr := errors.Join(app.audit.Close(), app.providers.Close())
		if closeErr != nil {
			return nil, fmt.Errorf("failed to initialize usage tracking: %w (also: close error: %v)", err, closeErr)
		}
		return nil, fmt.Errorf("failed to initialize usage tracking: %w", err)
	}
	app.usage = usageResult

	// Initialize batch lifecycle storage.
	var batchResult *batch.Result
	if auditResult.Storage != nil {
		batchResult, err = batch.NewWithSharedStorage(ctx, auditResult.Storage)
	} else if usageResult.Storage != nil {
		batchResult, err = batch.NewWithSharedStorage(ctx, usageResult.Storage)
	} else {
		batchResult, err = batch.New(ctx, appCfg)
	}
	if err != nil {
		closeErr := errors.Join(app.usage.Close(), app.audit.Close(), app.providers.Close())
		if closeErr != nil {
			return nil, fmt.Errorf("failed to initialize batch storage: %w (also: close error: %v)", err, closeErr)
		}
		return nil, fmt.Errorf("failed to initialize batch storage: %w", err)
	}
	app.batch = batchResult

	// Initialize aliases using shared storage when already available.
	var aliasResult *aliases.Result
	if auditResult.Storage != nil {
		aliasResult, err = aliases.NewWithSharedStorage(ctx, appCfg, auditResult.Storage, providerResult.Registry)
	} else if usageResult.Storage != nil {
		aliasResult, err = aliases.NewWithSharedStorage(ctx, appCfg, usageResult.Storage, providerResult.Registry)
	} else if batchResult.Storage != nil {
		aliasResult, err = aliases.NewWithSharedStorage(ctx, appCfg, batchResult.Storage, providerResult.Registry)
	} else {
		aliasResult, err = aliases.New(ctx, appCfg, providerResult.Registry)
	}
	if err != nil {
		closeErr := errors.Join(app.batch.Close(), app.usage.Close(), app.audit.Close(), app.providers.Close())
		if closeErr != nil {
			return nil, fmt.Errorf("failed to initialize aliases: %w (also: close error: %v)", err, closeErr)
		}
		return nil, fmt.Errorf("failed to initialize aliases: %w", err)
	}
	app.aliases = aliasResult

	// Log configuration status
	app.logStartupInfo()

	// Build runtime execution dependencies. Policy is passed explicitly into the
	// server; the live provider dependency remains the bare router.
	var provider core.RoutableProvider = app.providers.Router
	var translatedRequestPatcher server.TranslatedRequestPatcher
	var batchRequestPreparers []server.BatchRequestPreparer
	featureCaps := runtimeExecutionFeatureCaps(appCfg)
	var guardrailRegistry *guardrails.Registry
	if featureCaps.Guardrails {
		guardrailRegistry, err = buildGuardrailRegistry(appCfg.Guardrails)
		if err != nil {
			var (
				aliasCloseErr error
				batchCloseErr error
			)
			if app.aliases != nil {
				aliasCloseErr = app.aliases.Close()
			}
			if app.batch != nil {
				batchCloseErr = app.batch.Close()
			}
			closeErr := errors.Join(aliasCloseErr, batchCloseErr, app.usage.Close(), app.audit.Close(), app.providers.Close())
			if closeErr != nil {
				return nil, fmt.Errorf("failed to build guardrails: %w (also: close error: %v)", err, closeErr)
			}
			return nil, fmt.Errorf("failed to build guardrails: %w", err)
		}
	}

	var executionPlanResult *executionplans.Result
	sharedExecutionPlanStorage := firstSharedStorage(auditResult.Storage, usageResult.Storage, batchResult.Storage, aliasResult.Storage)
	executionPlanCompiler := executionplans.NewCompilerWithFeatureCaps(guardrailRegistry, featureCaps)
	if sharedExecutionPlanStorage != nil {
		executionPlanResult, err = executionplans.NewWithSharedStorage(ctx, sharedExecutionPlanStorage, executionPlanCompiler, time.Minute)
	} else {
		executionPlanResult, err = executionplans.New(ctx, appCfg, executionPlanCompiler, time.Minute)
	}
	if err != nil {
		closeErr := errors.Join(app.aliases.Close(), app.batch.Close(), app.usage.Close(), app.audit.Close(), app.providers.Close())
		if closeErr != nil {
			return nil, fmt.Errorf("failed to initialize execution plans: %w (also: close error: %v)", err, closeErr)
		}
		return nil, fmt.Errorf("failed to initialize execution plans: %w", err)
	}
	defaultExecutionPlan := defaultExecutionPlanInput(appCfg)
	if err := executionPlanResult.Service.EnsureDefaultGlobal(ctx, defaultExecutionPlan); err != nil {
		closeErr := errors.Join(executionPlanResult.Close(), app.aliases.Close(), app.batch.Close(), app.usage.Close(), app.audit.Close(), app.providers.Close())
		if closeErr != nil {
			return nil, fmt.Errorf("failed to seed execution plans: %w (also: close error: %v)", err, closeErr)
		}
		return nil, fmt.Errorf("failed to seed execution plans: %w", err)
	}
	if err := executionPlanResult.Service.Refresh(ctx); err != nil {
		closeErr := errors.Join(executionPlanResult.Close(), app.aliases.Close(), app.batch.Close(), app.usage.Close(), app.audit.Close(), app.providers.Close())
		if closeErr != nil {
			return nil, fmt.Errorf("failed to load execution plans: %w (also: close error: %v)", err, closeErr)
		}
		return nil, fmt.Errorf("failed to load execution plans: %w", err)
	}
	app.executionPlans = executionPlanResult

	if featureCaps.Guardrails {
		if guardrailRegistry != nil && guardrailRegistry.Len() > 0 {
			translatedRequestPatcher = guardrails.NewPlannedRequestPatcher(executionPlanResult.Service)
			if appCfg.Guardrails.EnableForBatchProcessing {
				batchRequestPreparers = append(batchRequestPreparers, guardrails.NewPlannedBatchPreparer(provider, executionPlanResult.Service))
			}
			slog.Info(
				"guardrails enabled",
				"count", guardrailRegistry.Len(),
				"enable_for_batch_processing", appCfg.Guardrails.EnableForBatchProcessing,
			)
		}
	}
	if app.aliases != nil && app.aliases.Service != nil {
		batchRequestPreparers = append([]server.BatchRequestPreparer{
			aliases.NewBatchPreparer(provider, app.aliases.Service),
		}, batchRequestPreparers...)
	}
	batchRequestPreparer := server.ComposeBatchRequestPreparers(providerAsNativeFileRouter(provider), batchRequestPreparers...)

	guardrailsHash := computeGuardrailsHashFromConfig(appCfg.Guardrails)

	// Create server
	allowPassthroughV1Alias := appCfg.Server.AllowPassthroughV1Alias
	serverCfg := &server.Config{
		MasterKey:                    appCfg.Server.MasterKey,
		MetricsEnabled:               appCfg.Metrics.Enabled,
		MetricsEndpoint:              appCfg.Metrics.Endpoint,
		BodySizeLimit:                appCfg.Server.BodySizeLimit,
		PprofEnabled:                 appCfg.Server.PprofEnabled,
		AuditLogger:                  auditResult.Logger,
		UsageLogger:                  usageResult.Logger,
		PricingResolver:              providerResult.Registry,
		ModelResolver:                app.aliases.Service,
		ExecutionPolicyResolver:      executionPlanResult.Service,
		TranslatedRequestPatcher:     translatedRequestPatcher,
		GuardrailsHash:               guardrailsHash,
		BatchRequestPreparer:         batchRequestPreparer,
		ExposedModelLister:           app.aliases.Service,
		PassthroughSemanticEnrichers: cfg.Factory.PassthroughSemanticEnrichers(),
		BatchStore:                   batchResult.Store,
		LogOnlyModelInteractions:     appCfg.Logging.OnlyModelInteractions,
		DisablePassthroughRoutes:     !appCfg.Server.EnablePassthroughRoutes,
		EnabledPassthroughProviders:  appCfg.Server.EnabledPassthroughProviders,
		AllowPassthroughV1Alias:      &allowPassthroughV1Alias,
		SwaggerEnabled:               appCfg.Server.SwaggerEnabled,
	}

	// Initialize admin API and dashboard (behind separate feature flags)
	adminCfg := appCfg.Admin
	if !adminCfg.EndpointsEnabled && adminCfg.UIEnabled {
		slog.Warn("ADMIN_UI_ENABLED=true requires ADMIN_ENDPOINTS_ENABLED=true — forcing UI to disabled")
		adminCfg.UIEnabled = false
	}
	if adminCfg.EndpointsEnabled {
		adminHandler, dashHandler, adminErr := initAdmin(auditResult.Storage, usageResult.Storage, providerResult.Registry, app.aliases.Service, adminCfg.UIEnabled)
		if adminErr != nil {
			slog.Warn("failed to initialize admin", "error", adminErr)
		} else {
			serverCfg.AdminEndpointsEnabled = true
			serverCfg.AdminHandler = adminHandler
			slog.Info("admin API enabled", "api", "/admin/api/v1")
			if adminCfg.UIEnabled {
				serverCfg.AdminUIEnabled = true
				serverCfg.DashboardHandler = dashHandler
				slog.Info("admin UI enabled", "url", fmt.Sprintf("http://localhost:%s/admin/dashboard", appCfg.Server.Port))
			}
		}
	} else {
		slog.Info("admin API disabled")
	}

	if appCfg.Server.SwaggerEnabled {
		slog.Info("swagger UI enabled", "path", "/swagger/index.html")
	}
	if appCfg.Server.PprofEnabled {
		slog.Info("pprof enabled", "path", "/debug/pprof/")
	}
	if appCfg.Server.EnablePassthroughRoutes {
		slog.Info("provider passthrough enabled", "path", "/p/{provider}/{endpoint}")
	} else {
		slog.Info("provider passthrough disabled")
	}

	rcm, err := responsecache.NewResponseCacheMiddleware(appCfg.Cache.Response, cfg.AppConfig.RawProviders)
	if err != nil {
		var (
			executionPlansCloseErr error
			aliasCloseErr          error
			batchCloseErr          error
		)
		if app.executionPlans != nil {
			executionPlansCloseErr = app.executionPlans.Close()
		}
		if app.aliases != nil {
			aliasCloseErr = app.aliases.Close()
		}
		if app.batch != nil {
			batchCloseErr = app.batch.Close()
		}
		closeErr := errors.Join(executionPlansCloseErr, aliasCloseErr, batchCloseErr, app.usage.Close(), app.audit.Close(), app.providers.Close())
		if closeErr != nil {
			return nil, fmt.Errorf("failed to initialize response cache: %w (also: close error: %v)", err, closeErr)
		}
		return nil, fmt.Errorf("failed to initialize response cache: %w", err)
	}
	serverCfg.ResponseCacheMiddleware = rcm

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

func providerAsNativeFileRouter(provider core.RoutableProvider) core.NativeFileRoutableProvider {
	if fileRouter, ok := provider.(core.NativeFileRoutableProvider); ok {
		return fileRouter
	}
	return nil
}

// Start starts the HTTP server on the given address.
// This is a blocking call that returns when the server stops.
func (a *App) Start(ctx context.Context, addr string) error {
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

	slog.Info("starting server", "address", addr)
	err := a.server.Start(serverCtx, addr)

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
// 1. Cancel HTTP server context and wait for the server to stop.
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

	// 1. Stop HTTP server first (stop accepting new requests)
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

	// 2. Close providers (stops model refresh and provider-owned resources)
	if a.providers != nil {
		if err := a.providers.Close(); err != nil {
			slog.Error("providers close error", "error", err)
			errs = append(errs, fmt.Errorf("providers close: %w", err))
		}
	}

	// 3. Close aliases subsystem.
	if a.aliases != nil {
		if err := a.aliases.Close(); err != nil {
			slog.Error("aliases close error", "error", err)
			errs = append(errs, fmt.Errorf("aliases close: %w", err))
		}
	}

	// 4. Close execution plans subsystem.
	if a.executionPlans != nil {
		if err := a.executionPlans.Close(); err != nil {
			slog.Error("execution plans close error", "error", err)
			errs = append(errs, fmt.Errorf("execution plans close: %w", err))
		}
	}

	// 5. Close batch store (flushes pending entries)
	if a.batch != nil {
		if err := a.batch.Close(); err != nil {
			slog.Error("batch store close error", "error", err)
			errs = append(errs, fmt.Errorf("batch close: %w", err))
		}
	}

	// 6. Close usage tracking (flushes pending entries)
	if a.usage != nil {
		if err := a.usage.Close(); err != nil {
			slog.Error("usage logger close error", "error", err)
			errs = append(errs, fmt.Errorf("usage close: %w", err))
		}
	}

	// 7. Close audit logging (flushes pending logs)
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
	if cfg.Server.MasterKey == "" {
		slog.Warn("SECURITY WARNING: GOMODEL_MASTER_KEY not set - server running in UNSAFE MODE",
			"security_risk", "unauthenticated access allowed",
			"recommendation", "set GOMODEL_MASTER_KEY environment variable to secure this gateway")
	} else {
		slog.Info("authentication enabled", "mode", "master_key")
	}

	// Metrics configuration
	if cfg.Metrics.Enabled {
		slog.Info("prometheus metrics enabled", "endpoint", cfg.Metrics.Endpoint)
	} else {
		slog.Info("prometheus metrics disabled")
	}

	// Storage configuration (shared by audit logging and usage tracking)
	slog.Info("storage configured", "type", cfg.Storage.Type)

	// Audit logging configuration
	if cfg.Logging.Enabled {
		slog.Info("audit logging enabled",
			"log_bodies", cfg.Logging.LogBodies,
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
func initAdmin(auditStorage, usageStorage storage.Storage, registry *providers.ModelRegistry, aliasService *aliases.Service, uiEnabled bool) (*admin.Handler, *dashboard.Handler, error) {
	// Find a storage connection for reading usage data
	var store storage.Storage
	if auditStorage != nil {
		store = auditStorage
	} else if usageStorage != nil {
		store = usageStorage
	}

	// Create usage reader (may be nil if no storage)
	var reader usage.UsageReader
	if store != nil {
		var err error
		reader, err = usage.NewReader(store)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to create usage reader: %w", err)
		}
	}

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

	adminHandler := admin.NewHandler(reader, registry, admin.WithAuditReader(auditReader), admin.WithAliases(aliasService))

	var dashHandler *dashboard.Handler
	if uiEnabled {
		var err error
		dashHandler, err = dashboard.New()
		if err != nil {
			return nil, nil, fmt.Errorf("failed to initialize dashboard: %w", err)
		}
	}

	return adminHandler, dashHandler, nil
}

func buildGuardrailRegistry(cfg config.GuardrailsConfig) (*guardrails.Registry, error) {
	registry := guardrails.NewRegistry()

	for i, rule := range cfg.Rules {
		g, err := buildGuardrail(rule)
		if err != nil {
			return nil, fmt.Errorf("guardrail rule #%d (%q): %w", i, rule.Name, err)
		}
		if err := registry.Register(g, responsecache.GuardrailRuleDescriptor{
			Type:    rule.Type,
			Order:   rule.Order,
			Mode:    effectiveSystemPromptMode(rule.SystemPrompt.Mode),
			Content: rule.SystemPrompt.Content,
		}); err != nil {
			return nil, fmt.Errorf("register guardrail %q: %w", rule.Name, err)
		}
		slog.Info("guardrail registered", "name", rule.Name, "type", rule.Type, "order", rule.Order)
	}

	return registry, nil
}

// buildGuardrail creates a single Guardrail instance from a rule config.
func buildGuardrail(rule config.GuardrailRuleConfig) (guardrails.Guardrail, error) {
	if rule.Name == "" {
		return nil, fmt.Errorf("name is required")
	}

	switch rule.Type {
	case "system_prompt":
		mode := guardrails.SystemPromptMode(effectiveSystemPromptMode(rule.SystemPrompt.Mode))
		return guardrails.NewSystemPromptGuardrail(rule.Name, mode, rule.SystemPrompt.Content)

	default:
		return nil, fmt.Errorf("unknown guardrail type: %q", rule.Type)
	}
}

// computeGuardrailsHashFromConfig computes a stable hash of all configured guardrail rules.
// The hash is stored in the Echo context post-patch so the semantic cache can include it
// in params_hash, ensuring automatic cache invalidation when guardrail policy changes.
func computeGuardrailsHashFromConfig(cfg config.GuardrailsConfig) string {
	if !cfg.Enabled || len(cfg.Rules) == 0 {
		return ""
	}
	rules := make([]responsecache.GuardrailRuleDescriptor, len(cfg.Rules))
	for i, r := range cfg.Rules {
		rules[i] = responsecache.GuardrailRuleDescriptor{
			Name:    r.Name,
			Type:    r.Type,
			Order:   r.Order,
			Mode:    effectiveSystemPromptMode(r.SystemPrompt.Mode),
			Content: r.SystemPrompt.Content,
		}
	}
	return responsecache.ComputeGuardrailsHash(rules)
}

func effectiveSystemPromptMode(mode string) string {
	resolved := guardrails.SystemPromptMode(mode)
	if resolved == "" {
		return string(guardrails.SystemPromptInject)
	}
	return string(resolved)
}

func defaultExecutionPlanInput(cfg *config.Config) executionplans.CreateInput {
	payload := executionplans.Payload{
		SchemaVersion: 1,
		Features: executionplans.FeatureFlags{
			Cache:      responseCacheConfigured(cfg.Cache.Response),
			Audit:      cfg.Logging.Enabled,
			Usage:      cfg.Usage.Enabled,
			Guardrails: cfg.Guardrails.Enabled && len(cfg.Guardrails.Rules) > 0,
		},
	}
	if payload.Features.Guardrails {
		payload.Guardrails = make([]executionplans.GuardrailStep, 0, len(cfg.Guardrails.Rules))
		for _, rule := range cfg.Guardrails.Rules {
			payload.Guardrails = append(payload.Guardrails, executionplans.GuardrailStep{
				Ref:  rule.Name,
				Step: rule.Order,
			})
		}
	}

	return executionplans.CreateInput{
		Scope:       executionplans.Scope{},
		Activate:    true,
		Name:        "default-global",
		Description: "Bootstrapped from runtime configuration",
		Payload:     payload,
	}
}

func runtimeExecutionFeatureCaps(cfg *config.Config) core.ExecutionFeatures {
	if cfg == nil {
		return core.ExecutionFeatures{}
	}
	return core.ExecutionFeatures{
		Cache:      responseCacheConfigured(cfg.Cache.Response),
		Audit:      cfg.Logging.Enabled,
		Usage:      cfg.Usage.Enabled,
		Guardrails: cfg.Guardrails.Enabled,
	}
}

func responseCacheConfigured(cfg config.ResponseCacheConfig) bool {
	return (cfg.Simple.Redis != nil && cfg.Simple.Redis.URL != "") || config.SemanticCacheActive(&cfg.Semantic)
}

func firstSharedStorage(candidates ...storage.Storage) storage.Storage {
	for _, candidate := range candidates {
		if candidate != nil {
			return candidate
		}
	}
	return nil
}
