package mediastore

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

	db := client.Database(mongoTestDatabaseName(t.Name()))
	store, err := NewMongoDBStore(db)
	if err != nil {
		_ = client.Disconnect(ctx)
		require.NoError(t, err)
	}
	t.Cleanup(func() {
		_ = db.Drop(ctx)
		_ = client.Disconnect(ctx)
	})
	return store
}

// MongoDB rejects database names of 64 bytes or more, so the test name is
// bounded rather than concatenated whole.
func mongoTestDatabaseName(testName string) string {
	const prefix = "gomodel_mediastore_test_"
	suffix := "_" + time.Now().Format("20060102150405_000000000")
	sanitized := strings.ReplaceAll(testName, "/", "_")
	if budget := 63 - len(prefix) - len(suffix); len(sanitized) > budget {
		sanitized = sanitized[:budget]
	}
	return prefix + sanitized + suffix
}

func testObject(id string, expiresAt time.Time) *Object {
	return &Object{
		ID:          id,
		Kind:        KindAudio,
		Source:      SourceAudit,
		ContentType: "audio/mpeg",
		Bytes:       12,
		StorageKey:  "audio/2026/09/22/" + id + ".mp3",
		RequestID:   "req-" + id,
		UserPath:    "/team/a",
		CreatedAt:   time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC),
		ExpiresAt:   expiresAt,
	}
}

func TestStore_InsertGetDelete(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()
		expires := time.Date(2026, 10, 22, 10, 0, 0, 0, time.UTC)
		require.NoError(t, store.Insert(ctx, testObject("a", expires)))

		got, err := store.Get(ctx, "a")
		require.NoError(t, err)
		assert.Equal(t, KindAudio, got.Kind)
		assert.Equal(t, SourceAudit, got.Source)
		assert.Equal(t, "audio/mpeg", got.ContentType)
		assert.Equal(t, int64(12), got.Bytes)
		assert.Equal(t, "audio/2026/09/22/a.mp3", got.StorageKey)
		assert.Equal(t, "req-a", got.RequestID)
		assert.Equal(t, "/team/a", got.UserPath)
		assert.True(t, got.CreatedAt.Equal(time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)), "created_at = %s", got.CreatedAt)
		assert.True(t, got.ExpiresAt.Equal(expires), "expires_at = %s", got.ExpiresAt)

		require.NoError(t, store.Delete(ctx, "a"))
		_, err = store.Get(ctx, "a")
		require.ErrorIs(t, err, ErrNotFound)
		assert.ErrorIs(t, store.Delete(ctx, "a"), ErrNotFound)
	})
}

func TestStore_NeverExpiresRoundTrips(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()
		require.NoError(t, store.Insert(ctx, testObject("forever", time.Time{})))
		got, err := store.Get(ctx, "forever")
		require.NoError(t, err)
		assert.True(t, got.ExpiresAt.IsZero(), "expires_at = %s, want zero", got.ExpiresAt)
		assert.False(t, got.Expired(time.Date(2999, 1, 1, 0, 0, 0, 0, time.UTC)))
	})
}

func TestStore_ExpiredOrdersOldestFirstAndHonorsLimit(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()
		base := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
		require.NoError(t, store.Insert(ctx, testObject("later", base.Add(2*time.Hour))))
		require.NoError(t, store.Insert(ctx, testObject("soon", base.Add(time.Hour))))
		require.NoError(t, store.Insert(ctx, testObject("future", base.Add(48*time.Hour))))
		require.NoError(t, store.Insert(ctx, testObject("forever", time.Time{})))

		expired, err := store.Expired(ctx, base.Add(3*time.Hour), 10)
		require.NoError(t, err)
		require.Len(t, expired, 2)
		assert.Equal(t, "soon", expired[0].ID)
		assert.Equal(t, "later", expired[1].ID)

		limited, err := store.Expired(ctx, base.Add(3*time.Hour), 1)
		require.NoError(t, err)
		require.Len(t, limited, 1)
		assert.Equal(t, "soon", limited[0].ID)

		none, err := store.Expired(ctx, base, 10)
		require.NoError(t, err)
		assert.Empty(t, none)
	})
}

func TestStore_RejectsIncompleteObjects(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()
		missingID := testObject("", time.Time{})
		require.Error(t, store.Insert(ctx, missingID))
		missingKey := testObject("x", time.Time{})
		missingKey.StorageKey = ""
		require.Error(t, store.Insert(ctx, missingKey))
		assert.Error(t, store.Insert(ctx, nil))
	})
}
