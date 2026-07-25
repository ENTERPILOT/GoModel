package conversationstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/goccy/go-json"

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
		if err != nil {
			t.Fatalf("NewSQLStore: %v", err)
		}
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

func TestSQLConversationAppendItemsPreservesOrder(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
		ctx := context.Background()

		if err := store.Create(ctx, testStoredConversation("conv-1")); err != nil {
			t.Fatalf("create: %v", err)
		}

		// A multi-item append exercises the chained '$[#]' json_insert paths.
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

func TestSQLConversationAppendItemsMissingReturnsNotFound(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
		err := store.AppendItems(context.Background(), "missing", []json.RawMessage{
			json.RawMessage(`{"type":"message"}`),
		})
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("append missing err = %v, want ErrNotFound", err)
		}
	})
}

func TestSQLConversationAppendItemsRejectsDuplicateID(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
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

func TestSQLConversationMergeMetadataAndDeleteItem(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
		ctx := context.Background()
		conv := testStoredConversation("conv-items")
		conv.Conversation.Metadata = map[string]string{"existing": "kept"}
		conv.Items = []json.RawMessage{
			json.RawMessage(`{"id":"msg_1","type":"message"}`),
			json.RawMessage(`{"id":"msg_2","type":"message"}`),
		}
		if err := store.Create(ctx, conv); err != nil {
			t.Fatalf("create: %v", err)
		}
		merged, err := store.MergeMetadata(ctx, "conv-items", map[string]string{"new": "value"})
		if err != nil {
			t.Fatalf("merge metadata: %v", err)
		}
		if merged.Conversation.Metadata["existing"] != "kept" || merged.Conversation.Metadata["new"] != "value" || len(merged.Items) != 2 {
			t.Fatalf("merged = %+v, want merged metadata and preserved items", merged)
		}
		updated, err := store.DeleteItem(ctx, "conv-items", "msg_1")
		if err != nil {
			t.Fatalf("delete item: %v", err)
		}
		if len(updated.Items) != 1 || itemID(updated.Items[0]) != "msg_2" {
			t.Fatalf("items = %s, want msg_2 only", updated.Items)
		}
		if _, err := store.DeleteItem(ctx, "conv-items", "missing"); !errors.Is(err, ErrItemNotFound) {
			t.Fatalf("delete missing item error = %v, want ErrItemNotFound", err)
		}
	})
}

func TestSQLConversationMergeMetadataRejectsOversizedResult(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
		conv := testStoredConversation("conv_sqlite_metadata_limit")
		conv.Conversation.Metadata = make(map[string]string, core.MaxConversationMetadataPairs)
		for index := range core.MaxConversationMetadataPairs {
			conv.Conversation.Metadata[fmt.Sprintf("key_%d", index)] = "value"
		}
		if err := store.Create(context.Background(), conv); err != nil {
			t.Fatalf("Create() error = %v", err)
		}

		if _, err := store.MergeMetadata(context.Background(), conv.Conversation.ID, map[string]string{"extra": "value"}); !errors.Is(err, ErrMetadataLimitExceeded) {
			t.Fatalf("MergeMetadata() error = %v, want ErrMetadataLimitExceeded", err)
		}
		got, err := store.Get(context.Background(), conv.Conversation.ID)
		if err != nil {
			t.Fatalf("Get() error = %v", err)
		}
		if len(got.Conversation.Metadata) != core.MaxConversationMetadataPairs {
			t.Fatalf("metadata size = %d, want %d", len(got.Conversation.Metadata), core.MaxConversationMetadataPairs)
		}
	})
}

func TestSQLConversationCreateRejectsDuplicates(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
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

func TestSQLConversationDeleteAndExpiry(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
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
