package users

import (
	"context"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/internal/storage/mongotest"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
	"github.com/stretchr/testify/require"
)

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

func TestStore_RoundTrip(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()
		now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)

		eng := User{
			UserPath: "/acme/eng", AllowedModels: []string{"anthropic/", "openai/gpt-4o"}, Description: "eng",
			DefaultModel: "anthropic/claude-sonnet-4-6", MaxReasoningEffort: "medium", MaxOutputTokens: 4096,
			CreatedAt: now, UpdatedAt: now,
		}
		root := User{UserPath: "/acme", CreatedAt: now, UpdatedAt: now}
		for _, user := range []User{eng, root} {
			err := store.Upsert(ctx, user)
			require.NoError(t, err, "Upsert(%s): %v", user.UserPath, err)
		}

		rows, err := store.List(ctx)
		require.NoError(t, err)
		require.Len(t, rows, 2)
		require.Equal(t, "/acme", rows[0].UserPath)
		require.Equal(t, "/acme/eng", rows[1].UserPath)
		require.Nil(t, rows[0].AllowedModels)
		require.Equal(t, eng.AllowedModels, rows[1].AllowedModels)
		require.Equal(t, "eng", rows[1].Description, "eng row = %#v", rows[1])
		require.Equal(t, "anthropic/claude-sonnet-4-6", rows[1].DefaultModel)
		require.Equal(t, "medium", rows[1].MaxReasoningEffort)
		require.Equal(t, 4096, rows[1].MaxOutputTokens)
		require.Empty(t, rows[0].DefaultModel)
		require.Empty(t, rows[0].MaxReasoningEffort)
		require.Zero(t, rows[0].MaxOutputTokens, "a row without request policy = %#v", rows[0])
		require.True(t, rows[1].CreatedAt.Equal(now))
		require.True(t, rows[1].UpdatedAt.Equal(now), "timestamps = %v / %v, want %v", rows[1].CreatedAt, rows[1].UpdatedAt, now)

		// Upsert keeps created_at and replaces the rest.
		later := now.Add(time.Hour)
		err = store.Upsert(ctx, User{UserPath: "/acme/eng", AllowedModels: []string{"openai/"}, CreatedAt: later, UpdatedAt: later})
		require.NoError(t, err)

		rows, err = store.List(ctx)
		require.NoError(t, err)
		require.Equal(t, []string{"openai/"}, rows[1].AllowedModels)
		require.Empty(t, rows[1].Description, "updated row = %#v", rows[1])
		require.Empty(t, rows[1].DefaultModel)
		require.Empty(t, rows[1].MaxReasoningEffort)
		require.Zero(t, rows[1].MaxOutputTokens, "an upsert without request policy clears it: %#v", rows[1])
		require.True(t, rows[1].CreatedAt.Equal(now))
		require.True(t, rows[1].UpdatedAt.Equal(later), "updated timestamps = %v / %v", rows[1].CreatedAt, rows[1].UpdatedAt)
		err = store.Delete(ctx, "/acme")
		require.NoError(t, err)
		err = store.Delete(ctx, "/acme")
		require.ErrorIs(t, err, ErrNotFound)

		rows, err = store.List(ctx)
		require.NoError(t, err)
		require.Len(t, rows, 1)
		require.Equal(t, "/acme/eng", rows[0].UserPath)
	})
}

// A users table created before the request policy columns existed gains them
// on startup, and its rows read back with no default and no ceiling.
func TestSQLStore_AddsRequestPolicyColumnsToAnExistingTable(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		require.NoError(t, db.Schema(ctx, `CREATE TABLE IF NOT EXISTS users (
			user_path TEXT PRIMARY KEY,
			allowed_models `+sqlx.TypeJSONText+` NOT NULL DEFAULT '[]',
			description TEXT NOT NULL DEFAULT '',
			created_at `+sqlx.TypeInt64+` NOT NULL,
			updated_at `+sqlx.TypeInt64+` NOT NULL
		)`))
		_, err := db.Exec(ctx, `INSERT INTO users (user_path, allowed_models, description, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
			"/acme", `["openai/"]`, "legacy", int64(1), int64(1))
		require.NoError(t, err)

		store, err := NewSQLStore(ctx, db)
		require.NoError(t, err)
		_, err = NewSQLStore(ctx, db)
		require.NoError(t, err, "the migration is idempotent")

		rows, err := store.List(ctx)
		require.NoError(t, err)
		require.Len(t, rows, 1)
		require.Equal(t, []string{"openai/"}, rows[0].AllowedModels)
		require.Empty(t, rows[0].DefaultModel)
		require.Empty(t, rows[0].MaxReasoningEffort)
		require.Zero(t, rows[0].MaxOutputTokens)

		now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
		require.NoError(t, store.Upsert(ctx, User{UserPath: "/acme", MaxReasoningEffort: "low", MaxOutputTokens: 256, CreatedAt: now, UpdatedAt: now}))
		rows, err = store.List(ctx)
		require.NoError(t, err)
		require.Equal(t, "low", rows[0].MaxReasoningEffort)
		require.Equal(t, 256, rows[0].MaxOutputTokens)
	})
}
