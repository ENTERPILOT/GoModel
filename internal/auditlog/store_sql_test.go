package auditlog

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
)

// These replace a pair of tests that only asserted the shape of a generated
// SQL string. The write path now executes against both engines instead.

func runSQLStoreTest(t *testing.T, retentionDays int, body func(t *testing.T, store *SQLStore, db sqlx.DB)) {
	t.Helper()
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, err := NewSQLStore(context.Background(), db, retentionDays)
		if err != nil {
			t.Fatalf("NewSQLStore: %v", err)
		}
		t.Cleanup(func() { _ = store.Close() })
		body(t, store, db)
	})
}

func testLogEntry(id string, at time.Time) *LogEntry {
	return &LogEntry{
		ID:             id,
		Timestamp:      at,
		DurationNs:     1234,
		RequestedModel: "gpt-4o-mini",
		ResolvedModel:  "gpt-4o-mini",
		Provider:       "openai",
		ProviderName:   "primary-openai",
		AliasUsed:      true,
		CacheType:      CacheTypeExact,
		StatusCode:     200,
		RequestID:      "req-" + id,
		PrincipalID:    "oidc:principal-1",
		AuthKeyID:      "auth-key-1",
		AuthMethod:     "bearer",
		ClientIP:       "127.0.0.1",
		Method:         "POST",
		Path:           "/v1/chat/completions",
		UserPath:       "/team",
		Stream:         true,
		Data:           &LogData{UserAgent: "test-agent", Labels: []string{"team-a"}},
	}
}

func TestSQLStoreWriteBatchRoundTrip(t *testing.T) {
	runSQLStoreTest(t, 0, func(t *testing.T, store *SQLStore, db sqlx.DB) {
		ctx := context.Background()
		now := time.Unix(1700000000, 0).UTC()
		if err := store.WriteBatch(ctx, []*LogEntry{testLogEntry("log-1", now)}); err != nil {
			t.Fatalf("WriteBatch: %v", err)
		}

		reader, err := NewSQLReader(db)
		if err != nil {
			t.Fatalf("NewSQLReader: %v", err)
		}
		result, err := reader.GetLogs(ctx, LogQueryParams{Limit: 10, OmitAttempts: true})
		if err != nil {
			t.Fatalf("GetLogs: %v", err)
		}
		if len(result.Entries) != 1 {
			t.Fatalf("entries = %d, want 1", len(result.Entries))
		}
		entry := result.Entries[0]
		if entry.RequestedModel != "gpt-4o-mini" || entry.UserPath != "/team" || entry.StatusCode != 200 {
			t.Errorf("entry = (%q, %q, %d), want (gpt-4o-mini, /team, 200)", entry.RequestedModel, entry.UserPath, entry.StatusCode)
		}
		if entry.PrincipalID != "oidc:principal-1" {
			t.Errorf("principal_id = %q, want oidc:principal-1", entry.PrincipalID)
		}
		if entry.RequestID != "req-log-1" || entry.AuthKeyID != "auth-key-1" || entry.AuthMethod != "bearer" {
			t.Errorf("auth fields = (%q, %q, %q), want (req-log-1, auth-key-1, bearer)", entry.RequestID, entry.AuthKeyID, entry.AuthMethod)
		}
		// Booleans round-trip as booleans on both engines, without an
		// int-conversion helper on either side.
		if !entry.AliasUsed || !entry.Stream {
			t.Errorf("alias_used=%v stream=%v, want both true", entry.AliasUsed, entry.Stream)
		}
	})
}

func TestSQLStoreWriteBatchIgnoresDuplicateIDs(t *testing.T) {
	runSQLStoreTest(t, 0, func(t *testing.T, store *SQLStore, db sqlx.DB) {
		ctx := context.Background()
		now := time.Unix(1700000000, 0).UTC()
		entry := testLogEntry("log-1", now)
		for range 2 {
			if err := store.WriteBatch(ctx, []*LogEntry{entry}); err != nil {
				t.Fatalf("WriteBatch: %v", err)
			}
		}

		// A retried flush must not fail or duplicate the row.
		var count int
		if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM audit_logs`).Scan(&count); err != nil {
			t.Fatalf("count: %v", err)
		}
		if count != 1 {
			t.Errorf("count = %d, want 1", count)
		}
	})
}

func TestSQLStoreWriteBatchChunksBeyondParameterLimit(t *testing.T) {
	runSQLStoreTest(t, 0, func(t *testing.T, store *SQLStore, db sqlx.DB) {
		ctx := context.Background()
		now := time.Unix(1700000000, 0).UTC()

		// More than one chunk: SQLite rejects a statement binding over 999
		// parameters, so the batch has to be split.
		total := maxEntriesPerBatch*2 + 3
		entries := make([]*LogEntry, 0, total)
		for i := range total {
			entries = append(entries, testLogEntry(fmt.Sprintf("log-%03d", i), now))
		}
		if err := store.WriteBatch(ctx, entries); err != nil {
			t.Fatalf("WriteBatch: %v", err)
		}

		var count int
		if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM audit_logs`).Scan(&count); err != nil {
			t.Fatalf("count: %v", err)
		}
		if count != total {
			t.Errorf("count = %d, want %d", count, total)
		}
	})
}

func TestSQLStoreParameterLimitFitsOneChunk(t *testing.T) {
	if got := maxEntriesPerBatch * columnsPerEntry; got > maxSQLParams {
		t.Fatalf("bind parameters per chunk = %d, want <= %d", got, maxSQLParams)
	}
}

func TestSQLStoreCleanupDropsEntriesPastRetention(t *testing.T) {
	runSQLStoreTest(t, 1, func(t *testing.T, store *SQLStore, db sqlx.DB) {
		ctx := context.Background()
		now := time.Now().UTC()
		if err := store.WriteBatch(ctx, []*LogEntry{
			testLogEntry("fresh", now),
			testLogEntry("stale", now.AddDate(0, 0, -3)),
		}); err != nil {
			t.Fatalf("WriteBatch: %v", err)
		}

		store.cleanup()

		var count int
		if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM audit_logs WHERE id = ?`, "stale").Scan(&count); err != nil {
			t.Fatalf("count stale: %v", err)
		}
		if count != 0 {
			t.Errorf("stale entry survived retention sweep")
		}
		if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM audit_logs WHERE id = ?`, "fresh").Scan(&count); err != nil {
			t.Fatalf("count fresh: %v", err)
		}
		if count != 1 {
			t.Errorf("fresh entry was swept, want it kept")
		}
	})
}

func TestNewSQLStoreRenamesLegacyModelColumn(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		// The column was called `model` before requested/resolved were split.
		if err := db.Schema(ctx, `
			CREATE TABLE audit_logs (
				id TEXT PRIMARY KEY,
				timestamp `+sqlx.TypeTimestamp+` NOT NULL,
				model TEXT,
				status_code INTEGER DEFAULT 0
			)`); err != nil {
			t.Fatalf("create legacy table: %v", err)
		}

		if _, err := NewSQLStore(ctx, db, 0); err != nil {
			t.Fatalf("NewSQLStore: %v", err)
		}

		columns, err := auditColumns(ctx, db, auditLogTable)
		if err != nil {
			t.Fatalf("auditColumns: %v", err)
		}
		if columns["model"] {
			t.Error("legacy model column still present")
		}
		if !columns["requested_model"] {
			t.Error("requested_model column missing after rename")
		}
	})
}

func TestNewSQLStoreReplacesSingleColumnUserPathIndex(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		// Databases created before the composite index carry the single-column one.
		if err := db.Schema(ctx, `
			CREATE TABLE audit_logs (
				id TEXT PRIMARY KEY,
				timestamp `+sqlx.TypeTimestamp+` NOT NULL,
				user_path TEXT,
				status_code INTEGER DEFAULT 0
			)`,
			`CREATE INDEX idx_audit_user_path ON audit_logs(user_path)`,
		); err != nil {
			t.Fatalf("create legacy table: %v", err)
		}

		if _, err := NewSQLStore(ctx, db, 0); err != nil {
			t.Fatalf("NewSQLStore: %v", err)
		}

		// Recreating the old index must succeed, proving the startup drop removed it.
		if _, err := db.Exec(ctx, `CREATE INDEX idx_audit_user_path ON audit_logs(user_path)`); err != nil {
			t.Fatalf("single-column index still present after NewSQLStore: %v", err)
		}
		// And the composite replacement must now exist.
		if _, err := db.Exec(ctx, `CREATE INDEX idx_audit_user_path_timestamp ON audit_logs(user_path, timestamp)`); err == nil {
			t.Fatal("composite user_path index missing after NewSQLStore")
		}
	})
}
