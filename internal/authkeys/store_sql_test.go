package authkeys

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
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

// Keys issued before passthrough_access existed keep passthrough after the
// upgrade, while a key stored without it stays without it across restarts.
func TestStore_PassthroughAccessBackfill(t *testing.T) {
	ctx := context.Background()
	assertBackfilled := func(t *testing.T, store Store, reopen func() Store) {
		t.Helper()
		require.True(t, listByID(t, store)["key-legacy"].PassthroughAccess, "legacy key keeps passthrough")

		require.NoError(t, store.Create(ctx, newTestKey("key-new", storeTestNow)))
		byID := listByID(t, reopen())
		require.True(t, byID["key-legacy"].PassthroughAccess)
		require.False(t, byID["key-new"].PassthroughAccess, "a key stored without passthrough is not backfilled")
	}

	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		legacyTable := strings.Replace(sqlTable, "passthrough_access "+sqlx.TypeBool+" NOT NULL DEFAULT TRUE,", "", 1)
		require.NotEqual(t, sqlTable, legacyTable)
		require.NoError(t, db.Schema(ctx, legacyTable))
		_, err := db.Exec(ctx, `INSERT INTO auth_keys (id, name, redacted_value, secret_hash, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?)`, "key-legacy", "legacy", TokenPrefix+"...old", "hash-legacy", storeTestNow.Unix(), storeTestNow.Unix())
		require.NoError(t, err)

		store, err := NewSQLStore(ctx, db)
		require.NoError(t, err)
		assertBackfilled(t, store, func() Store {
			reopened, err := NewSQLStore(ctx, db)
			require.NoError(t, err)
			return reopened
		})
	})
	mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
		_, err := db.Collection("auth_keys").InsertOne(ctx, bson.D{
			{Key: "_id", Value: "key-legacy"},
			{Key: "name", Value: "legacy"},
			{Key: "redacted_value", Value: TokenPrefix + "...old"},
			{Key: "secret_hash", Value: "hash-legacy"},
			{Key: "enabled", Value: true},
			{Key: "created_at", Value: storeTestNow},
			{Key: "updated_at", Value: storeTestNow},
		})
		require.NoError(t, err)

		store, err := NewMongoDBStore(db)
		require.NoError(t, err)
		assertBackfilled(t, store, func() Store {
			reopened, err := NewMongoDBStore(db)
			require.NoError(t, err)
			return reopened
		})
	})
}
