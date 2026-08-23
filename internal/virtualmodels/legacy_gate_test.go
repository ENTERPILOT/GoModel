package virtualmodels

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/storage"
)

func newSQLiteStorage(t *testing.T) storage.SQLiteStorage {
	t.Helper()
	conn, err := storage.NewSQLite(storage.SQLiteConfig{Path: filepath.Join(t.TempDir(), "vm.db")})
	if err != nil {
		t.Fatalf("storage.NewSQLite() error = %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// A database from before v0.1.44 with aliases or access overrides that the
// (removed) seed never imported must not start: the policies would otherwise
// vanish and previously restricted models would open up.
func TestNew_RefusesUnmigratedLegacyTables(t *testing.T) {
	ctx := context.Background()
	conn := newSQLiteStorage(t)
	db := conn.DB()
	for _, stmt := range []string{
		`CREATE TABLE model_overrides (selector TEXT PRIMARY KEY, provider_name TEXT, model TEXT, user_paths TEXT, created_at INTEGER, updated_at INTEGER)`,
		`INSERT INTO model_overrides VALUES ('openai/gpt-4o', 'openai', 'gpt-4o', '["/team"]', 0, 0)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seed legacy table: %v", err)
		}
	}

	_, err := New(ctx, &config.Config{}, conn, testCatalog(), nil)
	if err == nil || !strings.Contains(err.Error(), "model_overrides") || !strings.Contains(err.Error(), "v0.1.44") {
		t.Fatalf("New() error = %v, want refusal naming model_overrides and the upgrade path", err)
	}

	// Once virtual_models holds rows the migration has happened (or the
	// operator manages models here already) and the legacy rows are ignored.
	if _, err := db.Exec(`INSERT INTO virtual_models (source, targets, user_paths, enabled, created_at, updated_at) VALUES ('openai/gpt-4o', '[]', '["/team"]', 1, 0, 0)`); err != nil {
		t.Fatalf("insert virtual model: %v", err)
	}
	result, err := New(ctx, &config.Config{}, conn, testCatalog(), nil)
	if err != nil {
		t.Fatalf("New() with migrated data error = %v", err)
	}
	_ = result.Close()
}

func TestNew_IgnoresMissingOrEmptyLegacyTables(t *testing.T) {
	ctx := context.Background()
	conn := newSQLiteStorage(t)

	// No legacy tables at all: a database created after the seed shipped.
	result, err := New(ctx, &config.Config{}, conn, testCatalog(), nil)
	if err != nil {
		t.Fatalf("New() without legacy tables error = %v", err)
	}
	_ = result.Close()

	// Legacy tables left behind but already drained are just as fine.
	if _, err := conn.DB().Exec(`CREATE TABLE aliases (name TEXT PRIMARY KEY, target_model TEXT, target_provider TEXT, description TEXT, enabled INTEGER, created_at INTEGER, updated_at INTEGER)`); err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	result, err = New(ctx, &config.Config{}, conn, testCatalog(), nil)
	if err != nil {
		t.Fatalf("New() with empty legacy table error = %v", err)
	}
	_ = result.Close()
}
