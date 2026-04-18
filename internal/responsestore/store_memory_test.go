package responsestore

import (
	"context"
	"errors"
	"testing"
	"time"

	"gomodel/internal/core"
)

func TestMemoryStoreExpiresResponses(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore(WithTTL(time.Second))

	err := store.Create(ctx, &StoredResponse{
		Response: &core.ResponsesResponse{ID: "resp_old", Object: "response"},
		StoredAt: time.Now().UTC().Add(-2 * time.Second),
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if _, err := store.Get(ctx, "resp_old"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get() error = %v, want ErrNotFound", err)
	}
}

func TestMemoryStoreMaxEntriesEvictsOldest(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore(WithTTL(0), WithMaxEntries(2))
	now := time.Now().UTC()

	for _, response := range []*StoredResponse{
		{Response: &core.ResponsesResponse{ID: "resp_1", Object: "response"}, StoredAt: now.Add(-3 * time.Second)},
		{Response: &core.ResponsesResponse{ID: "resp_2", Object: "response"}, StoredAt: now.Add(-2 * time.Second)},
		{Response: &core.ResponsesResponse{ID: "resp_3", Object: "response"}, StoredAt: now.Add(-1 * time.Second)},
	} {
		if err := store.Create(ctx, response); err != nil {
			t.Fatalf("Create(%s) error = %v", response.Response.ID, err)
		}
	}

	if _, err := store.Get(ctx, "resp_1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(resp_1) error = %v, want ErrNotFound", err)
	}
	for _, id := range []string{"resp_2", "resp_3"} {
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

func TestMemoryStoreAllowsExplicitUnboundedRetention(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore(WithUnboundedRetention())

	err := store.Create(ctx, &StoredResponse{
		Response: &core.ResponsesResponse{ID: "resp_old", Object: "response"},
		StoredAt: time.Now().UTC().Add(-24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if _, err := store.Get(ctx, "resp_old"); err != nil {
		t.Fatalf("Get() error = %v", err)
	}
}
