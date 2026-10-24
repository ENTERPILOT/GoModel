package workflows

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// testWorkflowPayload is a minimal valid payload for store-level tests, which
// care about versioning and activation rather than workflow semantics.
func testWorkflowPayload() Payload {
	return Payload{
		SchemaVersion: 1,
		Features:      FeatureFlags{Cache: true, Audit: true, Usage: true},
	}
}

// The two migration cases below start from table shapes long-lived
// deployments still have on disk: one that already gained scope_user_path and
// one that predates it. Constructing over either must succeed and leave a
// working store.

func TestNewSQLStore_SkipsExistingScopeUserPathMigration(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		err := db.Schema(ctx, `
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
			)`)
		require.NoError(t, err)
		_, err = NewSQLStore(ctx, db)
		require.NoError(t, err)
	})
}

func TestNewSQLStore_AddsMissingScopeUserPathColumn(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		err := db.Schema(ctx, `
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
			)`)
		require.NoError(t, err)

		store, err := NewSQLStore(ctx, db)
		require.NoError(t, err)

		// Round-tripping a user-path scope proves the column arrived.
		created, err := store.Create(ctx, CreateInput{
			Scope:    Scope{UserPath: "/team/alpha"},
			Name:     "scoped",
			Payload:  testWorkflowPayload(),
			Activate: true,
		})
		require.NoError(t, err)

		got, err := store.Get(ctx, created.ID)
		require.NoError(t, err)
		assert.Equal(t, "/team/alpha", got.Scope.UserPath)
	})
}

// TestNewSQLStoreConvertsTimestamptzCreatedAt covers the one column where the
// two hand-written stores disagreed on representation: PostgreSQL kept
// created_at as TIMESTAMPTZ while SQLite kept unix seconds. Existing
// PostgreSQL deployments must be converted in place, not left with a column
// the shared scan cannot read.
func TestNewSQLStoreConvertsTimestamptzCreatedAt(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		if db.Dialect() != sqlx.PostgreSQL {
			t.Skip("timestamptz column only ever existed on PostgreSQL")
		}
		ctx := context.Background()
		err := db.Schema(ctx, `
			CREATE TABLE workflow_versions (
				id TEXT PRIMARY KEY,
				scope_provider TEXT,
				scope_model TEXT,
				scope_user_path TEXT,
				scope_key TEXT NOT NULL,
				version INTEGER NOT NULL,
				active BOOLEAN NOT NULL DEFAULT TRUE,
				managed_default BOOLEAN NOT NULL DEFAULT FALSE,
				name TEXT NOT NULL,
				description TEXT NOT NULL DEFAULT '',
				workflow_payload JSONB NOT NULL,
				workflow_hash TEXT NOT NULL,
				created_at TIMESTAMPTZ NOT NULL
			)`)
		require.NoError(t, err)

		// The fractional row is the interesting one: a plain
		// EXTRACT(EPOCH ...)::bigint rounds, which would push .6 seconds a
		// whole second into the future and disagree with the truncation every
		// later write performs through time.Unix.
		seeded := []struct {
			id      string
			scope   string
			epoch   float64
			wantSec int64
		}{
			{"legacy-whole", "global", 1700000000, 1700000000},
			{"legacy-frac-up", "openai", 1700000000.6, 1700000000},
			{"legacy-frac-down", "groq", 1700000000.4, 1700000000},
		}
		for _, row := range seeded {
			_, err := db.Exec(ctx, `
				INSERT INTO workflow_versions (
					id, scope_key, version, active, managed_default, name,
					workflow_payload, workflow_hash, created_at
				) VALUES (?, ?, ?, ?, ?, ?, ?, ?, to_timestamp(?))
			`, row.id, row.scope, 1, true, false, "legacy",
				`{"schema_version":1}`, "hash-"+row.id, row.epoch)
			require.NoError(t, err, "seed legacy row %s: %v", row.id, err)
		}

		store, err := NewSQLStore(ctx, db)
		require.NoError(t, err)

		for _, row := range seeded {
			got, err := store.Get(ctx, row.id)
			require.NoError(t, err, "Get %s: %v", row.id, err)

			// The instant must survive the conversion, not just the column type.
			assert.Equal(t, row.wantSec, got.CreatedAt.Unix(), "%s CreatedAt", row.id)
		}
	})
}
