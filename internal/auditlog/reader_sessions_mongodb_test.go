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
		entries := []*LogEntry{
			{ID: "a-1", Timestamp: base, Provider: "openai", SessionID: "sess-a", StatusCode: 200},
			{ID: "a-2", Timestamp: base.Add(2 * time.Minute), Provider: "openai", SessionID: "sess-a", StatusCode: 200},
			{ID: "b-1", Timestamp: base.Add(time.Minute), Provider: "anthropic", SessionID: "sess-b", StatusCode: 500},
			{ID: "solo", Timestamp: base.Add(3 * time.Minute), Provider: "openai", StatusCode: 200},
		}
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
		if threadA.SessionID != "sess-a" || threadA.Count != 2 || threadA.Latest.ID != "a-2" {
			t.Fatalf("sess-a summary = %+v", threadA)
		}
		if !threadA.FirstTimestamp.Equal(base) || !threadA.LastTimestamp.Equal(base.Add(2*time.Minute)) {
			t.Fatalf("sess-a span = %v..%v", threadA.FirstTimestamp, threadA.LastTimestamp)
		}
		if result.Sessions[2].SessionID != "sess-b" {
			t.Fatalf("sessions[2] = %+v", result.Sessions[2])
		}

		status := 500
		filtered, err := reader.GetSessions(ctx, LogQueryParams{StatusCode: &status, Limit: 10})
		if err != nil {
			t.Fatalf("GetSessions with filter failed: %v", err)
		}
		if filtered.Total != 1 || len(filtered.Sessions) != 1 || filtered.Sessions[0].SessionID != "sess-b" {
			t.Fatalf("filtered result = %+v", filtered)
		}
	})
}
