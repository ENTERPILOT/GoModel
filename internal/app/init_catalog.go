package app

import (
	"context"
	"errors"
	"fmt"
	"net"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/plugins"
	"github.com/enterpilot/gomodel/internal/pricingoverrides"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/tagging"
	"github.com/enterpilot/gomodel/internal/usage"
	"github.com/enterpilot/gomodel/internal/users"
	"github.com/enterpilot/gomodel/internal/virtualmodels"
	"github.com/enterpilot/gomodel/pluginapi"
)

// initModelCatalog builds the services that decide which models exist and
// who may use them: dashboard-managed provider credentials, virtual models,
// and user-side access policy. It needs the provider registry and the rate
// limit service from the earlier phases.
func (b *bootstrap) initModelCatalog() error {
	app := b.app
	providerResult := app.providers

	// Initialize virtual models (unified aliases + access overrides) using
	// shared storage when already available. Provider names declared in YAML —
	// including entries whose credentials did not resolve, which never register —
	// let validation tell a misspelled target provider (abort startup) from a
	// declared-but-inactive one (warn, target stays unavailable).
	declaredProviders := make([]string, 0, len(b.cfg.AppConfig.RawProviders))
	for name := range b.cfg.AppConfig.RawProviders {
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
	b.managedProviderNames = managedProviderNames

	providerCredentialsResult, err := providers.NewCredentialsStore(b.ctx, app.storage, providerResult.Factory, providerResult.Registry, managedProviderNames, b.appCfg.Resilience)
	if err != nil {
		return fmt.Errorf("failed to initialize provider credentials store: %w", err)
	}
	app.providerCredentials = providerCredentialsResult
	app.register(subsystemProviderCredentials, ownedByShutdown, app.providerCredentials.Close)

	// The routing-strategy resolver was built with the provider hooks in
	// initProviders; virtual models consult it for the plugin strategy.
	routeStrategies := app.routeStrategies
	virtualModelsResult, err := virtualmodels.New(b.ctx, b.appCfg, app.storage, providerResult.Registry, declaredProviders,
		virtualmodels.WithRouteResolver(routeStrategies))
	if err != nil {
		return fmt.Errorf("failed to initialize virtual models: %w", err)
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
	if app.rateLimits.Service != nil {
		registry := providerResult.Registry
		limiter := app.rateLimits.Service
		vm.SetTargetCapacity(func(qualifiedModel string) bool {
			return limiter.RouteAvailable(registry.GetProviderName(qualifiedModel), qualifiedModel)
		})
	}

	// Redirects with the adaptive strategy delegate target choice to the
	// extension route selector; without one they fall back to round robin
	// inside the balancer, so the strategy stays valid in plain core builds.
	if b.routeSelector != nil {
		vm.SetRouteSelector(b.routeSelector)
	}

	// Subject-side model access: auth key and user-path allowlists narrow what
	// the model-side policy rows expose, so one authorizer answers both.
	usersResult, err := users.New(b.ctx, b.appCfg, app.storage, providerResult.Registry, managedProviderNames)
	if err != nil {
		return fmt.Errorf("failed to initialize users: %w", err)
	}
	app.users = usersResult
	app.register(subsystemUsers, ownedByShutdown, app.users.Close)
	vm.SetAccessPolicy(usersResult.Service)
	return nil
}

// guardrailInstanceConfig returns the instance-scoped config source for
// routing-strategy plugins: the stored config of the guardrail definition
// carrying the plugin's own name and type, when one exists. The guardrails
// service is built in a later phase, so it is read at call time.
func guardrailInstanceConfig(app *App) plugins.RouteConfigSource {
	return func(name string) (json.RawMessage, bool) {
		if app == nil || app.guardrails == nil || app.guardrails.Service == nil {
			return nil, false
		}
		config, pluginType, ok := app.guardrails.Service.InstanceConfig(name)
		if !ok || pluginType != name {
			return nil, false
		}
		return config, true
	}
}

// routeStrategyHooks adapts upstream client lifecycle events into
// routing-strategy plugin outcomes, mirroring routeSelectorHooks. The
// resolver recovers plugin panics itself.
func routeStrategyHooks(resolver *plugins.RouteResolver) llmclient.Hooks {
	return llmclient.Hooks{
		OnRequestEnd: func(ctx context.Context, info llmclient.ResponseInfo) {
			source, _ := routeAffinityContext(ctx)
			resolver.ReportOutcome(routeOutcome(source, info))
		},
	}
}

// routeOutcome maps one completed upstream call to the plugin outcome shape.
func routeOutcome(source string, info llmclient.ResponseInfo) pluginapi.RouteOutcome {
	var netErr net.Error
	timeout := errors.Is(info.Error, context.DeadlineExceeded) ||
		(errors.As(info.Error, &netErr) && netErr.Timeout())
	return pluginapi.RouteOutcome{
		Source:     source,
		Target:     pluginapi.RouteTarget{Provider: info.Provider, Model: info.Model},
		Success:    info.Error == nil && (info.StatusCode == 0 || info.StatusCode < 400),
		StatusCode: info.StatusCode,
		Latency:    info.Duration,
		Timeout:    timeout,
	}
}

// initPricing builds request tagging and the model pricing overrides that
// refine the registry's pricing for usage cost attribution.
func (b *bootstrap) initPricing() error {
	app := b.app

	taggingResult, err := tagging.New(b.ctx, b.appCfg, app.storage)
	if err != nil {
		return fmt.Errorf("failed to initialize tagging: %w", err)
	}
	app.tagging = taggingResult
	app.register(subsystemTagging, ownedByShutdown, app.tagging.Close)

	registry := app.providers.Registry
	pricingOverrideResult, err := pricingoverrides.New(b.ctx, b.appCfg, app.storage, registry, registry)
	if err != nil {
		return fmt.Errorf("failed to initialize model pricing overrides: %w", err)
	}
	app.pricingOverrides = pricingOverrideResult
	app.register(subsystemPricingOverrides, ownedByShutdown, app.pricingOverrides.Close)
	b.pricingResolver = usage.PricingResolver(registry)
	if app.pricingOverrides != nil && app.pricingOverrides.Service != nil {
		b.pricingResolver = app.pricingOverrides.Service
	}
	return nil
}
