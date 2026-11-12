package auditlog

import (
	"github.com/enterpilot/gomodel/internal/storage"
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

// modelBackgroundIndexes are the PostgreSQL model indexes, built CONCURRENTLY
// so an existing large table keeps accepting writes while they build.
func modelBackgroundIndexes() []storage.BackgroundIndex {
	labels := []string{"audit requested model", "audit resolved model"}
	indexes := make([]storage.BackgroundIndex, 0, len(labels))
	for i, index := range modelIndexes(sqlx.PostgreSQL) {
		indexes = append(indexes, storage.BackgroundIndex{
			Name:   index.name,
			Create: "CREATE INDEX CONCURRENTLY IF NOT EXISTS " + index.name + " ON audit_logs(" + index.columns + ")",
			Label:  labels[i],
		})
	}
	return indexes
}

// legacyModelBackgroundIndex names the legacy index for its retirement, which
// waits until both replacements are valid.
var legacyModelBackgroundIndex = storage.BackgroundIndex{Name: legacyRequestedModelIndex, Label: "legacy audit model"}
