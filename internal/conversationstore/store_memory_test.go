package conversationstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"gomodel/internal/core"
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

func TestMemoryStoreCreateGetUpdateDelete(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()

	if err := store.Create(ctx, storedConversation("conv_1", time.Time{})); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	got, err := store.Get(ctx, "conv_1")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Conversation.ID != "conv_1" {
		t.Fatalf("id = %q, want conv_1", got.Conversation.ID)
	}

	got.Conversation.Metadata = map[string]string{"k": "v"}
	if err := store.Update(ctx, got); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	updated, err := store.Get(ctx, "conv_1")
	if err != nil {
		t.Fatalf("Get() after update error = %v", err)
	}
	if updated.Conversation.Metadata["k"] != "v" {
		t.Fatalf("metadata[k] = %q, want v", updated.Conversation.Metadata["k"])
	}

	if err := store.Delete(ctx, "conv_1"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := store.Get(ctx, "conv_1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get() after delete error = %v, want ErrNotFound", err)
	}
}

func TestMemoryStoreCreateRejectsDuplicate(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()

	if err := store.Create(ctx, storedConversation("conv_dup", time.Time{})); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := store.Create(ctx, storedConversation("conv_dup", time.Time{})); err == nil {
		t.Fatal("Create() duplicate error = nil, want error")
	}
}

func TestMemoryStoreUpdateMissingReturnsNotFound(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()

	if err := store.Update(ctx, storedConversation("conv_missing", time.Time{})); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Update() error = %v, want ErrNotFound", err)
	}
}

func TestMemoryStoreDeleteMissingReturnsNotFound(t *testing.T) {
	if err := NewMemoryStore().Delete(context.Background(), "conv_missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete() error = %v, want ErrNotFound", err)
	}
}

func TestMemoryStoreDeleteExpiredReturnsNotFound(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore(WithTTL(time.Second))

	if err := store.Create(ctx, storedConversation("conv_expired", time.Now().UTC().Add(-2*time.Second))); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := store.Delete(ctx, "conv_expired"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete() error = %v, want ErrNotFound", err)
	}
}

func TestMemoryStoreExpiresConversations(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore(WithTTL(time.Second))

	if err := store.Create(ctx, storedConversation("conv_old", time.Now().UTC().Add(-2*time.Second))); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if _, err := store.Get(ctx, "conv_old"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get() error = %v, want ErrNotFound", err)
	}
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
		if err := store.Create(ctx, conversation); err != nil {
			t.Fatalf("Create(%s) error = %v", conversation.Conversation.ID, err)
		}
	}

	if _, err := store.Get(ctx, "conv_1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(conv_1) error = %v, want ErrNotFound", err)
	}
	for _, id := range []string{"conv_2", "conv_3"} {
		if _, err := store.Get(ctx, id); err != nil {
			t.Fatalf("Get(%s) error = %v", id, err)
		}
	}
}

func TestMemoryStoreDefaultRetentionIsBounded(t *testing.T) {
	store := NewMemoryStore()

	if store.ttl != DefaultMemoryStoreTTL {
		t.Fatalf("ttl = %s, want %s", store.ttl, DefaultMemoryStoreTTL)
	}
	if store.maxEntries != DefaultMemoryStoreMaxEntries {
		t.Fatalf("maxEntries = %d, want %d", store.maxEntries, DefaultMemoryStoreMaxEntries)
	}
}

func TestMemoryStoreGetReturnsIsolatedCopy(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	if err := store.Create(ctx, storedConversation("conv_iso", time.Time{})); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	first, err := store.Get(ctx, "conv_iso")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	first.Conversation.Metadata["mutated"] = "true"

	second, err := store.Get(ctx, "conv_iso")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if _, mutated := second.Conversation.Metadata["mutated"]; mutated {
		t.Fatal("stored conversation mutated through returned copy")
	}
}
