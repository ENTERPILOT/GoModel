package auditlog

import (
	"context"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/internal/storage/mongotest"
)

// Mirrors TestSQLReader_GetSessions so the hand-written MongoDB aggregation
// cannot drift from the SQL behaviour. Skips without MONGO_TEST_DSN.
func TestMongoDBReader_GetSessions(t *testing.T) {
	mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
		ctx := context.Background()
		store, err := NewMongoDBStore(db, 0)
		if err != nil {
			t.Fatalf("failed to create store: %v", err)
		}
		defer store.Close()

		base := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
		entries := sessionThreadFixture(base)
		if err := store.WriteBatch(ctx, entries); err != nil {
			t.Fatalf("WriteBatch failed: %v", err)
		}

		reader, err := NewMongoDBReader(db)
		if err != nil {
			t.Fatalf("failed to create reader: %v", err)
		}

		result, err := reader.GetSessions(ctx, LogQueryParams{Limit: 10})
		if err != nil {
			t.Fatalf("GetSessions failed: %v", err)
		}
		if result.Total != 3 || len(result.Sessions) != 3 {
			t.Fatalf("total=%d sessions=%d, want 3/3", result.Total, len(result.Sessions))
		}
		if got := result.Sessions[0].Latest.ID; got != "solo" {
			t.Fatalf("sessions[0].Latest.ID = %q, want solo", got)
		}
		threadA := result.Sessions[1]
		if threadA.SessionID != "sess-a" || threadA.Count != 2 || threadA.TotalCount != 2 || threadA.Latest.ID != "a-2" {
			t.Fatalf("sess-a summary = %+v", threadA)
		}
		if !threadA.FirstTimestamp.Equal(base.Add(-24*time.Hour)) || !threadA.LastTimestamp.Equal(base.Add(2*time.Minute)) {
			t.Fatalf("sess-a span = %v..%v", threadA.FirstTimestamp, threadA.LastTimestamp)
		}
		if result.Sessions[2].SessionID != "sess-b" {
			t.Fatalf("sessions[2] = %+v", result.Sessions[2])
		}

		assertGetSessionsHeadPayload(t, reader)
		assertGetSessionsPaging(t, reader)
		assertGetSessionsFilters(t, reader)
	})
}

func TestMongoDBReader_GetConversationScopesSessionToUserPath(t *testing.T) {
	mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
		ctx := context.Background()
		store, err := NewMongoDBStore(db, 0)
		if err != nil {
			t.Fatalf("failed to create store: %v", err)
		}
		defer store.Close()

		if err := store.WriteBatch(ctx, conversationPathIsolationFixture(time.Now().UTC())); err != nil {
			t.Fatalf("WriteBatch failed: %v", err)
		}

		reader, err := NewMongoDBReader(db)
		if err != nil {
			t.Fatalf("failed to create reader: %v", err)
		}
		assertConversationUserPathIsolation(t, reader)
	})
}
