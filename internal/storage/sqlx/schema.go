package sqlx

import (
	"context"
	"fmt"
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
