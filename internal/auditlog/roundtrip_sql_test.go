package auditlog

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newSQLStoreForTest(t *testing.T, db sqlx.DB, retentionDays int) (*SQLStore, error) {
	t.Helper()
	return NewSQLStore(context.Background(), db, retentionDays)
}

func TestSQLStore_WriteBatch_NullDataPreservation(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, err := newSQLStoreForTest(t, db, 0)
		require.NoError(t, err)

		defer store.Close()

		ctx := context.Background()

		// Create entries - one with nil Data, one with Data
		entries := []*LogEntry{
			{
				ID:             "entry-nil-data",
				Timestamp:      time.Now(),
				RequestedModel: "gpt-4",
				Provider:       "openai",
				Data:           nil, // This should become SQL NULL
			},
			{
				ID:             "entry-with-data",
				Timestamp:      time.Now(),
				RequestedModel: "gpt-4",
				Provider:       "openai",
				Data: &LogData{
					UserAgent: "test-agent",
				},
			},
		}
		// Write entries
		err = store.WriteBatch(ctx, entries)
		require.NoError(t, err)

		// Query to check NULL vs non-NULL
		rows, err := db.Query(ctx, "SELECT id, data, data IS NULL as is_null FROM audit_logs ORDER BY id")
		require.NoError(t, err)

		defer rows.Close()

		results := make(map[string]bool) // id -> isNull
		for rows.Next() {
			var id string
			var data *string
			var isNull bool
			err := rows.Scan(&id, &data, &isNull)
			require.NoError(t, err)

			results[id] = isNull
		}

		assert.True(t, results["entry-nil-data"], "nil Data must be stored as SQL NULL")
		assert.False(t, results["entry-with-data"], "non-nil Data must not be stored as SQL NULL")
	})
}

func TestSQLStore_WriteBatch_EmptyEntries(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, err := newSQLStoreForTest(t, db, 0)
		require.NoError(t, err)

		defer store.Close()

		ctx := context.Background()
		// Empty slice should not error
		err = store.WriteBatch(ctx, []*LogEntry{})
		require.NoError(t, err)

		// Verify no entries in database
		var count int
		err = db.QueryRow(ctx, "SELECT COUNT(*) FROM audit_logs").Scan(&count)
		require.NoError(t, err)
		assert.Equal(t, 0, count)
	})
}

func TestSQLStore_WriteBatch_ExactBatchBoundary(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, err := newSQLStoreForTest(t, db, 0)
		require.NoError(t, err)

		defer store.Close()

		ctx := context.Background()

		// Test with exactly maxEntriesPerBatch entries
		numEntries := maxEntriesPerBatch
		entries := make([]*LogEntry, numEntries)
		for i := range numEntries {
			entries[i] = &LogEntry{
				ID:             fmt.Sprintf("exact-%03d", i),
				Timestamp:      time.Now(),
				RequestedModel: "gpt-4",
			}
		}
		err = store.WriteBatch(ctx, entries)
		require.NoError(t, err)

		var count int
		err = db.QueryRow(ctx, "SELECT COUNT(*) FROM audit_logs").Scan(&count)
		require.NoError(t, err)
		assert.Equal(t, numEntries, count)

		// Test with maxEntriesPerBatch + 1 entries - should require 2 batches
		entries = make([]*LogEntry, maxEntriesPerBatch+1)
		for i := 0; i <= maxEntriesPerBatch; i++ {
			entries[i] = &LogEntry{
				ID:             fmt.Sprintf("boundary-%03d", i),
				Timestamp:      time.Now(),
				RequestedModel: "gpt-4",
			}
		}
		err = store.WriteBatch(ctx, entries)
		require.NoError(t, err)
		err = db.QueryRow(ctx, "SELECT COUNT(*) FROM audit_logs").Scan(&count)
		require.NoError(t, err)

		expectedTotal := numEntries + maxEntriesPerBatch + 1
		assert.Equal(t, expectedTotal, count)
	})
}

func TestSQLStore_WriteBatch_PersistsAliasFields(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, err := newSQLStoreForTest(t, db, 0)
		require.NoError(t, err)

		defer store.Close()

		ctx := context.Background()
		entry := &LogEntry{
			ID:             "alias-entry",
			Timestamp:      time.Now(),
			RequestedModel: "anthropic/claude-opus-4-6",
			ResolvedModel:  "openai/gpt-5-nano",
			Provider:       "openai",
			AliasUsed:      true,
			StatusCode:     200,
		}
		err = store.WriteBatch(ctx, []*LogEntry{entry})
		require.NoError(t, err)

		reader, err := NewSQLReader(db)
		require.NoError(t, err)

		logEntry, err := reader.GetLogByID(ctx, entry.ID)
		require.NoError(t, err)

		require.NotNil(t, logEntry)
		require.Equal(t, entry.RequestedModel, logEntry.RequestedModel)
		require.Equal(t, entry.ResolvedModel, logEntry.ResolvedModel)
		require.Equal(t, entry.Provider, logEntry.Provider)
		require.True(t, logEntry.AliasUsed)
		require.Equal(t, "/", logEntry.UserPath)
	})
}

func TestSQLStore_WriteBatch_PersistsProviderAttempts(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, err := newSQLStoreForTest(t, db, 0)
		require.NoError(t, err)

		defer store.Close()

		ctx := context.Background()
		entry := &LogEntry{
			ID:             "attempt-entry",
			Timestamp:      time.Now(),
			RequestedModel: "anthropic/claude-fable-5",
			ResolvedModel:  "openai/gpt-5.5",
			Provider:       "openai",
			StatusCode:     200,
			Data: &LogData{
				Failover: &FailoverSnapshot{TargetModel: "openai/gpt-5.5"},
				Attempts: []AttemptSnapshot{
					{
						Seq:          1,
						Kind:         AttemptKindPrimary,
						ProviderType: "anthropic",
						Model:        "anthropic/claude-fable-5",
						StatusCode:   404,
						ErrorType:    "not_found_error",
						ErrorCode:    "model_not_found",
						ErrorMessage: "model is not available",
						ResponseBody: map[string]any{
							"error": map[string]any{"message": "model is not available", "code": "model_not_found"},
						},
						ResponseHeaders: map[string]string{"X-Request-Id": "req-123", "Retry-After": "30"},
					},
					{
						Seq:          2,
						Kind:         AttemptKindFailover,
						ProviderType: "openai",
						Model:        "openai/gpt-5.5",
						StatusCode:   200,
						Success:      true,
					},
				},
			},
		}
		err = store.WriteBatch(ctx, []*LogEntry{entry})
		require.NoError(t, err)

		var attemptRows int
		err = db.QueryRow(ctx, "SELECT COUNT(*) FROM audit_log_attempts WHERE audit_log_id = ?", entry.ID).Scan(&attemptRows)
		require.NoError(t, err)
		require.Equal(t, 2, attemptRows)

		reader, err := NewSQLReader(db)
		require.NoError(t, err)

		got, err := reader.GetLogByID(ctx, entry.ID)
		require.NoError(t, err)
		require.NotNil(t, got)
		require.NotNil(t, got.Data)
		require.Len(t, got.Data.Attempts, 2)
		require.Equal(t, AttemptKindPrimary, got.Data.Attempts[0].Kind)
		require.Equal(t, 404, got.Data.Attempts[0].StatusCode)
		require.Equal(t, AttemptKindFailover, got.Data.Attempts[1].Kind)
		require.True(t, got.Data.Attempts[1].Success)

		primary := got.Data.Attempts[0]
		body, ok := primary.ResponseBody.(map[string]any)
		require.True(t, ok, "primary response body type = %T, want map", primary.ResponseBody)

		errObj, ok := body["error"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "model_not_found", errObj["code"])
		require.Equal(t, "req-123", primary.ResponseHeaders["X-Request-Id"])
		require.Equal(t, "30", primary.ResponseHeaders["Retry-After"])
		require.Nil(t, got.Data.Attempts[1].ResponseBody)
		require.Nil(t, got.Data.Attempts[1].ResponseHeaders, "successful attempt must not carry a captured error body/headers")

		conversation, err := reader.GetConversation(ctx, entry.ID, 40)
		require.NoError(t, err)
		require.Len(t, conversation.Entries, 1)
		require.NotNil(t, conversation.Entries[0].Data)
		require.Empty(t, conversation.Entries[0].Data.Attempts)

		parent, err := reader.GetInteractionParent(ctx, entry.ID)
		require.NoError(t, err)
		require.NotNil(t, parent)
		require.Equal(t, "/", parent.UserPath)
		require.Empty(t, parent.SessionID)
	})
}

func TestSQLReader_AllowsNullWorkflowVersionIDAndErrorType(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()

		store, err := newSQLStoreForTest(t, db, 0)
		require.NoError(t, err)

		defer store.Close()

		now := db.Dialect().TimestampArg(time.Now())
		_, err = db.Exec(ctx, `
			INSERT INTO audit_logs (
				id, timestamp, duration_ns, requested_model, resolved_model, provider, alias_used, workflow_version_id,
				status_code, request_id, client_ip, method, path, stream, error_type, data
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`,
			"null-workflow-version",
			now,
			0,
			"gpt-4",
			"",
			"openai",
			false,
			nil,
			200,
			"req-1",
			"127.0.0.1",
			"POST",
			"/v1/chat/completions",
			false,
			nil,
			nil,
		)
		require.NoError(t, err)

		reader, err := NewSQLReader(db)
		require.NoError(t, err)

		entry, err := reader.GetLogByID(context.Background(), "null-workflow-version")
		require.NoError(t, err)

		require.NotNil(t, entry)
		require.Empty(t, entry.WorkflowVersionID)
		require.Empty(t, entry.ErrorType)

		logs, err := reader.GetLogs(context.Background(), LogQueryParams{Limit: 10})
		require.NoError(t, err)
		require.Len(t, logs.Entries, 1)
		require.Empty(t, logs.Entries[0].WorkflowVersionID)
		require.Empty(t, logs.Entries[0].ErrorType)
	})
}

func TestSQLReader_GetLogsFiltersByUserPathSubtree(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()

		store, err := newSQLStoreForTest(t, db, 0)
		require.NoError(t, err)

		defer store.Close()

		now := db.Dialect().TimestampArg(time.Now())
		_, err = db.Exec(ctx, `
			INSERT INTO audit_logs (
				id, timestamp, duration_ns, requested_model, resolved_model, provider, alias_used, workflow_version_id,
				status_code, request_id, client_ip, method, path, user_path, stream, error_type, data
			) VALUES
				(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?),
				(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`,
			"match-team",
			now,
			0,
			"gpt-4",
			"",
			"openai",
			false,
			nil,
			200,
			"req-1",
			"127.0.0.1",
			"POST",
			"/v1/chat/completions",
			"/team/a",
			false,
			"",
			nil,
			"miss-other",
			now,
			0,
			"gpt-4",
			"",
			"openai",
			false,
			nil,
			200,
			"req-2",
			"127.0.0.1",
			"POST",
			"/v1/chat/completions",
			"/other",
			false,
			"",
			nil,
		)
		require.NoError(t, err)

		reader, err := NewSQLReader(db)
		require.NoError(t, err)

		logs, err := reader.GetLogs(context.Background(), LogQueryParams{UserPath: "/team", Limit: 10})
		require.NoError(t, err)
		require.Len(t, logs.Entries, 1)
		require.Equal(t, "match-team", logs.Entries[0].ID)
		require.Equal(t, "/team/a", logs.Entries[0].UserPath)
	})
}

func TestSQLReader_GetLogsRootUserPathIncludesLegacyNullRows(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()

		store, err := newSQLStoreForTest(t, db, 0)
		require.NoError(t, err)

		defer store.Close()

		now := db.Dialect().TimestampArg(time.Now())
		_, err = db.Exec(ctx, `
			INSERT INTO audit_logs (
				id, timestamp, duration_ns, requested_model, resolved_model, provider, alias_used, workflow_version_id,
				status_code, request_id, client_ip, method, path, user_path, stream, error_type, data
			) VALUES
				(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?),
				(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`,
			"legacy-null",
			now,
			0,
			"gpt-4",
			"",
			"openai",
			false,
			nil,
			200,
			"req-legacy",
			"127.0.0.1",
			"POST",
			"/v1/chat/completions",
			nil,
			false,
			"",
			nil,
			"root-explicit",
			now,
			0,
			"gpt-4",
			"",
			"openai",
			false,
			nil,
			200,
			"req-root",
			"127.0.0.1",
			"POST",
			"/v1/chat/completions",
			"/",
			false,
			"",
			nil,
		)
		require.NoError(t, err)

		reader, err := NewSQLReader(db)
		require.NoError(t, err)

		logs, err := reader.GetLogs(context.Background(), LogQueryParams{UserPath: "/", Limit: 10})
		require.NoError(t, err)
		require.Len(t, logs.Entries, 2)
	})
}

func TestSQLStoreAndReader_PreserveCacheType(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, err := newSQLStoreForTest(t, db, 0)
		require.NoError(t, err)

		defer store.Close()

		ctx := context.Background()
		now := time.Now()
		err = store.WriteBatch(ctx, []*LogEntry{
			{
				ID:             "cache-exact",
				Timestamp:      now,
				RequestedModel: "gpt-4",
				Provider:       "openai",
				CacheType:      CacheTypeExact,
			},
			{
				ID:             "cache-none",
				Timestamp:      now.Add(time.Second),
				RequestedModel: "gpt-4",
				Provider:       "openai",
			},
		})
		require.NoError(t, err)

		var exactCacheType *string
		err = db.QueryRow(ctx, "SELECT cache_type FROM audit_logs WHERE id = ?", "cache-exact").Scan(&exactCacheType)
		require.NoError(t, err)
		require.NotNil(t, exactCacheType)
		require.Equal(t, CacheTypeExact, *exactCacheType)

		var noneCacheType *string
		err = db.QueryRow(ctx, "SELECT cache_type FROM audit_logs WHERE id = ?", "cache-none").Scan(&noneCacheType)
		require.NoError(t, err)
		require.Nil(t, noneCacheType)

		reader, err := NewSQLReader(db)
		require.NoError(t, err)

		exactEntry, err := reader.GetLogByID(ctx, "cache-exact")
		require.NoError(t, err)
		require.NotNil(t, exactEntry)
		require.Equal(t, CacheTypeExact, exactEntry.CacheType)

		noneEntry, err := reader.GetLogByID(ctx, "cache-none")
		require.NoError(t, err)
		require.NotNil(t, noneEntry)
		require.Empty(t, noneEntry.CacheType)
	})
}

func TestSQLReader_GetLogsUserPathSubtreeIsSegmentExact(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		store, err := newSQLStoreForTest(t, db, 0)
		require.NoError(t, err)

		defer store.Close()

		now := time.Now()
		entries := []*LogEntry{
			{ID: "self", Timestamp: now, UserPath: "/team"},
			{ID: "child", Timestamp: now, UserPath: "/team/a"},
			{ID: "grandchild", Timestamp: now, UserPath: "/team/a/b"},
			// Share the byte prefix but are not descendants: "-" sorts before "/"
			// and "0" is the first byte after it, so both straddle the range bounds.
			{ID: "prefix-sibling", Timestamp: now, UserPath: "/team-x"},
			{ID: "prefix-sibling-child", Timestamp: now, UserPath: "/team0/a"},
			// User paths are case-preserving, and the filter compares bytes.
			{ID: "other-case", Timestamp: now, UserPath: "/Team/a"},
		}
		err = store.WriteBatch(ctx, entries)
		require.NoError(t, err)

		reader, err := NewSQLReader(db)
		require.NoError(t, err)

		ids := func(params LogQueryParams) map[string]bool {
			t.Helper()
			logs, err := reader.GetLogs(ctx, params)
			require.NoError(t, err, "GetLogs(%+v)", params)

			got := make(map[string]bool, len(logs.Entries))
			for _, entry := range logs.Entries {
				got[entry.ID] = true
			}
			require.Equal(t, len(got), logs.Total)

			return got
		}

		subtree := ids(LogQueryParams{UserPath: "/team", Limit: 10})
		require.Len(t, subtree, 3)
		require.True(t, subtree["self"])
		require.True(t, subtree["child"])
		require.True(t, subtree["grandchild"])

		exact := ids(LogQueryParams{UserPath: "/team", ExactUserPath: true, Limit: 10})
		require.Len(t, exact, 1)
		require.True(t, exact["self"])
	})
}
