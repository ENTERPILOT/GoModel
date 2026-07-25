package guardrails

import (
	"context"
	"errors"
	"testing"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
)

func runSQLStoreTest(t *testing.T, body func(t *testing.T, store *SQLStore, db sqlx.DB)) {
	t.Helper()
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, err := NewSQLStore(context.Background(), db)
		if err != nil {
			t.Fatalf("NewSQLStore: %v", err)
		}
		body(t, store, db)
	})
}

// TestNewSQLStoreAddsMissingUserPathColumn starts from the pre-migration table
// shape a long-lived deployment still has on disk.
func TestNewSQLStoreAddsMissingUserPathColumn(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		if err := db.Schema(ctx, `
			CREATE TABLE guardrail_definitions (
				name TEXT PRIMARY KEY,
				type TEXT NOT NULL,
				description TEXT NOT NULL DEFAULT '',
				config `+sqlx.TypeJSON+` NOT NULL,
				created_at `+sqlx.TypeInt64+` NOT NULL,
				updated_at `+sqlx.TypeInt64+` NOT NULL
			)`); err != nil {
			t.Fatalf("create pre-migration table: %v", err)
		}

		store, err := NewSQLStore(ctx, db)
		if err != nil {
			t.Fatalf("NewSQLStore: %v", err)
		}

		// Round-tripping a user path proves the column arrived, without
		// reaching for engine-specific schema introspection.
		if err := store.Upsert(ctx, Definition{
			Name:     "after-migration",
			Type:     "system_prompt",
			UserPath: "/team/alpha",
			Config:   []byte(`{"content":"be concise"}`),
		}); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		got, err := store.Get(ctx, "after-migration")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.UserPath != "/team/alpha" {
			t.Errorf("UserPath = %q, want /team/alpha", got.UserPath)
		}
	})
}

func TestNewSQLStoreIsIdempotent(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		// Every restart re-runs the constructor, including the already-applied
		// user_path migration.
		for range 3 {
			if _, err := NewSQLStore(ctx, db); err != nil {
				t.Fatalf("NewSQLStore: %v", err)
			}
		}
	})
}

func TestSQLStoreUpsertAndListRoundTripsUserPath(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore, _ sqlx.DB) {
		ctx := context.Background()

		scoped := Definition{Name: "scoped", Type: "system_prompt", UserPath: "/team/alpha", Config: []byte(`{"content":"x"}`)}
		global := Definition{Name: "global", Type: "system_prompt", Config: []byte(`{"content":"y"}`)}
		for _, definition := range []Definition{scoped, global} {
			if err := store.Upsert(ctx, definition); err != nil {
				t.Fatalf("Upsert(%s): %v", definition.Name, err)
			}
		}

		definitions, err := store.List(ctx)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(definitions) != 2 {
			t.Fatalf("len = %d, want 2", len(definitions))
		}
		// Ordered by name ascending.
		if definitions[0].Name != "global" || definitions[1].Name != "scoped" {
			t.Fatalf("names = %s, %s; want global, scoped", definitions[0].Name, definitions[1].Name)
		}
		if definitions[0].UserPath != "" {
			t.Errorf("global UserPath = %q, want empty", definitions[0].UserPath)
		}
		if definitions[1].UserPath != "/team/alpha" {
			t.Errorf("scoped UserPath = %q, want /team/alpha", definitions[1].UserPath)
		}
	})
}

func TestSQLStoreUpsertPreservesCreatedAt(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore, _ sqlx.DB) {
		ctx := context.Background()

		if err := store.Upsert(ctx, Definition{Name: "g", Type: "system_prompt", Config: []byte(`{"content":"c"}`)}); err != nil {
			t.Fatalf("first Upsert: %v", err)
		}
		created, err := store.Get(ctx, "g")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}

		if err := store.Upsert(ctx, Definition{Name: "g", Type: "llm_based_altering", Config: []byte(`{"model":"openai/gpt-4o"}`)}); err != nil {
			t.Fatalf("second Upsert: %v", err)
		}
		updated, err := store.Get(ctx, "g")
		if err != nil {
			t.Fatalf("Get after update: %v", err)
		}
		if updated.Type != "llm_based_altering" {
			t.Errorf("Type = %q, want llm_based_altering", updated.Type)
		}
		// created_at is excluded from the ON CONFLICT update list.
		if !updated.CreatedAt.Equal(created.CreatedAt) {
			t.Errorf("CreatedAt = %v, want %v preserved", updated.CreatedAt, created.CreatedAt)
		}
	})
}

func TestSQLStoreUpsertManyIsAtomic(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore, _ sqlx.DB) {
		ctx := context.Background()

		// The second definition is invalid, so nothing from the batch should
		// land: config seeding must not half-apply.
		err := store.UpsertMany(ctx, []Definition{
			{Name: "valid", Type: "system_prompt", Config: []byte(`{"content":"c"}`)},
			{Name: "", Type: "system_prompt", Config: []byte(`{"content":"c"}`)},
		})
		if err == nil {
			t.Fatal("UpsertMany with an invalid definition succeeded, want failure")
		}

		definitions, err := store.List(ctx)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(definitions) != 0 {
			t.Errorf("len = %d after failed batch, want 0", len(definitions))
		}
	})
}

func TestSQLStoreUpsertManyCommits(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore, _ sqlx.DB) {
		ctx := context.Background()

		if err := store.UpsertMany(ctx, []Definition{
			{Name: "a", Type: "system_prompt", Config: []byte(`{"content":"c"}`)},
			{Name: "b", Type: "system_prompt", Config: []byte(`{"content":"c"}`)},
		}); err != nil {
			t.Fatalf("UpsertMany: %v", err)
		}

		definitions, err := store.List(ctx)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(definitions) != 2 {
			t.Errorf("len = %d, want 2", len(definitions))
		}
	})
}

func TestSQLStoreGetAndDeleteMissingReturnNotFound(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore, _ sqlx.DB) {
		ctx := context.Background()
		if _, err := store.Get(ctx, "absent"); !errors.Is(err, ErrNotFound) {
			t.Errorf("Get error = %v, want ErrNotFound", err)
		}
		if err := store.Delete(ctx, "absent"); !errors.Is(err, ErrNotFound) {
			t.Errorf("Delete error = %v, want ErrNotFound", err)
		}
	})
}

func TestSQLStoreDeleteRemovesDefinition(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore, _ sqlx.DB) {
		ctx := context.Background()
		if err := store.Upsert(ctx, Definition{Name: "g", Type: "system_prompt", Config: []byte(`{"content":"c"}`)}); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		// Names are trimmed on the way in, so a padded delete must still match.
		if err := store.Delete(ctx, "  g  "); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if _, err := store.Get(ctx, "g"); !errors.Is(err, ErrNotFound) {
			t.Errorf("Get after Delete = %v, want ErrNotFound", err)
		}
	})
}
