package authkeys

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Timestamps are whole seconds: the SQL stores keep Unix seconds, so a
// sub-second value would not round-trip on every backend.
var storeTestNow = time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)

func newTestKey(id string, createdAt time.Time) AuthKey {
	return AuthKey{
		ID:            id,
		Name:          id,
		RedactedValue: TokenPrefix + "..." + id,
		SecretHash:    "hash-" + id,
		Enabled:       true,
		CreatedAt:     createdAt,
		UpdatedAt:     createdAt,
	}
}

func listByID(t *testing.T, store Store) map[string]AuthKey {
	t.Helper()
	keys, err := store.List(context.Background())
	require.NoError(t, err)
	byID := make(map[string]AuthKey, len(keys))
	for _, key := range keys {
		require.Empty(t, byID[key.ID].ID, "List() returned duplicate key %s", key.ID)
		byID[key.ID] = key
	}
	return byID
}

func TestStore_RoundTrip(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()
		now := storeTestNow
		expires := now.Add(48 * time.Hour)
		deactivated := now.Add(-time.Minute)

		full := AuthKey{
			ID:              "key-full",
			Name:            "full",
			Description:     "every field set",
			UserPath:        "/team/alpha",
			Labels:          []string{"team-a", "batch"},
			AllowedModels:   []string{"anthropic/", "openai/gpt-4o"},
			DashboardAccess: true,
			RedactedValue:   TokenPrefix + "...full",
			SecretHash:      "hash-full",
			Enabled:         false,
			ExpiresAt:       &expires,
			DeactivatedAt:   &deactivated,
			CreatedAt:       now,
			UpdatedAt:       now.Add(time.Minute),
		}
		minimal := newTestKey("key-minimal", now.Add(-time.Hour))
		emptySlices := newTestKey("key-empty-slices", now.Add(-2*time.Hour))
		emptySlices.Labels = []string{}
		emptySlices.AllowedModels = []string{}

		for _, key := range []AuthKey{full, minimal, emptySlices} {
			require.NoError(t, store.Create(ctx, key))
		}

		keys, err := store.List(ctx)
		require.NoError(t, err)
		require.Len(t, keys, 3)
		// Newest first.
		require.Equal(t, "key-full", keys[0].ID)
		require.Equal(t, "key-minimal", keys[1].ID)
		require.Equal(t, "key-empty-slices", keys[2].ID)

		got := keys[0]
		require.Equal(t, full.Name, got.Name)
		require.Equal(t, full.Description, got.Description)
		require.Equal(t, full.UserPath, got.UserPath)
		require.Equal(t, full.Labels, got.Labels)
		require.Equal(t, full.AllowedModels, got.AllowedModels)
		require.True(t, got.DashboardAccess)
		require.Equal(t, full.RedactedValue, got.RedactedValue)
		require.Equal(t, full.SecretHash, got.SecretHash)
		require.False(t, got.Enabled)
		require.NotNil(t, got.ExpiresAt)
		require.True(t, got.ExpiresAt.Equal(expires), "ExpiresAt = %v, want %v", got.ExpiresAt, expires)
		require.NotNil(t, got.DeactivatedAt)
		require.True(t, got.DeactivatedAt.Equal(deactivated), "DeactivatedAt = %v, want %v", got.DeactivatedAt, deactivated)
		require.True(t, got.CreatedAt.Equal(full.CreatedAt), "CreatedAt = %v, want %v", got.CreatedAt, full.CreatedAt)
		require.True(t, got.UpdatedAt.Equal(full.UpdatedAt), "UpdatedAt = %v, want %v", got.UpdatedAt, full.UpdatedAt)

		for _, got := range keys[1:] {
			require.Equal(t, got.ID, got.Name)
			require.Empty(t, got.Description)
			require.Empty(t, got.UserPath)
			require.Nil(t, got.Labels, "%s: Labels = %#v, want nil", got.ID, got.Labels)
			require.Nil(t, got.AllowedModels, "%s: AllowedModels = %#v, want nil", got.ID, got.AllowedModels)
			require.False(t, got.DashboardAccess)
			require.True(t, got.Enabled)
			require.Nil(t, got.ExpiresAt)
			require.Nil(t, got.DeactivatedAt)
		}
		require.True(t, keys[1].CreatedAt.Equal(minimal.CreatedAt), "minimal CreatedAt = %v, want %v", keys[1].CreatedAt, minimal.CreatedAt)
	})
}

func TestStore_ListOrdersNewestFirstThenID(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()
		now := storeTestNow
		for _, key := range []AuthKey{
			newTestKey("key-b-old", now.Add(-time.Hour)),
			newTestKey("key-z-new", now),
			newTestKey("key-a-new", now),
			newTestKey("key-a-old", now.Add(-time.Hour)),
		} {
			require.NoError(t, store.Create(ctx, key))
		}

		keys, err := store.List(ctx)
		require.NoError(t, err)
		ids := make([]string, 0, len(keys))
		for _, key := range keys {
			ids = append(ids, key.ID)
		}
		require.Equal(t, []string{"key-a-new", "key-z-new", "key-a-old", "key-b-old"}, ids)
	})
}

func TestStore_ListEmpty(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		keys, err := store.List(context.Background())
		require.NoError(t, err)
		require.NotNil(t, keys)
		require.Empty(t, keys)
	})
}

func TestStore_CreateRejectsDuplicates(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()
		key := newTestKey("key-one", storeTestNow)
		require.NoError(t, store.Create(ctx, key))

		require.Error(t, store.Create(ctx, key), "duplicate id")

		sameSecret := newTestKey("key-two", storeTestNow)
		sameSecret.SecretHash = key.SecretHash
		require.Error(t, store.Create(ctx, sameSecret), "duplicate secret hash")

		keys, err := store.List(ctx)
		require.NoError(t, err)
		require.Len(t, keys, 1)
		require.Equal(t, "key-one", keys[0].ID)
	})
}

func TestStore_UpdateLabels(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()
		now := storeTestNow
		labelled := newTestKey("key-labelled", now)
		labelled.Labels = []string{"team-a", "batch"}
		unlabelled := newTestKey("key-unlabelled", now.Add(-time.Hour))
		for _, key := range []AuthKey{labelled, unlabelled} {
			require.NoError(t, store.Create(ctx, key))
		}

		byID := listByID(t, store)
		require.Equal(t, []string{"team-a", "batch"}, byID["key-labelled"].Labels)
		require.Nil(t, byID["key-unlabelled"].Labels)

		later := now.Add(time.Hour)
		require.NoError(t, store.UpdateLabels(ctx, "key-unlabelled", []string{"added"}, later))
		require.NoError(t, store.UpdateLabels(ctx, "key-labelled", nil, later))
		require.ErrorIs(t, store.UpdateLabels(ctx, "missing", []string{"x"}, later), ErrNotFound)

		byID = listByID(t, store)
		require.Equal(t, []string{"added"}, byID["key-unlabelled"].Labels)
		require.True(t, byID["key-unlabelled"].UpdatedAt.Equal(later), "updated key UpdatedAt = %v, want %v", byID["key-unlabelled"].UpdatedAt, later)
		require.Nil(t, byID["key-labelled"].Labels)
		require.True(t, byID["key-labelled"].UpdatedAt.Equal(later), "cleared key UpdatedAt = %v, want %v", byID["key-labelled"].UpdatedAt, later)

		// An empty slice clears like nil does.
		require.NoError(t, store.UpdateLabels(ctx, "key-unlabelled", []string{}, later))
		byID = listByID(t, store)
		require.Nil(t, byID["key-unlabelled"].Labels)
	})
}

func TestStore_UpdateAllowedModels(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()
		now := storeTestNow
		key := newTestKey("key-restricted", now)
		key.AllowedModels = []string{"anthropic/", "openai/gpt-4o"}
		require.NoError(t, store.Create(ctx, key))
		require.NoError(t, store.Create(ctx, newTestKey("key-other", now)))

		byID := listByID(t, store)
		require.Equal(t, key.AllowedModels, byID["key-restricted"].AllowedModels)
		require.Nil(t, byID["key-other"].AllowedModels)

		later := now.Add(time.Hour)
		require.NoError(t, store.UpdateAllowedModels(ctx, "key-restricted", nil, later))
		require.NoError(t, store.UpdateAllowedModels(ctx, "key-other", []string{"openai/"}, later))
		require.ErrorIs(t, store.UpdateAllowedModels(ctx, "missing", []string{"openai/"}, later), ErrNotFound)

		byID = listByID(t, store)
		require.Nil(t, byID["key-restricted"].AllowedModels)
		require.True(t, byID["key-restricted"].UpdatedAt.Equal(later), "cleared key = %#v, want nil allowed models and bumped updated_at", byID["key-restricted"])
		require.Equal(t, []string{"openai/"}, byID["key-other"].AllowedModels)
		require.True(t, byID["key-other"].UpdatedAt.Equal(later), "restricted key = %#v, want bumped updated_at", byID["key-other"])
	})
}

func TestStore_UpdateDashboardAccess(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()
		now := storeTestNow
		admin := newTestKey("key-admin", now)
		admin.DashboardAccess = true
		require.NoError(t, store.Create(ctx, admin))
		require.NoError(t, store.Create(ctx, newTestKey("key-plain", now)))

		assertAccess := func(want map[string]bool) {
			t.Helper()
			byID := listByID(t, store)
			require.Len(t, byID, len(want))
			for id, wantAccess := range want {
				require.Contains(t, byID, id)
				require.Equal(t, wantAccess, byID[id].DashboardAccess, "%s dashboard access", id)
			}
		}
		assertAccess(map[string]bool{"key-admin": true, "key-plain": false})

		later := now.Add(time.Hour)
		require.NoError(t, store.UpdateDashboardAccess(ctx, "key-plain", true, later))
		require.NoError(t, store.UpdateDashboardAccess(ctx, "key-admin", false, later))
		require.ErrorIs(t, store.UpdateDashboardAccess(ctx, "missing", true, later), ErrNotFound)

		assertAccess(map[string]bool{"key-admin": false, "key-plain": true})
		byID := listByID(t, store)
		require.True(t, byID["key-plain"].UpdatedAt.Equal(later), "UpdatedAt = %v, want %v", byID["key-plain"].UpdatedAt, later)
	})
}

func TestStore_Deactivate(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()
		now := storeTestNow
		require.NoError(t, store.Create(ctx, newTestKey("key-active", now)))
		require.NoError(t, store.Create(ctx, newTestKey("key-untouched", now)))

		first := now.Add(time.Hour)
		require.NoError(t, store.Deactivate(ctx, "key-active", first))
		require.ErrorIs(t, store.Deactivate(ctx, "missing", first), ErrNotFound)

		byID := listByID(t, store)
		got := byID["key-active"]
		require.False(t, got.Enabled)
		require.NotNil(t, got.DeactivatedAt)
		require.True(t, got.DeactivatedAt.Equal(first), "DeactivatedAt = %v, want %v", got.DeactivatedAt, first)
		require.True(t, got.UpdatedAt.Equal(first), "UpdatedAt = %v, want %v", got.UpdatedAt, first)
		require.False(t, got.Active(first))
		require.True(t, byID["key-untouched"].Enabled)
		require.Nil(t, byID["key-untouched"].DeactivatedAt)

		// Deactivating again keeps the original deactivation time but still
		// bumps updated_at.
		second := now.Add(2 * time.Hour)
		require.NoError(t, store.Deactivate(ctx, "key-active", second))
		got = listByID(t, store)["key-active"]
		require.False(t, got.Enabled)
		require.True(t, got.DeactivatedAt.Equal(first), "DeactivatedAt = %v, want first deactivation %v", got.DeactivatedAt, first)
		require.True(t, got.UpdatedAt.Equal(second), "UpdatedAt = %v, want %v", got.UpdatedAt, second)
	})
}
