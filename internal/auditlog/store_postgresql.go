package auditlog

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	auditLogInsertColumnCount     = 15
	postgresMaxBindParameters     = 65535
	auditLogInsertMaxRowsPerQuery = postgresMaxBindParameters / auditLogInsertColumnCount
)

const auditLogInsertPrefix = `
		INSERT INTO audit_logs (id, timestamp, duration_ns, model, resolved_model, provider, alias_used, status_code,
			request_id, client_ip, method, path, stream, error_type, data)
		VALUES `

const auditLogInsertSuffix = `
		ON CONFLICT (id) DO NOTHING
	`

type auditLogBatchExecutor interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

// PostgreSQLStore implements LogStore for PostgreSQL databases.
type PostgreSQLStore struct {
	pool          *pgxpool.Pool
	retentionDays int
	stopCleanup   chan struct{}
	closeOnce     sync.Once
}

// NewPostgreSQLStore creates a new PostgreSQL audit log store.
// It creates the audit_logs table if it doesn't exist and starts
// a background cleanup goroutine if retention is configured.
func NewPostgreSQLStore(pool *pgxpool.Pool, retentionDays int) (*PostgreSQLStore, error) {
	if pool == nil {
		return nil, fmt.Errorf("connection pool is required")
	}

	ctx := context.Background()

	// Create table with commonly-filtered fields as columns
	_, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS audit_logs (
			id UUID PRIMARY KEY,
			timestamp TIMESTAMPTZ NOT NULL,
			duration_ns BIGINT DEFAULT 0,
			model TEXT,
			resolved_model TEXT,
			provider TEXT,
			alias_used BOOLEAN DEFAULT FALSE,
			status_code INTEGER DEFAULT 0,
			request_id TEXT,
			client_ip TEXT,
			method TEXT,
			path TEXT,
			stream BOOLEAN DEFAULT FALSE,
			error_type TEXT,
			data JSONB
		)
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to create audit_logs table: %w", err)
	}

	migrations := []string{
		"ALTER TABLE audit_logs ADD COLUMN IF NOT EXISTS resolved_model TEXT",
		"ALTER TABLE audit_logs ADD COLUMN IF NOT EXISTS alias_used BOOLEAN DEFAULT FALSE",
	}
	for _, migration := range migrations {
		if _, err := pool.Exec(ctx, migration); err != nil {
			return nil, fmt.Errorf("failed to run migration %q: %w", migration, err)
		}
	}

	// Create indexes for common queries
	indexes := []string{
		"CREATE INDEX IF NOT EXISTS idx_audit_timestamp ON audit_logs(timestamp)",
		"CREATE INDEX IF NOT EXISTS idx_audit_model ON audit_logs(model)",
		"CREATE INDEX IF NOT EXISTS idx_audit_status ON audit_logs(status_code)",
		"CREATE INDEX IF NOT EXISTS idx_audit_provider ON audit_logs(provider)",
		"CREATE INDEX IF NOT EXISTS idx_audit_request_id ON audit_logs(request_id)",
		"CREATE INDEX IF NOT EXISTS idx_audit_client_ip ON audit_logs(client_ip)",
		"CREATE INDEX IF NOT EXISTS idx_audit_path ON audit_logs(path)",
		"CREATE INDEX IF NOT EXISTS idx_audit_error_type ON audit_logs(error_type)",
		"CREATE INDEX IF NOT EXISTS idx_audit_response_id ON audit_logs ((data->'response_body'->>'id'))",
		"CREATE INDEX IF NOT EXISTS idx_audit_previous_response_id ON audit_logs ((data->'request_body'->>'previous_response_id'))",
		"CREATE INDEX IF NOT EXISTS idx_audit_data_gin ON audit_logs USING GIN (data)",
	}
	for _, idx := range indexes {
		if _, err := pool.Exec(ctx, idx); err != nil {
			slog.Warn("failed to create index", "error", err)
		}
	}

	store := &PostgreSQLStore{
		pool:          pool,
		retentionDays: retentionDays,
		stopCleanup:   make(chan struct{}),
	}

	// Start background cleanup if retention is configured
	if retentionDays > 0 {
		go RunCleanupLoop(store.stopCleanup, store.cleanup)
	}

	return store, nil
}

// WriteBatch writes multiple log entries to PostgreSQL using batch insert.
func (s *PostgreSQLStore) WriteBatch(ctx context.Context, entries []*LogEntry) error {
	if len(entries) == 0 {
		return nil
	}

	// For larger batches, use a transaction to ensure atomicity
	// For smaller batches, use individual inserts without transaction overhead
	if len(entries) < 10 {
		return s.writeBatchSmall(ctx, entries)
	}

	return s.writeBatchLarge(ctx, entries)
}

// writeBatchSmall uses INSERT for small batches
func (s *PostgreSQLStore) writeBatchSmall(ctx context.Context, entries []*LogEntry) error {
	if err := writeAuditLogInsertChunks(ctx, s.pool, entries); err != nil {
		slog.Warn("failed to insert audit log batch", "error", err, "count", len(entries))
		return fmt.Errorf("failed to insert %d audit logs: %w", len(entries), err)
	}
	return nil
}

// writeBatchLarge uses batch insert for larger batches
func (s *PostgreSQLStore) writeBatchLarge(ctx context.Context, entries []*LogEntry) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if err := writeAuditLogInsertChunks(ctx, tx, entries); err != nil {
		slog.Warn("failed to insert audit log batch in transaction", "error", err, "count", len(entries))
		return fmt.Errorf("failed to insert %d audit logs: %w", len(entries), err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	return nil
}

func writeAuditLogInsertChunks(ctx context.Context, exec auditLogBatchExecutor, entries []*LogEntry) error {
	for start := 0; start < len(entries); start += auditLogInsertMaxRowsPerQuery {
		end := min(start+auditLogInsertMaxRowsPerQuery, len(entries))
		query, args := buildAuditLogInsert(entries[start:end])
		if _, err := exec.Exec(ctx, query, args...); err != nil {
			return fmt.Errorf("batch chunk [%d:%d): %w", start, end, err)
		}
	}
	return nil
}

func buildAuditLogInsert(entries []*LogEntry) (string, []any) {
	var builder strings.Builder
	builder.Grow(len(auditLogInsertPrefix) + len(auditLogInsertSuffix) + len(entries)*auditLogInsertColumnCount*4)
	builder.WriteString(auditLogInsertPrefix)

	args := make([]any, 0, len(entries)*auditLogInsertColumnCount)
	placeholder := 1

	for i, entry := range entries {
		if i > 0 {
			builder.WriteString(", ")
		}
		builder.WriteByte('(')
		for col := range auditLogInsertColumnCount {
			if col > 0 {
				builder.WriteString(", ")
			}
			builder.WriteByte('$')
			builder.WriteString(strconv.Itoa(placeholder))
			placeholder++
		}
		builder.WriteByte(')')

		dataJSON := marshalLogData(entry.Data, entry.ID)
		args = append(args,
			entry.ID,
			entry.Timestamp,
			entry.DurationNs,
			entry.Model,
			entry.ResolvedModel,
			entry.Provider,
			entry.AliasUsed,
			entry.StatusCode,
			entry.RequestID,
			entry.ClientIP,
			entry.Method,
			entry.Path,
			entry.Stream,
			entry.ErrorType,
			dataJSON,
		)
	}

	builder.WriteString(auditLogInsertSuffix)
	return builder.String(), args
}

// Flush is a no-op for PostgreSQL as writes are synchronous.
func (s *PostgreSQLStore) Flush(_ context.Context) error {
	return nil
}

// Close stops the cleanup goroutine.
// Note: We don't close the pool here as it's managed by the storage layer.
// Safe to call multiple times.
func (s *PostgreSQLStore) Close() error {
	if s.retentionDays > 0 && s.stopCleanup != nil {
		s.closeOnce.Do(func() {
			close(s.stopCleanup)
		})
	}
	return nil
}

// cleanup deletes log entries older than the retention period.
func (s *PostgreSQLStore) cleanup() {
	if s.retentionDays <= 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	cutoff := time.Now().AddDate(0, 0, -s.retentionDays)

	result, err := s.pool.Exec(ctx, "DELETE FROM audit_logs WHERE timestamp < $1", cutoff)
	if err != nil {
		slog.Error("failed to cleanup old audit logs", "error", err)
		return
	}

	if result.RowsAffected() > 0 {
		slog.Info("cleaned up old audit logs", "deleted", result.RowsAffected())
	}
}
