package responsestore

import (
	"context"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
)

func TestDetachRequiresResponseID(t *testing.T) {
	for _, src := range []*StoredResponse{
		nil,
		{},
		{Response: &core.ResponsesResponse{}},
	} {
		if _, err := Detach(src); err == nil {
			t.Fatalf("Detach(%+v) error = nil, want response id required", src)
		}
	}
}

func TestDetachSharesNoMemoryWithSource(t *testing.T) {
	src := testStoredResponse("resp-detached")
	snapshot, err := Detach(src)
	if err != nil {
		t.Fatalf("Detach: %v", err)
	}
	if snapshot.ID() != "resp-detached" {
		t.Fatalf("ID() = %q, want resp-detached", snapshot.ID())
	}

	// Mutate the source after detaching; the persisted snapshot must keep the
	// pre-mutation state.
	src.Response.Model = "gpt-mutated"
	src.InputItems[0] = []byte(`{"mutated":true}`)

	store := NewMemoryStore(WithUnboundedRetention())
	t.Cleanup(func() { _ = store.Close() })
	if err := snapshot.Persist(context.Background(), store); err != nil {
		t.Fatalf("Persist: %v", err)
	}
	got, err := store.Get(context.Background(), "resp-detached")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Response.Model != "gpt-test" {
		t.Fatalf("model = %q, want pre-mutation gpt-test", got.Response.Model)
	}
	if string(got.InputItems[0]) == `{"mutated":true}` {
		t.Fatal("input items reflect post-detach mutation")
	}
}

// TestDetachedPersistSuite exercises Persist against every Store backend:
// first write creates, second write upserts, retention is stamped.
func TestDetachedPersistSuite(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()

		first, err := Detach(testStoredResponse("resp-persist"))
		if err != nil {
			t.Fatalf("Detach: %v", err)
		}
		if err := first.Persist(ctx, store); err != nil {
			t.Fatalf("first Persist: %v", err)
		}
		got, err := store.Get(ctx, "resp-persist")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Response == nil || got.Response.Model != "gpt-test" {
			t.Fatalf("response = %+v, want model gpt-test", got.Response)
		}
		if got.Provider != "openai" || got.RequestID != "req-1" {
			t.Fatalf("metadata = %+v, want provider and request id preserved", got)
		}
		if got.StoredAt.IsZero() {
			t.Fatal("StoredAt not stamped")
		}

		// A second Persist for the same id must overwrite the live row via the
		// update fallback, not fail as a duplicate.
		updatedSrc := testStoredResponse("resp-persist")
		updatedSrc.Response.Model = "gpt-updated"
		second, err := Detach(updatedSrc)
		if err != nil {
			t.Fatalf("Detach updated: %v", err)
		}
		if err := second.Persist(ctx, store); err != nil {
			t.Fatalf("second Persist: %v", err)
		}
		got, err = store.Get(ctx, "resp-persist")
		if err != nil {
			t.Fatalf("Get after update: %v", err)
		}
		if got.Response.Model != "gpt-updated" {
			t.Fatalf("model = %q, want gpt-updated", got.Response.Model)
		}
	})
}

func TestSQLStorePersistStampsRetentionColumns(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
		ctx := context.Background()

		snapshot, err := Detach(testStoredResponse("resp-retention"))
		if err != nil {
			t.Fatalf("Detach: %v", err)
		}
		before := time.Now().UTC().Add(-time.Second)
		if err := snapshot.Persist(ctx, store); err != nil {
			t.Fatalf("Persist: %v", err)
		}
		got, err := store.Get(ctx, "resp-retention")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.StoredAt.Before(before) {
			t.Fatalf("StoredAt = %v, want stamped at write time", got.StoredAt)
		}
		if !got.ExpiresAt.After(got.StoredAt) {
			t.Fatalf("ExpiresAt = %v, want after StoredAt %v", got.ExpiresAt, got.StoredAt)
		}

		// The update fallback must preserve the original retention columns.
		storedAt, expiresAt := got.StoredAt, got.ExpiresAt
		updated, err := Detach(testStoredResponse("resp-retention"))
		if err != nil {
			t.Fatalf("Detach updated: %v", err)
		}
		if err := updated.Persist(ctx, store); err != nil {
			t.Fatalf("second Persist: %v", err)
		}
		got, err = store.Get(ctx, "resp-retention")
		if err != nil {
			t.Fatalf("Get after update: %v", err)
		}
		if !got.StoredAt.Equal(storedAt) || !got.ExpiresAt.Equal(expiresAt) {
			t.Fatalf("retention = (%v, %v), want preserved (%v, %v)",
				got.StoredAt, got.ExpiresAt, storedAt, expiresAt)
		}
	})
}
