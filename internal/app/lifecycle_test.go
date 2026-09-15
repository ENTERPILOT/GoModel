package app

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/stretchr/testify/require"
)

// TestNewThenShutdown covers the whole lifecycle: every subsystem is built on
// the one shared storage connection, and Shutdown closes them in order without
// error — including the connection itself, which now closes last.
//
// Nothing exercised this end to end before, which is how a nil-handling bug in
// the shutdown ordering could reach a running binary.
func TestNewThenShutdown(t *testing.T) {
	// Load() reads the ambient environment, so the test pins the storage
	// location and keeps startup local: no admin surface, no model discovery.
	t.Chdir(t.TempDir())
	t.Setenv("STORAGE_TYPE", "sqlite")
	t.Setenv("SQLITE_PATH", filepath.Join(t.TempDir(), "lifecycle.db"))
	t.Setenv("ADMIN_UI_ENABLED", "false")
	t.Setenv("ADMIN_ENDPOINTS_ENABLED", "false")
	t.Setenv("MCP_ENABLED", "false")

	loaded, err := config.Load()
	require.NoError(t, err)

	ctx := context.Background()
	app, err := New(ctx, Config{
		AppConfig: loaded,
		Factory:   providers.NewProviderFactory(),
	})
	require.NoError(t, err)
	err = app.Shutdown(ctx)
	require.NoError(t, err)
	err = // Shutdown is idempotent: the process may call it from both the signal
		// handler and the normal exit path.
		app.Shutdown(ctx)
	require.NoError(t, err)
}

func TestFailedConstructionDoesNotRebindAuthenticationEventRecorder(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("STORAGE_TYPE", "sqlite")
	t.Setenv("SQLITE_PATH", filepath.Join(t.TempDir(), "failed-reload.db"))
	t.Setenv("ADMIN_UI_ENABLED", "false")
	t.Setenv("ADMIN_ENDPOINTS_ENABLED", "false")
	t.Setenv("MCP_ENABLED", "false")

	loaded, err := config.Load()
	require.NoError(t, err)

	// Force a failure late in construction, after the replacement audit logger
	// exists. This models a rejected reload while an older generation serves.
	enabled := true
	loaded.Config.Cache.Response.Semantic = &config.SemanticCacheConfig{
		Enabled:  &enabled,
		Embedder: config.EmbedderConfig{Provider: "missing", Model: "embedding-model"},
	}

	previous := &appAuthenticationEventRecorder{}
	authenticator := &recorderAwareAppAuthenticator{recorder: previous}
	registry := &ext.Registry{}
	registry.RegisterAuthenticator(authenticator)

	application, err := New(context.Background(), Config{
		AppConfig:  loaded,
		Factory:    providers.NewProviderFactory(),
		Extensions: registry,
	})
	if err == nil {
		_ = application.Shutdown(context.Background())
		t.Fatal("New succeeded with a missing semantic-cache embedder provider")
	}
	require.Contains(t, err.Error(), "failed to initialize response cache")
	require.Equal(t, previous, authenticator.recorder)
}
