package conversationstore

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

// Interface-level behaviour, asserted once and run on every backend through
// runStoreSuite. Backend-specific cases live next to their store's helper.

func TestConversationStoreCreateGetRoundtrip(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()

		err := store.Create(ctx, testStoredConversation("conv-1"))
		require.NoError(t, err)

		got, err := store.Get(ctx, "conv-1")
		require.NoError(t, err)
		require.NotNil(t, got.Conversation)
		require.Equal(t, "conv-1", got.Conversation.ID)
		require.Equal(t, "testing", got.Conversation.Metadata["topic"], "metadata = %v", got.Conversation.Metadata)
		require.Len(t, got.Items, 1)
		require.Contains(t, string(got.Items[0]), "first")
		require.Equal(t, "/team-a", got.UserPath)
		require.Equal(t, "req-1", got.RequestID)
		require.False(t, got.StoredAt.IsZero(), "StoredAt not stamped")
		require.False(t, got.ExpiresAt.IsZero(), "ExpiresAt not stamped")
	})
}

func TestConversationStoreGetMissingReturnsNotFound(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		_, err := store.Get(context.Background(), "missing")
		require.ErrorIs(t, err, ErrNotFound)
	})
}

func TestConversationStoreCreateRejectsDuplicates(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()

		err := store.Create(ctx, testStoredConversation("conv-1"))
		require.NoError(t, err)

		err = store.Create(ctx, testStoredConversation("conv-1"))
		require.Error(t, err)
		require.Contains(t, err.Error(), "already exists")
	})
}

func TestConversationStoreMergeMetadataOverlays(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()
		conv := testStoredConversation("conv-meta")
		conv.Conversation.Metadata = map[string]string{"existing": "kept", "topic": "old"}
		conv.Items = []json.RawMessage{
			json.RawMessage(`{"id":"msg_1","type":"message"}`),
			json.RawMessage(`{"id":"msg_2","type":"message"}`),
		}
		err := store.Create(ctx, conv)
		require.NoError(t, err)

		merged, err := store.MergeMetadata(ctx, "conv-meta", map[string]string{"new": "value", "topic": "new"})
		require.NoError(t, err)
		require.Equal(t, map[string]string{"existing": "kept", "topic": "new", "new": "value"}, merged.Conversation.Metadata)
		// The returned snapshot must be the stored one, items included.
		require.Len(t, merged.Items, 2, "merged items = %s, want both items preserved", merged.Items)

		got, err := store.Get(ctx, "conv-meta")
		require.NoError(t, err)
		require.Equal(t, "value", got.Conversation.Metadata["new"], "stored metadata = %v, want merge persisted", got.Conversation.Metadata)
		require.Equal(t, "new", got.Conversation.Metadata["topic"], "stored metadata = %v, want merge persisted", got.Conversation.Metadata)

		_, err = store.MergeMetadata(ctx, "missing", map[string]string{"k": "v"})
		require.ErrorIs(t, err, ErrNotFound)
	})
}

func TestConversationStoreMergeMetadataRejectsOversizedResult(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()
		conv := testStoredConversation("conv-metadata-limit")
		conv.Conversation.Metadata = make(map[string]string, core.MaxConversationMetadataPairs)
		for index := range core.MaxConversationMetadataPairs {
			conv.Conversation.Metadata[fmt.Sprintf("key_%d", index)] = "value"
		}
		err := store.Create(ctx, conv)
		require.NoError(t, err)

		_, err = store.MergeMetadata(ctx, conv.Conversation.ID, map[string]string{"extra": "value"})
		require.ErrorIs(t, err, ErrMetadataLimitExceeded)

		got, err := store.Get(ctx, conv.Conversation.ID)
		require.NoError(t, err)
		require.Len(t, got.Conversation.Metadata, core.MaxConversationMetadataPairs)
	})
}

func TestConversationStoreAppendItemsPreservesOrder(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()

		err := store.Create(ctx, testStoredConversation("conv-1"))
		require.NoError(t, err)

		// A multi-item append with nested JSON exercises the per-backend
		// array-append paths (chained json_insert on SQL, $push on MongoDB).
		err = store.AppendItems(ctx, "conv-1", []json.RawMessage{
			json.RawMessage(`{"type":"message","role":"assistant","content":"second"}`),
			json.RawMessage(`{"type":"message","role":"user","content":"third","nested":{"n":1}}`),
		})
		require.NoError(t, err)
		err = store.AppendItems(ctx, "conv-1", []json.RawMessage{
			json.RawMessage(`{"type":"message","role":"assistant","content":"fourth"}`),
		})
		require.NoError(t, err, "second append")

		got, err := store.Get(ctx, "conv-1")
		require.NoError(t, err)
		require.Len(t, got.Items, 4)
		for i, want := range []string{"first", "second", "third", "fourth"} {
			require.Contains(t, string(got.Items[i]), want, "items[%d]", i)
		}
		var nested struct {
			Nested map[string]int `json:"nested"`
		}
		err = json.Unmarshal(got.Items[2], &nested)
		require.NoError(t, err, "items[2] = %s", got.Items[2])
		require.Equal(t, 1, nested.Nested["n"], "items[2] = %s, want nested.n=1", got.Items[2])
	})
}

func TestConversationStoreAppendItemsMissingReturnsNotFound(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		err := store.AppendItems(context.Background(), "missing", []json.RawMessage{
			json.RawMessage(`{"type":"message"}`),
		})
		require.ErrorIs(t, err, ErrNotFound)
	})
}

func TestConversationStoreAppendItemsRejectsDuplicateID(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
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
		require.Len(t, got.Items, 1, "stored items should be unchanged")
	})
}

// Two Responses turns completing at once must both land: an append that
// reads, modifies and writes the whole array would drop one side's items.
func TestConversationStoreConcurrentAppendsAllSurvive(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()
		conv := testStoredConversation("conv-concurrent")
		conv.Items = nil
		err := store.Create(ctx, conv)
		require.NoError(t, err)

		const writers, perWriter = 4, 8
		var wg sync.WaitGroup
		errs := make(chan error, writers*perWriter)
		for writer := range writers {
			wg.Go(func() {
				for seq := range perWriter {
					item := fmt.Sprintf(`{"id":"msg_%d_%d","type":"message"}`, writer, seq)
					if err := store.AppendItems(ctx, "conv-concurrent", []json.RawMessage{json.RawMessage(item)}); err != nil {
						errs <- fmt.Errorf("writer %d seq %d: %w", writer, seq, err)
					}
				}
			})
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			require.NoError(t, err)
		}

		got, err := store.Get(ctx, "conv-concurrent")
		require.NoError(t, err)
		require.Len(t, got.Items, writers*perWriter)
		// Every id survives exactly once, and each writer's own items keep
		// their submission order even when interleaved with other writers.
		seen := make(map[string]struct{}, len(got.Items))
		lastSeq := make(map[int]int, writers)
		for _, raw := range got.Items {
			id := itemID(raw)
			_, dup := seen[id]
			require.False(t, dup, "item %q stored twice", id)
			seen[id] = struct{}{}
			var writer, seq int
			_, err := fmt.Sscanf(id, "msg_%d_%d", &writer, &seq)
			require.NoError(t, err, "unexpected item id %q", id)
			if last, ok := lastSeq[writer]; ok {
				require.Greater(t, seq, last, "writer %d item %d stored after item %d", writer, seq, last)
			}
			lastSeq[writer] = seq
		}
	})
}

func TestConversationStoreDeleteItem(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()
		conv := testStoredConversation("conv-items")
		conv.Items = []json.RawMessage{
			json.RawMessage(`{"id":"msg_1","type":"message"}`),
			json.RawMessage(`{"id":"msg_2","type":"message"}`),
		}
		err := store.Create(ctx, conv)
		require.NoError(t, err)

		updated, err := store.DeleteItem(ctx, "conv-items", "msg_1")
		require.NoError(t, err)
		require.Len(t, updated.Items, 1, "items = %s, want msg_2 only", updated.Items)
		require.Equal(t, "msg_2", itemID(updated.Items[0]))
		require.NotNil(t, updated.Conversation, "returned snapshot = %+v, want full conversation", updated)
		require.Equal(t, "testing", updated.Conversation.Metadata["topic"], "returned snapshot = %+v, want full conversation", updated)

		got, err := store.Get(ctx, "conv-items")
		require.NoError(t, err)
		require.Len(t, got.Items, 1, "stored items = %s, want msg_2 only", got.Items)
		require.Equal(t, "msg_2", itemID(got.Items[0]))

		_, err = store.DeleteItem(ctx, "conv-items", "missing")
		require.ErrorIs(t, err, ErrItemNotFound)
		_, err = store.DeleteItem(ctx, "missing", "msg_2")
		require.ErrorIs(t, err, ErrNotFound)
	})
}

func TestConversationStoreDeleteThenGetNotFound(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()

		err := store.Create(ctx, testStoredConversation("conv-1"))
		require.NoError(t, err)
		err = store.Delete(ctx, "conv-1")
		require.NoError(t, err)
		_, err = store.Get(ctx, "conv-1")
		require.ErrorIs(t, err, ErrNotFound)
		err = store.Delete(ctx, "conv-1")
		require.ErrorIs(t, err, ErrNotFound, "second delete")
		// The id is free again once the snapshot is gone.
		err = store.Create(ctx, testStoredConversation("conv-1"))
		require.NoError(t, err, "recreate after delete")
	})
}
