package responsestore

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/require"
)

func TestMemoryStoreExpiresResponses(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore(WithTTL(time.Second))

	err := store.Create(ctx, &StoredResponse{
		Response: &core.ResponsesResponse{ID: "resp_old", Object: "response"},
		StoredAt: time.Now().UTC().Add(-2 * time.Second),
	})
	require.NoError(t, err)
	_, err = store.Get(ctx, "resp_old")
	require.ErrorIs(t, err, ErrNotFound)
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
		err := store.Create(ctx, response)
		require.NoError(t, err)
	}
	_, err := store.Get(ctx, "resp_1")
	require.ErrorIs(t, err, ErrNotFound)

	for _, id := range []string{"resp_2", "resp_3"} {
		_, err := store.Get(ctx, id)
		require.NoError(t, err)
	}
}

func TestMemoryStoreDefaultRetentionIsBounded(t *testing.T) {
	store := NewMemoryStore()

	require.Equal(t, DefaultMemoryStoreTTL, store.ttl)
	require.Equal(t, DefaultMemoryStoreMaxEntries, store.maxEntries)
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
	_, ok := store.items["resp_expired"]
	require.True(t, ok)

	store.cleanupExpiredLocked(now.Add(DefaultMemoryStoreCleanupInterval + time.Second))
	_, ok = store.items["resp_expired"]
	require.False(t, ok)
}

func TestMemoryStoreAllowsExplicitUnboundedRetention(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore(WithUnboundedRetention())

	err := store.Create(ctx, &StoredResponse{
		Response: &core.ResponsesResponse{ID: "resp_old", Object: "response"},
		StoredAt: time.Now().UTC().Add(-24 * time.Hour),
	})
	require.NoError(t, err)
	_, err = store.Get(ctx, "resp_old")
	require.NoError(t, err)
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
	err := probe.Create(ctx, large("probe", now))
	require.NoError(t, err)

	budget := 2*probe.totalBytes + 10

	store := NewMemoryStore(WithTTL(0), WithMaxEntries(0), WithMaxBytes(budget))
	for i, response := range []*StoredResponse{
		large("resp_1", now.Add(-3*time.Second)),
		large("resp_2", now.Add(-2*time.Second)),
		large("resp_3", now.Add(-1*time.Second)),
	} {
		err := store.Create(ctx, response)
		require.NoError(t, err, "create response %d", i)
	}
	_, err = store.Get(ctx, "resp_1")
	require.ErrorIs(t, err, ErrNotFound)

	for _, id := range []string{"resp_2", "resp_3"} {
		_, err := store.Get(ctx, id)
		require.NoError(t, err)
	}
	require.LessOrEqual(t, store.totalBytes, budget)
}

func TestMemoryStoreRejectsSnapshotOverByteBudget(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore(WithMaxBytes(100))
	err := store.Create(ctx, &StoredResponse{
		Response: &core.ResponsesResponse{ID: "resp_big", Object: "response", Model: strings.Repeat("x", 200)},
	})
	require.Error(t, err)
	_, getErr := store.Get(ctx, "resp_big")
	require.ErrorIs(t, getErr, ErrNotFound)
}

func TestMemoryStoreDeleteReleasesByteAccounting(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	err := store.Create(ctx, &StoredResponse{
		Response: &core.ResponsesResponse{ID: "resp_1", Object: "response"},
	})
	require.NoError(t, err)
	require.NotEqual(t, int64(0), store.totalBytes)
	err = store.Delete(ctx, "resp_1")
	require.NoError(t, err)
	require.Equal(t, int64(0), store.totalBytes)
	require.Empty(t, store.sizes)
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
	err := probe.Create(ctx, sized("probe", now, 600))
	require.NoError(t, err)

	budget := 2*probe.totalBytes + 10

	store := NewMemoryStore(WithTTL(0), WithMaxEntries(0), WithMaxBytes(budget))
	err = // resp_grow is the OLDEST entry — without protection, oldest-first
		// eviction would drop it right after its own successful update.
		store.Create(ctx, sized("resp_grow", now.Add(-time.Minute), 10))
	require.NoError(t, err)
	err = store.Create(ctx, sized("resp_new", now, 600))
	require.NoError(t, err)
	err = store.Update(ctx, sized("resp_grow", now.Add(-time.Minute), 1000))
	require.NoError(t, err)

	got, err := store.Get(ctx, "resp_grow")
	require.NoError(t, err)
	require.Len(t, got.Response.Model, 1000)
	_, err = store.Get(ctx, "resp_new")
	require.ErrorIs(t, err, ErrNotFound)
}
