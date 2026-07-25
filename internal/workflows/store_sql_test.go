package workflows

import (
	"context"
	"testing"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
)

// The two migration cases below start from table shapes long-lived
// deployments still have on disk: one that already gained scope_user_path and
// one that predates it. Constructing over either must succeed and leave a
// working store.

func TestNewSQLStore_SkipsExistingScopeUserPathMigration(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		if err := db.Schema(ctx, `
			CREATE TABLE workflow_versions (
				id TEXT PRIMARY KEY,
				scope_provider TEXT,
				scope_model TEXT,
				scope_user_path TEXT,
				scope_key TEXT NOT NULL,
				version INTEGER NOT NULL,
				active `+sqlx.TypeBool+` NOT NULL DEFAULT TRUE,
				name TEXT NOT NULL,
				description TEXT NOT NULL DEFAULT '',
				workflow_payload `+sqlx.TypeJSON+` NOT NULL,
				workflow_hash TEXT NOT NULL,
				created_at `+sqlx.TypeInt64+` NOT NULL
			)`); err != nil {
			t.Fatalf("create workflow_versions table: %v", err)
		}

		if _, err := NewSQLStore(ctx, db); err != nil {
			t.Fatalf("NewSQLStore() error = %v", err)
		}
	})
}

func TestNewSQLStore_AddsMissingScopeUserPathColumn(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		if err := db.Schema(ctx, `
			CREATE TABLE workflow_versions (
				id TEXT PRIMARY KEY,
				scope_provider TEXT,
				scope_model TEXT,
				scope_key TEXT NOT NULL,
				version INTEGER NOT NULL,
				active `+sqlx.TypeBool+` NOT NULL DEFAULT TRUE,
				name TEXT NOT NULL,
				description TEXT NOT NULL DEFAULT '',
				workflow_payload `+sqlx.TypeJSON+` NOT NULL,
				workflow_hash TEXT NOT NULL,
				created_at `+sqlx.TypeInt64+` NOT NULL
			)`); err != nil {
			t.Fatalf("create workflow_versions table: %v", err)
		}

		store, err := NewSQLStore(ctx, db)
		if err != nil {
			t.Fatalf("NewSQLStore() error = %v", err)
		}

		// Round-tripping a user-path scope proves the column arrived.
		created, err := store.Create(ctx, CreateInput{
			Scope:    Scope{UserPath: "/team/alpha"},
			Name:     "scoped",
			Payload:  testWorkflowPayload(),
			Activate: true,
		})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		got, err := store.Get(ctx, created.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Scope.UserPath != "/team/alpha" {
			t.Errorf("Scope.UserPath = %q, want /team/alpha", got.Scope.UserPath)
		}
	})
}

func TestSQLStoreCreateAllocatesVersionsAndDeactivatesPrevious(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		store, err := NewSQLStore(ctx, db)
		if err != nil {
			t.Fatalf("NewSQLStore: %v", err)
		}

		first, err := store.Create(ctx, CreateInput{
			Name: "first", Payload: testWorkflowPayload(), Activate: true,
		})
		if err != nil {
			t.Fatalf("Create(first): %v", err)
		}
		second, err := store.Create(ctx, CreateInput{
			Name: "second", Payload: testWorkflowPayload(), Activate: true,
		})
		if err != nil {
			t.Fatalf("Create(second): %v", err)
		}

		if first.Version != 1 || second.Version != 2 {
			t.Errorf("versions = %d, %d; want 1, 2", first.Version, second.Version)
		}

		// Activating a new version must retire the previous one: the unique
		// partial index allows only one active row per scope.
		active, err := store.ListActive(ctx)
		if err != nil {
			t.Fatalf("ListActive: %v", err)
		}
		if len(active) != 1 || active[0].ID != second.ID {
			t.Fatalf("active = %d rows, want only the second version", len(active))
		}
	})
}

func TestSQLStoreDeactivate(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		store, err := NewSQLStore(ctx, db)
		if err != nil {
			t.Fatalf("NewSQLStore: %v", err)
		}

		created, err := store.Create(ctx, CreateInput{
			Name: "only", Payload: testWorkflowPayload(), Activate: true,
		})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if err := store.Deactivate(ctx, created.ID); err != nil {
			t.Fatalf("Deactivate: %v", err)
		}
		// Deactivating twice reports not-found rather than silently succeeding.
		if err := store.Deactivate(ctx, created.ID); err != ErrNotFound {
			t.Errorf("second Deactivate = %v, want ErrNotFound", err)
		}

		active, err := store.ListActive(ctx)
		if err != nil {
			t.Fatalf("ListActive: %v", err)
		}
		if len(active) != 0 {
			t.Errorf("active = %d rows, want 0", len(active))
		}
	})
}

func TestSQLStoreEnsureManagedDefaultGlobalIsIdempotent(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		store, err := NewSQLStore(ctx, db)
		if err != nil {
			t.Fatalf("NewSQLStore: %v", err)
		}

		input := CreateInput{
			Name:        ManagedDefaultGlobalName,
			Description: ManagedDefaultGlobalDescription,
			Payload:     testWorkflowPayload(),
			Managed:     true,
			Activate:    true,
		}
		created, err := store.EnsureManagedDefaultGlobal(ctx, input, "hash-1")
		if err != nil {
			t.Fatalf("first EnsureManagedDefaultGlobal: %v", err)
		}
		if created == nil {
			t.Fatal("first call published nothing, want a version")
		}

		// Same hash: nothing new is published on the next start.
		again, err := store.EnsureManagedDefaultGlobal(ctx, input, "hash-1")
		if err != nil {
			t.Fatalf("second EnsureManagedDefaultGlobal: %v", err)
		}
		if again != nil {
			t.Errorf("second call published version %d, want no-op", again.Version)
		}

		// A changed hash publishes a new version and retires the old one.
		updated, err := store.EnsureManagedDefaultGlobal(ctx, input, "hash-2")
		if err != nil {
			t.Fatalf("third EnsureManagedDefaultGlobal: %v", err)
		}
		if updated == nil || updated.Version != 2 {
			t.Fatalf("third call = %+v, want version 2", updated)
		}
	})
}

func TestSQLStoreEnsureManagedDefaultGlobalLeavesOperatorVersionAlone(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		store, err := NewSQLStore(ctx, db)
		if err != nil {
			t.Fatalf("NewSQLStore: %v", err)
		}

		operator, err := store.Create(ctx, CreateInput{
			Name: "operator authored", Payload: testWorkflowPayload(), Activate: true,
		})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}

		published, err := store.EnsureManagedDefaultGlobal(ctx, CreateInput{
			Name:        ManagedDefaultGlobalName,
			Description: ManagedDefaultGlobalDescription,
			Payload:     testWorkflowPayload(),
			Managed:     true,
			Activate:    true,
		}, "hash-1")
		if err != nil {
			t.Fatalf("EnsureManagedDefaultGlobal: %v", err)
		}
		if published != nil {
			t.Errorf("published over an operator-authored version: %+v", published)
		}

		active, err := store.ListActive(ctx)
		if err != nil {
			t.Fatalf("ListActive: %v", err)
		}
		if len(active) != 1 || active[0].ID != operator.ID {
			t.Errorf("active version changed, want the operator's to survive")
		}
	})
}

// testWorkflowPayload is a minimal valid payload for store-level tests, which
// care about versioning and activation rather than workflow semantics.
func testWorkflowPayload() Payload {
	return Payload{
		SchemaVersion: 1,
		Features:      FeatureFlags{Cache: true, Audit: true, Usage: true},
	}
}
