package sqlx

import (
	"context"
	"fmt"
	"strings"
)

// execSchema runs DDL statements in order with portable type tokens expanded.
//
// Statements run outside a transaction: SQLite cannot run every schema change
// transactionally, and `CREATE TABLE IF NOT EXISTS` / `CREATE INDEX IF NOT
// EXISTS` are individually idempotent, so a partial application is safe to
// retry on the next start.
func execSchema(ctx context.Context, q Querier, dialect Dialect, statements []string) error {
	for _, statement := range statements {
		if _, err := q.Exec(ctx, dialect.ExpandTypes(statement)); err != nil {
			return fmt.Errorf("apply schema statement: %w", err)
		}
	}
	return nil
}

// AddColumns applies ALTER TABLE ... ADD COLUMN migrations, expanding type
// tokens and ignoring the error each engine returns when the column is already
// present.
//
// Tolerating the error is the portable form: SQLite has no ADD COLUMN IF NOT
// EXISTS, so the PostgreSQL store used the IF NOT EXISTS spelling while the
// SQLite store pattern-matched its error text. Both engines reject a duplicate
// add with a recognisable message, so one list now serves both.
func AddColumns(ctx context.Context, db DB, statements ...string) error {
	for _, statement := range statements {
		_, err := db.Exec(ctx, db.Dialect().ExpandTypes(statement))
		if err != nil && !IsDuplicateColumnError(err) {
			return fmt.Errorf("apply migration %q: %w", statement, err)
		}
	}
	return nil
}

// IsDuplicateColumnError reports whether err is an engine's complaint that a
// column being added already exists: SQLite says "duplicate column name", and
// PostgreSQL says `column "x" of relation "y" already exists`.
//
// The "already exists" arm requires the word "column" as well. The two stores
// that pattern-matched this before disagreed on that point, and the loose form
// would let an unrelated already-exists failure pass for an applied migration.
func IsDuplicateColumnError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "duplicate column") {
		return true
	}
	return strings.Contains(message, "already exists") && strings.Contains(message, "column")
}
