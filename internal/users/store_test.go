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

		eng := User{UserPath: "/acme/eng", AllowedModels: []string{"anthropic/", "openai/gpt-4o"}, Description: "eng", CreatedAt: now, UpdatedAt: now}
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
