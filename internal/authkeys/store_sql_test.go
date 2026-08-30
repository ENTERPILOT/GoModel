package authkeys

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
)

func runSQLStoreTest(t *testing.T, body func(t *testing.T, store *SQLStore, db sqlx.DB)) {
	t.Helper()
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, err := NewSQLStore(context.Background(), db)
		if err != nil {
			t.Fatalf("NewSQLStore: %v", err)
		}
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
			if err := store.Create(ctx, key); err != nil {
				t.Fatalf("Create(%s) error = %v", key.ID, err)
			}
		}

		// Reopening against the same database must tolerate the already-applied
		// labels migration.
		if _, err := NewSQLStore(ctx, db); err != nil {
			t.Fatalf("NewSQLStore() reopen error = %v", err)
		}

		keys, err := store.List(ctx)
		if err != nil {
			t.Fatalf("List() error = %v", err)
		}
		if len(keys) != 2 {
			t.Fatalf("List() len = %d, want 2", len(keys))
		}
		byID := map[string]AuthKey{}
		for _, key := range keys {
			byID[key.ID] = key
		}
		if got := byID["key-labelled"].Labels; !reflect.DeepEqual(got, []string{"team-a", "batch"}) {
			t.Fatalf("labelled key labels = %v, want [team-a batch]", got)
		}
		if got := byID["key-unlabelled"].Labels; got != nil {
			t.Fatalf("unlabelled key labels = %v, want nil", got)
		}

		later := now.Add(time.Hour)
		if err := store.UpdateLabels(ctx, "key-unlabelled", []string{"added"}, later); err != nil {
			t.Fatalf("UpdateLabels() error = %v", err)
		}
		if err := store.UpdateLabels(ctx, "key-labelled", nil, later); err != nil {
			t.Fatalf("UpdateLabels(clear) error = %v", err)
		}
		if err := store.UpdateLabels(ctx, "missing", []string{"x"}, later); !errors.Is(err, ErrNotFound) {
			t.Fatalf("UpdateLabels(missing) error = %v, want %v", err, ErrNotFound)
		}

		keys, err = store.List(ctx)
		if err != nil {
			t.Fatalf("List() after update error = %v", err)
		}
		byID = map[string]AuthKey{}
		for _, key := range keys {
			byID[key.ID] = key
		}
		if got := byID["key-unlabelled"].Labels; !reflect.DeepEqual(got, []string{"added"}) {
			t.Fatalf("updated key labels = %v, want [added]", got)
		}
		if got := byID["key-unlabelled"].UpdatedAt; !got.Equal(later) {
			t.Fatalf("updated key UpdatedAt = %v, want %v", got, later)
		}
		if got := byID["key-labelled"].Labels; got != nil {
			t.Fatalf("cleared key labels = %v, want nil", got)
		}
	})
}

func TestSQLStoreAuthKeyDashboardAccessRoundTrip(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore, _ sqlx.DB) {
		now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
		ctx := context.Background()
		if err := store.Create(ctx, AuthKey{
			ID:              "key-admin",
			Name:            "admin",
			DashboardAccess: true,
			RedactedValue:   TokenPrefix + "...abcd",
			SecretHash:      "hash-admin",
			Enabled:         true,
			CreatedAt:       now,
			UpdatedAt:       now,
		}); err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		if err := store.Create(ctx, AuthKey{
			ID:            "key-plain",
			Name:          "plain",
			RedactedValue: TokenPrefix + "...efgh",
			SecretHash:    "hash-plain",
			Enabled:       true,
			CreatedAt:     now,
			UpdatedAt:     now,
		}); err != nil {
			t.Fatalf("Create() error = %v", err)
		}

		assertAccess := func(want map[string]bool) {
			t.Helper()
			keys, err := store.List(ctx)
			if err != nil {
				t.Fatalf("List() error = %v", err)
			}
			if len(keys) != len(want) {
				t.Fatalf("List() len = %d, want %d", len(keys), len(want))
			}
			seen := make(map[string]bool, len(keys))
			for _, key := range keys {
				wantAccess, expected := want[key.ID]
				if !expected {
					t.Fatalf("List() returned unexpected key %s", key.ID)
				}
				if seen[key.ID] {
					t.Fatalf("List() returned duplicate key %s", key.ID)
				}
				seen[key.ID] = true
				if key.DashboardAccess != wantAccess {
					t.Fatalf("key %s dashboard access = %v, want %v", key.ID, key.DashboardAccess, wantAccess)
				}
			}
		}
		assertAccess(map[string]bool{"key-admin": true, "key-plain": false})

		later := now.Add(time.Hour)
		if err := store.UpdateDashboardAccess(ctx, "key-plain", true, later); err != nil {
			t.Fatalf("UpdateDashboardAccess(grant) error = %v", err)
		}
		if err := store.UpdateDashboardAccess(ctx, "key-admin", false, later); err != nil {
			t.Fatalf("UpdateDashboardAccess(revoke) error = %v", err)
		}
		if err := store.UpdateDashboardAccess(ctx, "missing", true, later); !errors.Is(err, ErrNotFound) {
			t.Fatalf("UpdateDashboardAccess(missing) error = %v, want %v", err, ErrNotFound)
		}
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
		if err := store.Create(ctx, key); err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		keys, err := store.List(ctx)
		if err != nil {
			t.Fatalf("List() error = %v", err)
		}
		if len(keys) != 1 || !reflect.DeepEqual(keys[0].AllowedModels, key.AllowedModels) {
			t.Fatalf("List() = %#v, want allowed models %v", keys, key.AllowedModels)
		}

		if err := store.UpdateAllowedModels(ctx, key.ID, nil, now.Add(time.Hour)); err != nil {
			t.Fatalf("UpdateAllowedModels(clear) error = %v", err)
		}
		keys, err = store.List(ctx)
		if err != nil {
			t.Fatalf("List() error = %v", err)
		}
		if keys[0].AllowedModels != nil || !keys[0].UpdatedAt.Equal(now.Add(time.Hour)) {
			t.Fatalf("cleared key = %#v, want nil allowed models and bumped updated_at", keys[0])
		}
		if err := store.UpdateAllowedModels(ctx, "missing", []string{"openai/"}, now); !errors.Is(err, ErrNotFound) {
			t.Fatalf("UpdateAllowedModels(missing) error = %v, want ErrNotFound", err)
		}
	})
}
