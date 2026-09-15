package conversationstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/goccy/go-json"
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
		if err != nil {
			t.Fatalf("NewSQLStore: %v", err)
		}
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
		if err != nil {
			t.Fatalf("NewSQLStore: %v", err)
		}
		t.Cleanup(func() { _ = store.Close() })
		body(t, store)
	})
	mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
		store, err := NewMongoDBStore(db)
		if err != nil {
			t.Fatalf("NewMongoDBStore: %v", err)
		}
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

		if err := store.Create(ctx, testStoredConversation("conv-2")); err != nil {
			t.Fatalf("create conv-2: %v", err)
		}
		if _, err := store.db.Exec(ctx,
			"UPDATE conversation_snapshots SET expires_at = ? WHERE id = ?",
			time.Now().Add(-time.Minute).Unix(), "conv-2",
		); err != nil {
			t.Fatalf("expire row: %v", err)
		}
		if _, err := store.Get(ctx, "conv-2"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("get expired err = %v, want ErrNotFound", err)
		}
		if err := store.AppendItems(ctx, "conv-2", []json.RawMessage{json.RawMessage(`{}`)}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("append expired err = %v, want ErrNotFound", err)
		}
		if err := store.DeleteExpired(ctx); err != nil {
			t.Fatalf("delete expired: %v", err)
		}
		var count int
		if err := store.db.QueryRow(ctx, "SELECT COUNT(*) FROM conversation_snapshots").Scan(&count); err != nil {
			t.Fatalf("count rows: %v", err)
		}
		if count != 0 {
			t.Fatalf("rows after sweep = %d, want 0", count)
		}
	})
}
