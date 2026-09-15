package auditlog

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
	"github.com/stretchr/testify/require"
)

func TestSQLStore_SessionIDRoundtripAndFilter(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, err := newSQLStoreForTest(t, db, 0)
		require.NoError(t, err)

		defer store.Close()

		ctx := context.Background()
		base := time.Now().UTC()
		entries := []*LogEntry{
			{ID: "s-1", Timestamp: base, Provider: "openai", SessionID: "sess-a"},
			{ID: "s-2", Timestamp: base.Add(time.Second), Provider: "openai", SessionID: "sess-a"},
			{ID: "s-3", Timestamp: base.Add(2 * time.Second), Provider: "openai", SessionID: "sess-b"},
			{ID: "s-4", Timestamp: base.Add(3 * time.Second), Provider: "openai"},
		}
		err = store.WriteBatch(ctx, entries)
		require.NoError(t, err)

		reader, err := NewSQLReader(db)
		require.NoError(t, err)

		all, err := reader.GetLogs(ctx, LogQueryParams{Limit: 10})
		require.NoError(t, err)

		bySession := make(map[string]string, len(all.Entries))
		for _, entry := range all.Entries {
			bySession[entry.ID] = entry.SessionID
		}
		require.Equal(t, "sess-a", bySession["s-1"])
		require.Equal(t, "sess-b", bySession["s-3"])
		require.Empty(t, bySession["s-4"], "session ids not round-tripped: %#v", bySession)

		filtered, err := reader.GetLogs(ctx, LogQueryParams{SessionID: "sess-a", Limit: 10})
		require.NoError(t, err)
		require.Equal(t, 2, filtered.Total)
		require.Len(t, filtered.Entries, 2)

		for _, entry := range filtered.Entries {
			require.Equal(t, "sess-a", entry.SessionID, "filter leaked entry %q with session %q", entry.ID, entry.SessionID)
		}

		conversation, err := reader.GetConversation(ctx, "s-1", 10)
		require.NoError(t, err)
		require.Equal(t, "s-1", conversation.AnchorID)
		require.Len(t, conversation.Entries, 2, "conversation = %+v, want the two-entry sess-a thread", conversation)
		require.Equal(t, "s-1", conversation.Entries[0].ID)
		require.Equal(t, "s-2", conversation.Entries[1].ID)
	})
}

func TestSQLReader_GetConversationUsesKeysetPagination(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, err := newSQLStoreForTest(t, db, 0)
		require.NoError(t, err)

		defer store.Close()

		ctx := context.Background()
		reader, err := NewSQLReader(db)
		require.NoError(t, err)

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
				err := store.WriteBatch(ctx, entries)
				require.NoError(t, err)

				conversation, err := reader.GetConversation(ctx, entries[0].ID, 120)
				require.NoError(t, err)
				require.Len(t, conversation.Entries, 120)
				require.Equal(t, tc.truncated, conversation.Truncated)

				seen := make(map[string]struct{}, len(conversation.Entries))
				for i, entry := range conversation.Entries {
					wantID := fmt.Sprintf("%s%03d", prefix, i)
					require.Equal(t, wantID, entry.ID, "entry %d", i)
					_, exists := seen[entry.ID]
					require.False(t, exists, "duplicate entry %q", entry.ID)

					seen[entry.ID] = struct{}{}
				}
			})
		}
	})
}

func TestSQLReader_GetInteractionParentAllowsLegacyNulls(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, err := newSQLStoreForTest(t, db, 0)
		require.NoError(t, err)

		defer store.Close()

		ctx := context.Background()
		entry := &LogEntry{ID: "legacy-parent", Timestamp: time.Now().UTC(), UserPath: "/"}
		err = store.WriteBatch(ctx, []*LogEntry{entry})
		require.NoError(t, err)
		_, err = db.Exec(ctx,
			"UPDATE audit_logs SET user_path = NULL, session_id = NULL WHERE id = ?", entry.ID)
		require.NoError(t, err)

		reader, err := NewSQLReader(db)
		require.NoError(t, err)

		parent, err := reader.GetInteractionParent(ctx, entry.ID)
		require.NoError(t, err)
		require.NotNil(t, parent)
		require.Empty(t, parent.UserPath)
		require.Empty(t, parent.SessionID)
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
	require.NoError(t, err)
	require.Len(t, conversation.Entries, 2)

	for _, entry := range conversation.Entries {
		require.Equal(t, "/tenants/a", entry.UserPath, "conversation leaked %q from %q", entry.ID, entry.UserPath)
	}

	rootConversation, err := reader.GetConversation(ctx, "root-1", 10)
	require.NoError(t, err)
	require.Len(t, rootConversation.Entries, 2)
	require.Equal(t, "root-1", rootConversation.Entries[0].ID)
	require.Equal(t, "root-legacy", rootConversation.Entries[1].ID)
}

func TestSQLReader_GetConversationScopesSessionToUserPath(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, err := newSQLStoreForTest(t, db, 0)
		require.NoError(t, err)

		defer store.Close()

		ctx := context.Background()
		err = store.WriteBatch(ctx, conversationPathIsolationFixture(time.Now().UTC()))
		require.NoError(t, err)

		reader, err := NewSQLReader(db)
		require.NoError(t, err)

		assertConversationUserPathIsolation(t, reader)
	})
}

func TestSQLReader_GetSessions(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, err := newSQLStoreForTest(t, db, 0)
		require.NoError(t, err)

		defer store.Close()

		ctx := context.Background()
		base := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
		entries := sessionThreadFixture(base)
		err = store.WriteBatch(ctx, entries)
		require.NoError(t, err)

		reader, err := NewSQLReader(db)
		require.NoError(t, err)

		result, err := reader.GetSessions(ctx, LogQueryParams{Limit: 10})
		require.NoError(t, err)
		require.Equal(t, 3, result.Total)
		require.Len(t, result.Sessions, 3)
		// Ordered by latest activity: solo (10:03), sess-a (10:02), sess-b (10:01).
		require.Equal(t, "solo", result.Sessions[0].Latest.ID)
		require.Empty(t, result.Sessions[0].SessionID)
		require.Equal(t, 1, result.Sessions[0].RequestCount, "singleton thread = %+v", result.Sessions[0])

		threadA := result.Sessions[1]
		require.Equal(t, "sess-a", threadA.SessionID)
		require.Equal(t, 2, threadA.RequestCount, "sess-a summary = %+v", threadA)
		require.Equal(t, "a-2", threadA.Latest.ID)
		require.Equal(t, "sess-b", result.Sessions[2].SessionID, "sessions[2] = %+v", result.Sessions[2])

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
		err := db.Schema(ctx, `
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
			)`)
		require.NoError(t, err)

		store, err := newSQLStoreForTest(t, db, 0)
		require.NoError(t, err)

		defer store.Close()

		base := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
		entries := []*LogEntry{
			{ID: "b32d7a52-0000-4000-8000-000000000001", Timestamp: base, Provider: "openai", SessionID: "sess-a", StatusCode: 200},
			{ID: "b32d7a52-0000-4000-8000-000000000002", Timestamp: base.Add(time.Minute), Provider: "openai", SessionID: "sess-a", StatusCode: 200},
			{ID: "b32d7a52-0000-4000-8000-000000000003", Timestamp: base.Add(2 * time.Minute), Provider: "openai", StatusCode: 200},
		}
		err = store.WriteBatch(ctx, entries)
		require.NoError(t, err)

		reader, err := NewSQLReader(db)
		require.NoError(t, err)

		result, err := reader.GetSessions(ctx, LogQueryParams{Limit: 10})
		require.NoError(t, err)
		require.Equal(t, 2, result.Total)
		require.Len(t, result.Sessions, 2)

		want := []struct {
			latestID     string
			sessionID    string
			requestCount int
		}{
			{"b32d7a52-0000-4000-8000-000000000003", "", 1},
			{"b32d7a52-0000-4000-8000-000000000002", "sess-a", 2},
		}
		for i, tt := range want {
			got := result.Sessions[i]
			require.Equal(t, tt.latestID, got.Latest.ID)
			require.Equal(t, tt.sessionID, got.SessionID)
			require.Equal(t, tt.requestCount, got.RequestCount, "sessions[%d] = %+v, want latest %q session %q count %d", i, got, tt.latestID, tt.sessionID, tt.requestCount)
		}
	})
}

// sessionThreadFixture is the shared grouped-view corpus: a two-entry session
// crossing a UTC day boundary, a one-entry session and a sessionless request.
// The thread head (a-2) carries list columns and a data payload so the readers'
// head re-read is checked.
func sessionThreadFixture(base time.Time) []*LogEntry {
	return []*LogEntry{
		{ID: "a-1", Timestamp: base.Add(-24 * time.Hour), Provider: "openai", SessionID: "sess-a", UserPath: "/tenants/a", StatusCode: 200},
		{
			ID: "a-2", Timestamp: base.Add(2 * time.Minute), Provider: "openai",
			SessionID: "sess-a", UserPath: "/tenants/b", StatusCode: 200, Path: "/v1/chat/completions",
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
	require.NoError(t, err)
	require.Len(t, result.Sessions, 1)

	head := result.Sessions[0].Latest
	require.Equal(t, "openai", head.Provider)
	require.Equal(t, "/v1/chat/completions", head.Path)
	require.Equal(t, 200, head.StatusCode, "head lost list columns: %+v", head)
	require.NotNil(t, head.Data)
	require.Equal(t, "probe/1.0", head.Data.UserAgent)
}

// assertGetSessionsPaging walks the thread list one page at a time: the window
// pass and the head re-read must agree on the slice, and total must stay the
// full thread count rather than the page size.
func assertGetSessionsPaging(t *testing.T, reader Reader) {
	t.Helper()
	var ids []string
	for offset := range 3 {
		result, err := reader.GetSessions(context.Background(), LogQueryParams{Limit: 1, Offset: offset})
		require.NoError(t, err)
		require.Equal(t, 3, result.Total, "offset %d", offset)
		require.Len(t, result.Sessions, 1)

		ids = append(ids, result.Sessions[0].Latest.ID)
	}
	require.Equal(t, []string{"solo", "a-2", "b-1"}, ids)
}

// assertGetSessionsFilters runs the shared filtered-grouping cases against a
// reader, so both backends prove filters apply to entries before grouping.
func assertGetSessionsFilters(t *testing.T, reader Reader) {
	t.Helper()
	status := 500
	tests := []struct {
		name             string
		params           LogQueryParams
		wantSessionID    string
		wantRequestCount int
		wantLatestID     string
	}{
		{
			name:             "status filter keeps only the thread with a 500",
			params:           LogQueryParams{StatusCode: &status, Limit: 10},
			wantSessionID:    "sess-b",
			wantRequestCount: 1,
			wantLatestID:     "b-1",
		},
		{
			name:             "session filter narrows the grouped view to one thread",
			params:           LogQueryParams{SessionID: "sess-a", Limit: 10},
			wantSessionID:    "sess-a",
			wantRequestCount: 2,
			wantLatestID:     "a-2",
		},
		{
			name: "user path filter keeps the complete session count",
			params: LogQueryParams{
				UserPath: "/tenants/b", ExactUserPath: true, SessionID: "sess-a", Limit: 10,
			},
			wantSessionID:    "sess-a",
			wantRequestCount: 2,
			wantLatestID:     "a-2",
		},
		{
			name: "date filter keeps the complete session count",
			params: LogQueryParams{
				QueryParams: QueryParams{StartDate: time.Date(2026, 7, 27, 0, 0, 0, 0, time.UTC), EndDate: time.Date(2026, 7, 27, 0, 0, 0, 0, time.UTC)},
				SessionID:   "sess-a",
				Limit:       10,
			},
			wantSessionID:    "sess-a",
			wantRequestCount: 2,
			wantLatestID:     "a-2",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := reader.GetSessions(context.Background(), tt.params)
			require.NoError(t, err)
			require.Equal(t, 1, result.Total)
			require.Len(t, result.Sessions, 1, "result = %+v, want exactly one thread", result)

			got := result.Sessions[0]
			require.Equal(t, tt.wantSessionID, got.SessionID)
			require.Equal(t, tt.wantRequestCount, got.RequestCount)
			require.Equal(t, tt.wantLatestID, got.Latest.ID, "thread = %+v, want session %q request count %d latest %q", got, tt.wantSessionID, tt.wantRequestCount, tt.wantLatestID)
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
	require.NotNil(t, streamEntry)
	require.Equal(t, "sess-42", streamEntry.SessionID)
}

// The stream copy is created mid-handler, before the audit middleware's
// post-handler enrichment stamps the session id onto the base entry — so
// CreateStreamEntry must capture it from the request context itself, or every
// streamed request loses its session id.
func TestCreateStreamEntryCapturesSessionIDFromContext(t *testing.T) {
	ctx := core.WithSessionID(context.Background(), "sess-ctx")
	streamEntry := CreateStreamEntry(ctx, &LogEntry{ID: "entry-1", Path: "/v1/chat/completions"})
	require.NotNil(t, streamEntry)
	require.Equal(t, "sess-ctx", streamEntry.SessionID)
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
	require.NotNil(t, streamEntry)
	require.NotNil(t, streamEntry.Data)
	require.Equal(t, "sess-ctx", streamEntry.SessionID)
	require.Equal(t, "key-1", streamEntry.AuthKeyID)
	require.Len(t, streamEntry.Data.Labels, 2)
	require.Equal(t, "team-a", streamEntry.Data.Labels[0])
}

// The page query yields the thread total on every row, so an empty page has
// to find it another way: zero when the window holds no threads, the real
// count when the offset merely ran past the last page.
func TestSQLReader_GetSessionsTotalOnEmptyPage(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, err := newSQLStoreForTest(t, db, 0)
		require.NoError(t, err)

		defer store.Close()
		reader, err := NewSQLReader(db)
		require.NoError(t, err)

		ctx := context.Background()

		empty, err := reader.GetSessions(ctx, LogQueryParams{Limit: 10})
		require.NoError(t, err)
		require.Equal(t, 0, empty.Total)
		require.Empty(t, empty.Sessions)

		base := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
		err = store.WriteBatch(ctx, sessionThreadFixture(base))
		require.NoError(t, err)

		past, err := reader.GetSessions(ctx, LogQueryParams{Limit: 10, Offset: 50})
		require.NoError(t, err)
		require.Equal(t, 3, past.Total)
		require.Empty(t, past.Sessions)
	})
}
