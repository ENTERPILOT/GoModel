package mcpgateway

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func newSQLiteMCPStore(t *testing.T) *SQLiteStore {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	store, err := NewSQLiteStore(db)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	return store
}

func TestSQLiteStoreRoundTrip(t *testing.T) {
	t.Parallel()
	store := newSQLiteMCPStore(t)
	ctx := context.Background()

	server := ManagedServer{
		Name:               "github",
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
	if err := store.Upsert(ctx, server); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	got, err := store.Get(ctx, "github")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.URL != server.URL || got.Transport != "http" || !got.Enabled {
		t.Fatalf("Get() = %+v, want round-tripped row", got)
	}
	if got.Headers["Authorization"] != "Bearer secret" {
		t.Fatalf("Get().Headers = %v, want secret preserved", got.Headers)
	}
	if len(got.AllowedTools) != 1 || got.AllowedTools[0] != "create_issue" {
		t.Fatalf("Get().AllowedTools = %v", got.AllowedTools)
	}
	if got.ToolTimeoutSeconds != 45 {
		t.Fatalf("Get().ToolTimeoutSeconds = %d, want 45", got.ToolTimeoutSeconds)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Fatalf("Get() timestamps not stamped: %+v", got)
	}

	// Update preserves CreatedAt and bumps the row.
	server.Description = "updated"
	server.Enabled = false
	if err := store.Upsert(ctx, server); err != nil {
		t.Fatalf("Upsert(update) error = %v", err)
	}
	updated, err := store.Get(ctx, "github")
	if err != nil {
		t.Fatalf("Get(updated) error = %v", err)
	}
	if updated.Description != "updated" || updated.Enabled {
		t.Fatalf("Get(updated) = %+v, want updated row", updated)
	}
	if !updated.CreatedAt.Equal(got.CreatedAt) {
		t.Fatalf("Get(updated).CreatedAt = %v, want original %v preserved", updated.CreatedAt, got.CreatedAt)
	}

	list, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("len(List()) = %d, want 1", len(list))
	}

	if err := store.Delete(ctx, "github"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := store.Get(ctx, "github"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(deleted) error = %v, want ErrNotFound", err)
	}
	if err := store.Delete(ctx, "github"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete(missing) error = %v, want ErrNotFound", err)
	}
}

func TestManagedServerValidateRejectsStdio(t *testing.T) {
	t.Parallel()
	server := ManagedServer{Name: "local", Transport: "stdio"}
	if err := server.Validate(); err == nil {
		t.Fatalf("Validate(stdio) should fail: runtime-registered subprocesses are forbidden")
	}
}

func TestManagedServerValidateRequiresURL(t *testing.T) {
	t.Parallel()
	server := ManagedServer{Name: "web", Transport: "http"}
	if err := server.Validate(); err == nil {
		t.Fatalf("Validate(http without url) should fail")
	}
	server.URL = "ftp://nope"
	if err := server.Validate(); err == nil {
		t.Fatalf("Validate(non-http url) should fail")
	}
	server.URL = "https://example.com/mcp"
	if err := server.Validate(); err != nil {
		t.Fatalf("Validate(valid) error = %v", err)
	}
}

func TestManagedServerSpecDefaultsTimeout(t *testing.T) {
	t.Parallel()
	spec := ManagedServer{Name: "web", URL: "https://example.com/mcp", Transport: "http"}.Spec()
	if spec.ToolTimeout <= 0 {
		t.Fatalf("Spec().ToolTimeout = %v, want default applied", spec.ToolTimeout)
	}
	if spec.Managed {
		t.Fatalf("Spec().Managed = true, want false for store rows")
	}
}
