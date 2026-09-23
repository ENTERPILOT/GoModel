package auditlog

import (
	"context"
	"log/slog"
	"time"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// authKeyTimestampIndex serves the auth_key_id filter and the per-key
// MAX(timestamp) last-used lookup from the index alone. It replaces the
// single-column legacyAuthKeyIndex.
const (
	authKeyTimestampIndex = "idx_audit_auth_key_timestamp"
	legacyAuthKeySQLIndex = "idx_audit_auth_key_id"
)

// authKeyIndexes builds the auth-key index inline on SQLite, creating the
// replacement before dropping its predecessor. PostgreSQL builds it in the
// background instead (ensureAuthKeyTimestampIndex).
func authKeyIndexes(dialect sqlx.Dialect) []string {
	if dialect == sqlx.PostgreSQL {
		return nil
	}
	return []string{
		"CREATE INDEX IF NOT EXISTS " + authKeyTimestampIndex + " ON audit_logs(auth_key_id, timestamp)",
		"DROP INDEX IF EXISTS " + legacyAuthKeySQLIndex,
	}
}

// ensureAuthKeyTimestampIndex builds the auth-key index on PostgreSQL
// CONCURRENTLY, so an existing large audit_logs table keeps accepting writes,
// and retires the single-column predecessor only once the replacement is
// valid. A build interrupted midway leaves an invalid index, which is dropped
// and rebuilt.
func ensureAuthKeyTimestampIndex(ctx context.Context, db sqlx.DB) {
	if db.Dialect() != sqlx.PostgreSQL {
		return
	}
	if valid, exists := indexValidity(ctx, db, authKeyTimestampIndex); exists && !valid {
		slog.Warn("auditlog: rebuilding interrupted auth key index")
		if _, err := db.Exec(ctx, "DROP INDEX CONCURRENTLY IF EXISTS "+authKeyTimestampIndex); err != nil {
			slog.Warn("auditlog: failed to drop invalid auth key index", "error", err)
			return
		}
	}
	started := time.Now()
	if _, err := db.Exec(ctx, "CREATE INDEX CONCURRENTLY IF NOT EXISTS "+authKeyTimestampIndex+" ON audit_logs(auth_key_id, timestamp)"); err != nil {
		slog.Warn("auditlog: failed to create auth key index", "error", err)
		return
	}
	if valid, _ := indexValidity(ctx, db, authKeyTimestampIndex); !valid {
		return
	}
	if _, err := db.Exec(ctx, "DROP INDEX CONCURRENTLY IF EXISTS "+legacyAuthKeySQLIndex); err != nil {
		slog.Warn("auditlog: failed to drop legacy auth key index", "error", err)
		return
	}
	slog.Debug("auditlog: auth key index ready", "duration", time.Since(started).Round(time.Millisecond))
}

// indexValidity reports whether a PostgreSQL index exists and, if so, whether
// it is valid (a CONCURRENTLY build interrupted midway leaves it invalid).
func indexValidity(ctx context.Context, db sqlx.DB, name string) (valid, exists bool) {
	var isValid *bool
	if err := db.QueryRow(ctx, `SELECT i.indisvalid FROM pg_index i WHERE i.indexrelid = to_regclass(?)`, name).Scan(&isValid); err != nil || isValid == nil {
		return false, false
	}
	return *isValid, true
}
