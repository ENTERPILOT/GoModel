//go:build e2e

package e2e

import (
	"context"
	"database/sql"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"gomodel/internal/admin"
	"gomodel/internal/admin/dashboard"
	"gomodel/internal/providers"
	"gomodel/internal/server"
	"gomodel/internal/usage"
)

type e2eServerOptions struct {
	masterKey             string
	adminEndpointsEnabled bool
	adminUIEnabled        bool
	adminUsageReader      usage.UsageReader
	usageLogger           usage.LoggerInterface
	providerType          string
}

type e2eUsageFixture struct {
	reader usage.UsageReader
	logger *usage.Logger
}

// setupAuthServer creates a new server instance with authentication enabled.
func setupAuthServer(t *testing.T, masterKey string) *server.Server {
	t.Helper()

	return setupE2EServer(t, e2eServerOptions{
		masterKey: masterKey,
	})
}

// setupAdminServer creates a new server instance with admin features configured.
func setupAdminServer(t *testing.T, masterKey string, endpointsEnabled, uiEnabled bool) *httptest.Server {
	t.Helper()

	srv := setupE2EServer(t, e2eServerOptions{
		masterKey:             masterKey,
		adminEndpointsEnabled: endpointsEnabled,
		adminUIEnabled:        uiEnabled,
		providerType:          "test",
	})
	return httptest.NewServer(srv)
}

func setupE2EAdminServer(t *testing.T, opts e2eServerOptions) *httptest.Server {
	t.Helper()

	opts.adminEndpointsEnabled = true
	if opts.providerType == "" {
		opts.providerType = "test"
	}
	return httptest.NewServer(setupE2EServer(t, opts))
}

func setupE2EServer(t *testing.T, opts e2eServerOptions) *server.Server {
	t.Helper()

	registry := setupE2ERegistry(t, opts.providerType)
	router, err := providers.NewRouter(registry)
	require.NoError(t, err, "failed to create router")

	cfg := &server.Config{
		MasterKey:             opts.masterKey,
		UsageLogger:           opts.usageLogger,
		AdminEndpointsEnabled: opts.adminEndpointsEnabled,
	}

	if opts.adminEndpointsEnabled {
		cfg.AdminHandler = admin.NewHandler(opts.adminUsageReader, registry)
	}

	if opts.adminUIEnabled {
		cfg.AdminUIEnabled = true
		dashHandler, err := dashboard.New()
		require.NoError(t, err, "failed to create dashboard handler")
		cfg.DashboardHandler = dashHandler
	}

	return server.New(router, cfg)
}

func setupE2ERegistry(t *testing.T, providerType string) *providers.ModelRegistry {
	t.Helper()

	testProvider := NewTestProvider(mockLLMURL, "sk-test-key-12345")
	registry := providers.NewModelRegistry()
	if providerType == "" {
		registry.RegisterProvider(testProvider)
	} else {
		registry.RegisterProviderWithType(testProvider, providerType)
	}

	require.NoError(t, registry.Initialize(context.Background()), "failed to initialize registry")
	return registry
}

func setupSQLiteUsageFixture(t *testing.T) *e2eUsageFixture {
	t.Helper()

	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() {
		require.NoError(t, db.Close())
	})

	store, err := usage.NewSQLiteStore(db, 0)
	require.NoError(t, err)

	reader, err := usage.NewSQLiteReader(db)
	require.NoError(t, err)

	cfg := usage.DefaultConfig()
	cfg.Enabled = true
	cfg.BufferSize = 10
	cfg.FlushInterval = time.Hour
	logger := usage.NewLogger(store, cfg)
	t.Cleanup(func() {
		require.NoError(t, logger.Close())
	})

	return &e2eUsageFixture{
		reader: reader,
		logger: logger,
	}
}

func (f *e2eUsageFixture) flush(t *testing.T) {
	t.Helper()

	require.NoError(t, f.logger.Close())
}
