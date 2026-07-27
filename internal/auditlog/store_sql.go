package auditlog

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/enterpilot/gomodel/internal/storage"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// SQLite allows 999 bindable parameters per statement
// (SQLITE_MAX_VARIABLE_NUMBER). At 22 columns per entry that is 45 entries,
// so larger batches are chunked. PostgreSQL's limit is far higher, but one
// chunk size keeps the write path identical on both.
const (
	maxSQLParams       = 999
	columnsPerEntry    = 22
	maxEntriesPerBatch = maxSQLParams / columnsPerEntry
)

const auditLogTable = "audit_logs"

// SQLStore implements LogStore for SQL databases.
type SQLStore struct {
	db            sqlx.DB
	retentionDays int
	stopCleanup   chan struct{}
	closeOnce     sync.Once
}

var sqlTables = []string{
	`CREATE TABLE IF NOT EXISTS audit_logs (
		id TEXT PRIMARY KEY,
		timestamp ` + sqlx.TypeTimestamp + ` NOT NULL,
		duration_ns ` + sqlx.TypeInt64 + ` DEFAULT 0,
		requested_model TEXT,
		resolved_model TEXT,
		provider TEXT,
		provider_name TEXT,
		alias_used ` + sqlx.TypeBool + ` DEFAULT FALSE,
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
		session_id TEXT,
		stream ` + sqlx.TypeBool + ` DEFAULT FALSE,
		error_type TEXT,
		data ` + sqlx.TypeJSON + `
	)`,
	`CREATE TABLE IF NOT EXISTS audit_log_attempts (
		id ` + sqlx.TypeSerialPK + `,
		audit_log_id TEXT NOT NULL,
		seq INTEGER NOT NULL,
		kind TEXT NOT NULL,
		provider_type TEXT,
		provider_name TEXT,
		model TEXT,
		status_code INTEGER DEFAULT 0,
		success ` + sqlx.TypeBool + ` DEFAULT FALSE,
		error_type TEXT,
		error_code TEXT,
		error_message TEXT,
		response_body TEXT,
		response_headers TEXT,
		started_at ` + sqlx.TypeTimestamp + `,
		duration_ns ` + sqlx.TypeInt64 + ` DEFAULT 0,
		UNIQUE(audit_log_id, seq),
		FOREIGN KEY(audit_log_id) REFERENCES audit_logs(id) ON DELETE CASCADE
	)`,
}

// sqlMigrations backfill columns added after the tables' first release.
var sqlMigrations = []string{
	"ALTER TABLE audit_logs ADD COLUMN requested_model TEXT",
	"ALTER TABLE audit_logs ADD COLUMN resolved_model TEXT",
	"ALTER TABLE audit_logs ADD COLUMN provider_name TEXT",
	"ALTER TABLE audit_logs ADD COLUMN alias_used " + sqlx.TypeBool + " DEFAULT FALSE",
	"ALTER TABLE audit_logs ADD COLUMN workflow_version_id TEXT",
	"ALTER TABLE audit_logs ADD COLUMN cache_type TEXT",
	"ALTER TABLE audit_logs ADD COLUMN auth_key_id TEXT",
	"ALTER TABLE audit_logs ADD COLUMN auth_method TEXT",
	"ALTER TABLE audit_logs ADD COLUMN user_path TEXT",
	"ALTER TABLE audit_logs ADD COLUMN session_id TEXT",
	"ALTER TABLE audit_log_attempts ADD COLUMN response_body TEXT",
	"ALTER TABLE audit_log_attempts ADD COLUMN response_headers TEXT",
}

// sqlIndexes are best-effort: a failure is logged and startup continues, since
// a missing index degrades query speed rather than correctness.
var sqlIndexes = []string{
	"CREATE INDEX IF NOT EXISTS idx_audit_timestamp ON audit_logs(timestamp)",
	"DROP INDEX IF EXISTS idx_audit_model",
	"CREATE INDEX IF NOT EXISTS idx_audit_requested_model ON audit_logs(requested_model)",
	"CREATE INDEX IF NOT EXISTS idx_audit_status ON audit_logs(status_code)",
	"CREATE INDEX IF NOT EXISTS idx_audit_provider ON audit_logs(provider)",
	"CREATE INDEX IF NOT EXISTS idx_audit_provider_name ON audit_logs(provider_name)",
	"CREATE INDEX IF NOT EXISTS idx_audit_workflow_version_id ON audit_logs(workflow_version_id)",
	"CREATE INDEX IF NOT EXISTS idx_audit_request_id ON audit_logs(request_id)",
	"CREATE INDEX IF NOT EXISTS idx_audit_auth_key_id ON audit_logs(auth_key_id)",
	"CREATE INDEX IF NOT EXISTS idx_audit_client_ip ON audit_logs(client_ip)",
	"CREATE INDEX IF NOT EXISTS idx_audit_path ON audit_logs(path)",
	"CREATE INDEX IF NOT EXISTS idx_audit_user_path ON audit_logs(user_path)",
	"CREATE INDEX IF NOT EXISTS idx_audit_session_id ON audit_logs(session_id)",
	"CREATE INDEX IF NOT EXISTS idx_audit_error_type ON audit_logs(error_type)",
	"CREATE INDEX IF NOT EXISTS idx_audit_attempts_log_seq ON audit_log_attempts(audit_log_id, seq)",
	"CREATE INDEX IF NOT EXISTS idx_audit_attempts_provider ON audit_log_attempts(provider_type)",
	"CREATE INDEX IF NOT EXISTS idx_audit_attempts_started_at ON audit_log_attempts(started_at)",
}

const insertAuditLogPrefix = `INSERT INTO audit_logs (
	id, timestamp, duration_ns, requested_model, resolved_model, provider,
	provider_name, alias_used, workflow_version_id, cache_type, status_code,
	request_id, auth_key_id, auth_method, client_ip, method, path, user_path,
	session_id, stream, error_type, data
) VALUES `

const insertAttemptSQL = `
	INSERT INTO audit_log_attempts (
		audit_log_id, seq, kind, provider_type, provider_name, model,
		status_code, success, error_type, error_code, error_message,
		response_body, response_headers, started_at, duration_ns
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT (audit_log_id, seq) DO NOTHING
`

// NewSQLStore creates a SQL audit log store, creating its tables if needed and
// starting the retention sweep when one is configured.
func NewSQLStore(ctx context.Context, db sqlx.DB, retentionDays int) (*SQLStore, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection is required")
	}
	if err := db.Schema(ctx, sqlTables...); err != nil {
		return nil, fmt.Errorf("failed to create audit log tables: %w", err)
	}
	if err := renameAuditColumn(ctx, db, auditLogTable, "model", "requested_model"); err != nil {
		return nil, fmt.Errorf("failed to rename audit_logs.model to requested_model: %w", err)
	}
	if err := sqlx.AddColumns(ctx, db, sqlMigrations...); err != nil {
		return nil, err
	}
	for _, statement := range append(sqlIndexes, jsonPathIndexes(db.Dialect())...) {
		if _, err := db.Exec(ctx, statement); err != nil {
			slog.Warn("failed to create index", "error", err)
		}
	}

	store := &SQLStore{
		db:            db,
		retentionDays: retentionDays,
		stopCleanup:   make(chan struct{}),
	}
	if retentionDays > 0 {
		go storage.RunCleanupLoop(store.stopCleanup, CleanupInterval, store.cleanup)
	}
	return store, nil
}

// jsonPathIndexes index the two JSON fields the Responses lookups filter on.
// The expression is engine-specific, so it is built rather than tokenised.
func jsonPathIndexes(dialect sqlx.Dialect) []string {
	switch dialect {
	case sqlx.SQLite:
		return []string{
			`CREATE INDEX IF NOT EXISTS idx_audit_response_id ON audit_logs(json_extract(data, '$.response_body.id'))`,
			`CREATE INDEX IF NOT EXISTS idx_audit_previous_response_id ON audit_logs(json_extract(data, '$.request_body.previous_response_id'))`,
		}
	case sqlx.PostgreSQL:
		return []string{
			`CREATE INDEX IF NOT EXISTS idx_audit_response_id ON audit_logs((data #>> '{response_body,id}'))`,
			`CREATE INDEX IF NOT EXISTS idx_audit_previous_response_id ON audit_logs((data #>> '{request_body,previous_response_id}'))`,
		}
	default:
		return nil
	}
}

// WriteBatch writes log entries, chunked to stay within the parameter limit.
func (s *SQLStore) WriteBatch(ctx context.Context, entries []*LogEntry) error {
	if len(entries) == 0 {
		return nil
	}
	dialect := s.db.Dialect()

	for i := 0; i < len(entries); i += maxEntriesPerBatch {
		chunk := entries[i:min(i+maxEntriesPerBatch, len(entries))]

		placeholders := make([]string, len(chunk))
		values := make([]any, 0, len(chunk)*columnsPerEntry)
		for j, e := range chunk {
			placeholders[j] = "(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)"
			values = append(values, auditLogValues(dialect, e)...)
		}

		query := insertAuditLogPrefix + strings.Join(placeholders, ",") + ` ON CONFLICT (id) DO NOTHING`
		if _, err := s.db.Exec(ctx, query, values...); err != nil {
			return fmt.Errorf("failed to insert audit logs batch %d: %w", i/maxEntriesPerBatch, err)
		}
		if err := s.writeAttempts(ctx, chunk); err != nil {
			return err
		}
	}
	return nil
}

func auditLogValues(dialect sqlx.Dialect, e *LogEntry) []any {
	// A nil data document becomes SQL NULL rather than the string "null".
	var dataValue any
	if dataJSON := marshalLogData(e.Data, e.ID); dataJSON != nil {
		dataValue = string(dataJSON)
	}
	var cacheTypeValue any
	if cacheType := normalizeCacheType(e.CacheType); cacheType != "" {
		cacheTypeValue = cacheType
	}
	userPathValue := e.UserPath
	if strings.TrimSpace(userPathValue) == "" {
		userPathValue = "/"
	}

	return []any{
		e.ID,
		dialect.TimestampArg(e.Timestamp),
		e.DurationNs,
		e.RequestedModel,
		e.ResolvedModel,
		e.Provider,
		e.ProviderName,
		e.AliasUsed,
		e.WorkflowVersionID,
		cacheTypeValue,
		e.StatusCode,
		e.RequestID,
		e.AuthKeyID,
		e.AuthMethod,
		e.ClientIP,
		e.Method,
		e.Path,
		userPathValue,
		e.SessionID,
		e.Stream,
		e.ErrorType,
		dataValue,
	}
}

func (s *SQLStore) writeAttempts(ctx context.Context, entries []*LogEntry) error {
	dialect := s.db.Dialect()
	for _, entry := range entries {
		for _, attempt := range auditAttempts(entry) {
			_, err := s.db.Exec(ctx, insertAttemptSQL,
				entry.ID,
				attempt.Seq,
				attempt.Kind,
				attempt.ProviderType,
				attempt.ProviderName,
				attempt.Model,
				attempt.StatusCode,
				attempt.Success,
				attempt.ErrorType,
				attempt.ErrorCode,
				attempt.ErrorMessage,
				marshalAttemptColumn(attempt.ResponseBody),
				marshalAttemptColumn(attempt.ResponseHeaders),
				dialect.NullableTimestampArg(attempt.StartedAt),
				attempt.DurationNs,
			)
			if err != nil {
				return fmt.Errorf("failed to insert audit log attempt for %s seq %d: %w",
					entry.ID, attempt.Seq, err)
			}
		}
	}
	return nil
}

// Flush is a no-op: writes are synchronous.
func (s *SQLStore) Flush(_ context.Context) error {
	return nil
}

// Close stops the cleanup goroutine. The connection is managed by the storage
// layer. Safe to call multiple times.
func (s *SQLStore) Close() error {
	if s.retentionDays > 0 && s.stopCleanup != nil {
		s.closeOnce.Do(func() {
			close(s.stopCleanup)
		})
	}
	return nil
}

// cleanup deletes log entries older than the retention period.
func (s *SQLStore) cleanup() {
	if s.retentionDays <= 0 {
		return
	}
	ctx := context.Background()
	cutoff := s.db.Dialect().TimestampArg(time.Now().AddDate(0, 0, -s.retentionDays))

	if _, err := s.db.Exec(ctx,
		"DELETE FROM audit_log_attempts WHERE audit_log_id IN (SELECT id FROM audit_logs WHERE timestamp < ?)",
		cutoff); err != nil {
		slog.Error("failed to cleanup old audit log attempts", "error", err)
		return
	}
	deleted, err := s.db.Exec(ctx, "DELETE FROM audit_logs WHERE timestamp < ?", cutoff)
	if err != nil {
		slog.Error("failed to cleanup old audit logs", "error", err)
		return
	}
	if deleted > 0 {
		slog.Info("cleaned up old audit logs", "deleted", deleted)
	}
}

// renameAuditColumn renames a column when the old name is still present and
// the new one is not, so the rename runs once and is a no-op thereafter.
func renameAuditColumn(ctx context.Context, db sqlx.DB, table, from, to string) error {
	columns, err := auditColumns(ctx, db, table)
	if err != nil {
		return err
	}
	if !columns[from] || columns[to] {
		return nil
	}
	_, err = db.Exec(ctx, fmt.Sprintf("ALTER TABLE %s RENAME COLUMN %s TO %s", table, from, to))
	return err
}

// auditColumns reports the table's current column names, lowercased.
func auditColumns(ctx context.Context, db sqlx.DB, table string) (map[string]bool, error) {
	var query string
	var args []any
	switch db.Dialect() {
	case sqlx.SQLite:
		query = fmt.Sprintf(`SELECT name FROM pragma_table_info('%s')`, table)
	case sqlx.PostgreSQL:
		query = `SELECT column_name FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = ?`
		args = []any{table}
	default:
		return nil, fmt.Errorf("unsupported dialect %q", db.Dialect())
	}

	rows, err := db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("inspect %s columns: %w", table, err)
	}
	defer rows.Close()

	columns := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("inspect %s columns: %w", table, err)
		}
		columns[strings.ToLower(name)] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("inspect %s columns: %w", table, err)
	}
	return columns, nil
}
