package mcpgateway

import (
	"context"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/internal/storage/mongotest"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
	"github.com/stretchr/testify/require"
)

// runStoreSuite exercises behaviour every Store implementation owes its
// callers, against each backend available in this environment.
func runStoreSuite(t *testing.T, body func(t *testing.T, store Store)) {
	t.Helper()
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, err := NewSQLStore(context.Background(), db)
		require.NoError(t, err)

		t.Cleanup(func() { _ = store.Close() })
		body(t, store)
	})
	mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
		store, err := NewMongoDBStore(db)
		require.NoError(t, err)

		t.Cleanup(func() { _ = store.Close() })
		body(t, store)
	})
}

func TestStoreRoundTrip(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()

		server := ManagedServer{
			Name:               "github",
			DisplayName:        "GitHub MCP",
			URL:                "https://api.githubcopilot.com/mcp",
			Transport:          "http",
			Headers:            map[string]string{"Authorization": "Bearer secret"},
			Description:        "GitHub tools",
			Enabled:            true,
			AllowedTools:       []string{"create_issue"},
			DisallowedTools:    []string{"delete_repo"},
			UserPaths:          []string{"/team-a"},
			ToolTimeoutSeconds: 45,
		}
		err := store.Upsert(ctx, server)
		require.NoError(t, err)

		got, err := store.Get(ctx, "github")
		require.NoError(t, err)
		require.Equal(t, server.URL, got.URL)
		require.Equal(t, "http", got.Transport)
		require.True(t, got.Enabled, "Get() = %+v, want round-tripped row", got)
		require.Equal(t, "github", got.Name)
		require.Equal(t, "GitHub MCP", got.DisplayName)
		require.Equal(t, "Bearer secret", got.Headers["Authorization"], "Get().Headers = %v, want secret preserved", got.Headers)
		require.Len(t, got.AllowedTools, 1)
		require.Equal(t, "create_issue", got.AllowedTools[0])
		require.Equal(t, 45, got.ToolTimeoutSeconds)
		require.False(t, got.CreatedAt.IsZero())
		require.False(t, got.UpdatedAt.IsZero(), "Get() timestamps not stamped: %+v", got)

		// Update preserves CreatedAt and bumps the row.
		server.Description = "updated"
		server.DisplayName = "GitHub 工具"
		server.Enabled = false
		err = store.Upsert(ctx, server)
		require.NoError(t, err)

		updated, err := store.Get(ctx, "github")
		require.NoError(t, err)
		require.Equal(t, "updated", updated.Description)
		require.False(t, updated.Enabled, "Get(updated) = %+v, want updated row", updated)
		require.Equal(t, "github", updated.Name)
		require.Equal(t, "GitHub 工具", updated.DisplayName)
		require.True(t, updated.CreatedAt.Equal(got.CreatedAt), "Get(updated).CreatedAt = %v, want original %v preserved", updated.CreatedAt, got.CreatedAt)

		list, err := store.List(ctx)
		require.NoError(t, err)
		require.Len(t, list, 1)
		err = store.Delete(ctx, "github")
		require.NoError(t, err)
		_, err = store.Get(ctx, "github")
		require.ErrorIs(t, err, ErrNotFound)
		err = store.Delete(ctx, "github")
		require.ErrorIs(t, err, ErrNotFound)
	})
}

// TestSQLStoreMigratesDisplayName starts from the pre-display_name table shape
// a long-lived deployment still has on disk.
func TestSQLStoreMigratesDisplayName(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		err := db.Schema(ctx, `
			CREATE TABLE mcp_servers (
				name TEXT PRIMARY KEY, url TEXT NOT NULL DEFAULT '', transport TEXT NOT NULL DEFAULT 'http',
				headers TEXT NOT NULL DEFAULT '{}', description TEXT NOT NULL DEFAULT '',
				enabled `+sqlx.TypeBool+` NOT NULL DEFAULT TRUE,
				allowed_tools TEXT NOT NULL DEFAULT '[]', disallowed_tools TEXT NOT NULL DEFAULT '[]',
				user_paths TEXT NOT NULL DEFAULT '[]', tool_timeout_seconds INTEGER NOT NULL DEFAULT 0,
				created_at `+sqlx.TypeInt64+` NOT NULL, updated_at `+sqlx.TypeInt64+` NOT NULL
			)`)
		require.NoError(t, err)
		_, err = db.Exec(ctx,
			`INSERT INTO mcp_servers (name, created_at, updated_at) VALUES (?, ?, ?)`, "linear", 1, 1)
		require.NoError(t, err)

		store, err := NewSQLStore(ctx, db)
		require.NoError(t, err)

		server, err := store.Get(ctx, "linear")
		require.NoError(t, err)
		require.Equal(t, "linear", server.DisplayName)
	})
}

func TestManagedServerValidateRejectsStdio(t *testing.T) {
	t.Parallel()
	server := ManagedServer{Name: "local", Transport: "stdio"}
	require.Error(t, server.Validate())
}

func TestManagedServerValidateRequiresURL(t *testing.T) {
	t.Parallel()
	server := ManagedServer{Name: "web", Transport: "http"}
	require.Error(t, server.Validate())

	server.URL = "ftp://nope"
	require.Error(t, server.Validate())

	server.URL = "https://example.com/mcp"
	err := server.Validate()
	require.NoError(t, err)
}

func TestManagedServerValidateRejectsInvalidUserPath(t *testing.T) {
	t.Parallel()
	server := ManagedServer{
		Name:      "web",
		URL:       "https://example.com/mcp",
		Transport: "http",
		UserPaths: []string{"/team/../admin"},
	}
	require.Error(t, server.Validate())
}

func TestManagedServerSpecDefaultsTimeout(t *testing.T) {
	t.Parallel()
	spec := ManagedServer{Name: "web", URL: "https://example.com/mcp", Transport: "http"}.Spec()
	require.Greater(t, spec.ToolTimeout, time.Duration(0))
	require.False(t, spec.Managed)
}
