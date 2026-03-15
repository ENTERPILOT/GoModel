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
	for i := 0; i < numEntries; i++ {
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
	for i := 0; i < numEntries; i++ {
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
