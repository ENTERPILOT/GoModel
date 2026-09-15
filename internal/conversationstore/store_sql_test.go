package conversationstore

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
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

func TestSQLConversationCreateGetRoundtrip(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
		ctx := context.Background()
		err := store.Create(ctx, testStoredConversation("conv-1"))
		require.NoError(t, err)

		got, err := store.Get(ctx, "conv-1")
		require.NoError(t, err)
		require.NotNil(t, got.Conversation)
		require.Equal(t, "conv-1", got.Conversation.ID)
		require.Equal(t, "testing", got.Conversation.Metadata["topic"], "metadata = %v, want topic=testing", got.Conversation.Metadata)
		require.Len(t, got.Items, 1)
		require.Contains(t, string(got.Items[0]), "first")
		require.Equal(t, "/team-a", got.UserPath)
		require.Equal(t, "req-1", got.RequestID, "metadata = %+v, want user path and request id preserved", got)
		require.False(t, got.StoredAt.IsZero())
		require.False(t, got.ExpiresAt.IsZero(), "retention not stamped: stored %v expires %v", got.StoredAt, got.ExpiresAt)

	})
}

func TestSQLConversationAppendItemsPreservesOrder(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
		ctx := context.Background()
		err := store.Create(ctx, testStoredConversation("conv-1"))
		require.NoError(t, err)

		// A multi-item append exercises the chained '$[#]' json_insert paths.
		err = store.AppendItems(ctx, "conv-1", []json.RawMessage{
			json.RawMessage(`{"type":"message","role":"assistant","content":"second"}`),
			json.RawMessage(`{"type":"message","role":"user","content":"third","nested":{"n":1}}`),
		})
		require.NoError(t, err)
		err = store.AppendItems(ctx, "conv-1", []json.RawMessage{
			json.RawMessage(`{"type":"message","role":"assistant","content":"fourth"}`),
		})
		require.NoError(t, err)

		got, err := store.Get(ctx, "conv-1")
		require.NoError(t, err)
		require.Len(t, got.Items, 4)

		for i, want := range []string{"first", "second", "third", "fourth"} {
			require.Contains(t, string(got.Items[i]), want)
		}
		var nested struct {
			Nested map[string]int `json:"nested"`
		}
		err = json.Unmarshal(got.Items[2], &nested)
		require.NoError(t, err)
		require.Equal(t, 1, nested.Nested["n"])

	})
}

func TestSQLConversationAppendItemsMissingReturnsNotFound(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
		err := store.AppendItems(context.Background(), "missing", []json.RawMessage{
			json.RawMessage(`{"type":"message"}`),
		})
		require.ErrorIs(t, err, ErrNotFound)

	})
}

func TestSQLConversationAppendItemsRejectsDuplicateID(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
		ctx := context.Background()
		conv := testStoredConversation("conv-duplicate-items")
		conv.Items = []json.RawMessage{json.RawMessage(`{"id":"msg_existing","type":"message"}`)}
		err := store.Create(ctx, conv)
		require.NoError(t, err)

		err = store.AppendItems(ctx, conv.Conversation.ID, []json.RawMessage{
			json.RawMessage(`{"id":"msg_existing","type":"message","content":"duplicate"}`),
		})
		require.ErrorIs(t, err, ErrDuplicateItem)

		got, err := store.Get(ctx, conv.Conversation.ID)
		require.NoError(t, err)
		require.Len(t, got.Items, 1)

	})
}

func TestSQLConversationMergeMetadataAndDeleteItem(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
		ctx := context.Background()
		conv := testStoredConversation("conv-items")
		conv.Conversation.Metadata = map[string]string{"existing": "kept"}
		conv.Items = []json.RawMessage{
			json.RawMessage(`{"id":"msg_1","type":"message"}`),
			json.RawMessage(`{"id":"msg_2","type":"message"}`),
		}
		err := store.Create(ctx, conv)
		require.NoError(t, err)

		merged, err := store.MergeMetadata(ctx, "conv-items", map[string]string{"new": "value"})
		require.NoError(t, err)
		require.Equal(t, "kept", merged.Conversation.Metadata["existing"])
		require.Equal(t, "value", merged.Conversation.Metadata["new"])
		require.Len(t, merged.Items, 2, "merged = %+v, want merged metadata and preserved items", merged)

		updated, err := store.DeleteItem(ctx, "conv-items", "msg_1")
		require.NoError(t, err)
		require.Len(t, updated.Items, 1)
		require.Equal(t, "msg_2", itemID(updated.Items[0]))
		_, err = store.DeleteItem(ctx, "conv-items", "missing")
		require.ErrorIs(t, err, ErrItemNotFound)

	})
}

func TestSQLConversationMergeMetadataRejectsOversizedResult(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
		conv := testStoredConversation("conv_sqlite_metadata_limit")
		conv.Conversation.Metadata = make(map[string]string, core.MaxConversationMetadataPairs)
		for index := range core.MaxConversationMetadataPairs {
			conv.Conversation.Metadata[fmt.Sprintf("key_%d", index)] = "value"
		}
		err := store.Create(context.Background(), conv)
		require.NoError(t, err)
		_, err = store.MergeMetadata(context.Background(), conv.Conversation.ID, map[string]string{"extra": "value"})
		require.ErrorIs(t, err, ErrMetadataLimitExceeded)

		got, err := store.Get(context.Background(), conv.Conversation.ID)
		require.NoError(t, err)
		require.Equal(t, core.MaxConversationMetadataPairs, len(got.Conversation.Metadata))

	})
}

func TestSQLConversationCreateRejectsDuplicates(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
		ctx := context.Background()
		err := store.Create(ctx, testStoredConversation("conv-1"))
		require.NoError(t, err)

		err = store.Create(ctx, testStoredConversation("conv-1"))
		require.Error(t, err)
		require.Contains(t, err.Error(), "already exists")

	})
}

func TestSQLConversationDeleteAndExpiry(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
		ctx := context.Background()
		err := store.Create(ctx, testStoredConversation("conv-1"))
		require.NoError(t, err)
		err = store.Delete(ctx, "conv-1")
		require.NoError(t, err)
		_, err = store.Get(ctx, "conv-1")
		require.ErrorIs(t, err, ErrNotFound)
		err = store.Create(ctx, testStoredConversation("conv-2"))
		require.NoError(t, err)
		_, err = store.db.Exec(ctx,
			"UPDATE conversation_snapshots SET expires_at = ? WHERE id = ?",
			time.Now().Add(-time.Minute).Unix(), "conv-2",
		)
		require.NoError(t, err)
		_, err = store.Get(ctx, "conv-2")
		require.ErrorIs(t, err, ErrNotFound)
		err = store.AppendItems(ctx, "conv-2", []json.RawMessage{json.RawMessage(`{}`)})
		require.ErrorIs(t, err, ErrNotFound)
		err = store.DeleteExpired(ctx)
		require.NoError(t, err)

		var count int
		err = store.db.QueryRow(ctx, "SELECT COUNT(*) FROM conversation_snapshots").Scan(&count)
		require.NoError(t, err)
		require.Equal(t, 0, count)

	})
}
