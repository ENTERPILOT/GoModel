package auditlog

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// SQLite has a default limit of 999 bindable parameters per query (SQLITE_MAX_VARIABLE_NUMBER).
// With 16 columns per log entry, we can safely insert up to 62 entries per batch (62 * 16 = 992).
// We chunk larger batches to avoid hitting this limit.
const (
	maxSQLiteParams    = 999
	columnsPerEntry    = 16
	maxEntriesPerBatch = maxSQLiteParams / columnsPerEntry // 62 entries
)

// SQLiteStore implements LogStore for SQLite databases.
type SQLiteStore struct {
	db            *sql.DB
	retentionDays int
	stopCleanup   chan struct{}
	closeOnce     sync.Once
}

// NewSQLiteStore creates a new SQLite audit log store.
// It creates the audit_logs table if it doesn't exist and starts
// a background cleanup goroutine if retention is configured.
func NewSQLiteStore(db *sql.DB, retentionDays int) (*SQLiteStore, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection is required")
	}

	// Create table with commonly-filtered fields as columns
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS audit_logs (
			id TEXT PRIMARY KEY,
			timestamp DATETIME NOT NULL,
			duration_ns INTEGER DEFAULT 0,
			model TEXT,
			resolved_model TEXT,
			provider TEXT,
			alias_used INTEGER DEFAULT 0,
			execution_plan_version_id TEXT,
			status_code INTEGER DEFAULT 0,
			request_id TEXT,
			client_ip TEXT,
			method TEXT,
			path TEXT,
			stream INTEGER DEFAULT 0,
			error_type TEXT,
			data JSON
		)
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to create audit_logs table: %w", err)
	}

	migrations := []string{
		"ALTER TABLE audit_logs ADD COLUMN resolved_model TEXT",
		"ALTER TABLE audit_logs ADD COLUMN alias_used INTEGER DEFAULT 0",
		"ALTER TABLE audit_logs ADD COLUMN execution_plan_version_id TEXT",
	}
	for _, migration := range migrations {
		if _, err := db.Exec(migration); err != nil {
			if !strings.Contains(err.Error(), "duplicate column") {
				return nil, fmt.Errorf("failed to run migration %q: %w", migration, err)
			}
		}
	}

	// Create indexes for common queries
	indexes := []string{
		"CREATE INDEX IF NOT EXISTS idx_audit_timestamp ON audit_logs(timestamp)",
		"CREATE INDEX IF NOT EXISTS idx_audit_model ON audit_logs(model)",
		"CREATE INDEX IF NOT EXISTS idx_audit_status ON audit_logs(status_code)",
		"CREATE INDEX IF NOT EXISTS idx_audit_provider ON audit_logs(provider)",
		"CREATE INDEX IF NOT EXISTS idx_audit_execution_plan_version_id ON audit_logs(execution_plan_version_id)",
		"CREATE INDEX IF NOT EXISTS idx_audit_request_id ON audit_logs(request_id)",
		"CREATE INDEX IF NOT EXISTS idx_audit_client_ip ON audit_logs(client_ip)",
		"CREATE INDEX IF NOT EXISTS idx_audit_path ON audit_logs(path)",
		"CREATE INDEX IF NOT EXISTS idx_audit_error_type ON audit_logs(error_type)",
		"CREATE INDEX IF NOT EXISTS idx_audit_response_id ON audit_logs(json_extract(data, '$.response_body.id'))",
		"CREATE INDEX IF NOT EXISTS idx_audit_previous_response_id ON audit_logs(json_extract(data, '$.request_body.previous_response_id'))",
	}
	for _, idx := range indexes {
		if _, err := db.Exec(idx); err != nil {
			slog.Warn("failed to create index", "error", err)
		}
	}

	store := &SQLiteStore{
		db:            db,
		retentionDays: retentionDays,
		stopCleanup:   make(chan struct{}),
	}

	// Start background cleanup if retention is configured
	if retentionDays > 0 {
		go RunCleanupLoop(store.stopCleanup, store.cleanup)
	}

	return store, nil
}

// WriteBatch writes multiple log entries to SQLite using batch insert.
// Entries are chunked to stay within SQLite's parameter limit.
func (s *SQLiteStore) WriteBatch(ctx context.Context, entries []*LogEntry) error {
	if len(entries) == 0 {
		return nil
	}

	// Process entries in chunks to stay within SQLite's parameter limit
	for i := 0; i < len(entries); i += maxEntriesPerBatch {
		end := min(i+maxEntriesPerBatch, len(entries))
		chunk := entries[i:end]

		// Build batch insert query for this chunk
		placeholders := make([]string, len(chunk))
		values := make([]any, 0, len(chunk)*columnsPerEntry)

		for j, e := range chunk {
			placeholders[j] = "(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)"

			dataJSON := marshalLogData(e.Data, e.ID)

			// Convert bool to int for SQLite
			streamInt := 0
			if e.Stream {
				streamInt = 1
			}
			aliasUsedInt := 0
			if e.AliasUsed {
				aliasUsedInt = 1
			}

			// Handle NULL for data field: nil becomes SQL NULL, non-nil becomes JSON string
			var dataValue any
			if dataJSON != nil {
				dataValue = string(dataJSON)
			}

			values = append(values,
				e.ID,
				e.Timestamp.UTC().Format(time.RFC3339Nano),
				e.DurationNs,
				e.Model,
				e.ResolvedModel,
				e.Provider,
				aliasUsedInt,
				e.ExecutionPlanVersionID,
				e.StatusCode,
				e.RequestID,
				e.ClientIP,
				e.Method,
				e.Path,
				streamInt,
				e.ErrorType,
				dataValue,
			)
		}

		query := `INSERT OR IGNORE INTO audit_logs (id, timestamp, duration_ns, model, resolved_model, provider, alias_used, execution_plan_version_id, status_code,
			request_id, client_ip, method, path, stream, error_type, data) VALUES ` +
			strings.Join(placeholders, ",")

		_, err := s.db.ExecContext(ctx, query, values...)
		if err != nil {
			return fmt.Errorf("failed to insert audit logs batch %d: %w", i/maxEntriesPerBatch, err)
		}
	}

	return nil
}

// Flush is a no-op for SQLite as writes are synchronous.
func (s *SQLiteStore) Flush(_ context.Context) error {
	return nil
}

// Close stops the cleanup goroutine.
// Note: We don't close the DB here as it's managed by the storage layer.
// Safe to call multiple times.
func (s *SQLiteStore) Close() error {
	if s.retentionDays > 0 && s.stopCleanup != nil {
		s.closeOnce.Do(func() {
			close(s.stopCleanup)
		})
	}
	return nil
}

// cleanup deletes log entries older than the retention period.
func (s *SQLiteStore) cleanup() {
	if s.retentionDays <= 0 {
		return
	}

	cutoff := time.Now().AddDate(0, 0, -s.retentionDays).UTC().Format(time.RFC3339)

	result, err := s.db.Exec("DELETE FROM audit_logs WHERE timestamp < ?", cutoff)
	if err != nil {
		slog.Error("failed to cleanup old audit logs", "error", err)
		return
	}

	if rowsAffected, err := result.RowsAffected(); err == nil && rowsAffected > 0 {
		slog.Info("cleaned up old audit logs", "deleted", rowsAffected)
	}
}
