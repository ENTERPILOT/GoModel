package auditlog

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// createTestDB creates an in-memory SQLite database for testing.
func createTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}
	return db
}

func TestSQLiteStore_WriteBatch_NullDataPreservation(t *testing.T) {
	db := createTestDB(t)
	defer db.Close()

	store, err := NewSQLiteStore(db, 0)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
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
	if err := store.WriteBatch(ctx, entries); err != nil {
		t.Fatalf("WriteBatch failed: %v", err)
	}

	// Query to check NULL vs non-NULL
	rows, err := db.Query("SELECT id, data, data IS NULL as is_null FROM audit_logs ORDER BY id")
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	defer rows.Close()

	results := make(map[string]bool) // id -> isNull
	for rows.Next() {
		var id string
		var data sql.NullString
		var isNull bool
		if err := rows.Scan(&id, &data, &isNull); err != nil {
			t.Fatalf("scan failed: %v", err)
		}
		results[id] = isNull
	}

	// Verify entry with nil Data has NULL in database
	if !results["entry-nil-data"] {
		t.Error("entry with nil Data should have NULL in database, got non-NULL")
	}

	// Verify entry with Data has non-NULL in database
	if results["entry-with-data"] {
		t.Error("entry with Data should have non-NULL in database, got NULL")
	}
}

func TestSQLiteStore_WriteBatch_Chunking(t *testing.T) {
	db := createTestDB(t)
	defer db.Close()

	store, err := NewSQLiteStore(db, 0)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	// Create more entries than can fit in a single batch (>62 entries)
	// Using 150 entries to ensure we need at least 3 batches
	numEntries := 150
	entries := make([]*LogEntry, numEntries)
	for i := range numEntries {
		entries[i] = &LogEntry{
			ID:             fmt.Sprintf("entry-%03d", i),
			Timestamp:      time.Now(),
			RequestedModel: "gpt-4",
			Provider:       "openai",
			StatusCode:     200,
		}
	}

	// Write all entries - this should internally chunk into multiple batches
	if err := store.WriteBatch(ctx, entries); err != nil {
		t.Fatalf("WriteBatch failed: %v", err)
	}

	// Verify all entries were persisted
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM audit_logs").Scan(&count); err != nil {
		t.Fatalf("count query failed: %v", err)
	}

	if count != numEntries {
		t.Errorf("expected %d entries, got %d", numEntries, count)
	}

	// Verify entries are actually in the database by sampling a few
	for _, id := range []string{"entry-000", "entry-062", "entry-124", "entry-149"} {
		var exists bool
		err := db.QueryRow("SELECT 1 FROM audit_logs WHERE id = ?", id).Scan(&exists)
		if err == sql.ErrNoRows {
			t.Errorf("entry %s not found in database", id)
		} else if err != nil {
			t.Fatalf("query for %s failed: %v", id, err)
		}
	}
}

func TestSQLiteStore_WriteBatch_EmptyEntries(t *testing.T) {
	db := createTestDB(t)
	defer db.Close()

	store, err := NewSQLiteStore(db, 0)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	// Empty slice should not error
	if err := store.WriteBatch(ctx, []*LogEntry{}); err != nil {
		t.Fatalf("WriteBatch with empty entries failed: %v", err)
	}

	// Verify no entries in database
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM audit_logs").Scan(&count); err != nil {
		t.Fatalf("count query failed: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 entries, got %d", count)
	}
}

func TestSQLiteStore_WriteBatch_ExactBatchBoundary(t *testing.T) {
	db := createTestDB(t)
	defer db.Close()

	store, err := NewSQLiteStore(db, 0)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
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

	if err := store.WriteBatch(ctx, entries); err != nil {
		t.Fatalf("WriteBatch failed: %v", err)
	}

	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM audit_logs").Scan(&count); err != nil {
		t.Fatalf("count query failed: %v", err)
	}
	if count != numEntries {
		t.Errorf("expected %d entries, got %d", numEntries, count)
	}

	// Test with maxEntriesPerBatch + 1 entries - should require 2 batches
	entries = make([]*LogEntry, maxEntriesPerBatch+1)
	for i := 0; i <= maxEntriesPerBatch; i++ {
		entries[i] = &LogEntry{
			ID:             fmt.Sprintf("boundary-%03d", i),
			Timestamp:      time.Now(),
			RequestedModel: "gpt-4",
		}
	}

	if err := store.WriteBatch(ctx, entries); err != nil {
		t.Fatalf("WriteBatch failed at boundary: %v", err)
	}

	if err := db.QueryRow("SELECT COUNT(*) FROM audit_logs").Scan(&count); err != nil {
		t.Fatalf("count query failed: %v", err)
	}
	expectedTotal := numEntries + maxEntriesPerBatch + 1
	if count != expectedTotal {
		t.Errorf("expected %d entries, got %d", expectedTotal, count)
	}
}

func TestSQLiteStore_WriteBatch_PersistsAliasFields(t *testing.T) {
	db := createTestDB(t)
	defer db.Close()

	store, err := NewSQLiteStore(db, 0)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
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

	if err := store.WriteBatch(ctx, []*LogEntry{entry}); err != nil {
		t.Fatalf("WriteBatch failed: %v", err)
	}

	reader, err := NewSQLiteReader(db)
	if err != nil {
		t.Fatalf("failed to create reader: %v", err)
	}

	logEntry, err := reader.GetLogByID(ctx, entry.ID)
	if err != nil {
		t.Fatalf("GetLogByID failed: %v", err)
	}
	if logEntry == nil {
		t.Fatal("expected log entry, got nil")
		return
	}
	if logEntry.RequestedModel != entry.RequestedModel {
		t.Fatalf("RequestedModel = %q, want %q", logEntry.RequestedModel, entry.RequestedModel)
	}
	if logEntry.ResolvedModel != entry.ResolvedModel {
		t.Fatalf("ResolvedModel = %q, want %q", logEntry.ResolvedModel, entry.ResolvedModel)
	}
	if logEntry.Provider != entry.Provider {
		t.Fatalf("Provider = %q, want %q", logEntry.Provider, entry.Provider)
	}
	if !logEntry.AliasUsed {
		t.Fatal("AliasUsed = false, want true")
	}
	if logEntry.UserPath != "/" {
		t.Fatalf("UserPath = %q, want /", logEntry.UserPath)
	}
}

func TestSQLiteStore_WriteBatch_PersistsProviderAttempts(t *testing.T) {
	db := createTestDB(t)
	db.SetMaxOpenConns(1)
	defer db.Close()

	store, err := NewSQLiteStore(db, 0)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
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

	if err := store.WriteBatch(ctx, []*LogEntry{entry}); err != nil {
		t.Fatalf("WriteBatch failed: %v", err)
	}

	var attemptRows int
	if err := db.QueryRow("SELECT COUNT(*) FROM audit_log_attempts WHERE audit_log_id = ?", entry.ID).Scan(&attemptRows); err != nil {
		t.Fatalf("count audit_log_attempts failed: %v", err)
	}
	if attemptRows != 2 {
		t.Fatalf("attempt rows = %d, want 2", attemptRows)
	}

	reader, err := NewSQLiteReader(db)
	if err != nil {
		t.Fatalf("failed to create reader: %v", err)
	}
	got, err := reader.GetLogByID(ctx, entry.ID)
	if err != nil {
		t.Fatalf("GetLogByID failed: %v", err)
	}
	if got == nil || got.Data == nil {
		t.Fatalf("entry data = %#v, want populated", got)
	}
	if len(got.Data.Attempts) != 2 {
		t.Fatalf("hydrated attempts = %#v, want 2", got.Data.Attempts)
	}
	if got.Data.Attempts[0].Kind != AttemptKindPrimary || got.Data.Attempts[0].StatusCode != 404 {
		t.Fatalf("primary attempt = %#v, want failed 404 primary", got.Data.Attempts[0])
	}
	if got.Data.Attempts[1].Kind != AttemptKindFailover || !got.Data.Attempts[1].Success {
		t.Fatalf("failover attempt = %#v, want successful failover", got.Data.Attempts[1])
	}

	primary := got.Data.Attempts[0]
	body, ok := primary.ResponseBody.(map[string]any)
	if !ok {
		t.Fatalf("primary response body type = %T, want map", primary.ResponseBody)
	}
	errObj, ok := body["error"].(map[string]any)
	if !ok || errObj["code"] != "model_not_found" {
		t.Fatalf("primary response body = %#v, want nested provider error", primary.ResponseBody)
	}
	if primary.ResponseHeaders["X-Request-Id"] != "req-123" || primary.ResponseHeaders["Retry-After"] != "30" {
		t.Fatalf("primary response headers = %#v, want captured upstream headers", primary.ResponseHeaders)
	}
	if got.Data.Attempts[1].ResponseBody != nil || got.Data.Attempts[1].ResponseHeaders != nil {
		t.Fatalf("successful attempt should not carry a captured error body/headers: %#v", got.Data.Attempts[1])
	}
}

func TestSQLiteReader_AllowsNullWorkflowVersionIDAndErrorType(t *testing.T) {
	db := createTestDB(t)
	defer db.Close()

	store, err := NewSQLiteStore(db, 0)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`
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
		0,
		nil,
		200,
		"req-1",
		"127.0.0.1",
		"POST",
		"/v1/chat/completions",
		0,
		nil,
		nil,
	); err != nil {
		t.Fatalf("failed to insert audit log row: %v", err)
	}

	reader, err := NewSQLiteReader(db)
	if err != nil {
		t.Fatalf("failed to create reader: %v", err)
	}

	entry, err := reader.GetLogByID(context.Background(), "null-workflow-version")
	if err != nil {
		t.Fatalf("GetLogByID failed: %v", err)
	}
	if entry == nil {
		t.Fatal("expected log entry, got nil")
		return
	}
	if entry.WorkflowVersionID != "" {
		t.Fatalf("WorkflowVersionID = %q, want empty", entry.WorkflowVersionID)
	}
	if entry.ErrorType != "" {
		t.Fatalf("ErrorType = %q, want empty", entry.ErrorType)
	}

	logs, err := reader.GetLogs(context.Background(), LogQueryParams{Limit: 10})
	if err != nil {
		t.Fatalf("GetLogs failed: %v", err)
	}
	if len(logs.Entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1", len(logs.Entries))
	}
	if logs.Entries[0].WorkflowVersionID != "" {
		t.Fatalf("list WorkflowVersionID = %q, want empty", logs.Entries[0].WorkflowVersionID)
	}
	if logs.Entries[0].ErrorType != "" {
		t.Fatalf("list ErrorType = %q, want empty", logs.Entries[0].ErrorType)
	}
}

func TestSQLiteReader_GetLogsFiltersByUserPathSubtree(t *testing.T) {
	db := createTestDB(t)
	defer db.Close()

	store, err := NewSQLiteStore(db, 0)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = db.Exec(`
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
		0,
		nil,
		200,
		"req-1",
		"127.0.0.1",
		"POST",
		"/v1/chat/completions",
		"/team/a",
		0,
		"",
		nil,
		"miss-other",
		now,
		0,
		"gpt-4",
		"",
		"openai",
		0,
		nil,
		200,
		"req-2",
		"127.0.0.1",
		"POST",
		"/v1/chat/completions",
		"/other",
		0,
		"",
		nil,
	)
	if err != nil {
		t.Fatalf("failed to insert audit log rows: %v", err)
	}

	reader, err := NewSQLiteReader(db)
	if err != nil {
		t.Fatalf("failed to create reader: %v", err)
	}

	logs, err := reader.GetLogs(context.Background(), LogQueryParams{UserPath: "/team", Limit: 10})
	if err != nil {
		t.Fatalf("GetLogs failed: %v", err)
	}
	if len(logs.Entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1", len(logs.Entries))
	}
	if logs.Entries[0].ID != "match-team" {
		t.Fatalf("entry id = %q, want match-team", logs.Entries[0].ID)
	}
	if logs.Entries[0].UserPath != "/team/a" {
		t.Fatalf("entry user_path = %q, want /team/a", logs.Entries[0].UserPath)
	}
}

func TestSQLiteReader_GetLogsRootUserPathIncludesLegacyNullRows(t *testing.T) {
	db := createTestDB(t)
	defer db.Close()

	store, err := NewSQLiteStore(db, 0)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = db.Exec(`
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
		0,
		nil,
		200,
		"req-legacy",
		"127.0.0.1",
		"POST",
		"/v1/chat/completions",
		nil,
		0,
		"",
		nil,
		"root-explicit",
		now,
		0,
		"gpt-4",
		"",
		"openai",
		0,
		nil,
		200,
		"req-root",
		"127.0.0.1",
		"POST",
		"/v1/chat/completions",
		"/",
		0,
		"",
		nil,
	)
	if err != nil {
		t.Fatalf("failed to insert audit log rows: %v", err)
	}

	reader, err := NewSQLiteReader(db)
	if err != nil {
		t.Fatalf("failed to create reader: %v", err)
	}

	logs, err := reader.GetLogs(context.Background(), LogQueryParams{UserPath: "/", Limit: 10})
	if err != nil {
		t.Fatalf("GetLogs failed: %v", err)
	}
	if len(logs.Entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2", len(logs.Entries))
	}
}

func TestSQLiteStoreAndReader_PreserveCacheType(t *testing.T) {
	db := createTestDB(t)
	defer db.Close()

	store, err := NewSQLiteStore(db, 0)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	now := time.Now()
	if err := store.WriteBatch(ctx, []*LogEntry{
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
	}); err != nil {
		t.Fatalf("WriteBatch failed: %v", err)
	}

	var exactCacheType sql.NullString
	if err := db.QueryRow("SELECT cache_type FROM audit_logs WHERE id = ?", "cache-exact").Scan(&exactCacheType); err != nil {
		t.Fatalf("query exact cache_type failed: %v", err)
	}
	if !exactCacheType.Valid || exactCacheType.String != CacheTypeExact {
		t.Fatalf("exact cache_type = %#v, want %q", exactCacheType, CacheTypeExact)
	}

	var noneCacheType sql.NullString
	if err := db.QueryRow("SELECT cache_type FROM audit_logs WHERE id = ?", "cache-none").Scan(&noneCacheType); err != nil {
		t.Fatalf("query nil cache_type failed: %v", err)
	}
	if noneCacheType.Valid {
		t.Fatalf("nil cache_type = %#v, want SQL NULL", noneCacheType)
	}

	reader, err := NewSQLiteReader(db)
	if err != nil {
		t.Fatalf("failed to create reader: %v", err)
	}

	exactEntry, err := reader.GetLogByID(ctx, "cache-exact")
	if err != nil {
		t.Fatalf("GetLogByID(exact) failed: %v", err)
	}
	if exactEntry == nil || exactEntry.CacheType != CacheTypeExact {
		t.Fatalf("exact entry cache_type = %#v, want %q", exactEntry, CacheTypeExact)
	}

	noneEntry, err := reader.GetLogByID(ctx, "cache-none")
	if err != nil {
		t.Fatalf("GetLogByID(none) failed: %v", err)
	}
	if noneEntry == nil || noneEntry.CacheType != "" {
		t.Fatalf("none entry cache_type = %#v, want empty", noneEntry)
	}
}
