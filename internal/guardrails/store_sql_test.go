package guardrails

import (
	"context"
	"testing"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func runSQLStoreTest(t *testing.T, body func(t *testing.T, store *SQLStore, db sqlx.DB)) {
	t.Helper()
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, err := NewSQLStore(context.Background(), db)
		require.NoError(t, err)

		body(t, store, db)
	})
}

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
		require.NoError(t, err)

		store, err := NewSQLStore(ctx, db)
		require.NoError(t, err)
		err = // Round-tripping a user path proves the column arrived, without
			// reaching for engine-specific schema introspection.
			store.Upsert(ctx, Definition{
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

func TestSQLStoreUpsertAndListRoundTripsUserPath(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore, _ sqlx.DB) {
		ctx := context.Background()

		scoped := Definition{Name: "scoped", Type: "system_prompt", UserPath: "/team/alpha", Config: []byte(`{"content":"x"}`)}
		global := Definition{Name: "global", Type: "system_prompt", Config: []byte(`{"content":"y"}`)}
		for _, definition := range []Definition{scoped, global} {
			err := store.Upsert(ctx, definition)
			require.NoError(t, err, "Upsert(%s): %v", definition.Name, err)
		}

		definitions, err := store.List(ctx)
		require.NoError(t, err)
		require.Len(t, definitions, 2)

		// Ordered by name ascending.
		require.Equal(t, "global", definitions[0].Name)
		require.Equal(t, "scoped", definitions[1].Name)
		assert.Empty(t, definitions[0].UserPath)
		assert.Equal(t, "/team/alpha", definitions[1].UserPath)

	})
}

func TestSQLStoreUpsertPreservesCreatedAt(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore, _ sqlx.DB) {
		ctx := context.Background()
		err := store.Upsert(ctx, Definition{Name: "g", Type: "system_prompt", Config: []byte(`{"content":"c"}`)})
		require.NoError(t, err)

		created, err := store.Get(ctx, "g")
		require.NoError(t, err)
		err = store.Upsert(ctx, Definition{Name: "g", Type: "llm_based_altering", Config: []byte(`{"model":"openai/gpt-4o"}`)})
		require.NoError(t, err)

		updated, err := store.Get(ctx, "g")
		require.NoError(t, err)
		assert.Equal(t, "llm_based_altering", updated.Type)

		// created_at is excluded from the ON CONFLICT update list.
		assert.True(t, updated.CreatedAt.Equal(created.CreatedAt), "CreatedAt = %v, want %v preserved", updated.CreatedAt, created.CreatedAt)

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
		require.Error(t, err)

		definitions, err := store.List(ctx)
		require.NoError(t, err)
		assert.Empty(t, definitions)

	})
}

func TestSQLStoreUpsertManyCommits(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore, _ sqlx.DB) {
		ctx := context.Background()
		err := store.UpsertMany(ctx, []Definition{
			{Name: "a", Type: "system_prompt", Config: []byte(`{"content":"c"}`)},
			{Name: "b", Type: "system_prompt", Config: []byte(`{"content":"c"}`)},
		})
		require.NoError(t, err)

		definitions, err := store.List(ctx)
		require.NoError(t, err)
		assert.Len(t, definitions, 2)

	})
}

func TestSQLStoreGetAndDeleteMissingReturnNotFound(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore, _ sqlx.DB) {
		ctx := context.Background()
		_, err := store.Get(ctx, "absent")
		assert.ErrorIs(t, err, ErrNotFound)
		err = store.Delete(ctx, "absent")
		assert.ErrorIs(t, err, ErrNotFound)

	})
}

func TestSQLStoreDeleteRemovesDefinition(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore, _ sqlx.DB) {
		ctx := context.Background()
		err := store.Upsert(ctx, Definition{Name: "g", Type: "system_prompt", Config: []byte(`{"content":"c"}`)})
		require.NoError(t, err)
		err = // Names are trimmed on the way in, so a padded delete must still match.
			store.Delete(ctx, "  g  ")
		require.NoError(t, err)
		_, err = store.Get(ctx, "g")
		assert.ErrorIs(t, err, ErrNotFound)

	})
}

// TestSQLStoreRoundTripsFailModeAndTimeout covers the columns added with the
// plugin system, including the migration from a table that lacks them.
func TestSQLStoreRoundTripsFailModeAndTimeout(t *testing.T) {
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
		require.NoError(t, err)

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
		require.Equal(t, "open", got.FailMode)
		require.Equal(t, 1500, got.TimeoutMS)
		err = store.Upsert(ctx, Definition{Name: "timed", Type: "system_prompt", Config: []byte(`{"content":"x"}`)})
		require.NoError(t, err)
		got, _ = store.Get(ctx, "timed")
		require.Empty(t, got.FailMode)
		require.Equal(t, 0, got.TimeoutMS)

	})
}
