package conversationstore

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/storage"
)

func newSQLiteTestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	st, err := storage.NewSQLite(storage.SQLiteConfig{Path: filepath.Join(t.TempDir(), "conversations.db")})
	if err != nil {
		t.Fatalf("new sqlite storage: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	store, err := NewSQLiteStore(st.DB())
	if err != nil {
		t.Fatalf("new sqlite conversation store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
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

func TestSQLiteConversationCreateGetRoundtrip(t *testing.T) {
	store := newSQLiteTestStore(t)
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
}

func TestSQLiteConversationAppendItemsPreservesOrder(t *testing.T) {
	store := newSQLiteTestStore(t)
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
}

func TestSQLiteConversationAppendItemsMissingReturnsNotFound(t *testing.T) {
	store := newSQLiteTestStore(t)
	err := store.AppendItems(context.Background(), "missing", []json.RawMessage{
		json.RawMessage(`{"type":"message"}`),
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("append missing err = %v, want ErrNotFound", err)
	}
}

func TestSQLiteConversationUpdateReplacesItemsAndPreservesRetention(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()

	if err := store.Create(ctx, testStoredConversation("conv-1")); err != nil {
		t.Fatalf("create: %v", err)
	}
	created, err := store.Get(ctx, "conv-1")
	if err != nil {
		t.Fatalf("get created: %v", err)
	}

	updated := testStoredConversation("conv-1")
	updated.Conversation.Metadata = map[string]string{"topic": "changed"}
	updated.Items = []json.RawMessage{json.RawMessage(`{"type":"message","content":"replaced"}`)}
	if err := store.Update(ctx, updated); err != nil {
		t.Fatalf("update: %v", err)
	}

	got, err := store.Get(ctx, "conv-1")
	if err != nil {
		t.Fatalf("get updated: %v", err)
	}
	if got.Conversation.Metadata["topic"] != "changed" {
		t.Fatalf("metadata = %v, want topic=changed", got.Conversation.Metadata)
	}
	if len(got.Items) != 1 || !strings.Contains(string(got.Items[0]), "replaced") {
		t.Fatalf("items = %v, want replaced item only", got.Items)
	}
	if !got.StoredAt.Equal(created.StoredAt) || !got.ExpiresAt.Equal(created.ExpiresAt) {
		t.Fatalf("retention changed: stored %v→%v expires %v→%v",
			created.StoredAt, got.StoredAt, created.ExpiresAt, got.ExpiresAt)
	}
}

func TestSQLiteConversationCreateRejectsDuplicates(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()

	if err := store.Create(ctx, testStoredConversation("conv-1")); err != nil {
		t.Fatalf("create: %v", err)
	}
	err := store.Create(ctx, testStoredConversation("conv-1"))
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("duplicate create err = %v, want already exists", err)
	}
}

func TestSQLiteConversationDeleteAndExpiry(t *testing.T) {
	store := newSQLiteTestStore(t)
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
	if _, err := store.db.Exec(
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
	if err := store.db.QueryRow("SELECT COUNT(*) FROM conversation_snapshots").Scan(&count); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if count != 0 {
		t.Fatalf("rows after sweep = %d, want 0", count)
	}
}
