package failover

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/internal/storage/mongotest"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
)

// runStoreSuite exercises behaviour every Store implementation owes its
// callers, against each backend available in this environment.
func runStoreSuite(t *testing.T, body func(t *testing.T, store Store)) {
	t.Helper()
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, err := NewSQLStore(context.Background(), db)
		if err != nil {
			t.Fatalf("NewSQLStore: %v", err)
		}
		t.Cleanup(func() { _ = store.Close() })
		body(t, store)
	})
	mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
		store, err := NewMongoDBStore(db)
		if err != nil {
			t.Fatalf("NewMongoDBStore: %v", err)
		}
		t.Cleanup(func() { _ = store.Close() })
		body(t, store)
	})
}

func TestStoreCRUDRoundTrip(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()

		// Insert: untrimmed source is trimmed; targets and metadata round-trip.
		insert := Rule{
			Source:        "  openai/gpt-5  ",
			Targets:       []string{"openrouter/openai/gpt-5", "anthropic/claude-fable-5"},
			Enabled:       true,
			ManagedSource: ManagedSourceDashboard,
			CreatedAt:     time.Unix(1000, 0).UTC(),
		}
		if err := store.Upsert(ctx, insert); err != nil {
			t.Fatalf("Upsert(insert) error = %v", err)
		}

		got, err := store.Get(ctx, "openai/gpt-5")
		if err != nil || got == nil {
			t.Fatalf("Get() after insert = %+v, %v; want the inserted rule", got, err)
		}
		if got.Source != "openai/gpt-5" {
			t.Fatalf("Get().Source = %q, want trimmed openai/gpt-5", got.Source)
		}
		if !reflect.DeepEqual(got.Targets, insert.Targets) {
			t.Fatalf("Get().Targets = %v, want %v", got.Targets, insert.Targets)
		}
		if !got.Enabled || got.ManagedSource != ManagedSourceDashboard {
			t.Fatalf("Get() metadata = enabled:%v managed:%q, want enabled:true dashboard", got.Enabled, got.ManagedSource)
		}
		if got.CreatedAt.Unix() != 1000 {
			t.Fatalf("Get().CreatedAt = %d, want 1000 (preserved)", got.CreatedAt.Unix())
		}
		if got.UpdatedAt.IsZero() {
			t.Fatalf("Get().UpdatedAt is zero; stampUpsert should set it")
		}

		// Update via ON CONFLICT: targets/enabled change, created_at is preserved even
		// though the caller passes a different value, while updated_at advances.
		update := Rule{
			Source:        "openai/gpt-5",
			Targets:       []string{"groq/llama-3.3-70b-versatile"},
			Enabled:       false,
			ManagedSource: ManagedSourceDashboard,
			CreatedAt:     time.Unix(5000, 0).UTC(),
		}
		if err := store.Upsert(ctx, update); err != nil {
			t.Fatalf("Upsert(update) error = %v", err)
		}
		got, err = store.Get(ctx, "openai/gpt-5")
		if err != nil || got == nil {
			t.Fatalf("Get() after update = %+v, %v", got, err)
		}
		if got.Enabled {
			t.Fatalf("Get().Enabled = true after disabling; enabled=false must round-trip")
		}
		if !reflect.DeepEqual(got.Targets, []string{"groq/llama-3.3-70b-versatile"}) {
			t.Fatalf("Get().Targets = %v, want updated single target", got.Targets)
		}
		if got.CreatedAt.Unix() != 1000 {
			t.Fatalf("Get().CreatedAt = %d after update, want 1000 (ON CONFLICT must not touch created_at)", got.CreatedAt.Unix())
		}

		// A second rule lets us assert List ordering by primary_model ASC.
		if err := store.Upsert(ctx, Rule{Source: "anthropic/claude-opus-4-8", Targets: []string{"openai/gpt-5"}, Enabled: true, ManagedSource: ManagedSourceDashboard}); err != nil {
			t.Fatalf("Upsert(second) error = %v", err)
		}
		rules, err := store.List(ctx)
		if err != nil {
			t.Fatalf("List() error = %v", err)
		}
		if len(rules) != 2 {
			t.Fatalf("List() returned %d rules, want 2", len(rules))
		}
		if rules[0].Source != "anthropic/claude-opus-4-8" || rules[1].Source != "openai/gpt-5" {
			t.Fatalf("List() order = [%q,%q], want sorted ascending", rules[0].Source, rules[1].Source)
		}

		// Get for an unknown source returns ErrNotFound.
		if _, err := store.Get(ctx, "does/not-exist"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Get(unknown) error = %v, want ErrNotFound", err)
		}

		// Delete removes an existing rule; deleting again reports ErrNotFound.
		if err := store.Delete(ctx, "openai/gpt-5"); err != nil {
			t.Fatalf("Delete(existing) error = %v", err)
		}
		if err := store.Delete(ctx, "openai/gpt-5"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Delete(missing) error = %v, want ErrNotFound", err)
		}

		// DeleteAll clears the remaining rows.
		if err := store.DeleteAll(ctx); err != nil {
			t.Fatalf("DeleteAll() error = %v", err)
		}
		rules, err = store.List(ctx)
		if err != nil {
			t.Fatalf("List() after DeleteAll error = %v", err)
		}
		if len(rules) != 0 {
			t.Fatalf("List() after DeleteAll returned %d rules, want 0", len(rules))
		}
	})
}

func TestStoreUpsertNilTargetsRoundTrip(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()

		// A rule with no targets must persist as an empty list and read back as nil
		// (decodeTargets collapses "[]" to nil) rather than erroring on NULL.
		if err := store.Upsert(ctx, Rule{Source: "openai/gpt-5", ManagedSource: ManagedSourceDashboard}); err != nil {
			t.Fatalf("Upsert(nil targets) error = %v", err)
		}
		got, err := store.Get(ctx, "openai/gpt-5")
		if err != nil || got == nil {
			t.Fatalf("Get() = %+v, %v", got, err)
		}
		if got.Targets != nil {
			t.Fatalf("Get().Targets = %v, want nil for an empty target list", got.Targets)
		}
	})
}

// TestSQLStoreMigratesLegacyFailoverRulesSchema starts from the pre-rename
// table shape. Until now only the SQLite migration was covered; the PostgreSQL
// one had no test at all.
func TestSQLStoreMigratesLegacyFailoverRulesSchema(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		if err := db.Schema(ctx, `
			CREATE TABLE failover_rules (
				source TEXT PRIMARY KEY,
				targets TEXT NOT NULL DEFAULT '[]',
				description TEXT NOT NULL DEFAULT '',
				enabled `+sqlx.TypeBool+` NOT NULL DEFAULT TRUE,
				managed_source TEXT NOT NULL DEFAULT 'dashboard',
				created_at `+sqlx.TypeInt64+` NOT NULL,
				updated_at `+sqlx.TypeInt64+` NOT NULL
			)`); err != nil {
			t.Fatalf("create legacy failover_rules table: %v", err)
		}
		if _, err := db.Exec(ctx, `
			INSERT INTO failover_rules (
				source, targets, description, enabled, managed_source, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?)
		`, " gpt-4o ", `["azure/gpt-4o","gemini/gemini-2.5-pro"]`, "legacy note",
			true, "dashboard", 100, 200); err != nil {
			t.Fatalf("seed legacy row: %v", err)
		}

		store, err := NewSQLStore(ctx, db)
		if err != nil {
			t.Fatalf("NewSQLStore() error = %v", err)
		}
		rows, err := store.List(ctx)
		if err != nil {
			t.Fatalf("List() error = %v", err)
		}
		if len(rows) != 1 {
			t.Fatalf("List() returned %d rows, want 1", len(rows))
		}
		// The legacy primary key was padded; it must migrate trimmed so Get and
		// Delete (which trim their input) can find it.
		if rows[0].Source != "gpt-4o" {
			t.Fatalf("row source = %q, want gpt-4o (trimmed)", rows[0].Source)
		}
		wantTargets := []string{"azure/gpt-4o", "gemini/gemini-2.5-pro"}
		if !reflect.DeepEqual(rows[0].Targets, wantTargets) {
			t.Fatalf("row targets = %v, want %v", rows[0].Targets, wantTargets)
		}
		if !rows[0].Enabled || rows[0].ManagedSource != "dashboard" {
			t.Fatalf("row metadata = enabled:%v managed_source:%q, want enabled:true dashboard",
				rows[0].Enabled, rows[0].ManagedSource)
		}
		if rows[0].CreatedAt.Unix() != 100 || rows[0].UpdatedAt.Unix() != 200 {
			t.Fatalf("row timestamps = created:%d updated:%d, want created:100 updated:200",
				rows[0].CreatedAt.Unix(), rows[0].UpdatedAt.Unix())
		}
		if got, err := store.Get(ctx, "gpt-4o"); err != nil || got == nil {
			t.Fatalf("Get(gpt-4o) after migration = %+v, %v; want the migrated rule", got, err)
		}

		// The dropped legacy columns must really be gone, on either engine.
		for _, removed := range []string{"source", "targets", "description"} {
			var value string
			err := db.QueryRow(ctx, `SELECT `+removed+` FROM failover_rules`).Scan(&value)
			if err == nil {
				t.Errorf("legacy column %q still queryable after migration", removed)
			}
		}
	})
}
