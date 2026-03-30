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
			ID:        "entry-nil-data",
			Timestamp: time.Now(),
			Model:     "gpt-4",
			Provider:  "openai",
			Data:      nil, // This should become SQL NULL
		},
		{
			ID:        "entry-with-data",
			Timestamp: time.Now(),
			Model:     "gpt-4",
			Provider:  "openai",
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
			ID:         fmt.Sprintf("entry-%03d", i),
			Timestamp:  time.Now(),
			Model:      "gpt-4",
			Provider:   "openai",
			StatusCode: 200,
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
			ID:        fmt.Sprintf("exact-%03d", i),
			Timestamp: time.Now(),
			Model:     "gpt-4",
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
			ID:        fmt.Sprintf("boundary-%03d", i),
			Timestamp: time.Now(),
			Model:     "gpt-4",
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
		ID:            "alias-entry",
		Timestamp:     time.Now(),
		Model:         "anthropic/claude-opus-4-6",
		ResolvedModel: "openai/gpt-5-nano",
		Provider:      "openai",
		AliasUsed:     true,
		StatusCode:    200,
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
	}
	if logEntry.Model != entry.Model {
		t.Fatalf("Model = %q, want %q", logEntry.Model, entry.Model)
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
}

func TestSQLiteReader_AllowsNullExecutionPlanVersionID(t *testing.T) {
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
			id, timestamp, duration_ns, model, resolved_model, provider, alias_used, execution_plan_version_id,
			status_code, request_id, client_ip, method, path, stream, error_type, data
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		"null-plan-version",
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
		"",
		nil,
	); err != nil {
		t.Fatalf("failed to insert audit log row: %v", err)
	}

	reader, err := NewSQLiteReader(db)
	if err != nil {
		t.Fatalf("failed to create reader: %v", err)
	}

	entry, err := reader.GetLogByID(context.Background(), "null-plan-version")
	if err != nil {
		t.Fatalf("GetLogByID failed: %v", err)
	}
	if entry == nil {
		t.Fatal("expected log entry, got nil")
	}
	if entry.ExecutionPlanVersionID != "" {
		t.Fatalf("ExecutionPlanVersionID = %q, want empty", entry.ExecutionPlanVersionID)
	}

	logs, err := reader.GetLogs(context.Background(), LogQueryParams{Limit: 10})
	if err != nil {
		t.Fatalf("GetLogs failed: %v", err)
	}
	if len(logs.Entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1", len(logs.Entries))
	}
	if logs.Entries[0].ExecutionPlanVersionID != "" {
		t.Fatalf("list ExecutionPlanVersionID = %q, want empty", logs.Entries[0].ExecutionPlanVersionID)
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
			id, timestamp, duration_ns, model, resolved_model, provider, alias_used, execution_plan_version_id,
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
			ID:        "cache-exact",
			Timestamp: now,
			Model:     "gpt-4",
			Provider:  "openai",
			CacheType: CacheTypeExact,
		},
		{
			ID:        "cache-none",
			Timestamp: now.Add(time.Second),
			Model:     "gpt-4",
			Provider:  "openai",
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
