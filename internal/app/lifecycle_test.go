package app

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/providers"
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
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}

	ctx := context.Background()
	app, err := New(ctx, Config{
		AppConfig: loaded,
		Factory:   providers.NewProviderFactory(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := app.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	// Shutdown is idempotent: the process may call it from both the signal
	// handler and the normal exit path.
	if err := app.Shutdown(ctx); err != nil {
		t.Fatalf("second Shutdown: %v", err)
	}
}
