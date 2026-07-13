package responsestore

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
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

func TestMemoryStoreCleanupExpiredRunsPeriodically(t *testing.T) {
	now := time.Now().UTC()
	store := NewMemoryStore(WithTTL(time.Second))
	store.items["resp_expired"] = &StoredResponse{
		Response:  &core.ResponsesResponse{ID: "resp_expired", Object: "response"},
		StoredAt:  now.Add(-2 * time.Second),
		ExpiresAt: now.Add(-time.Second),
	}
	store.lastCleanup = now

	store.cleanupExpiredLocked(now.Add(time.Second / 2))
	if _, ok := store.items["resp_expired"]; !ok {
		t.Fatal("expired response removed before cleanup interval elapsed")
	}

	store.cleanupExpiredLocked(now.Add(DefaultMemoryStoreCleanupInterval + time.Second))
	if _, ok := store.items["resp_expired"]; ok {
		t.Fatal("expired response retained after cleanup interval elapsed")
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

func TestMemoryStoreMaxBytesEvictsOldest(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	large := func(id string, storedAt time.Time) *StoredResponse {
		return &StoredResponse{
			Response: &core.ResponsesResponse{ID: id, Object: "response", Model: strings.Repeat("x", 600)},
			StoredAt: storedAt,
		}
	}

	// Size one entry via a probe store, then budget for exactly two.
	probe := NewMemoryStore(WithTTL(0))
	if err := probe.Create(ctx, large("probe", now)); err != nil {
		t.Fatalf("Create(probe) error = %v", err)
	}
	budget := 2*probe.totalBytes + 10

	store := NewMemoryStore(WithTTL(0), WithMaxEntries(0), WithMaxBytes(budget))
	for i, response := range []*StoredResponse{
		large("resp_1", now.Add(-3*time.Second)),
		large("resp_2", now.Add(-2*time.Second)),
		large("resp_3", now.Add(-1*time.Second)),
	} {
		if err := store.Create(ctx, response); err != nil {
			t.Fatalf("Create(%d) error = %v", i, err)
		}
	}

	if _, err := store.Get(ctx, "resp_1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(resp_1) error = %v, want ErrNotFound (oldest evicted)", err)
	}
	for _, id := range []string{"resp_2", "resp_3"} {
		if _, err := store.Get(ctx, id); err != nil {
			t.Fatalf("Get(%s) error = %v, want kept", id, err)
		}
	}
	if store.totalBytes > budget {
		t.Fatalf("totalBytes = %d, want <= %d", store.totalBytes, budget)
	}
}

func TestMemoryStoreRejectsSnapshotOverByteBudget(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore(WithMaxBytes(100))
	err := store.Create(ctx, &StoredResponse{
		Response: &core.ResponsesResponse{ID: "resp_big", Object: "response", Model: strings.Repeat("x", 200)},
	})
	if err == nil {
		t.Fatal("Create() error = nil, want byte budget rejection")
	}
	if _, getErr := store.Get(ctx, "resp_big"); !errors.Is(getErr, ErrNotFound) {
		t.Fatalf("Get() error = %v, want ErrNotFound", getErr)
	}
}

func TestMemoryStoreDeleteReleasesByteAccounting(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	if err := store.Create(ctx, &StoredResponse{
		Response: &core.ResponsesResponse{ID: "resp_1", Object: "response"},
	}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if store.totalBytes == 0 {
		t.Fatal("totalBytes = 0 after create, want > 0")
	}
	if err := store.Delete(ctx, "resp_1"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if store.totalBytes != 0 || len(store.sizes) != 0 {
		t.Fatalf("accounting after delete = %d bytes / %d sizes, want 0/0", store.totalBytes, len(store.sizes))
	}
}

func TestMemoryStoreUpdateNeverEvictsUpdatedEntry(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	sized := func(id string, storedAt time.Time, n int) *StoredResponse {
		return &StoredResponse{
			Response: &core.ResponsesResponse{ID: id, Object: "response", Model: strings.Repeat("x", n)},
			StoredAt: storedAt,
		}
	}

	probe := NewMemoryStore(WithTTL(0))
	if err := probe.Create(ctx, sized("probe", now, 600)); err != nil {
		t.Fatalf("Create(probe) error = %v", err)
	}
	budget := 2*probe.totalBytes + 10

	store := NewMemoryStore(WithTTL(0), WithMaxEntries(0), WithMaxBytes(budget))
	// resp_grow is the OLDEST entry — without protection, oldest-first
	// eviction would drop it right after its own successful update.
	if err := store.Create(ctx, sized("resp_grow", now.Add(-time.Minute), 10)); err != nil {
		t.Fatalf("Create(resp_grow) error = %v", err)
	}
	if err := store.Create(ctx, sized("resp_new", now, 600)); err != nil {
		t.Fatalf("Create(resp_new) error = %v", err)
	}

	if err := store.Update(ctx, sized("resp_grow", now.Add(-time.Minute), 1000)); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	got, err := store.Get(ctx, "resp_grow")
	if err != nil {
		t.Fatalf("Get(resp_grow) error = %v, want protected from self-eviction", err)
	}
	if len(got.Response.Model) != 1000 {
		t.Fatalf("model length = %d, want updated value", len(got.Response.Model))
	}
	if _, err := store.Get(ctx, "resp_new"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(resp_new) error = %v, want ErrNotFound (evicted instead)", err)
	}
}
