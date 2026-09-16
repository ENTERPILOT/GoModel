package authkeys

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/internal/storage/mongotest"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
)

func runSQLStoreTest(t *testing.T, body func(t *testing.T, store *SQLStore, db sqlx.DB)) {
	t.Helper()
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, err := NewSQLStore(context.Background(), db)
		require.NoError(t, err)

		body(t, store, db)
	})
}

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

func TestSQLStoreReopenKeepsRows(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore, db sqlx.DB) {
		now := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
		ctx := context.Background()
		err := store.Create(ctx, AuthKey{
			ID:            "key-labelled",
			Name:          "labelled",
			Labels:        []string{"team-a"},
			AllowedModels: []string{"openai/"},
			RedactedValue: TokenPrefix + "...abcd",
			SecretHash:    "hash-labelled",
			Enabled:       true,
			CreatedAt:     now,
			UpdatedAt:     now,
		})
		require.NoError(t, err)

		// Reopening against the same database is what every restart does. It
		// must tolerate the already-applied column migrations and keep rows.
		reopened, err := NewSQLStore(ctx, db)
		require.NoError(t, err)

		keys, err := reopened.List(ctx)
		require.NoError(t, err)
		require.Len(t, keys, 1)
		require.Equal(t, []string{"team-a"}, keys[0].Labels)
		require.Equal(t, []string{"openai/"}, keys[0].AllowedModels)
	})
}
