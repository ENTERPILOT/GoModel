package virtualmodels

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"gomodel/internal/storage"
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

// createLegacyTables creates the legacy aliases and model_overrides tables so
// the self-contained seed readers have something to read.
func createLegacyTables(t *testing.T, db *sql.DB) {
	t.Helper()
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS aliases (
			name TEXT PRIMARY KEY,
			target_model TEXT NOT NULL,
			target_provider TEXT NOT NULL DEFAULT '',
			description TEXT NOT NULL DEFAULT '',
			enabled INTEGER NOT NULL DEFAULT 1,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS model_overrides (
			selector TEXT PRIMARY KEY,
			provider_name TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			user_paths TEXT NOT NULL DEFAULT '[]',
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("create legacy table: %v", err)
		}
	}
}

func insertLegacyAlias(t *testing.T, db *sql.DB, name, targetModel, targetProvider string, enabled bool) {
	t.Helper()
	now := time.Now().UTC().Unix()
	en := 0
	if enabled {
		en = 1
	}
	if _, err := db.Exec(
		`INSERT INTO aliases (name, target_model, target_provider, description, enabled, created_at, updated_at) VALUES (?, ?, ?, '', ?, ?, ?)`,
		name, targetModel, targetProvider, en, now, now,
	); err != nil {
		t.Fatalf("insert legacy alias: %v", err)
	}
}

func insertLegacyOverride(t *testing.T, db *sql.DB, selector, providerName, model, userPathsJSON string) {
	t.Helper()
	now := time.Now().UTC().Unix()
	if _, err := db.Exec(
		`INSERT INTO model_overrides (selector, provider_name, model, user_paths, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		selector, providerName, model, userPathsJSON, now, now,
	); err != nil {
		t.Fatalf("insert legacy override: %v", err)
	}
}

func TestSeedFromLegacy_CopiesAndIsIdempotent(t *testing.T) {
	t.Parallel()
	conn := newSQLiteStorage(t)
	ctx := context.Background()
	db := conn.DB()

	createLegacyTables(t, db)
	insertLegacyAlias(t, db, "fast", "gpt-4o", "openai", true)
	insertLegacyOverride(t, db, "openai/gpt-4o", "openai", "gpt-4o", `["/team"]`)

	vmStore, err := NewSQLiteStore(db)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	if err := seedFromLegacy(ctx, vmStore, conn); err != nil {
		t.Fatalf("seedFromLegacy() error = %v", err)
	}

	assertSeeded := func() {
		t.Helper()
		got, err := vmStore.List(ctx)
		if err != nil {
			t.Fatalf("List() error = %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("len(List()) = %d, want 2 (%#v)", len(got), got)
		}
		byKind := map[string]VirtualModel{}
		for _, vm := range got {
			byKind[vm.Kind()] = vm
		}
		if r, ok := byKind[KindRedirect]; !ok || r.Source != "fast" || len(r.Targets) != 1 {
			t.Fatalf("redirect not seeded correctly: %#v", byKind)
		}
		if p, ok := byKind[KindPolicy]; !ok || p.Source != "openai/gpt-4o" || len(p.UserPaths) != 1 {
			t.Fatalf("policy not seeded correctly: %#v", byKind)
		}
	}
	assertSeeded()

	// Idempotent: a second run with the table already populated is a no-op.
	if err := seedFromLegacy(ctx, vmStore, conn); err != nil {
		t.Fatalf("seedFromLegacy() second run error = %v", err)
	}
	assertSeeded()
}

func TestSeedFromLegacy_CollisionFailsClosed(t *testing.T) {
	t.Parallel()
	conn := newSQLiteStorage(t)
	ctx := context.Background()
	db := conn.DB()

	createLegacyTables(t, db)
	// An alias and an access override that share the same source string.
	insertLegacyAlias(t, db, "gpt-4o", "gpt-4o-real", "openai", true)
	insertLegacyOverride(t, db, "gpt-4o", "", "gpt-4o", `["/team"]`)

	vmStore, err := NewSQLiteStore(db)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	// A name shared by an alias and an access override must fail the migration
	// rather than silently dropping the access control.
	if err := seedFromLegacy(ctx, vmStore, conn); err == nil {
		t.Fatalf("seedFromLegacy() error = nil, want migration conflict error")
	}
}

func TestSeedFromLegacy_MissingLegacyTablesIsNoOp(t *testing.T) {
	t.Parallel()
	conn := newSQLiteStorage(t)
	ctx := context.Background()

	vmStore, err := NewSQLiteStore(conn.DB())
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	// No legacy tables exist; seeding must succeed as a no-op.
	if err := seedFromLegacy(ctx, vmStore, conn); err != nil {
		t.Fatalf("seedFromLegacy() error = %v, want nil", err)
	}
	got, err := vmStore.List(ctx)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("len(List()) = %d, want 0", len(got))
	}
}
