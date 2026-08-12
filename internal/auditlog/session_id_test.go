package auditlog

import (
	"context"
	"fmt"
	"slices"
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

		conversation, err := reader.GetConversation(ctx, "s-1", 10)
		if err != nil {
			t.Fatalf("GetConversation failed: %v", err)
		}
		if conversation.AnchorID != "s-1" || len(conversation.Entries) != 2 {
			t.Fatalf("conversation = %+v, want the two-entry sess-a thread", conversation)
		}
		if conversation.Entries[0].ID != "s-1" || conversation.Entries[1].ID != "s-2" {
			t.Fatalf("conversation ids = %q, %q; want s-1, s-2",
				conversation.Entries[0].ID, conversation.Entries[1].ID)
		}
	})
}

func TestSQLReader_GetConversationUsesKeysetPagination(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, err := newSQLStoreForTest(t, db, 0)
		if err != nil {
			t.Fatalf("failed to create store: %v", err)
		}
		defer store.Close()

		ctx := context.Background()
		reader, err := NewSQLReader(db)
		if err != nil {
			t.Fatalf("NewSQLReader failed: %v", err)
		}

		base := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
		cases := []struct {
			name      string
			count     int
			timestamp func(int) time.Time
			truncated bool
		}{
			{
				name:      "distinct timestamps",
				count:     120,
				timestamp: func(i int) time.Time { return base.Add(time.Duration(i) * time.Second) },
			},
			{
				name:      "equal timestamps across page boundary",
				count:     121,
				timestamp: func(int) time.Time { return base.Add(time.Hour) },
				truncated: true,
			},
		}
		for caseIndex, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				prefix := fmt.Sprintf("paged-%d-", caseIndex)
				sessionID := fmt.Sprintf("paged-session-%d", caseIndex)
				entries := make([]*LogEntry, tc.count)
				for i := range entries {
					entries[i] = &LogEntry{
						ID:        fmt.Sprintf("%s%03d", prefix, i),
						Timestamp: tc.timestamp(i),
						SessionID: sessionID,
					}
				}
				if err := store.WriteBatch(ctx, entries); err != nil {
					t.Fatalf("WriteBatch failed: %v", err)
				}

				conversation, err := reader.GetConversation(ctx, entries[0].ID, 120)
				if err != nil {
					t.Fatalf("GetConversation failed: %v", err)
				}
				if len(conversation.Entries) != 120 || conversation.Truncated != tc.truncated {
					t.Fatalf("conversation entries/truncated = %d/%v, want 120/%v",
						len(conversation.Entries), conversation.Truncated, tc.truncated)
				}
				seen := make(map[string]struct{}, len(conversation.Entries))
				for i, entry := range conversation.Entries {
					wantID := fmt.Sprintf("%s%03d", prefix, i)
					if entry.ID != wantID {
						t.Fatalf("entry %d = %q, want %q", i, entry.ID, wantID)
					}
					if _, exists := seen[entry.ID]; exists {
						t.Fatalf("duplicate entry %q", entry.ID)
					}
					seen[entry.ID] = struct{}{}
				}
			})
		}
	})
}

func TestSQLReader_GetInteractionParentAllowsLegacyNulls(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, err := newSQLStoreForTest(t, db, 0)
		if err != nil {
			t.Fatalf("failed to create store: %v", err)
		}
		defer store.Close()

		ctx := context.Background()
		entry := &LogEntry{ID: "legacy-parent", Timestamp: time.Now().UTC(), UserPath: "/"}
		if err := store.WriteBatch(ctx, []*LogEntry{entry}); err != nil {
			t.Fatalf("WriteBatch failed: %v", err)
		}
		if _, err := db.Exec(ctx,
			"UPDATE audit_logs SET user_path = NULL, session_id = NULL WHERE id = ?", entry.ID); err != nil {
			t.Fatalf("clear legacy parent fields: %v", err)
		}

		reader, err := NewSQLReader(db)
		if err != nil {
			t.Fatalf("NewSQLReader failed: %v", err)
		}
		parent, err := reader.GetInteractionParent(ctx, entry.ID)
		if err != nil {
			t.Fatalf("GetInteractionParent failed: %v", err)
		}
		if parent == nil || parent.UserPath != "" || parent.SessionID != "" {
			t.Fatalf("interaction parent = %#v, want empty legacy fields", parent)
		}
	})
}

func conversationPathIsolationFixture(base time.Time) []*LogEntry {
	return []*LogEntry{
		{ID: "tenant-a-1", Timestamp: base, SessionID: "shared-session", UserPath: "/tenants/a"},
		{ID: "tenant-a-2", Timestamp: base.Add(time.Second), SessionID: "shared-session", UserPath: "/tenants/a"},
		{ID: "tenant-b-secret", Timestamp: base.Add(2 * time.Second), SessionID: "shared-session", UserPath: "/tenants/b"},
		{ID: "tenant-a-child-secret", Timestamp: base.Add(3 * time.Second), SessionID: "shared-session", UserPath: "/tenants/a/child"},
		{ID: "root-1", Timestamp: base.Add(4 * time.Second), SessionID: "root-shared", UserPath: "/"},
		{ID: "root-legacy", Timestamp: base.Add(5 * time.Second), SessionID: "root-shared"},
		{ID: "root-child-secret", Timestamp: base.Add(6 * time.Second), SessionID: "root-shared", UserPath: "/tenants/a"},
	}
}

func assertConversationUserPathIsolation(t *testing.T, reader Reader) {
	t.Helper()
	ctx := context.Background()
	conversation, err := reader.GetConversation(ctx, "tenant-a-1", 10)
	if err != nil {
		t.Fatalf("GetConversation failed: %v", err)
	}
	if len(conversation.Entries) != 2 {
		t.Fatalf("conversation entries = %+v, want only tenant A", conversation.Entries)
	}
	for _, entry := range conversation.Entries {
		if entry.UserPath != "/tenants/a" {
			t.Fatalf("conversation leaked %q from %q", entry.ID, entry.UserPath)
		}
	}

	rootConversation, err := reader.GetConversation(ctx, "root-1", 10)
	if err != nil {
		t.Fatalf("root GetConversation failed: %v", err)
	}
	if len(rootConversation.Entries) != 2 || rootConversation.Entries[0].ID != "root-1" || rootConversation.Entries[1].ID != "root-legacy" {
		t.Fatalf("root conversation leaked child paths: %+v", rootConversation.Entries)
	}
}

func TestSQLReader_GetConversationScopesSessionToUserPath(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, err := newSQLStoreForTest(t, db, 0)
		if err != nil {
			t.Fatalf("failed to create store: %v", err)
		}
		defer store.Close()

		ctx := context.Background()
		if err := store.WriteBatch(ctx, conversationPathIsolationFixture(time.Now().UTC())); err != nil {
			t.Fatalf("WriteBatch failed: %v", err)
		}

		reader, err := NewSQLReader(db)
		if err != nil {
			t.Fatalf("failed to create reader: %v", err)
		}
		assertConversationUserPathIsolation(t, reader)
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
		entries := sessionThreadFixture(base)
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
		if result.Sessions[0].SessionID != "" || result.Sessions[0].Count != 1 || result.Sessions[0].TotalCount != 1 {
			t.Fatalf("singleton thread = %+v", result.Sessions[0])
		}

		threadA := result.Sessions[1]
		if threadA.SessionID != "sess-a" || threadA.Count != 2 || threadA.TotalCount != 2 {
			t.Fatalf("sess-a summary = %+v", threadA)
		}
		if threadA.Latest.ID != "a-2" {
			t.Fatalf("sess-a latest = %q, want a-2", threadA.Latest.ID)
		}
		if !threadA.FirstTimestamp.Equal(base.Add(-24*time.Hour)) || !threadA.LastTimestamp.Equal(base.Add(2*time.Minute)) {
			t.Fatalf("sess-a span = %v..%v", threadA.FirstTimestamp, threadA.LastTimestamp)
		}

		if result.Sessions[2].SessionID != "sess-b" {
			t.Fatalf("sessions[2] = %+v", result.Sessions[2])
		}

		// The thread head is a full list row, not just the columns the
		// grouping pass ranks on.
		assertGetSessionsHeadPayload(t, reader)
		assertGetSessionsPaging(t, reader)

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

// sessionThreadFixture is the shared grouped-view corpus: a two-entry session
// crossing a UTC day boundary, a one-entry session and a sessionless request.
// The thread head (a-2) carries list columns and a data payload so the readers'
// head re-read is checked.
func sessionThreadFixture(base time.Time) []*LogEntry {
	return []*LogEntry{
		{ID: "a-1", Timestamp: base.Add(-24 * time.Hour), Provider: "openai", SessionID: "sess-a", StatusCode: 200},
		{
			ID: "a-2", Timestamp: base.Add(2 * time.Minute), Provider: "openai",
			SessionID: "sess-a", StatusCode: 200, Path: "/v1/chat/completions",
			Data: &LogData{UserAgent: "probe/1.0"},
		},
		{ID: "b-1", Timestamp: base.Add(time.Minute), Provider: "anthropic", SessionID: "sess-b", StatusCode: 500},
		{ID: "solo", Timestamp: base.Add(3 * time.Minute), Provider: "openai", StatusCode: 200},
	}
}

// assertGetSessionsHeadPayload proves the grouped view returns complete
// entries. Both readers rank threads over a narrow projection (id + timestamp)
// and re-read the page's heads afterwards, so a broken re-read would surface
// here as a head stripped of everything the ranking pass did not carry.
func assertGetSessionsHeadPayload(t *testing.T, reader Reader) {
	t.Helper()
	result, err := reader.GetSessions(context.Background(), LogQueryParams{SessionID: "sess-a", Limit: 10})
	if err != nil {
		t.Fatalf("GetSessions() error = %v", err)
	}
	if len(result.Sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(result.Sessions))
	}
	head := result.Sessions[0].Latest
	if head.Provider != "openai" || head.Path != "/v1/chat/completions" || head.StatusCode != 200 {
		t.Fatalf("head lost list columns: %+v", head)
	}
	if head.Data == nil || head.Data.UserAgent != "probe/1.0" {
		t.Fatalf("head lost its data payload: %+v", head.Data)
	}
}

// assertGetSessionsPaging walks the thread list one page at a time: the window
// pass and the head re-read must agree on the slice, and total must stay the
// full thread count rather than the page size.
func assertGetSessionsPaging(t *testing.T, reader Reader) {
	t.Helper()
	var ids []string
	for offset := range 3 {
		result, err := reader.GetSessions(context.Background(), LogQueryParams{Limit: 1, Offset: offset})
		if err != nil {
			t.Fatalf("GetSessions(offset=%d) error = %v", offset, err)
		}
		if result.Total != 3 {
			t.Fatalf("offset %d: total = %d, want 3", offset, result.Total)
		}
		if len(result.Sessions) != 1 {
			t.Fatalf("offset %d: sessions = %d, want 1", offset, len(result.Sessions))
		}
		ids = append(ids, result.Sessions[0].Latest.ID)
	}
	if want := []string{"solo", "a-2", "b-1"}; !slices.Equal(ids, want) {
		t.Fatalf("paged heads = %v, want %v", ids, want)
	}
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
		wantTotal     int
		wantLatestID  string
	}{
		{
			name:          "status filter keeps only the thread with a 500",
			params:        LogQueryParams{StatusCode: &status, Limit: 10},
			wantSessionID: "sess-b",
			wantCount:     1,
			wantTotal:     1,
			wantLatestID:  "b-1",
		},
		{
			name:          "session filter narrows the grouped view to one thread",
			params:        LogQueryParams{SessionID: "sess-a", Limit: 10},
			wantSessionID: "sess-a",
			wantCount:     2,
			wantTotal:     2,
			wantLatestID:  "a-2",
		},
		{
			name: "date filter reports matching and complete session counts",
			params: LogQueryParams{
				QueryParams: QueryParams{StartDate: time.Date(2026, 7, 27, 0, 0, 0, 0, time.UTC), EndDate: time.Date(2026, 7, 27, 0, 0, 0, 0, time.UTC)},
				SessionID:   "sess-a",
				Limit:       10,
			},
			wantSessionID: "sess-a",
			wantCount:     1,
			wantTotal:     2,
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
			if got.SessionID != tt.wantSessionID || got.Count != tt.wantCount || got.TotalCount != tt.wantTotal || got.Latest.ID != tt.wantLatestID {
				t.Fatalf("thread = %+v, want session %q count %d total %d latest %q",
					got, tt.wantSessionID, tt.wantCount, tt.wantTotal, tt.wantLatestID)
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
