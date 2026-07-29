package auditlog

import (
	"context"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
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

		// Filters apply to entries before grouping.
		assertGetSessionsFilters(t, reader)
	})
}

// PostgreSQL databases created before the unified schema keep their original
// uuid id column — CREATE TABLE IF NOT EXISTS never retypes it. The session
// thread key coalesces id with the text session_id, so without a cast every
// GetSessions call on such a database fails with SQLSTATE 42804 ("COALESCE
// types text and uuid cannot be matched").
func TestSQLReader_GetSessionsOnLegacyUUIDSchema(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		if db.Dialect() != sqlx.PostgreSQL {
			t.Skip("only pre-unification PostgreSQL schemas used a uuid id column")
		}

		ctx := context.Background()
		// The audit_logs table exactly as the standalone PostgreSQL store
		// created it; session_id is absent and arrives via migration.
		if err := db.Schema(ctx, `
			CREATE TABLE audit_logs (
				id UUID PRIMARY KEY,
				timestamp TIMESTAMPTZ NOT NULL,
				duration_ns BIGINT DEFAULT 0,
				requested_model TEXT,
				resolved_model TEXT,
				provider TEXT,
				provider_name TEXT,
				alias_used BOOLEAN DEFAULT FALSE,
				workflow_version_id TEXT,
				cache_type TEXT,
				status_code INTEGER DEFAULT 0,
				request_id TEXT,
				auth_key_id TEXT,
				auth_method TEXT,
				client_ip TEXT,
				method TEXT,
				path TEXT,
				user_path TEXT,
				stream BOOLEAN DEFAULT FALSE,
				error_type TEXT,
				data JSONB
			)`, `
			CREATE TABLE audit_log_attempts (
				id BIGSERIAL PRIMARY KEY,
				audit_log_id UUID NOT NULL REFERENCES audit_logs(id) ON DELETE CASCADE,
				seq INTEGER NOT NULL,
				kind TEXT NOT NULL,
				provider_type TEXT,
				provider_name TEXT,
				model TEXT,
				status_code INTEGER DEFAULT 0,
				success BOOLEAN DEFAULT FALSE,
				error_type TEXT,
				error_code TEXT,
				error_message TEXT,
				response_body TEXT,
				response_headers TEXT,
				started_at TIMESTAMPTZ,
				duration_ns BIGINT DEFAULT 0,
				UNIQUE(audit_log_id, seq)
			)`); err != nil {
			t.Fatalf("create legacy tables: %v", err)
		}

		store, err := newSQLStoreForTest(t, db, 0)
		if err != nil {
			t.Fatalf("failed to create store: %v", err)
		}
		defer store.Close()

		base := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
		entries := []*LogEntry{
			{ID: "b32d7a52-0000-4000-8000-000000000001", Timestamp: base, Provider: "openai", SessionID: "sess-a", StatusCode: 200},
			{ID: "b32d7a52-0000-4000-8000-000000000002", Timestamp: base.Add(time.Minute), Provider: "openai", SessionID: "sess-a", StatusCode: 200},
			{ID: "b32d7a52-0000-4000-8000-000000000003", Timestamp: base.Add(2 * time.Minute), Provider: "openai", StatusCode: 200},
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
		if result.Total != 2 || len(result.Sessions) != 2 {
			t.Fatalf("total=%d sessions=%d, want 2/2", result.Total, len(result.Sessions))
		}
		want := []struct {
			latestID  string
			sessionID string
			count     int
		}{
			{"b32d7a52-0000-4000-8000-000000000003", "", 1},
			{"b32d7a52-0000-4000-8000-000000000002", "sess-a", 2},
		}
		for i, tt := range want {
			got := result.Sessions[i]
			if got.Latest.ID != tt.latestID || got.SessionID != tt.sessionID || got.Count != tt.count {
				t.Fatalf("sessions[%d] = %+v, want latest %q session %q count %d",
					i, got, tt.latestID, tt.sessionID, tt.count)
			}
		}
	})
}

// assertGetSessionsFilters runs the shared filtered-grouping cases against a
// reader, so both backends prove filters apply to entries before grouping.
func assertGetSessionsFilters(t *testing.T, reader Reader) {
	t.Helper()
	status := 500
	tests := []struct {
		name          string
		params        LogQueryParams
		wantSessionID string
		wantCount     int
		wantLatestID  string
	}{
		{
			name:          "status filter keeps only the thread with a 500",
			params:        LogQueryParams{StatusCode: &status, Limit: 10},
			wantSessionID: "sess-b",
			wantCount:     1,
			wantLatestID:  "b-1",
		},
		{
			name:          "session filter narrows the grouped view to one thread",
			params:        LogQueryParams{SessionID: "sess-a", Limit: 10},
			wantSessionID: "sess-a",
			wantCount:     2,
			wantLatestID:  "a-2",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := reader.GetSessions(context.Background(), tt.params)
			if err != nil {
				t.Fatalf("GetSessions() error = %v", err)
			}
			if result.Total != 1 || len(result.Sessions) != 1 {
				t.Fatalf("result = %+v, want exactly one thread", result)
			}
			got := result.Sessions[0]
			if got.SessionID != tt.wantSessionID || got.Count != tt.wantCount || got.Latest.ID != tt.wantLatestID {
				t.Fatalf("thread = %+v, want session %q count %d latest %q",
					got, tt.wantSessionID, tt.wantCount, tt.wantLatestID)
			}
		})
	}
}

func TestCreateStreamEntryPreservesSessionID(t *testing.T) {
	base := &LogEntry{
		ID:        "entry-1",
		Path:      "/v1/chat/completions",
		SessionID: "sess-42",
	}
	streamEntry := CreateStreamEntry(context.Background(), base)
	if streamEntry == nil {
		t.Fatal("expected a stream entry")
	}
	if streamEntry.SessionID != "sess-42" {
		t.Fatalf("SessionID = %q, want %q (lost in the whitelist copy)", streamEntry.SessionID, "sess-42")
	}
}

// The stream copy is created mid-handler, before the audit middleware's
// post-handler enrichment stamps the session id onto the base entry — so
// CreateStreamEntry must capture it from the request context itself, or every
// streamed request loses its session id.
func TestCreateStreamEntryCapturesSessionIDFromContext(t *testing.T) {
	ctx := core.WithSessionID(context.Background(), "sess-ctx")
	streamEntry := CreateStreamEntry(ctx, &LogEntry{ID: "entry-1", Path: "/v1/chat/completions"})
	if streamEntry == nil {
		t.Fatal("expected a stream entry")
	}
	if streamEntry.SessionID != "sess-ctx" {
		t.Fatalf("SessionID = %q, want context-derived %q", streamEntry.SessionID, "sess-ctx")
	}
}

// The stream copy must finalize every context-derived identity field the
// audit middleware would apply post-handler — not only the session id.
// Managed-key labels merge into the context during authentication, after the
// base entry snapshotted its pre-auth labels.
func TestCreateStreamEntryFinalizesContextIdentity(t *testing.T) {
	ctx := core.WithSessionID(context.Background(), "sess-ctx")
	ctx = core.WithAuthKeyID(ctx, "key-1")
	ctx = core.WithRequestLabels(ctx, []string{"team-a", "billing"})

	streamEntry := CreateStreamEntry(ctx, &LogEntry{
		ID:   "entry-1",
		Path: "/v1/chat/completions",
		Data: &LogData{Labels: []string{"pre-auth"}},
	})
	if streamEntry == nil || streamEntry.Data == nil {
		t.Fatal("expected a stream entry with data")
	}
	if streamEntry.SessionID != "sess-ctx" || streamEntry.AuthKeyID != "key-1" {
		t.Fatalf("identity not finalized: session=%q auth=%q", streamEntry.SessionID, streamEntry.AuthKeyID)
	}
	if len(streamEntry.Data.Labels) != 2 || streamEntry.Data.Labels[0] != "team-a" {
		t.Fatalf("managed-key labels lost on the stream copy: %#v", streamEntry.Data.Labels)
	}
}
