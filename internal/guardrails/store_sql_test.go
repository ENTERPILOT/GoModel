package guardrails

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
)

// Behaviour shared by every backend lives in store_test.go; this file keeps
// the SQL-only concerns: schema migrations from older table shapes.

// TestNewSQLStoreAddsMissingUserPathColumn starts from the pre-migration table
// shape a long-lived deployment still has on disk.
func TestNewSQLStoreAddsMissingUserPathColumn(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		err := db.Schema(ctx, `
			CREATE TABLE guardrail_definitions (
				name TEXT PRIMARY KEY,
				type TEXT NOT NULL,
				description TEXT NOT NULL DEFAULT '',
				config `+sqlx.TypeJSON+` NOT NULL,
				created_at `+sqlx.TypeInt64+` NOT NULL,
				updated_at `+sqlx.TypeInt64+` NOT NULL
			)`)
		require.NoError(t, err, "create pre-migration table")

		store, err := NewSQLStore(ctx, db)
		require.NoError(t, err)

		// Round-tripping a user path proves the column arrived, without
		// reaching for engine-specific schema introspection.
		err = store.Upsert(ctx, Definition{
			Name:     "after-migration",
			Type:     "system_prompt",
			UserPath: "/team/alpha",
			Config:   []byte(`{"content":"be concise"}`),
		})
		require.NoError(t, err)

		got, err := store.Get(ctx, "after-migration")
		require.NoError(t, err)
		assert.Equal(t, "/team/alpha", got.UserPath)
	})
}

func TestNewSQLStoreIsIdempotent(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		// Every restart re-runs the constructor, including the already-applied
		// user_path migration.
		for range 3 {
			_, err := NewSQLStore(ctx, db)
			require.NoError(t, err)
		}
	})
}

// TestSQLStoreMigratesFailModeAndTimeoutColumns covers the columns added with
// the plugin system, starting from a table that lacks them.
func TestSQLStoreMigratesFailModeAndTimeoutColumns(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		err := db.Schema(ctx, `
			CREATE TABLE guardrail_definitions (
				name TEXT PRIMARY KEY,
				type TEXT NOT NULL,
				description TEXT NOT NULL DEFAULT '',
				user_path TEXT,
				config `+sqlx.TypeJSON+` NOT NULL,
				created_at `+sqlx.TypeInt64+` NOT NULL,
				updated_at `+sqlx.TypeInt64+` NOT NULL
			)`)
		require.NoError(t, err, "create pre-migration table")

		store, err := NewSQLStore(ctx, db)
		require.NoError(t, err)

		err = store.Upsert(ctx, Definition{
			Name:      "timed",
			Type:      "system_prompt",
			FailMode:  "open",
			TimeoutMS: 1500,
			Config:    []byte(`{"content":"be concise"}`),
		})
		require.NoError(t, err)

		got, err := store.Get(ctx, "timed")
		require.NoError(t, err)
		assert.Equal(t, "open", got.FailMode)
		assert.Equal(t, 1500, got.TimeoutMS)
	})
}
