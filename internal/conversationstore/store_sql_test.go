package conversationstore

import (
	"context"
	"testing"
	"time"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/storage/mongotest"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
)

// These cases matter most on PostgreSQL: the atomic JSON mutations they cover
// are the one place the two engines need genuinely different statements.
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
// callers, against each backend available in this environment: SQL dialects,
// MongoDB when MONGO_TEST_DSN is set, and the in-memory store.
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
	t.Run("memory", func(t *testing.T) {
		store := NewMemoryStore()
		t.Cleanup(func() { _ = store.Close() })
		body(t, store)
	})
}

func testStoredConversation(id string) *StoredConversation {
	return &StoredConversation{
		Conversation: &core.Conversation{
			ID:       id,
			Object:   "conversation",
			Metadata: map[string]string{"topic": "testing"},
		},
		Items: []json.RawMessage{
			json.RawMessage(`{"type":"message","role":"user","content":"first"}`),
		},
		UserPath:  "/team-a",
		RequestID: "req-1",
	}
}

func TestSQLConversationExpiry(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
		ctx := context.Background()

		err := store.Create(ctx, testStoredConversation("conv-2"))
		require.NoError(t, err, "create conv-2")

		_, err = store.db.Exec(ctx,
			"UPDATE conversation_snapshots SET expires_at = ? WHERE id = ?",
			time.Now().Add(-time.Minute).Unix(), "conv-2",
		)
		require.NoError(t, err, "expire row")

		_, err = store.Get(ctx, "conv-2")
		require.ErrorIs(t, err, ErrNotFound, "get expired")
		err = store.AppendItems(ctx, "conv-2", []json.RawMessage{json.RawMessage(`{}`)})
		require.ErrorIs(t, err, ErrNotFound, "append expired")
		err = store.DeleteExpired(ctx)
		require.NoError(t, err)

		var count int
		err = store.db.QueryRow(ctx, "SELECT COUNT(*) FROM conversation_snapshots").Scan(&count)
		require.NoError(t, err)
		require.Equal(t, 0, count, "rows after sweep")
	})
}
