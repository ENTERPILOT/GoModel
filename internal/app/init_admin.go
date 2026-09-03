package app

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/admin"
	"github.com/enterpilot/gomodel/internal/admin/dashboard"
	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/authkeys"
	"github.com/enterpilot/gomodel/internal/budget"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/guardrails"
	"github.com/enterpilot/gomodel/internal/live"
	"github.com/enterpilot/gomodel/internal/mcpgateway"
	"github.com/enterpilot/gomodel/internal/pricingoverrides"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/ratelimit"
	"github.com/enterpilot/gomodel/internal/runtimesettings"
	"github.com/enterpilot/gomodel/internal/storage"
	"github.com/enterpilot/gomodel/internal/tagging"
	"github.com/enterpilot/gomodel/internal/usage"
	"github.com/enterpilot/gomodel/internal/users"
	"github.com/enterpilot/gomodel/internal/virtualmodels"
	"github.com/enterpilot/gomodel/internal/workflows"
)

// initAdmin wires the admin API and dashboard into the server config, behind
// their separate feature flags, and logs which optional surfaces are on. It
// reads every service the earlier phases built, so it runs after all of them
// and after initServerConfig has created the config it fills in.
func (b *bootstrap) initAdmin() error {
	app := b.app
	appCfg := b.appCfg
	serverCfg := b.serverCfg

	adminCfg := appCfg.Admin
	if !adminCfg.EndpointsEnabled && adminCfg.UIEnabled {
		slog.Warn("ADMIN_UI_ENABLED=true requires ADMIN_ENDPOINTS_ENABLED=true — forcing UI to disabled")
		adminCfg.UIEnabled = false
	}
	if adminCfg.EndpointsEnabled {
		usageEnabledForDashboard := app.usage.Logger.Config().Enabled
		adminRuntimeConfig := dashboardRuntimeConfig(appCfg, usageEnabledForDashboard, b.cfg.DemoMode, b.routeSelector != nil)
		adminRuntimeConfig.QuotaTemplatesEnabled = dashboardEnabledValue(b.quotaTemplatesEnabled)
		adminHandler, dashHandler, auditReader, adminErr := newAdminHandlers(
			b.usageReader,
			app.storage,
			app.providers.Registry,
			app.providers.ConfiguredProviders,
			app.authKeys.Service,
			app.users.Service,
			app.virtualModels.Service,
			app.pricingOverrides.Service,
			app.workflows.Service,
			app.guardrails.Service,
			app.budgets.Service,
			app.rateLimits.Service,
			app.tagging.Service,
			app.runtimeSettings,
			app.mcpGateway,
			app.providerCredentials,
			app,
			adminRuntimeConfig,
			b.quotaTemplatesEnabled,
			app.live,
			b.requestHealth,
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
			b.livePublishersEnabled = true
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

	if serverCfg.SwaggerEnabled {
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
	return nil
}

// newAdminHandlers creates the admin API handler and optionally the dashboard handler.
// Returns nil dashboard handler if uiEnabled is false or the dashboard build
// is missing from the binary.
func newAdminHandlers(
	reader usage.UsageReader,
	sharedStorage storage.Storage,
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
	if sharedStorage != nil && usagePricingRecalculationEnabled {
		var err error
		pricingRecalculator, err = usage.NewPricingRecalculator(sharedStorage)
		if err != nil {
			slog.Warn("usage pricing recalculation unavailable", "error", err)
			pricingRecalculator = nil
		}
	}
	runtimeConfig.PricingRecalculation = dashboardEnabledValue(usagePricingRecalculationEnabled && pricingRecalculator != nil)

	// Create audit reader from the shared storage.
	var auditReader auditlog.Reader
	if sharedStorage != nil {
		var err error
		auditReader, err = auditlog.NewReader(sharedStorage)
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
		UserPathHeader:         dashboardUserPathHeader(cfg),
	}
}

// dashboardUserPathHeader is the canonical user-path header name the public
// API reads, so the Playground sends the configured USER_PATH_HEADER rather
// than assuming the default.
func dashboardUserPathHeader(cfg *config.Config) string {
	if cfg == nil {
		return core.UserPathHeader
	}
	return core.UserPathHeaderName(cfg.Server.UserPathHeader)
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
