package virtualmodels

import (
	"context"
	"path/filepath"
	"testing"

	"gomodel/internal/aliases"
	"gomodel/internal/modeloverrides"
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

func TestSeedFromLegacy_CopiesAndIsIdempotent(t *testing.T) {
	t.Parallel()
	conn := newSQLiteStorage(t)
	ctx := context.Background()
	db := conn.DB()

	aliasStore, err := aliases.NewSQLiteStore(db)
	if err != nil {
		t.Fatalf("aliases.NewSQLiteStore() error = %v", err)
	}
	if err := aliasStore.Upsert(ctx, aliases.Alias{Name: "fast", TargetModel: "gpt-4o", TargetProvider: "openai", Enabled: true}); err != nil {
		t.Fatalf("seed legacy alias: %v", err)
	}
	overrideStore, err := modeloverrides.NewSQLiteStore(db)
	if err != nil {
		t.Fatalf("modeloverrides.NewSQLiteStore() error = %v", err)
	}
	if err := overrideStore.Upsert(ctx, modeloverrides.Override{Selector: "openai/gpt-4o", ProviderName: "openai", Model: "gpt-4o", UserPaths: []string{"/team"}}); err != nil {
		t.Fatalf("seed legacy override: %v", err)
	}

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

	aliasStore, err := aliases.NewSQLiteStore(db)
	if err != nil {
		t.Fatalf("aliases.NewSQLiteStore() error = %v", err)
	}
	// An alias and an access override that share the same source string.
	if err := aliasStore.Upsert(ctx, aliases.Alias{Name: "gpt-4o", TargetModel: "gpt-4o-real", TargetProvider: "openai", Enabled: true}); err != nil {
		t.Fatalf("seed legacy alias: %v", err)
	}
	overrideStore, err := modeloverrides.NewSQLiteStore(db)
	if err != nil {
		t.Fatalf("modeloverrides.NewSQLiteStore() error = %v", err)
	}
	if err := overrideStore.Upsert(ctx, modeloverrides.Override{Selector: "gpt-4o", Model: "gpt-4o", UserPaths: []string{"/team"}}); err != nil {
		t.Fatalf("seed legacy override: %v", err)
	}

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
