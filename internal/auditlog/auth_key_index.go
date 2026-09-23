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

// ensureSQLiteAuthKeyIndex builds the auth-key index inline on SQLite and
// drops its predecessor only once the replacement exists. PostgreSQL builds it
// in the background instead (ensureAuthKeyTimestampIndex).
func ensureSQLiteAuthKeyIndex(ctx context.Context, db sqlx.DB) {
	if db.Dialect() != sqlx.SQLite {
		return
	}
	if _, err := db.Exec(ctx, "CREATE INDEX IF NOT EXISTS "+authKeyTimestampIndex+" ON audit_logs(auth_key_id, timestamp)"); err != nil {
		slog.Warn("auditlog: failed to create auth key index", "error", err)
		return
	}
	if _, err := db.Exec(ctx, "DROP INDEX IF EXISTS "+legacyAuthKeySQLIndex); err != nil {
		slog.Warn("auditlog: failed to drop legacy auth key index", "error", err)
	}
}

// ensureAuthKeyTimestampIndex builds the auth-key index on PostgreSQL
// CONCURRENTLY, so an existing large audit_logs table keeps accepting writes,
// and retires the single-column predecessor only once the replacement is
// valid. A build interrupted midway leaves an invalid index, which is dropped
// and rebuilt; an index another instance is still building is also invalid,
// so that one is left to the instance building it.
func ensureAuthKeyTimestampIndex(ctx context.Context, db sqlx.DB) {
	if db.Dialect() != sqlx.PostgreSQL {
		return
	}
	if valid, exists := indexValidity(ctx, db, authKeyTimestampIndex); exists && !valid {
		if indexBuildInProgress(ctx, db, authKeyTimestampIndex) {
			return
		}
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

// indexBuildInProgress reports whether some session is building the index
// right now. GoModel instances share a database role, so they see each
// other's builds in pg_stat_progress_create_index.
func indexBuildInProgress(ctx context.Context, db sqlx.DB, name string) bool {
	var count int
	err := db.QueryRow(ctx, `SELECT COUNT(*) FROM pg_stat_progress_create_index WHERE index_relid = to_regclass(?)`, name).Scan(&count)
	return err == nil && count > 0
}
