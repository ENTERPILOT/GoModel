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
			Group:       "team",
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
		if listed[1].Group != "" {
			t.Fatalf("ListUsers()[1].Group = %q, want empty", listed[1].Group)
		}

		// Update in place keeps the row count and changes fields.
		user.Name = "Alpha"
		user.Group = ""
		if err := store.UpsertUser(ctx, user); err != nil {
			t.Fatalf("UpsertUser(update) error = %v", err)
		}
		listed, err = store.ListUsers(ctx)
		if err != nil {
			t.Fatalf("ListUsers() error = %v", err)
		}
		if len(listed) != 2 || listed[0].Name != "Alpha" || listed[0].Group != "" {
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

func TestSQLStoreMigratesPreTreeSchema(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		// The schema before paths were derived from the group tree: a JSON
		// membership column on users, no parent on groups.
		err := db.Schema(ctx,
			`CREATE TABLE users (
				id TEXT PRIMARY KEY,
				user_path TEXT NOT NULL UNIQUE,
				name TEXT NOT NULL DEFAULT '',
				description TEXT NOT NULL DEFAULT '',
				user_groups `+sqlx.TypeJSON+`,
				created_at `+sqlx.TypeInt64+` NOT NULL,
				updated_at `+sqlx.TypeInt64+` NOT NULL
			)`,
			`CREATE TABLE user_groups (
				name TEXT PRIMARY KEY,
				description TEXT NOT NULL DEFAULT '',
				created_at `+sqlx.TypeInt64+` NOT NULL,
				updated_at `+sqlx.TypeInt64+` NOT NULL
			)`,
		)
		if err != nil {
			t.Fatalf("create old schema: %v", err)
		}

		store, err := NewSQLStore(ctx, db)
		if err != nil {
			t.Fatalf("NewSQLStore: %v", err)
		}
		now := time.Now().UTC().Truncate(time.Second)
		user := User{ID: "u1", UserPath: "/team/anna", Name: "anna", Group: "team", CreatedAt: now, UpdatedAt: now}
		if err := store.UpsertUser(ctx, user); err != nil {
			t.Fatalf("UpsertUser() error = %v", err)
		}
		group := Group{Name: "team", Parent: "org", CreatedAt: now, UpdatedAt: now}
		if err := store.UpsertGroup(ctx, group); err != nil {
			t.Fatalf("UpsertGroup() error = %v", err)
		}
		listedUsers, err := store.ListUsers(ctx)
		if err != nil {
			t.Fatalf("ListUsers() error = %v", err)
		}
		if len(listedUsers) != 1 || !reflect.DeepEqual(listedUsers[0], user) {
			t.Fatalf("ListUsers() = %+v, want [%+v]", listedUsers, user)
		}
		listedGroups, err := store.ListGroups(ctx)
		if err != nil {
			t.Fatalf("ListGroups() error = %v", err)
		}
		if len(listedGroups) != 1 || !reflect.DeepEqual(listedGroups[0], group) {
			t.Fatalf("ListGroups() = %+v, want [%+v]", listedGroups, group)
		}
	})
}

func TestSQLStoreGroupRoundTrip(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
		ctx := context.Background()
		now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
		group := Group{Name: "beta-testers", Description: "early access", Parent: "engineering", CreatedAt: now, UpdatedAt: now}
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
