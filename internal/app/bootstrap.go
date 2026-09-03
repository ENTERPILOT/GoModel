package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/guardrails"
	"github.com/enterpilot/gomodel/internal/httpclient"
	"github.com/enterpilot/gomodel/internal/live"
	"github.com/enterpilot/gomodel/internal/providers/health"
	"github.com/enterpilot/gomodel/internal/responsecache"
	"github.com/enterpilot/gomodel/internal/server"
	"github.com/enterpilot/gomodel/internal/usage"
)

// bootstrap carries the state New threads through its initialization phases:
// the inputs, the App under construction, and the values one phase produces
// for a later one. Subsystems that live on the App are read back from it, so
// only cross-phase intermediates are held here.
//
// Phases run in the order phases() lists them. Several depend on that order
// (hooks before providers, audit before workflows, every fallible step before
// the server binds); the comment on each phase says why.
type bootstrap struct {
	ctx    context.Context
	cfg    Config
	appCfg *config.Config
	app    *App

	// Decided in the prologue, read by several phases.
	quotaTemplatesEnabled bool
	routeSelector         ext.RouteSelector
	requestHealth         *health.Tracker

	// Produced by the catalog phases for the server phases.
	managedProviderNames []string
	pricingResolver      usage.PricingResolver
	seedGuardrails       []guardrails.Definition
	featureCaps          core.WorkflowFeatures

	// Produced by the server phases.
	provider                 core.RoutableProvider
	translatedRequestPatcher server.TranslatedRequestPatcher
	batchRequestPreparer     server.BatchRequestPreparer
	swaggerEnabled           bool
	serverUsageLogger        usage.LoggerInterface
	usageReader              usage.UsageReader
	serverCfg                *server.Config
	livePublishersEnabled    bool
	responseCache            *responsecache.ResponseCacheMiddleware
}

// newBootstrap runs the infallible prologue: process-wide settings that must
// be in place before any subsystem constructs, and the App shell with its
// first registered subsystem.
func newBootstrap(ctx context.Context, cfg Config) *bootstrap {
	appCfg := cfg.AppConfig.Config
	// Install config-file HTTP timeouts before any provider constructs a
	// transport; env vars still take precedence inside httpclient.
	httpclient.SetConfiguredTimeouts(appCfg.HTTP.Timeout, appCfg.HTTP.ResponseHeaderTimeout)
	if appCfg.Offline {
		catalog := "disabled"
		if appCfg.Cache.Model.ModelList.URL != "" {
			catalog = "local file"
		}
		slog.Info("offline mode: update check disabled, remote model catalog download disabled; only configured providers and declared endpoints are contacted",
			"model_catalog", catalog)
	}
	if appCfg.HTTP.TLS.InsecureSkipVerify {
		slog.Warn("outbound TLS certificate verification is disabled (http.tls.insecure_skip_verify); do not run this in production")
	}
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

	return &bootstrap{
		ctx:                   ctx,
		cfg:                   cfg,
		appCfg:                appCfg,
		app:                   app,
		quotaTemplatesEnabled: cfg.Extensions != nil && cfg.Extensions.HasCapability(ext.CapabilityQuotaTemplates),
	}
}

// phases is the construction order. It is load-bearing: see the comment on
// each phase for what it requires from the ones before it.
func (b *bootstrap) phases() []func() error {
	return []func() error{
		b.initStorage,
		b.initProviders,
		b.initTracking,
		b.initStores,
		b.initModelCatalog,
		b.initPricing,
		b.initWorkflows,
		b.initServerDependencies,
		b.initServerConfig,
		b.initAdmin,
		b.initResponseCache,
		b.initServer,
	}
}

// fail releases every subsystem registered so far, in reverse construction
// order, and reports the phase error together with any close error.
func (b *bootstrap) fail(err error) error {
	if closeErr := b.app.unwind(); closeErr != nil {
		return fmt.Errorf("%w (also: close error: %v)", err, closeErr)
	}
	return err
}
