package users

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/internal/storage/mongotest"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
)

func runStoreSuite(t *testing.T, body func(t *testing.T, store Store)) {
	t.Helper()
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, err := NewSQLStore(context.Background(), db)
		if err != nil {
			t.Fatalf("NewSQLStore: %v", err)
		}
		t.Cleanup(func() { _ = store.Close() })
		body(t, store)
	})
	mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
		store, err := NewMongoDBStore(db)
		if err != nil {
			t.Fatalf("NewMongoDBStore: %v", err)
		}
		t.Cleanup(func() { _ = store.Close() })
		body(t, store)
	})
}

func TestStore_RoundTrip(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()
		now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)

		eng := User{UserPath: "/acme/eng", AllowedModels: []string{"anthropic/", "openai/gpt-4o"}, Description: "eng", CreatedAt: now, UpdatedAt: now}
		root := User{UserPath: "/acme", CreatedAt: now, UpdatedAt: now}
		for _, user := range []User{eng, root} {
			if err := store.Upsert(ctx, user); err != nil {
				t.Fatalf("Upsert(%s): %v", user.UserPath, err)
			}
		}

		rows, err := store.List(ctx)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(rows) != 2 || rows[0].UserPath != "/acme" || rows[1].UserPath != "/acme/eng" {
			t.Fatalf("List = %#v, want /acme then /acme/eng", rows)
		}
		if rows[0].AllowedModels != nil {
			t.Fatalf("empty allowlist round-tripped as %#v, want nil", rows[0].AllowedModels)
		}
		if !reflect.DeepEqual(rows[1].AllowedModels, eng.AllowedModels) || rows[1].Description != "eng" {
			t.Fatalf("eng row = %#v", rows[1])
		}
		if !rows[1].CreatedAt.Equal(now) || !rows[1].UpdatedAt.Equal(now) {
			t.Fatalf("timestamps = %v / %v, want %v", rows[1].CreatedAt, rows[1].UpdatedAt, now)
		}

		// Upsert keeps created_at and replaces the rest.
		later := now.Add(time.Hour)
		if err := store.Upsert(ctx, User{UserPath: "/acme/eng", AllowedModels: []string{"openai/"}, CreatedAt: later, UpdatedAt: later}); err != nil {
			t.Fatalf("Upsert(update): %v", err)
		}
		rows, err = store.List(ctx)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if !reflect.DeepEqual(rows[1].AllowedModels, []string{"openai/"}) || rows[1].Description != "" {
			t.Fatalf("updated row = %#v", rows[1])
		}
		if !rows[1].CreatedAt.Equal(now) || !rows[1].UpdatedAt.Equal(later) {
			t.Fatalf("updated timestamps = %v / %v", rows[1].CreatedAt, rows[1].UpdatedAt)
		}

		if err := store.Delete(ctx, "/acme"); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if err := store.Delete(ctx, "/acme"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Delete(missing) error = %v, want ErrNotFound", err)
		}
		rows, err = store.List(ctx)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(rows) != 1 || rows[0].UserPath != "/acme/eng" {
			t.Fatalf("List after delete = %#v", rows)
		}
	})
}
