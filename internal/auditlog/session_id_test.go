package auditlog

import (
	"context"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
)

func TestSQLStore_SessionIDRoundtripAndFilter(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, err := newSQLStoreForTest(t, db, 0)
		if err != nil {
			t.Fatalf("failed to create store: %v", err)
		}
		defer store.Close()

		ctx := context.Background()
		base := time.Now().UTC()
		entries := []*LogEntry{
			{ID: "s-1", Timestamp: base, Provider: "openai", SessionID: "sess-a"},
			{ID: "s-2", Timestamp: base.Add(time.Second), Provider: "openai", SessionID: "sess-a"},
			{ID: "s-3", Timestamp: base.Add(2 * time.Second), Provider: "openai", SessionID: "sess-b"},
			{ID: "s-4", Timestamp: base.Add(3 * time.Second), Provider: "openai"},
		}
		if err := store.WriteBatch(ctx, entries); err != nil {
			t.Fatalf("WriteBatch failed: %v", err)
		}

		reader, err := NewSQLReader(db)
		if err != nil {
			t.Fatalf("failed to create reader: %v", err)
		}

		all, err := reader.GetLogs(ctx, LogQueryParams{Limit: 10})
		if err != nil {
			t.Fatalf("GetLogs failed: %v", err)
		}
		bySession := make(map[string]string, len(all.Entries))
		for _, entry := range all.Entries {
			bySession[entry.ID] = entry.SessionID
		}
		if bySession["s-1"] != "sess-a" || bySession["s-3"] != "sess-b" || bySession["s-4"] != "" {
			t.Fatalf("session ids not round-tripped: %#v", bySession)
		}

		filtered, err := reader.GetLogs(ctx, LogQueryParams{SessionID: "sess-a", Limit: 10})
		if err != nil {
			t.Fatalf("GetLogs with session filter failed: %v", err)
		}
		if filtered.Total != 2 || len(filtered.Entries) != 2 {
			t.Fatalf("session filter: total=%d entries=%d, want 2/2", filtered.Total, len(filtered.Entries))
		}
		for _, entry := range filtered.Entries {
			if entry.SessionID != "sess-a" {
				t.Fatalf("filter leaked entry %q with session %q", entry.ID, entry.SessionID)
			}
		}
	})
}

func TestSQLReader_GetSessions(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, err := newSQLStoreForTest(t, db, 0)
		if err != nil {
			t.Fatalf("failed to create store: %v", err)
		}
		defer store.Close()

		ctx := context.Background()
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

		reader, err := NewSQLReader(db)
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

		// Ordered by latest activity: solo (10:03), sess-a (10:02), sess-b (10:01).
		if got := result.Sessions[0].Latest.ID; got != "solo" {
			t.Fatalf("sessions[0].Latest.ID = %q, want solo", got)
		}
		if result.Sessions[0].SessionID != "" || result.Sessions[0].Count != 1 {
			t.Fatalf("singleton thread = %+v", result.Sessions[0])
		}

		threadA := result.Sessions[1]
		if threadA.SessionID != "sess-a" || threadA.Count != 2 {
			t.Fatalf("sess-a summary = %+v", threadA)
		}
		if threadA.Latest.ID != "a-2" {
			t.Fatalf("sess-a latest = %q, want a-2", threadA.Latest.ID)
		}
		if !threadA.FirstTimestamp.Equal(base) || !threadA.LastTimestamp.Equal(base.Add(2*time.Minute)) {
			t.Fatalf("sess-a span = %v..%v", threadA.FirstTimestamp, threadA.LastTimestamp)
		}

		if result.Sessions[2].SessionID != "sess-b" {
			t.Fatalf("sessions[2] = %+v", result.Sessions[2])
		}

		// Filters apply to entries before grouping: only sess-b has a 500.
		status := 500
		filtered, err := reader.GetSessions(ctx, LogQueryParams{StatusCode: &status, Limit: 10})
		if err != nil {
			t.Fatalf("GetSessions with filter failed: %v", err)
		}
		if filtered.Total != 1 || len(filtered.Sessions) != 1 || filtered.Sessions[0].SessionID != "sess-b" {
			t.Fatalf("filtered result = %+v", filtered)
		}

		// The SessionID filter narrows the grouped view to one thread.
		bySession, err := reader.GetSessions(ctx, LogQueryParams{SessionID: "sess-a", Limit: 10})
		if err != nil {
			t.Fatalf("GetSessions with session filter failed: %v", err)
		}
		if bySession.Total != 1 || len(bySession.Sessions) != 1 {
			t.Fatalf("session-filtered result = %+v", bySession)
		}
		if got := bySession.Sessions[0]; got.SessionID != "sess-a" || got.Count != 2 || got.Latest.ID != "a-2" {
			t.Fatalf("session-filtered thread = %+v", got)
		}
	})
}

func TestCreateStreamEntryPreservesSessionID(t *testing.T) {
	base := &LogEntry{
		ID:        "entry-1",
		Path:      "/v1/chat/completions",
		SessionID: "sess-42",
	}
	streamEntry := CreateStreamEntry(base)
	if streamEntry == nil {
		t.Fatal("expected a stream entry")
	}
	if streamEntry.SessionID != "sess-42" {
		t.Fatalf("SessionID = %q, want %q (lost in the whitelist copy)", streamEntry.SessionID, "sess-42")
	}
}
