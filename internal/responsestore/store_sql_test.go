package responsestore

import (
	"context"
	"testing"
	"time"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/internal/storage/mongotest"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
)

func runSQLStoreTest(t *testing.T, body func(t *testing.T, store *SQLStore)) {
	t.Helper()
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, err := NewSQLStore(context.Background(), db)
		require.NoError(t, err)

		t.Cleanup(func() { _ = store.Close() })
		body(t, store)
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

func testStoredResponse(id string) *StoredResponse {
	return &StoredResponse{
		Response: &core.ResponsesResponse{
			ID:     id,
			Object: "response",
			Model:  "gpt-test",
		},
		InputItems: []json.RawMessage{
			json.RawMessage(`{"role":"user","content":"hello"}`),
		},
		Provider:  "openai",
		UserPath:  "/team-a",
		RequestID: "req-1",
	}
}

func TestSQLStoreCreateGetRoundtrip(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
		ctx := context.Background()
		err := store.Create(ctx, testStoredResponse("resp-1"))
		require.NoError(t, err)

		got, err := store.Get(ctx, "resp-1")
		require.NoError(t, err)
		require.NotNil(t, got.Response)
		require.Equal(t, "resp-1", got.Response.ID)
		require.Equal(t, "gpt-test", got.Response.Model)
		require.Len(t, got.InputItems, 1)
		require.Contains(t, string(got.InputItems[0]), "hello")
		require.Equal(t, "openai", got.Provider)
		require.Equal(t, "/team-a", got.UserPath)
		require.Equal(t, "req-1", got.RequestID, "metadata = %+v, want provider/user path/request id preserved", got)
		require.False(t, got.StoredAt.IsZero())
		require.False(t, got.ExpiresAt.IsZero())
		require.True(t, got.ExpiresAt.After(got.StoredAt), "ExpiresAt = %v, want after StoredAt %v", got.ExpiresAt, got.StoredAt)

	})
}

func TestSQLStoreCreateRejectsDuplicates(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
		ctx := context.Background()
		err := store.Create(ctx, testStoredResponse("resp-1"))
		require.NoError(t, err)

		err = store.Create(ctx, testStoredResponse("resp-1"))
		require.Error(t, err)
		require.Contains(t, err.Error(), "already exists")

	})
}

func TestSQLStoreCreateReplacesExpired(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
		ctx := context.Background()

		expired := testStoredResponse("resp-1")
		expired.StoredAt = time.Now().UTC().Add(-2 * time.Hour)
		expired.ExpiresAt = time.Now().UTC().Add(-time.Hour)
		_, err := // Expired-at-write snapshots are silently skipped, so seed the row directly.
			store.db.Exec(ctx,
				"INSERT INTO response_snapshots (id, data, stored_at, expires_at) VALUES (?, ?, ?, ?)",
				"resp-1", `{"response":{"id":"resp-1"}}`, expired.StoredAt.Unix(), expired.ExpiresAt.Unix(),
			)
		require.NoError(t, err)

		replacement := testStoredResponse("resp-1")
		replacement.Response.Model = "gpt-replacement"
		err = store.Create(ctx, replacement)
		require.NoError(t, err)

		got, err := store.Get(ctx, "resp-1")
		require.NoError(t, err)
		require.Equal(t, "gpt-replacement", got.Response.Model)

	})
}

func TestSQLStoreUpdatePreservesRetentionColumns(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
		ctx := context.Background()
		err := store.Create(ctx, testStoredResponse("resp-1"))
		require.NoError(t, err)

		created, err := store.Get(ctx, "resp-1")
		require.NoError(t, err)

		updated := testStoredResponse("resp-1")
		updated.Response.Model = "gpt-updated"
		err = store.Update(ctx, updated)
		require.NoError(t, err)

		got, err := store.Get(ctx, "resp-1")
		require.NoError(t, err)
		require.Equal(t, "gpt-updated", got.Response.Model)
		require.True(t, got.StoredAt.Equal(created.StoredAt))
		require.True(t, got.ExpiresAt.Equal(created.ExpiresAt), "retention changed: stored %v→%v expires %v→%v", created.StoredAt, got.StoredAt, created.ExpiresAt, got.ExpiresAt)

	})
}

func TestSQLStoreUpdateMissingReturnsNotFound(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
		err := store.Update(context.Background(), testStoredResponse("missing"))
		require.ErrorIs(t, err, ErrNotFound)

	})
}

func TestStoreDelete(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()
		err := store.Create(ctx, testStoredResponse("resp-1"))
		require.NoError(t, err)
		err = store.Delete(ctx, "resp-1")
		require.NoError(t, err)
		_, err = store.Get(ctx, "resp-1")
		require.ErrorIs(t, err, ErrNotFound)
		err = store.Delete(ctx, "resp-1")
		require.ErrorIs(t, err, ErrNotFound)

	})
}

func TestSQLStoreExpiryAndSweep(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
		ctx := context.Background()

		entry := testStoredResponse("resp-1")
		entry.ExpiresAt = time.Now().UTC().Add(time.Second)
		err := store.Create(ctx, entry)
		require.NoError(t, err)
		_, err = // Simulate expiry passing by rewriting the retention column.
			store.db.Exec(ctx,
				"UPDATE response_snapshots SET expires_at = ? WHERE id = ?",
				time.Now().Add(-time.Minute).Unix(), "resp-1",
			)
		require.NoError(t, err)
		_, err = store.Get(ctx, "resp-1")
		require.ErrorIs(t, err, ErrNotFound)
		err = store.DeleteExpired(ctx)
		require.NoError(t, err)

		var count int
		err = store.db.QueryRow(ctx, "SELECT COUNT(*) FROM response_snapshots").Scan(&count)
		require.NoError(t, err)
		require.Equal(t, 0, count)

	})
}
