package virtualmodels

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/storage"
)

// failingUpsertStore wraps a Store and fails Upsert after failAfter successful
// writes, to exercise the seed's partial-write rollback.
type failingUpsertStore struct {
	Store
	failAfter int
	count     int
}

func (s *failingUpsertStore) Upsert(ctx context.Context, vm VirtualModel) error {
	s.count++
	if s.count > s.failAfter {
		return errors.New("simulated write failure")
	}
	return s.Store.Upsert(ctx, vm)
}

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
	insertLegacyAlias(t, db, "slow", "gpt-4o-mini", "openai", false)
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
		if len(got) != 3 {
			t.Fatalf("len(List()) = %d, want 3 (%#v)", len(got), got)
		}
		bySource := map[string]VirtualModel{}
		for _, vm := range got {
			bySource[vm.Source] = vm
		}
		// An enabled legacy alias stays an enabled redirect.
		if r, ok := bySource["fast"]; !ok || !r.IsRedirect() || len(r.Targets) != 1 || !r.Enabled {
			t.Fatalf("enabled alias not migrated correctly: %#v", bySource["fast"])
		}
		// A disabled legacy alias stays a disabled redirect (Enabled is authoritative).
		if r, ok := bySource["slow"]; !ok || !r.IsRedirect() || r.Enabled {
			t.Fatalf("disabled alias not migrated correctly: %#v", bySource["slow"])
		}
		// A legacy access override becomes an enabled, user-path-restricted policy.
		if p, ok := bySource["openai/gpt-4o"]; !ok || p.IsRedirect() || len(p.UserPaths) != 1 || !p.Enabled {
			t.Fatalf("override not migrated correctly: %#v", bySource["openai/gpt-4o"])
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
	// The collision is detected before any write, so the table must be left
	// empty — a partial seed would trip the len(existing) > 0 guard next startup
	// and skip importing the access overrides entirely.
	got, err := vmStore.List(ctx)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("len(List()) = %d after failed seed, want 0 (no partial write)", len(got))
	}
}

func TestSeedFromLegacy_RollsBackPartialWriteOnError(t *testing.T) {
	t.Parallel()
	conn := newSQLiteStorage(t)
	ctx := context.Background()
	db := conn.DB()

	createLegacyTables(t, db)
	insertLegacyAlias(t, db, "fast", "gpt-4o", "openai", true)
	insertLegacyAlias(t, db, "slow", "gpt-4o-mini", "openai", true)
	insertLegacyOverride(t, db, "openai/gpt-4o", "openai", "gpt-4o", `["/team"]`)

	vmStore, err := NewSQLiteStore(db)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	// Fail the second write; the first must be rolled back so the table is left
	// empty (not partially seeded, which would skip the rest next startup).
	failing := &failingUpsertStore{Store: vmStore, failAfter: 1}
	if err := seedFromLegacy(ctx, failing, conn); err == nil {
		t.Fatal("seedFromLegacy() error = nil, want write failure")
	}
	got, err := vmStore.List(ctx)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("len(List()) = %d after failed seed, want 0 (partial write rolled back)", len(got))
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
