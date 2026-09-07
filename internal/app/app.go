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
	"sync"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/authkeys"
	"github.com/enterpilot/gomodel/internal/batch"
	"github.com/enterpilot/gomodel/internal/budget"
	"github.com/enterpilot/gomodel/internal/conversationstore"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/filestore"
	"github.com/enterpilot/gomodel/internal/guardrails"
	"github.com/enterpilot/gomodel/internal/httpclient"
	"github.com/enterpilot/gomodel/internal/live"
	"github.com/enterpilot/gomodel/internal/mcpgateway"
	"github.com/enterpilot/gomodel/internal/pricingoverrides"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/ratelimit"
	"github.com/enterpilot/gomodel/internal/responsestore"
	"github.com/enterpilot/gomodel/internal/runtimesettings"
	"github.com/enterpilot/gomodel/internal/server"
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

// New creates a new App with all dependencies initialized.
// The caller must call Shutdown to release resources.
//
// Construction runs as an ordered list of phases (see bootstrap.phases). A
// failing phase unwinds every subsystem registered so far before the error
// is returned.
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

	// Outbound trust must be in place before any provider builds a transport,
	// and a missing CA file is a startup error rather than a silent fallback.
	// On a reload the generation that keeps serving must not inherit settings
	// its replacement was rejected with, so the previous configuration is put
	// back whenever construction fails after this point.
	previousTLS := httpclient.SnapshotTLS()
	tlsCfg := cfg.AppConfig.Config.HTTP.TLS
	if err := httpclient.SetConfiguredTLS(httpclient.TLSSettings{
		CAFile:             tlsCfg.CAFile,
		ClientCertFile:     tlsCfg.ClientCertFile,
		ClientKeyFile:      tlsCfg.ClientKeyFile,
		InsecureSkipVerify: tlsCfg.InsecureSkipVerify,
	}); err != nil {
		return nil, err
	}

	b := newBootstrap(ctx, cfg)
	for _, phase := range b.phases() {
		if err := phase(); err != nil {
			httpclient.RestoreTLS(previousTLS)
			return nil, b.fail(err)
		}
	}
	return b.app, nil
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
