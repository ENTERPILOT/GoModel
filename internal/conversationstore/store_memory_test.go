package conversationstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func storedConversation(id string, storedAt time.Time) *StoredConversation {
	return &StoredConversation{
		Conversation: &core.Conversation{
			ID:       id,
			Object:   core.ConversationObject,
			Metadata: map[string]string{},
		},
		StoredAt: storedAt,
	}
}

func TestMemoryStoreCreateGetDelete(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	err := store.Create(ctx, storedConversation("conv_1", time.Time{}))
	require.NoError(t, err)

	got, err := store.Get(ctx, "conv_1")
	require.NoError(t, err)
	require.Equal(t, "conv_1", got.Conversation.ID)
	err = store.Delete(ctx, "conv_1")
	require.NoError(t, err)
	_, err = store.Get(ctx, "conv_1")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestMemoryStoreCreateRejectsDuplicate(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	err := store.Create(ctx, storedConversation("conv_dup", time.Time{}))
	require.NoError(t, err)
	require.Error(t, store.Create(ctx, storedConversation("conv_dup", time.Time{})))
}

func TestMemoryStoreDeleteMissingReturnsNotFound(t *testing.T) {
	err := NewMemoryStore().Delete(context.Background(), "conv_missing")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestMemoryStoreConcurrentAppendRejectsDuplicateItemID(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	err := store.Create(ctx, storedConversation("conv_duplicate_items", time.Time{}))
	require.NoError(t, err)

	item := json.RawMessage(`{"id":"msg_shared","type":"message","role":"user","content":[]}`)
	const writers = 32
	start := make(chan struct{})
	errs := make(chan error, writers)
	var wg sync.WaitGroup
	for range writers {
		wg.Go(func() {
			<-start
			errs <- store.AppendItems(ctx, "conv_duplicate_items", []json.RawMessage{item})
		})
	}
	close(start)
	wg.Wait()
	close(errs)

	succeeded := 0
	duplicates := 0
	for err := range errs {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrDuplicateItem):
			duplicates++
		default:
			t.Fatalf("AppendItems() unexpected error = %v", err)
		}
	}
	require.Equal(t, 1, succeeded)
	require.Equal(t, writers-1, duplicates)

	got, err := store.Get(ctx, "conv_duplicate_items")
	require.NoError(t, err)
	require.Len(t, got.Items, 1)
}

func TestMemoryStoreDeleteExpiredReturnsNotFound(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore(WithTTL(time.Second))
	err := store.Create(ctx, storedConversation("conv_expired", time.Now().UTC().Add(-2*time.Second)))
	require.NoError(t, err)
	err = store.Delete(ctx, "conv_expired")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestMemoryStoreExpiresConversations(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore(WithTTL(time.Second))
	err := store.Create(ctx, storedConversation("conv_old", time.Now().UTC().Add(-2*time.Second)))
	require.NoError(t, err)
	_, err = store.Get(ctx, "conv_old")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestMemoryStoreMaxEntriesEvictsOldest(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore(WithTTL(0), WithMaxEntries(2))
	now := time.Now().UTC()

	for _, conversation := range []*StoredConversation{
		storedConversation("conv_1", now.Add(-3*time.Second)),
		storedConversation("conv_2", now.Add(-2*time.Second)),
		storedConversation("conv_3", now.Add(-1*time.Second)),
	} {
		err := store.Create(ctx, conversation)
		require.NoError(t, err)
	}
	_, err := store.Get(ctx, "conv_1")
	require.ErrorIs(t, err, ErrNotFound)

	for _, id := range []string{"conv_2", "conv_3"} {
		_, err := store.Get(ctx, id)
		require.NoError(t, err)
	}
}

func TestMemoryStoreDefaultRetentionIsBounded(t *testing.T) {
	store := NewMemoryStore()

	require.Equal(t, DefaultMemoryStoreTTL, store.ttl)
	require.Equal(t, DefaultMemoryStoreMaxEntries, store.maxEntries)
}

func TestMemoryStoreGetReturnsIsolatedCopy(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	err := store.Create(ctx, storedConversation("conv_iso", time.Time{}))
	require.NoError(t, err)

	first, err := store.Get(ctx, "conv_iso")
	require.NoError(t, err)

	first.Conversation.Metadata["mutated"] = "true"

	second, err := store.Get(ctx, "conv_iso")
	require.NoError(t, err)
	_, mutated := second.Conversation.Metadata["mutated"]
	require.False(t, mutated)
}

func TestMemoryStoreAppendItems(t *testing.T) {
	store := NewMemoryStore()
	conv := &StoredConversation{
		Conversation: &core.Conversation{ID: "conv_append", Object: "conversation"},
		Items:        []json.RawMessage{json.RawMessage(`{"n":0}`)},
	}
	err := store.Create(context.Background(), conv)
	require.NoError(t, err)
	err = store.AppendItems(context.Background(), "conv_append", []json.RawMessage{json.RawMessage(`{"n":1}`)})
	require.NoError(t, err)
	err = store.AppendItems(context.Background(), "conv_append", nil)
	require.NoError(t, err)
	err = store.AppendItems(context.Background(), "missing", []json.RawMessage{json.RawMessage(`{}`)})
	require.ErrorIs(t, err, ErrNotFound)

	got, err := store.Get(context.Background(), "conv_append")
	require.NoError(t, err)
	require.Len(t, got.Items, 2)
	require.Equal(t, `{"n":1}`, string(got.Items[1]))
}

func TestMemoryStoreMergeMetadataAndDeleteItem(t *testing.T) {
	store := NewMemoryStore()
	conv := storedConversation("conv_items", time.Time{})
	conv.Conversation.Metadata = map[string]string{"existing": "kept"}
	conv.Items = []json.RawMessage{
		json.RawMessage(`{"id":"msg_1","type":"message"}`),
		json.RawMessage(`{"id":"msg_2","type":"message"}`),
	}
	err := store.Create(context.Background(), conv)
	require.NoError(t, err)

	merged, err := store.MergeMetadata(context.Background(), "conv_items", map[string]string{"new": "value"})
	require.NoError(t, err)
	require.Equal(t, "kept", merged.Conversation.Metadata["existing"])
	require.Equal(t, "value", merged.Conversation.Metadata["new"])
	require.Len(t, merged.Items, 2, "merged = %+v, want merged metadata and preserved items", merged)

	updated, err := store.DeleteItem(context.Background(), "conv_items", "msg_1")
	require.NoError(t, err)
	require.Len(t, updated.Items, 1)
	require.Equal(t, "msg_2", itemID(updated.Items[0]))
	_, err = store.DeleteItem(context.Background(), "conv_items", "missing")
	require.ErrorIs(t, err, ErrItemNotFound)
}

func TestMemoryStoreMergeMetadataRejectsOversizedResult(t *testing.T) {
	store := NewMemoryStore()
	conv := storedConversation("conv_metadata_limit", time.Time{})
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
	require.Len(t, got.Conversation.Metadata, core.MaxConversationMetadataPairs)
}

func TestMemoryStoreAppendItems_ConcurrentAppendsAllSurvive(t *testing.T) {
	store := NewMemoryStore()
	conv := &StoredConversation{Conversation: &core.Conversation{ID: "conv_race", Object: "conversation"}}
	err := store.Create(context.Background(), conv)
	require.NoError(t, err)

	const writers = 20
	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			item := json.RawMessage(fmt.Sprintf(`{"writer":%d}`, n))
			err := store.AppendItems(context.Background(), "conv_race", []json.RawMessage{item})
			assert.NoError(t, err)

		}(i)
	}
	wg.Wait()

	got, err := store.Get(context.Background(), "conv_race")
	require.NoError(t, err)
	require.Len(t, got.Items, writers)

	seen := make(map[int]int, writers)
	for _, raw := range got.Items {
		var item struct {
			Writer int `json:"writer"`
		}
		err := json.Unmarshal(raw, &item)
		require.NoError(t, err)

		seen[item.Writer]++
	}
	for i := range writers {
		require.Equal(t, 1, seen[i], "writer %d count = %d, want exactly once (no lost or duplicated appends)", i, seen[i])
	}
}

func TestMemoryStoreMaxBytesEvictsOldest(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	item := json.RawMessage(fmt.Sprintf(`{"type":"message","content":%q}`, strings.Repeat("x", 500)))
	large := func(id string, storedAt time.Time) *StoredConversation {
		c := storedConversation(id, storedAt)
		c.Items = []json.RawMessage{item}
		return c
	}

	// Size one entry via a probe store, then budget for exactly two.
	probe := NewMemoryStore(WithTTL(0))
	err := probe.Create(ctx, large("probe", now))
	require.NoError(t, err)

	budget := 2*probe.totalBytes + 10

	store := NewMemoryStore(WithTTL(0), WithMaxEntries(0), WithMaxBytes(budget))
	for i, conversation := range []*StoredConversation{
		large("conv_1", now.Add(-3*time.Second)),
		large("conv_2", now.Add(-2*time.Second)),
		large("conv_3", now.Add(-1*time.Second)),
	} {
		err := store.Create(ctx, conversation)
		require.NoError(t, err, "create conversation %d", i)
	}
	_, err = store.Get(ctx, "conv_1")
	require.ErrorIs(t, err, ErrNotFound)

	for _, id := range []string{"conv_2", "conv_3"} {
		_, err := store.Get(ctx, id)
		require.NoError(t, err)
	}
}

func TestMemoryStoreAppendItemsCountsTowardByteBudget(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()

	store := NewMemoryStore(WithTTL(0), WithMaxEntries(0), WithMaxBytes(2100))
	err := store.Create(ctx, storedConversation("conv_old", now.Add(-time.Minute)))
	require.NoError(t, err)
	err = store.Create(ctx, storedConversation("conv_grow", now))
	require.NoError(t, err)

	// Growing conv_grow within its own budget but past the total evicts the
	// older conversation, never the one just appended to.
	item := json.RawMessage(fmt.Sprintf(`{"type":"message","content":%q}`, strings.Repeat("x", 1800)))
	err = store.AppendItems(ctx, "conv_grow", []json.RawMessage{item})
	require.NoError(t, err)
	_, err = store.Get(ctx, "conv_old")
	require.ErrorIs(t, err, ErrNotFound)

	grown, err := store.Get(ctx, "conv_grow")
	require.NoError(t, err)
	require.Len(t, grown.Items, 1)
	require.LessOrEqual(t, store.totalBytes, int64(2100))

	_, exactSize, err := cloneConversationWithSize(grown)
	require.NoError(t, err)
	require.Equal(t, exactSize, store.sizes["conv_grow"])
}

func TestMemoryStoreAppendItemsRejectsOversizeGrowth(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()

	store := NewMemoryStore(WithTTL(0), WithMaxEntries(0), WithMaxBytes(2100))
	err := store.Create(ctx, storedConversation("conv_other", now.Add(-time.Minute)))
	require.NoError(t, err)
	err = store.Create(ctx, storedConversation("conv_grow", now))
	require.NoError(t, err)

	// An append that would grow the conversation past the whole budget is
	// rejected outright instead of evicting the store out from under it.
	item := json.RawMessage(fmt.Sprintf(`{"type":"message","content":%q}`, strings.Repeat("x", 2500)))
	require.Error(t, store.AppendItems(ctx, "conv_grow", []json.RawMessage{item}))

	grown, err := store.Get(ctx, "conv_grow")
	require.NoError(t, err)
	require.Empty(t, grown.Items)
	_, err = store.Get(ctx, "conv_other")
	require.NoError(t, err)
}

func TestMemoryStoreAppendItemsNeverEvictsAppendedConversation(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()

	store := NewMemoryStore(WithTTL(0), WithMaxEntries(0), WithMaxBytes(2100))
	err := // conv_grow is the OLDEST entry — without protection, oldest-first
		// eviction would drop it right after its own successful append.
		store.Create(ctx, storedConversation("conv_grow", now.Add(-time.Minute)))
	require.NoError(t, err)
	err = store.Create(ctx, storedConversation("conv_new", now))
	require.NoError(t, err)

	item := json.RawMessage(fmt.Sprintf(`{"type":"message","content":%q}`, strings.Repeat("x", 1800)))
	err = store.AppendItems(ctx, "conv_grow", []json.RawMessage{item})
	require.NoError(t, err)

	grown, err := store.Get(ctx, "conv_grow")
	require.NoError(t, err)
	require.Len(t, grown.Items, 1)
	_, err = store.Get(ctx, "conv_new")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestMemoryStoreRejectsConversationOverByteBudget(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore(WithMaxBytes(100))
	c := storedConversation("conv_big", time.Now().UTC())
	c.Items = []json.RawMessage{json.RawMessage(fmt.Sprintf(`{"content":%q}`, strings.Repeat("x", 200)))}
	require.Error(t, store.Create(ctx, c))
}
