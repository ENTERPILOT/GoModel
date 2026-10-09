package run

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/mcpgateway"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/storage"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// A reload must not drop a dashboard-managed provider credential or MCP
// server that is serving because one of its references stopped resolving.
// The reload is rejected instead, as for a config.yaml reference, and the
// serving generation keeps running the entity. An entity that was already
// skipped at startup does not block a reload.
func TestReloadKeepsServingEntitiesWhoseReferencesFail(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("server:\n  master_key: ${vault:master}\n"), 0o600))
	dbPath := filepath.Join(dir, "gomodel.db")
	t.Setenv("GOMODEL_MASTER_KEY", "")
	t.Setenv("PORT", "0")
	t.Setenv("GOMODEL_OFFLINE", "true")
	t.Setenv("STORAGE_TYPE", "sqlite")
	t.Setenv("SQLITE_PATH", dbPath)
	t.Setenv("ADMIN_UI_ENABLED", "false")
	t.Setenv("MCP_ENABLED", "true")
	t.Setenv("LOG_FORMAT", "text")
	seedReloadEntities(t, dbPath)

	var mu sync.Mutex
	vault := map[string]string{"master": "m1", "served": "sk-served", "header": "Bearer token"}
	set := func(reference, value string) {
		mu.Lock()
		defer mu.Unlock()
		if value == "" {
			delete(vault, reference)
		} else {
			vault[reference] = value
		}
	}
	resolver := config.SecretResolverFunc(func(_ context.Context, reference string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		value, ok := vault[reference]
		if !ok {
			return "", errors.New("secret not found")
		}
		return value, nil
	})

	logs := &syncBuffer{}
	t.Cleanup(func() {
		if t.Failed() {
			t.Log(logs.String())
		}
	})
	setup := make(chan *config.Secrets, 1)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	served := make(chan error, 1)
	go func() {
		served <- Run(ctx, Options{
			Args:   []string{},
			Stdout: logs,
			Stderr: logs,
			SetupConfig: func(_ context.Context, result *config.LoadResult) error {
				setup <- result.Secrets
				return result.Secrets.Register("vault", resolver)
			},
			ReloadConfig: func(_ context.Context, result *config.LoadResult) error {
				return result.Secrets.Register("vault", resolver)
			},
		})
	}()

	var secrets *config.Secrets
	select {
	case secrets = <-setup:
	case err := <-served:
		t.Fatalf("Run returned before serving: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for the first generation")
	}
	waitForLog := func(what string, count int) string {
		t.Helper()
		var line string
		require.Eventually(t, func() bool {
			lines := matchingLines(logs.String(), what)
			if len(lines) < count {
				return false
			}
			line = lines[count-1]
			return true
		}, 30*time.Second, 20*time.Millisecond, "never logged %q", what)
		return line
	}
	waitForLog("starting server", 1)
	require.Contains(t, waitForLog("failed to apply stored provider credential", 1), "provider=broken", "the credential broken at startup is skipped")

	// The serving credential's key stops resolving, and a rotated master key
	// asks for a reload.
	set("served", "")
	set("master", "m2")
	secrets.NotifyChanged()
	rejected := waitForLog("reload failed; keeping the running configuration", 1)
	assert.Contains(t, rejected, "provider_credentials.served.api_keys[0]")

	// It resolves, but to a blank key the provider cannot use.
	set("served", "  ")
	secrets.NotifyChanged()
	rejected = waitForLog("reload failed; keeping the running configuration", 2)
	assert.Contains(t, rejected, `\"served\": credentials did not resolve`)

	// Then the serving MCP server's header.
	set("served", "sk-served")
	set("header", "")
	secrets.NotifyChanged()
	rejected = waitForLog("reload failed; keeping the running configuration", 3)
	assert.Contains(t, rejected, "mcp_servers.github.headers.Authorization")
	assert.NotContains(t, logs.String(), "configuration reloaded")

	// Once both resolve again, the reload goes through, although the
	// credential skipped at startup still does not resolve.
	set("header", "Bearer token")
	secrets.NotifyChanged()
	waitForLog("configuration reloaded", 1)

	cancel()
	select {
	case err := <-served:
		require.NoError(t, err)
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not stop")
	}
}

func seedReloadEntities(t *testing.T, dbPath string) {
	t.Helper()
	ctx := context.Background()
	shared, err := storage.New(ctx, storage.Config{Type: storage.TypeSQLite, SQLite: storage.SQLiteConfig{Path: dbPath}})
	require.NoError(t, err)
	defer func() { _ = shared.Close() }()
	db, err := sqlx.NewSQLite(shared.(storage.SQLiteStorage).DB())
	require.NoError(t, err)

	creds, err := providers.NewSQLCredentialStore(ctx, db)
	require.NoError(t, err)
	for _, cred := range []providers.ManagedProviderCredential{
		{Name: "served", Type: "openai", BaseURL: "http://127.0.0.1:1/v1", APIKeys: []string{"${vault:served}"}, Enabled: true},
		{Name: "broken", Type: "openai", BaseURL: "http://127.0.0.1:1/v1", APIKeys: []string{"${vault:missing}"}, Enabled: true},
	} {
		require.NoError(t, creds.Upsert(ctx, cred))
	}
	servers, err := mcpgateway.NewSQLStore(ctx, db)
	require.NoError(t, err)
	require.NoError(t, servers.Upsert(ctx, mcpgateway.ManagedServer{
		Name: "github", URL: "http://127.0.0.1:1/mcp", Transport: "http",
		Headers: map[string]string{"Authorization": "${vault:header}"}, Enabled: true,
	}))
}

func matchingLines(logs, what string) []string {
	var lines []string
	for line := range strings.SplitSeq(logs, "\n") {
		if strings.Contains(line, what) {
			lines = append(lines, line)
		}
	}
	return lines
}
