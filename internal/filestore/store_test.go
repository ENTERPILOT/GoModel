package filestore

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
)

// runStoreSuite exercises the behaviour every Store implementation owes its
// callers, against each backend available in this environment.
func runStoreSuite(t *testing.T, suite func(t *testing.T, store Store)) {
	t.Helper()

	t.Run("memory", func(t *testing.T) {
		suite(t, NewMemoryStore())
	})

	t.Run("sql", func(t *testing.T) {
		sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
			store, err := NewSQLStore(context.Background(), db)
			if err != nil {
				t.Fatalf("NewSQLStore: %v", err)
			}
			suite(t, store)
		})
	})

	t.Run("mongo", func(t *testing.T) {
		suite(t, newMongoTestStore(t))
	})
}

func newMongoTestStore(t *testing.T) Store {
	t.Helper()

	dsn := os.Getenv("MONGO_TEST_DSN")
	if dsn == "" {
		t.Skip("MONGO_TEST_DSN is not set")
	}
	ctx := context.Background()
	client, err := mongo.Connect(options.Client().ApplyURI(dsn))
	if err != nil {
		t.Fatalf("mongo.Connect: %v", err)
	}
	// MongoDB rejects database names of 64 bytes or more, and the prefix plus
	// a timestamp already spends 48 of them, so the test name is bounded
	// rather than concatenated whole.
	db := client.Database(mongoTestDatabaseName(t.Name()))
	store, err := NewMongoDBStore(db)
	if err != nil {
		_ = client.Disconnect(ctx)
		t.Fatalf("NewMongoDBStore: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Drop(ctx)
		_ = client.Disconnect(ctx)
	})
	return store
}

// mongoTestDatabaseName builds a unique database name that stays inside
// MongoDB's 64-byte limit.
func mongoTestDatabaseName(testName string) string {
	const prefix = "gomodel_filestore_test_"
	suffix := "_" + time.Now().Format("20060102150405_000000000")

	sanitized := strings.ReplaceAll(testName, "/", "_")
	if budget := 63 - len(prefix) - len(suffix); len(sanitized) > budget {
		sanitized = sanitized[:budget]
	}
	return prefix + sanitized + suffix
}

func TestStoreUpsertPreservesCreatedAt(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()

		if err := store.Upsert(ctx, &StoredFile{
			ID:           "file-1",
			ProviderType: "openai",
			Purpose:      "batch",
			Filename:     "original.jsonl",
			Bytes:        10,
			CreatedAt:    111,
			UserPath:     "/v1/files",
		}); err != nil {
			t.Fatalf("initial Upsert: %v", err)
		}
		if err := store.Upsert(ctx, &StoredFile{
			ID:           "file-1",
			ProviderType: "anthropic",
			Purpose:      "fine-tune",
			Filename:     "updated.jsonl",
			Bytes:        20,
			CreatedAt:    222,
			UserPath:     "/v1/files?provider=anthropic",
		}); err != nil {
			t.Fatalf("second Upsert: %v", err)
		}

		stored, err := store.Get(ctx, "file-1")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		// created_at is deliberately absent from the ON CONFLICT update list:
		// a re-upsert refreshes provider ownership without rewriting when the
		// file was first seen.
		if stored.CreatedAt != 111 {
			t.Errorf("CreatedAt = %d, want 111 preserved", stored.CreatedAt)
		}
		if stored.ProviderType != "anthropic" {
			t.Errorf("ProviderType = %q, want anthropic", stored.ProviderType)
		}
		if stored.Filename != "updated.jsonl" {
			t.Errorf("Filename = %q, want updated.jsonl", stored.Filename)
		}
	})
}

func TestStoreGetMissingReturnsNotFound(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		_, err := store.Get(context.Background(), "absent")
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("Get error = %v, want ErrNotFound", err)
		}
	})
}

func TestStoreDeleteMissingReturnsNotFound(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		err := store.Delete(context.Background(), "absent")
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("Delete error = %v, want ErrNotFound", err)
		}
	})
}

func TestStoreDeleteRemovesMapping(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()
		if err := store.Upsert(ctx, &StoredFile{ID: "file-1", ProviderType: "openai"}); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		if err := store.Delete(ctx, "file-1"); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if _, err := store.Get(ctx, "file-1"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Get after Delete = %v, want ErrNotFound", err)
		}
	})
}

func TestStoreRejectsIncompleteMapping(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()
		if err := store.Upsert(ctx, &StoredFile{ProviderType: "openai"}); err == nil {
			t.Error("Upsert without an id succeeded, want failure")
		}
		if err := store.Upsert(ctx, &StoredFile{ID: "file-1"}); err == nil {
			t.Error("Upsert without a provider type succeeded, want failure")
		}
	})
}
