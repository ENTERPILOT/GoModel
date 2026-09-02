package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/batch"
	"github.com/enterpilot/gomodel/internal/budget"
	"github.com/enterpilot/gomodel/internal/conversationstore"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/filestore"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/health"
	"github.com/enterpilot/gomodel/internal/ratelimit"
	"github.com/enterpilot/gomodel/internal/responsestore"
	"github.com/enterpilot/gomodel/internal/runtimesettings"
	"github.com/enterpilot/gomodel/internal/storage"
	"github.com/enterpilot/gomodel/internal/telemetry"
	"github.com/enterpilot/gomodel/internal/usage"
)

// initStorage opens the one storage connection every subsystem shares, then
// the runtime settings service that reconciles live configuration through it.
func (b *bootstrap) initStorage() error {
	app := b.app

	// One storage connection serves every subsystem. Each used to be able to
	// open its own, which meant a deployment with audit logging and usage
	// tracking both disabled opened a separate connection per subsystem to the
	// same database.
	sharedStorage, err := storage.New(b.ctx, b.appCfg.Storage.BackendConfig())
	if err != nil {
		return fmt.Errorf("failed to create storage: %w", err)
	}
	app.storage = sharedStorage
	app.register(subsystemStorage, ownedByShutdown, sharedStorage.Close)

	var registeredSettings []ext.RuntimeSetting
	if b.cfg.Extensions != nil {
		registeredSettings = b.cfg.Extensions.Settings()
	}
	app.runtimeSettings, err = runtimesettings.New(b.ctx, sharedStorage, registeredSettings)
	if err != nil {
		return fmt.Errorf("failed to initialize runtime settings: %w", err)
	}
	if app.runtimeSettings != nil {
		app.register(subsystemRuntimeSettings, ownedByShutdown, app.runtimeSettings.Close)
	}
	return nil
}

// initProviders composes every upstream client hook, then builds the
// providers. Hooks must be attached to the factory before the first provider
// exists, because providers capture the hook set at construction.
func (b *bootstrap) initProviders() error {
	app := b.app

	// Track real-traffic outcomes per provider/model for the dashboard's
	// provider status; hooks must be composed before any provider is created.
	b.requestHealth = health.NewTracker()
	b.cfg.Factory.AddHooks(b.requestHealth.Hooks())

	// An extension route selector observes every upstream attempt — primaries,
	// retries, and failovers — to steer adaptive load balancing. Like the
	// health tracker, its hooks must be attached before any provider exists.
	if b.cfg.Extensions != nil {
		b.routeSelector = b.cfg.Extensions.RouteSelector()
	}
	if b.routeSelector != nil {
		b.cfg.Factory.AddHooks(routeSelectorHooks(b.routeSelector))
	}
	// OpenTelemetry instruments provider calls through the same hooks, so it
	// too must exist before the first provider is constructed.
	if b.appCfg.OpenTelemetry.Enabled {
		metricsEndpoint := config.ResolveMetricsEndpointWithPprof(b.appCfg.Metrics.Endpoint, b.appCfg.Server.PprofEnabled)
		var err error
		app.telemetry, err = telemetry.New(b.ctx, b.appCfg.OpenTelemetry, metricsEndpoint, b.cfg.ProductName)
		if err != nil {
			return fmt.Errorf("failed to initialize opentelemetry: %w", err)
		}
		app.register(subsystemTelemetry, ownedByShutdown, app.telemetry.Close)
		b.cfg.Factory.AddHooks(app.telemetry.Hooks())
	}

	providerResult, err := providers.Init(b.ctx, b.cfg.AppConfig, b.cfg.Factory)
	if err != nil {
		return fmt.Errorf("failed to initialize providers: %w", err)
	}
	app.providers = providerResult
	app.register(subsystemProviders, ownedByShutdown, app.providers.Close)
	return nil
}

// initTracking builds the request-accounting subsystems: audit logging,
// usage tracking, and the budgets and rate limits that consume usage.
func (b *bootstrap) initTracking() error {
	app := b.app
	appCfg := b.appCfg

	auditResult, err := auditlog.New(b.ctx, appCfg, app.storage)
	if err != nil {
		return fmt.Errorf("failed to initialize audit logging: %w", err)
	}
	app.audit = auditResult
	app.register(subsystemAudit, ownedByShutdown, app.audit.Close)

	// Initialize usage tracking. Disabled tracking yields a noop logger.
	usageResult, err := usage.New(b.ctx, appCfg, app.storage)
	if err != nil {
		return fmt.Errorf("failed to initialize usage tracking: %w", err)
	}
	if usageResult == nil || usageResult.Logger == nil {
		if usageResult != nil {
			app.register(subsystemUsage, ownedByShutdown, usageResult.Close)
		}
		return errors.New("usage tracking initialization returned nil result")
	}
	app.usage = usageResult
	app.register(subsystemUsage, ownedByShutdown, app.usage.Close)

	var budgetResult *budget.Result
	if appCfg.Budgets.Enabled {
		budgetResult, err = budget.New(b.ctx, appCfg, app.storage, b.quotaTemplatesEnabled)
		if err != nil {
			return fmt.Errorf("failed to initialize budgets: %w", err)
		}
	} else {
		budgetResult = &budget.Result{}
		slog.Info("budgets disabled")
	}
	app.budgets = budgetResult
	app.register(subsystemBudgets, ownedByShutdown, app.budgets.Close)

	var rateLimitResult *ratelimit.Result
	if appCfg.RateLimits.Enabled {
		rateLimitResult, err = ratelimit.New(b.ctx, appCfg, app.storage, b.quotaTemplatesEnabled)
		if err != nil {
			return fmt.Errorf("failed to initialize rate limits: %w", err)
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
	return nil
}

// initStores opens the lifecycle stores behind the OpenAI-compatible Batches,
// Files, Responses, and Conversations surfaces.
func (b *bootstrap) initStores() error {
	app := b.app

	// Initialize batch lifecycle storage.
	batchResult, err := batch.New(b.ctx, app.storage)
	if err != nil {
		return fmt.Errorf("failed to initialize batch storage: %w", err)
	}
	app.batch = batchResult
	app.register(subsystemBatch, ownedByShutdown, app.batch.Close)

	// Initialize file provider mapping storage for OpenAI-compatible Files/Batches workflows.
	fileStoreResult, err := filestore.New(b.ctx, app.storage)
	if err != nil {
		return fmt.Errorf("failed to initialize file mapping storage: %w", err)
	}
	app.fileStore = fileStoreResult
	app.register(subsystemFileStore, ownedByShutdown, app.fileStore.Close)

	// Initialize Responses/Conversations lifecycle persistence so agentic
	// response chains and conversation history land in storage instead of
	// accumulating in process memory.
	responseStoreResult, err := responsestore.New(b.ctx, app.storage)
	if err != nil {
		return fmt.Errorf("failed to initialize response snapshot storage: %w", err)
	}
	app.responseStore = responseStoreResult
	app.register(subsystemResponseStore, ownedByServer, app.responseStore.Close)

	conversationStoreResult, err := conversationstore.New(b.ctx, app.storage)
	if err != nil {
		return fmt.Errorf("failed to initialize conversation storage: %w", err)
	}
	app.conversations = conversationStoreResult
	app.register(subsystemConversationStore, ownedByServer, app.conversations.Close)
	return nil
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
