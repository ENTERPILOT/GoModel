package auditlog

import (
	"context"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/internal/storage/mongotest"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
	"github.com/stretchr/testify/require"
)

// runReaderSuite exercises behaviour every store/reader pair owes its
// callers, against each backend available in this environment. The MongoDB
// leg skips without MONGO_TEST_DSN.
func runReaderSuite(t *testing.T, body func(t *testing.T, store LogStore, reader Reader)) {
	t.Helper()
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, err := newSQLStoreForTest(t, db, 0)
		require.NoError(t, err)
		t.Cleanup(func() { _ = store.Close() })

		reader, err := NewSQLReader(db)
		require.NoError(t, err)
		body(t, store, reader)
	})
	mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
		store, err := NewMongoDBStore(db, 0)
		require.NoError(t, err)
		t.Cleanup(func() { _ = store.Close() })

		reader, err := NewMongoDBReader(db)
		require.NoError(t, err)
		body(t, store, reader)
	})
}

func TestReader_GetLogByIDAndInteractionParent(t *testing.T) {
	runReaderSuite(t, func(t *testing.T, store LogStore, reader Reader) {
		ctx := context.Background()
		err := store.WriteBatch(ctx, []*LogEntry{{
			ID:             "by-id",
			Timestamp:      time.Date(2026, 1, 16, 12, 0, 0, 0, time.UTC),
			RequestedModel: "gpt-5",
			Provider:       "openai",
			UserPath:       "/team/a",
			SessionID:      "sess-1",
			StatusCode:     200,
		}})
		require.NoError(t, err)

		entry, err := reader.GetLogByID(ctx, "by-id")
		require.NoError(t, err)
		require.NotNil(t, entry)
		require.Equal(t, "gpt-5", entry.RequestedModel)
		require.Equal(t, "/team/a", entry.UserPath)
		require.Equal(t, "sess-1", entry.SessionID)
		require.Equal(t, 200, entry.StatusCode)

		missing, err := reader.GetLogByID(ctx, "absent")
		require.NoError(t, err)
		require.Nil(t, missing)

		parent, err := reader.GetInteractionParent(ctx, "by-id")
		require.NoError(t, err)
		require.Equal(t, &InteractionParent{UserPath: "/team/a", SessionID: "sess-1"}, parent)

		noParent, err := reader.GetInteractionParent(ctx, "absent")
		require.NoError(t, err)
		require.Nil(t, noParent)
	})
}
