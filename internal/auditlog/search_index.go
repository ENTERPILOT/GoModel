package auditlog

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// searchColumns are the plain-text columns the free-text search sweeps, in
// addition to the error message inside the JSON data blob.
var searchColumns = []string{
	"request_id", "auth_key_id", "requested_model", "provider", "provider_name",
	"method", "path", "user_path", "session_id", "error_type",
}

const trigramSearchIndex = "idx_audit_search_trgm"

// minTrigramSearchLength is the shortest term, in characters, that yields a
// trigram.
const minTrigramSearchLength = 3

// postgresErrorMessage extracts the error message from the JSON data blob.
const postgresErrorMessage = `data->>'error_message'`

// searchText is the single text the trigram index covers: every search
// column joined by spaces, plus the error message — parsed out of the JSON
// blob only for rows that carry an error type, as the column sweep does.
// The reader must match against this exact expression for the planner to
// pick the index, so it is built once and shared.
func searchText(errorMessage string) string {
	parts := make([]string, 0, len(searchColumns)+1)
	for _, column := range searchColumns {
		parts = append(parts, "COALESCE("+column+", '')")
	}
	parts = append(parts,
		"(CASE WHEN error_type IS NOT NULL AND error_type <> '' THEN COALESCE("+errorMessage+", '') ELSE '' END)")
	return "(" + strings.Join(parts, " || ' ' || ") + ")"
}

// ensureTrigramSearchIndex gives PostgreSQL a trigram index over searchText so
// a free-text search is an index lookup instead of a LIKE sweep of every row
// in the window. It is best-effort: pg_trgm ships with PostgreSQL but the role
// may not be allowed to install it, in which case the search keeps its slow
// path and the reason is logged once. The index builds CONCURRENTLY so an
// existing large table keeps accepting writes while it builds; a build
// interrupted midway leaves an invalid index, which is dropped and rebuilt.
func ensureTrigramSearchIndex(ctx context.Context, db sqlx.DB, errorMessage string) {
	if db.Dialect() != sqlx.PostgreSQL {
		return
	}
	if _, err := db.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS pg_trgm"); err != nil {
		slog.Info("auditlog: pg_trgm extension unavailable, free-text search stays unindexed; install it with CREATE EXTENSION pg_trgm", "error", err)
		return
	}
	var schema string
	if err := db.QueryRow(ctx, `SELECT n.nspname FROM pg_extension e JOIN pg_namespace n ON n.oid = e.extnamespace WHERE e.extname = 'pg_trgm'`).Scan(&schema); err != nil {
		slog.Warn("auditlog: failed to locate pg_trgm", "error", err)
		return
	}
	var valid *bool
	if err := db.QueryRow(ctx, `SELECT i.indisvalid FROM pg_index i WHERE i.indexrelid = to_regclass(?)`, trigramSearchIndex).Scan(&valid); err == nil && valid != nil && !*valid {
		slog.Warn("auditlog: rebuilding interrupted trigram search index")
		if _, err := db.Exec(ctx, "DROP INDEX "+trigramSearchIndex); err != nil {
			slog.Warn("auditlog: failed to drop invalid trigram search index", "error", err)
			return
		}
	}
	if hasTrigramSearchIndex(ctx, db) {
		return
	}
	statement := fmt.Sprintf(`CREATE INDEX CONCURRENTLY IF NOT EXISTS %s ON audit_logs USING GIN (%s %s.gin_trgm_ops)`,
		trigramSearchIndex, searchText(errorMessage), `"`+strings.ReplaceAll(schema, `"`, `""`)+`"`)
	started := time.Now()
	if _, err := db.Exec(ctx, statement); err != nil {
		slog.Warn("auditlog: failed to create trigram search index", "error", err)
		return
	}
	slog.Info("auditlog: built trigram search index", "duration", time.Since(started).Round(time.Millisecond))
}

// hasTrigramSearchIndex reports whether the trigram index exists and is
// valid, so the reader knows whether matching against searchText pays off.
func hasTrigramSearchIndex(ctx context.Context, db sqlx.DB) bool {
	if db.Dialect() != sqlx.PostgreSQL {
		return false
	}
	var valid bool
	err := db.QueryRow(ctx, `SELECT COALESCE(i.indisvalid, FALSE) FROM pg_index i WHERE i.indexrelid = to_regclass(?)`, trigramSearchIndex).Scan(&valid)
	return err == nil && valid
}
