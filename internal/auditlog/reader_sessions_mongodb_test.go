package auditlog

import (
	"context"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/internal/storage/mongotest"
	"github.com/stretchr/testify/require"
)

func TestMongoRequestCountLookup(t *testing.T) {
	stage, err := bson.MarshalExtJSON(mongoRequestCountLookup("custom_audit_logs"), false, false)
	require.NoError(t, err)

	encoded := string(stage)
	for _, want := range []string{
		`"from":"custom_audit_logs"`,
		`"$ifNull":["$latest.session_id",""]`,
		`"$ne":["$$sid",""]`,
		`"$eq":["$session_id","$$sid"]`,
		`"$count":"count"`,
		`"as":"request_count"`,
	} {
		require.Contains(t, encoded, want)
	}
}

// Mirrors TestSQLReader_GetSessions so the hand-written MongoDB aggregation
// cannot drift from the SQL behaviour. Skips without MONGO_TEST_DSN.
func TestMongoDBReader_GetSessions(t *testing.T) {
	mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
		ctx := context.Background()
		store, err := NewMongoDBStore(db, 0)
		require.NoError(t, err)

		defer store.Close()

		base := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
		entries := sessionThreadFixture(base)
		err = store.WriteBatch(ctx, entries)
		require.NoError(t, err)

		reader, err := NewMongoDBReader(db)
		require.NoError(t, err)

		result, err := reader.GetSessions(ctx, LogQueryParams{Limit: 10})
		require.NoError(t, err)
		require.Equal(t, 3, result.Total)
		require.Len(t, result.Sessions, 3)
		require.Equal(t, "solo", result.Sessions[0].Latest.ID)

		threadA := result.Sessions[1]
		require.Equal(t, "sess-a", threadA.SessionID)
		require.Equal(t, 2, threadA.RequestCount)
		require.Equal(t, "a-2", threadA.Latest.ID, "sess-a summary = %+v", threadA)
		require.Equal(t, "sess-b", result.Sessions[2].SessionID, "sessions[2] = %+v", result.Sessions[2])

		assertGetSessionsHeadPayload(t, reader)
		assertGetSessionsPaging(t, reader)
		assertGetSessionsFilters(t, reader)
	})
}

func TestMongoDBReader_GetConversationScopesSessionToUserPath(t *testing.T) {
	mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
		ctx := context.Background()
		store, err := NewMongoDBStore(db, 0)
		require.NoError(t, err)

		defer store.Close()
		err = store.WriteBatch(ctx, conversationPathIsolationFixture(time.Now().UTC()))
		require.NoError(t, err)

		reader, err := NewMongoDBReader(db)
		require.NoError(t, err)

		assertConversationUserPathIsolation(t, reader)
	})
}
