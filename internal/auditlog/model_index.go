package auditlog

import (
	"context"
	"log/slog"
	"time"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// auditIndex is one audit_logs index: its name and the column list it covers.
type auditIndex struct {
	name    string
	columns string
}

// legacyRequestedModelIndex is the single-column requested_model index the
// model indexes replace; nothing compares that column under its default
// collation.
const legacyRequestedModelIndex = "idx_audit_requested_model"

// modelIndexes serve the model prefix filter, which matches the requested or
// the resolved model: each branch of that OR is a range on its own index, in
// byte-wise ordering to match readerDialect.requestedModel and resolvedModel,
// and the trailing timestamp keeps the date window inside the index.
func modelIndexes(dialect sqlx.Dialect) []auditIndex {
	requested, resolved := "requested_model", "resolved_model"
	if dialect == sqlx.PostgreSQL {
		requested, resolved = `requested_model COLLATE "C"`, `resolved_model COLLATE "C"`
	}
	return []auditIndex{
		{name: "idx_audit_requested_model_timestamp", columns: requested + ", timestamp"},
		{name: "idx_audit_resolved_model_timestamp", columns: resolved + ", timestamp"},
	}
}

// modelIndexStatements builds the model indexes inline, as SQLite does at
// startup, then retires the legacy index.
func modelIndexStatements(dialect sqlx.Dialect) []string {
	statements := make([]string, 0, 3)
	for _, index := range modelIndexes(dialect) {
		statements = append(statements, "CREATE INDEX IF NOT EXISTS "+index.name+" ON audit_logs("+index.columns+")")
	}
	return append(statements, "DROP INDEX IF EXISTS "+legacyRequestedModelIndex)
}

// ensureModelIndexesConcurrently builds the PostgreSQL model indexes without
// blocking writes to an existing large table. The legacy index is dropped only
// once both replacements exist. Failures are logged: a missing index slows the
// model filter down but does not break it.
func ensureModelIndexesConcurrently(ctx context.Context, db sqlx.DB) {
	if db.Dialect() != sqlx.PostgreSQL {
		return
	}
	for _, index := range modelIndexes(sqlx.PostgreSQL) {
		if err := buildIndexConcurrently(ctx, db, index); err != nil {
			slog.Warn("auditlog: failed to create model index", "index", index.name, "error", err)
			return
		}
	}
	if _, err := db.Exec(ctx, "DROP INDEX CONCURRENTLY IF EXISTS "+legacyRequestedModelIndex); err != nil {
		slog.Warn("auditlog: failed to drop legacy model index", "index", legacyRequestedModelIndex, "error", err)
	}
}

// buildIndexConcurrently creates a PostgreSQL index with CREATE INDEX
// CONCURRENTLY, which keeps the table writable while it builds. A build
// interrupted midway leaves an invalid index behind; it is dropped and rebuilt.
func buildIndexConcurrently(ctx context.Context, db sqlx.DB, index auditIndex) error {
	var valid *bool
	err := db.QueryRow(ctx, `SELECT i.indisvalid FROM pg_index i WHERE i.indexrelid = to_regclass(?)`, index.name).Scan(&valid)
	switch {
	case err == nil && valid != nil && *valid:
		return nil
	case err == nil && valid != nil:
		slog.Warn("auditlog: rebuilding interrupted index", "index", index.name)
		if _, err := db.Exec(ctx, "DROP INDEX "+index.name); err != nil {
			return err
		}
	}
	started := time.Now()
	if _, err := db.Exec(ctx, "CREATE INDEX CONCURRENTLY IF NOT EXISTS "+index.name+" ON audit_logs("+index.columns+")"); err != nil {
		return err
	}
	slog.Info("auditlog: built index", "index", index.name, "duration", time.Since(started).Round(time.Millisecond))
	return nil
}
