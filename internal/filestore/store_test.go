package filestore

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	_ "modernc.org/sqlite"
)

func TestStoreUpsertPreservesCreatedAt(t *testing.T) {
	ctx := context.Background()
	stores := map[string]func(t *testing.T) (Store, func()){
		"memory": func(t *testing.T) (Store, func()) {
			return NewMemoryStore(), func() {}
		},
		"sqlite": func(t *testing.T) (Store, func()) {
			db, err := sql.Open("sqlite", ":memory:")
			if err != nil {
				t.Fatalf("sql.Open() error = %v", err)
			}
			store, err := NewSQLiteStore(db)
			if err != nil {
				_ = db.Close()
				t.Fatalf("NewSQLiteStore() error = %v", err)
			}
			return store, func() {
				_ = db.Close()
			}
		},
		"postgres": func(t *testing.T) (Store, func()) {
			dsn := os.Getenv("TEST_DATABASE_DSN")
			if dsn == "" {
				t.Skip("TEST_DATABASE_DSN is not set")
			}
			pool, err := pgxpool.New(ctx, dsn)
			if err != nil {
				t.Fatalf("pgxpool.New() error = %v", err)
			}
			store, err := NewPostgreSQLStore(ctx, pool)
			if err != nil {
				pool.Close()
				t.Fatalf("NewPostgreSQLStore() error = %v", err)
			}
			return store, pool.Close
		},
		"mongo": func(t *testing.T) (Store, func()) {
			dsn := os.Getenv("MONGO_TEST_DSN")
			if dsn == "" {
				t.Skip("MONGO_TEST_DSN is not set")
			}
			client, err := mongo.Connect(options.Client().ApplyURI(dsn))
			if err != nil {
				t.Fatalf("mongo.Connect() error = %v", err)
			}
			db := client.Database("gomodel_filestore_test_" + strings.ReplaceAll(t.Name(), "/", "_") + "_" + time.Now().Format("20060102150405_000000000"))
			store, err := NewMongoDBStore(db)
			if err != nil {
				_ = client.Disconnect(ctx)
				t.Fatalf("NewMongoDBStore() error = %v", err)
			}
			return store, func() {
				_ = db.Drop(ctx)
				_ = client.Disconnect(ctx)
			}
		},
	}

	for name, newStore := range stores {
		t.Run(name, func(t *testing.T) {
			store, cleanup := newStore(t)
			defer cleanup()
			fileID := "file_" + strings.ReplaceAll(t.Name(), "/", "_") + "_" + time.Now().Format("20060102150405.000000000")
			defer func() {
				_ = store.Delete(ctx, fileID)
			}()

			if err := store.Upsert(ctx, &StoredFile{
				ID:           fileID,
				ProviderType: "openai",
				Purpose:      "batch",
				Filename:     "original.jsonl",
				Bytes:        10,
				CreatedAt:    111,
				UserPath:     "/v1/files",
			}); err != nil {
				t.Fatalf("initial Upsert() error = %v", err)
			}
			if err := store.Upsert(ctx, &StoredFile{
				ID:           fileID,
				ProviderType: "anthropic",
				Purpose:      "fine-tune",
				Filename:     "updated.jsonl",
				Bytes:        20,
				CreatedAt:    222,
				UserPath:     "/v1/files?provider=anthropic",
			}); err != nil {
				t.Fatalf("second Upsert() error = %v", err)
			}

			stored, err := store.Get(ctx, fileID)
			if err != nil {
				t.Fatalf("Get() error = %v", err)
			}
			if stored.CreatedAt != 111 {
				t.Fatalf("CreatedAt = %d, want 111", stored.CreatedAt)
			}
			if stored.ProviderType != "anthropic" {
				t.Fatalf("ProviderType = %q, want anthropic", stored.ProviderType)
			}
			if stored.Filename != "updated.jsonl" {
				t.Fatalf("Filename = %q, want updated.jsonl", stored.Filename)
			}
		})
	}
}
