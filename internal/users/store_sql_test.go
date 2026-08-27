package users

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
)

func runSQLStoreTest(t *testing.T, body func(t *testing.T, store *SQLStore)) {
	t.Helper()
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, err := NewSQLStore(context.Background(), db)
		if err != nil {
			t.Fatalf("NewSQLStore: %v", err)
		}
		body(t, store)
	})
}

func TestSQLStoreUserRoundTrip(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
		ctx := context.Background()
		now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
		user := User{
			ID:          "user-1",
			UserPath:    "/team/alpha",
			Name:        "Team Alpha",
			Description: "alpha service account",
			Groups:      []string{"beta-testers", "premium"},
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		if err := store.UpsertUser(ctx, user); err != nil {
			t.Fatalf("UpsertUser() error = %v", err)
		}
		plain := User{ID: "user-2", UserPath: "/team/beta", CreatedAt: now, UpdatedAt: now}
		if err := store.UpsertUser(ctx, plain); err != nil {
			t.Fatalf("UpsertUser(plain) error = %v", err)
		}

		listed, err := store.ListUsers(ctx)
		if err != nil {
			t.Fatalf("ListUsers() error = %v", err)
		}
		if len(listed) != 2 {
			t.Fatalf("ListUsers() len = %d, want 2", len(listed))
		}
		if !reflect.DeepEqual(listed[0], user) {
			t.Fatalf("ListUsers()[0] = %+v, want %+v", listed[0], user)
		}
		if listed[1].Groups != nil {
			t.Fatalf("ListUsers()[1].Groups = %v, want nil", listed[1].Groups)
		}

		// Update in place keeps the row count and changes fields.
		user.Name = "Alpha"
		user.Groups = nil
		if err := store.UpsertUser(ctx, user); err != nil {
			t.Fatalf("UpsertUser(update) error = %v", err)
		}
		listed, err = store.ListUsers(ctx)
		if err != nil {
			t.Fatalf("ListUsers() error = %v", err)
		}
		if len(listed) != 2 || listed[0].Name != "Alpha" || listed[0].Groups != nil {
			t.Fatalf("ListUsers() after update = %+v", listed)
		}

		if err := store.DeleteUser(ctx, "user-1"); err != nil {
			t.Fatalf("DeleteUser() error = %v", err)
		}
		if err := store.DeleteUser(ctx, "user-1"); !errors.Is(err, ErrUserNotFound) {
			t.Fatalf("DeleteUser(gone) error = %v, want ErrUserNotFound", err)
		}
	})
}

func TestSQLStoreUserPathUnique(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
		ctx := context.Background()
		now := time.Now().UTC().Truncate(time.Second)
		if err := store.UpsertUser(ctx, User{ID: "a", UserPath: "/x", CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatalf("UpsertUser(a) error = %v", err)
		}
		if err := store.UpsertUser(ctx, User{ID: "b", UserPath: "/x", CreatedAt: now, UpdatedAt: now}); err == nil {
			t.Fatalf("UpsertUser(duplicate path) error = nil, want unique violation")
		}
	})
}

func TestSQLStoreGroupRoundTrip(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
		ctx := context.Background()
		now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
		group := Group{Name: "beta-testers", Description: "early access", CreatedAt: now, UpdatedAt: now}
		if err := store.UpsertGroup(ctx, group); err != nil {
			t.Fatalf("UpsertGroup() error = %v", err)
		}

		listed, err := store.ListGroups(ctx)
		if err != nil {
			t.Fatalf("ListGroups() error = %v", err)
		}
		if len(listed) != 1 || !reflect.DeepEqual(listed[0], group) {
			t.Fatalf("ListGroups() = %+v, want [%+v]", listed, group)
		}

		group.Description = "updated"
		if err := store.UpsertGroup(ctx, group); err != nil {
			t.Fatalf("UpsertGroup(update) error = %v", err)
		}
		listed, _ = store.ListGroups(ctx)
		if len(listed) != 1 || listed[0].Description != "updated" {
			t.Fatalf("ListGroups() after update = %+v", listed)
		}

		if err := store.DeleteGroup(ctx, "beta-testers"); err != nil {
			t.Fatalf("DeleteGroup() error = %v", err)
		}
		if err := store.DeleteGroup(ctx, "beta-testers"); !errors.Is(err, ErrGroupNotFound) {
			t.Fatalf("DeleteGroup(gone) error = %v, want ErrGroupNotFound", err)
		}
	})
}
