package auditlog

import (
	"context"
	"log/slog"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// duplicateAttemptsIndex repeated the index behind audit_log_attempts'
// UNIQUE(audit_log_id, seq): same columns, same order. Every attempt insert
// paid for both B-trees.
const duplicateAttemptsIndex = "idx_audit_attempts_log_seq"

// dropDuplicateAttemptsIndex retires duplicateAttemptsIndex where it exists.
// The catalog is checked first, so once the index is gone a start issues no
// DROP. Replicas starting together may both see it and both drop it; IF EXISTS
// makes the later one a no-op. PostgreSQL drops it CONCURRENTLY, which never
// blocks attempt inserts and reads. A failure is logged: the index wastes
// writes and disk but does not break anything.
func dropDuplicateAttemptsIndex(ctx context.Context, db sqlx.DB) {
	exists := `SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`
	drop := "DROP INDEX IF EXISTS " + duplicateAttemptsIndex
	if db.Dialect() == sqlx.PostgreSQL {
		exists = `SELECT COUNT(*) FROM pg_class WHERE oid = to_regclass(?)`
		drop = "DROP INDEX CONCURRENTLY IF EXISTS " + duplicateAttemptsIndex
	}
	var count int
	if err := db.QueryRow(ctx, exists, duplicateAttemptsIndex).Scan(&count); err != nil {
		slog.Warn("auditlog: failed to look up duplicate attempts index", "index", duplicateAttemptsIndex, "error", err)
		return
	}
	if count == 0 {
		return
	}
	if _, err := db.Exec(ctx, drop); err != nil {
		slog.Warn("auditlog: failed to drop duplicate attempts index", "index", duplicateAttemptsIndex, "error", err)
		return
	}
	slog.Info("auditlog: dropped duplicate attempts index", "index", duplicateAttemptsIndex)
}
