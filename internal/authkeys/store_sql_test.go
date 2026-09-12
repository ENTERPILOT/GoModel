package authkeys

import (
	"context"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
	"github.com/stretchr/testify/require"
)

func runSQLStoreTest(t *testing.T, body func(t *testing.T, store *SQLStore, db sqlx.DB)) {
	t.Helper()
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, err := NewSQLStore(context.Background(), db)
		require.NoError(t, err)

		body(t, store, db)
	})
}

func TestSQLStoreAuthKeyLabelsRoundTrip(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore, db sqlx.DB) {
		now := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
		ctx := context.Background()
		labelled := AuthKey{
			ID:            "key-labelled",
			Name:          "labelled",
			UserPath:      "/team/alpha",
			Labels:        []string{"team-a", "batch"},
			RedactedValue: TokenPrefix + "...abcd",
			SecretHash:    "hash-labelled",
			Enabled:       true,
			CreatedAt:     now,
			UpdatedAt:     now,
		}
		unlabelled := AuthKey{
			ID:            "key-unlabelled",
			Name:          "unlabelled",
			RedactedValue: TokenPrefix + "...efgh",
			SecretHash:    "hash-unlabelled",
			Enabled:       true,
			CreatedAt:     now.Add(-time.Hour),
			UpdatedAt:     now.Add(-time.Hour),
		}
		for _, key := range []AuthKey{labelled, unlabelled} {
			err := store.Create(ctx, key)
			require.NoError(t, err)
		}
		// Reopening against the same database must tolerate the already-applied
		// labels migration.
		_, err := NewSQLStore(ctx, db)
		require.NoError(t, err)

		keys, err := store.List(ctx)
		require.NoError(t, err)
		require.Len(t, keys, 2)

		byID := map[string]AuthKey{}
		for _, key := range keys {
			byID[key.ID] = key
		}
		got := byID["key-labelled"].Labels
		require.Equal(t, []string{"team-a", "batch"}, got)
		got = byID["key-unlabelled"].Labels
		require.Nil(t, got)

		later := now.Add(time.Hour)
		err = store.UpdateLabels(ctx, "key-unlabelled", []string{"added"}, later)
		require.NoError(t, err)
		err = store.UpdateLabels(ctx, "key-labelled", nil, later)
		require.NoError(t, err)
		err = store.UpdateLabels(ctx, "missing", []string{"x"}, later)
		require.ErrorIs(t, err, ErrNotFound)

		keys, err = store.List(ctx)
		require.NoError(t, err)

		byID = map[string]AuthKey{}
		for _, key := range keys {
			byID[key.ID] = key
		}
		got = byID["key-unlabelled"].Labels
		require.Equal(t, []string{"added"}, got)

		require.True(t, byID["key-unlabelled"].UpdatedAt.Equal(later), "updated key UpdatedAt = %v, want %v", byID["key-unlabelled"].UpdatedAt, later)
		got = byID["key-labelled"].Labels
		require.Nil(t, got)
	})
}

func TestSQLStoreAuthKeyDashboardAccessRoundTrip(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore, _ sqlx.DB) {
		now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
		ctx := context.Background()
		err := store.Create(ctx, AuthKey{
			ID:              "key-admin",
			Name:            "admin",
			DashboardAccess: true,
			RedactedValue:   TokenPrefix + "...abcd",
			SecretHash:      "hash-admin",
			Enabled:         true,
			CreatedAt:       now,
			UpdatedAt:       now,
		})
		require.NoError(t, err)
		err = store.Create(ctx, AuthKey{
			ID:            "key-plain",
			Name:          "plain",
			RedactedValue: TokenPrefix + "...efgh",
			SecretHash:    "hash-plain",
			Enabled:       true,
			CreatedAt:     now,
			UpdatedAt:     now,
		})
		require.NoError(t, err)

		assertAccess := func(want map[string]bool) {
			t.Helper()
			keys, err := store.List(ctx)
			require.NoError(t, err)
			require.Len(t, keys, len(want))

			seen := make(map[string]bool, len(keys))
			for _, key := range keys {
				wantAccess, expected := want[key.ID]
				require.True(t, expected)

				require.False(t, seen[key.ID], "List() returned duplicate key %s", key.ID)
				seen[key.ID] = true
				require.Equal(t, wantAccess, key.DashboardAccess)
			}
		}
		assertAccess(map[string]bool{"key-admin": true, "key-plain": false})

		later := now.Add(time.Hour)
		err = store.UpdateDashboardAccess(ctx, "key-plain", true, later)
		require.NoError(t, err)
		err = store.UpdateDashboardAccess(ctx, "key-admin", false, later)
		require.NoError(t, err)
		err = store.UpdateDashboardAccess(ctx, "missing", true, later)
		require.ErrorIs(t, err, ErrNotFound)

		assertAccess(map[string]bool{"key-admin": false, "key-plain": true})
	})
}

func TestSQLStoreAuthKeyAllowedModelsRoundTrip(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore, db sqlx.DB) {
		now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
		ctx := context.Background()
		key := AuthKey{
			ID:            "key-restricted",
			Name:          "restricted",
			AllowedModels: []string{"anthropic/", "openai/gpt-4o"},
			RedactedValue: TokenPrefix + "...abcd",
			SecretHash:    "hash-restricted",
			Enabled:       true,
			CreatedAt:     now,
			UpdatedAt:     now,
		}
		err := store.Create(ctx, key)
		require.NoError(t, err)

		keys, err := store.List(ctx)
		require.NoError(t, err)
		require.Len(t, keys, 1)
		require.Equal(t, key.AllowedModels, keys[0].AllowedModels)
		err = store.UpdateAllowedModels(ctx, key.ID, nil, now.Add(time.Hour))
		require.NoError(t, err)

		keys, err = store.List(ctx)
		require.NoError(t, err)
		require.Nil(t, keys[0].AllowedModels)
		require.True(t, keys[0].UpdatedAt.Equal(now.Add(time.Hour)), "cleared key = %#v, want nil allowed models and bumped updated_at", keys[0])
		err = store.UpdateAllowedModels(ctx, "missing", []string{"openai/"}, now)
		require.ErrorIs(t, err, ErrNotFound)
	})
}
