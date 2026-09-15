package conversationstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

// Interface-level behaviour, asserted once and run on every backend through
// runStoreSuite. Backend-specific cases live next to their store's helper.

func TestConversationStoreCreateGetRoundtrip(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()

		if err := store.Create(ctx, testStoredConversation("conv-1")); err != nil {
			t.Fatalf("create: %v", err)
		}

		got, err := store.Get(ctx, "conv-1")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if got.Conversation == nil || got.Conversation.ID != "conv-1" {
			t.Fatalf("conversation = %+v, want id conv-1", got.Conversation)
		}
		if got.Conversation.Metadata["topic"] != "testing" {
			t.Fatalf("metadata = %v, want topic=testing", got.Conversation.Metadata)
		}
		if len(got.Items) != 1 || !strings.Contains(string(got.Items[0]), "first") {
			t.Fatalf("items = %v, want original item", got.Items)
		}
		if got.UserPath != "/team-a" || got.RequestID != "req-1" {
			t.Fatalf("metadata = %+v, want user path and request id preserved", got)
		}
		if got.StoredAt.IsZero() || got.ExpiresAt.IsZero() {
			t.Fatalf("retention not stamped: stored %v expires %v", got.StoredAt, got.ExpiresAt)
		}
	})
}

func TestConversationStoreGetMissingReturnsNotFound(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		if _, err := store.Get(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("get missing err = %v, want ErrNotFound", err)
		}
	})
}

func TestConversationStoreCreateRejectsDuplicates(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()

		if err := store.Create(ctx, testStoredConversation("conv-1")); err != nil {
			t.Fatalf("create: %v", err)
		}
		err := store.Create(ctx, testStoredConversation("conv-1"))
		if err == nil || !strings.Contains(err.Error(), "already exists") {
			t.Fatalf("duplicate create err = %v, want already exists", err)
		}
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
		if err := store.Create(ctx, conv); err != nil {
			t.Fatalf("create: %v", err)
		}

		merged, err := store.MergeMetadata(ctx, "conv-meta", map[string]string{"new": "value", "topic": "new"})
		if err != nil {
			t.Fatalf("merge metadata: %v", err)
		}
		want := map[string]string{"existing": "kept", "topic": "new", "new": "value"}
		if len(merged.Conversation.Metadata) != len(want) {
			t.Fatalf("merged metadata = %v, want %v", merged.Conversation.Metadata, want)
		}
		for key, value := range want {
			if merged.Conversation.Metadata[key] != value {
				t.Fatalf("merged metadata[%q] = %q, want %q", key, merged.Conversation.Metadata[key], value)
			}
		}
		// The returned snapshot must be the stored one, items included.
		if len(merged.Items) != 2 {
			t.Fatalf("merged items = %s, want both items preserved", merged.Items)
		}
		got, err := store.Get(ctx, "conv-meta")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if got.Conversation.Metadata["new"] != "value" || got.Conversation.Metadata["topic"] != "new" {
			t.Fatalf("stored metadata = %v, want merge persisted", got.Conversation.Metadata)
		}

		if _, err := store.MergeMetadata(ctx, "missing", map[string]string{"k": "v"}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("merge missing err = %v, want ErrNotFound", err)
		}
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
		if err := store.Create(ctx, conv); err != nil {
			t.Fatalf("Create() error = %v", err)
		}

		if _, err := store.MergeMetadata(ctx, conv.Conversation.ID, map[string]string{"extra": "value"}); !errors.Is(err, ErrMetadataLimitExceeded) {
			t.Fatalf("MergeMetadata() error = %v, want ErrMetadataLimitExceeded", err)
		}
		got, err := store.Get(ctx, conv.Conversation.ID)
		if err != nil {
			t.Fatalf("Get() error = %v", err)
		}
		if len(got.Conversation.Metadata) != core.MaxConversationMetadataPairs {
			t.Fatalf("metadata size = %d, want %d", len(got.Conversation.Metadata), core.MaxConversationMetadataPairs)
		}
	})
}

func TestConversationStoreAppendItemsPreservesOrder(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()

		if err := store.Create(ctx, testStoredConversation("conv-1")); err != nil {
			t.Fatalf("create: %v", err)
		}

		// A multi-item append with nested JSON exercises the per-backend
		// array-append paths (chained json_insert on SQL, $push on MongoDB).
		err := store.AppendItems(ctx, "conv-1", []json.RawMessage{
			json.RawMessage(`{"type":"message","role":"assistant","content":"second"}`),
			json.RawMessage(`{"type":"message","role":"user","content":"third","nested":{"n":1}}`),
		})
		if err != nil {
			t.Fatalf("append: %v", err)
		}
		if err := store.AppendItems(ctx, "conv-1", []json.RawMessage{
			json.RawMessage(`{"type":"message","role":"assistant","content":"fourth"}`),
		}); err != nil {
			t.Fatalf("second append: %v", err)
		}

		got, err := store.Get(ctx, "conv-1")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if len(got.Items) != 4 {
			t.Fatalf("items len = %d, want 4", len(got.Items))
		}
		for i, want := range []string{"first", "second", "third", "fourth"} {
			if !strings.Contains(string(got.Items[i]), want) {
				t.Fatalf("items[%d] = %s, want to contain %q", i, got.Items[i], want)
			}
		}
		var nested struct {
			Nested map[string]int `json:"nested"`
		}
		if err := json.Unmarshal(got.Items[2], &nested); err != nil || nested.Nested["n"] != 1 {
			t.Fatalf("items[2] nested = %s (err %v), want nested.n=1", got.Items[2], err)
		}
	})
}

func TestConversationStoreAppendItemsMissingReturnsNotFound(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		err := store.AppendItems(context.Background(), "missing", []json.RawMessage{
			json.RawMessage(`{"type":"message"}`),
		})
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("append missing err = %v, want ErrNotFound", err)
		}
	})
}

func TestConversationStoreAppendItemsRejectsDuplicateID(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()
		conv := testStoredConversation("conv-duplicate-items")
		conv.Items = []json.RawMessage{json.RawMessage(`{"id":"msg_existing","type":"message"}`)}
		if err := store.Create(ctx, conv); err != nil {
			t.Fatalf("create: %v", err)
		}

		err := store.AppendItems(ctx, conv.Conversation.ID, []json.RawMessage{
			json.RawMessage(`{"id":"msg_existing","type":"message","content":"duplicate"}`),
		})
		if !errors.Is(err, ErrDuplicateItem) {
			t.Fatalf("append duplicate err = %v, want ErrDuplicateItem", err)
		}
		got, err := store.Get(ctx, conv.Conversation.ID)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if len(got.Items) != 1 {
			t.Fatalf("stored items = %d, want unchanged length 1", len(got.Items))
		}
	})
}

// Two Responses turns completing at once must both land: an append that
// reads, modifies and writes the whole array would drop one side's items.
func TestConversationStoreConcurrentAppendsAllSurvive(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()
		conv := testStoredConversation("conv-concurrent")
		conv.Items = nil
		if err := store.Create(ctx, conv); err != nil {
			t.Fatalf("create: %v", err)
		}

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
			t.Error(err)
		}

		got, err := store.Get(ctx, "conv-concurrent")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if len(got.Items) != writers*perWriter {
			t.Fatalf("items len = %d, want %d", len(got.Items), writers*perWriter)
		}
		// Every id survives exactly once, and each writer's own items keep
		// their submission order even when interleaved with other writers.
		seen := make(map[string]struct{}, len(got.Items))
		lastSeq := make(map[int]int, writers)
		for _, raw := range got.Items {
			id := itemID(raw)
			if _, dup := seen[id]; dup {
				t.Fatalf("item %q stored twice", id)
			}
			seen[id] = struct{}{}
			var writer, seq int
			if _, err := fmt.Sscanf(id, "msg_%d_%d", &writer, &seq); err != nil {
				t.Fatalf("unexpected item id %q: %v", id, err)
			}
			if last, ok := lastSeq[writer]; ok && seq <= last {
				t.Fatalf("writer %d item %d stored after item %d", writer, seq, last)
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
		if err := store.Create(ctx, conv); err != nil {
			t.Fatalf("create: %v", err)
		}

		updated, err := store.DeleteItem(ctx, "conv-items", "msg_1")
		if err != nil {
			t.Fatalf("delete item: %v", err)
		}
		if len(updated.Items) != 1 || itemID(updated.Items[0]) != "msg_2" {
			t.Fatalf("items = %s, want msg_2 only", updated.Items)
		}
		if updated.Conversation == nil || updated.Conversation.Metadata["topic"] != "testing" {
			t.Fatalf("returned snapshot = %+v, want full conversation", updated)
		}
		got, err := store.Get(ctx, "conv-items")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if len(got.Items) != 1 || itemID(got.Items[0]) != "msg_2" {
			t.Fatalf("stored items = %s, want msg_2 only", got.Items)
		}

		if _, err := store.DeleteItem(ctx, "conv-items", "missing"); !errors.Is(err, ErrItemNotFound) {
			t.Fatalf("delete missing item err = %v, want ErrItemNotFound", err)
		}
		if _, err := store.DeleteItem(ctx, "missing", "msg_2"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("delete item of missing conversation err = %v, want ErrNotFound", err)
		}
	})
}

func TestConversationStoreDeleteThenGetNotFound(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()

		if err := store.Create(ctx, testStoredConversation("conv-1")); err != nil {
			t.Fatalf("create: %v", err)
		}
		if err := store.Delete(ctx, "conv-1"); err != nil {
			t.Fatalf("delete: %v", err)
		}
		if _, err := store.Get(ctx, "conv-1"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("get after delete err = %v, want ErrNotFound", err)
		}
		if err := store.Delete(ctx, "conv-1"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("second delete err = %v, want ErrNotFound", err)
		}
		// The id is free again once the snapshot is gone.
		if err := store.Create(ctx, testStoredConversation("conv-1")); err != nil {
			t.Fatalf("recreate after delete: %v", err)
		}
	})
}
