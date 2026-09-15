package filestore

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
			require.NoError(t, err)

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
	require.NoError(t, err)

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
		err := store.Upsert(ctx, &StoredFile{
			ID:           "file-1",
			ProviderType: "openai",
			Purpose:      "batch",
			Filename:     "original.jsonl",
			Bytes:        10,
			CreatedAt:    111,
			UserPath:     "/v1/files",
		})
		require.NoError(t, err)
		err = store.Upsert(ctx, &StoredFile{
			ID:           "file-1",
			ProviderType: "anthropic",
			Purpose:      "fine-tune",
			Filename:     "updated.jsonl",
			Bytes:        20,
			CreatedAt:    222,
			UserPath:     "/v1/files?provider=anthropic",
		})
		require.NoError(t, err)

		stored, err := store.Get(ctx, "file-1")
		require.NoError(t, err)

		// created_at is deliberately absent from the ON CONFLICT update list:
		// a re-upsert refreshes provider ownership without rewriting when the
		// file was first seen.
		assert.Equal(t, int64(111), stored.CreatedAt)
		assert.Equal(t, "anthropic", stored.ProviderType)
		assert.Equal(t, "updated.jsonl", stored.Filename)
	})
}

func TestStoreGetMissingReturnsNotFound(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		_, err := store.Get(context.Background(), "absent")
		require.ErrorIs(t, err, ErrNotFound)
	})
}

func TestStoreDeleteMissingReturnsNotFound(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		err := store.Delete(context.Background(), "absent")
		require.ErrorIs(t, err, ErrNotFound)
	})
}

func TestStoreDeleteRemovesMapping(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()
		err := store.Upsert(ctx, &StoredFile{ID: "file-1", ProviderType: "openai"})
		require.NoError(t, err)
		err = store.Delete(ctx, "file-1")
		require.NoError(t, err)
		_, err = store.Get(ctx, "file-1")
		require.ErrorIs(t, err, ErrNotFound)
	})
}

func TestStoreRejectsIncompleteMapping(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()
		assert.Error(t, store.Upsert(ctx, &StoredFile{ProviderType: "openai"}))
		assert.Error(t, store.Upsert(ctx, &StoredFile{ID: "file-1"}))
	})
}

func fileIDs(files []*StoredFile) []string {
	ids := make([]string, 0, len(files))
	for _, file := range files {
		ids = append(ids, file.ID)
	}
	return ids
}

func TestStoreListFiltersByUserPathSubtree(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()
		rows := []struct {
			id, userPath, provider, purpose string
			createdAt                       int64
		}{
			{"file-beta", "/team/beta", "openai", "batch", 6},
			{"file-alpha-new", "/team/alpha", "openai", "batch", 5},
			{"file-alpha-anthropic", "/team/alpha", "anthropic", "batch", 4},
			{"file-alpha-child", "/team/alpha/service", "openai", "assistants", 3},
			{"file-alpha-sibling", "/team/alpha-2", "openai", "batch", 2},
			{"file-legacy", "", "openai", "batch", 1},
		}
		for _, row := range rows {
			err := store.Upsert(ctx, &StoredFile{ID: row.id, ProviderType: row.provider, Purpose: row.purpose, UserPath: row.userPath, CreatedAt: row.createdAt})
			require.NoError(t, err, "upsert %s: %v", row.id, err)
		}

		tests := []struct {
			name   string
			filter ListFilter
			after  string
			want   []string
			notFnd bool
		}{
			{name: "no filter", want: []string{"file-beta", "file-alpha-new", "file-alpha-anthropic", "file-alpha-child", "file-alpha-sibling", "file-legacy"}},
			{name: "subtree", filter: ListFilter{UserPath: "/team/alpha"}, want: []string{"file-alpha-new", "file-alpha-anthropic", "file-alpha-child"}},
			{name: "subtree and provider", filter: ListFilter{UserPath: "/team/alpha", ProviderType: "openai"}, want: []string{"file-alpha-new", "file-alpha-child"}},
			{name: "subtree and purpose", filter: ListFilter{UserPath: "/team/alpha", Purpose: "assistants"}, want: []string{"file-alpha-child"}},
			{name: "subtree after cursor", filter: ListFilter{UserPath: "/team/alpha"}, after: "file-alpha-new", want: []string{"file-alpha-anthropic", "file-alpha-child"}},
			{name: "root keeps every tracked path", filter: ListFilter{UserPath: "/"}, want: []string{"file-beta", "file-alpha-new", "file-alpha-anthropic", "file-alpha-child", "file-alpha-sibling"}},
			{name: "foreign cursor is missing", filter: ListFilter{UserPath: "/team/alpha"}, after: "file-beta", notFnd: true},
			{name: "unknown cursor is missing", after: "nope", notFnd: true},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got, err := store.List(ctx, tt.filter, 10, tt.after)
				if tt.notFnd {
					require.ErrorIs(t, err, ErrNotFound)

					return
				}
				require.NoError(t, err)
				ids := fileIDs(got)
				require.True(t, slices.Equal(ids, tt.want), "ids = %v, want %v", ids, tt.want)
			})
		}

		page, err := store.List(ctx, ListFilter{UserPath: "/team/alpha"}, 2, "")
		require.NoError(t, err)
		ids := fileIDs(page)
		require.True(t, slices.Equal(ids, []string{"file-alpha-new", "file-alpha-anthropic"}), "page ids = %v", ids)
	})
}
