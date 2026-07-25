package budget

import (
	"context"
	"fmt"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// migratePreScopeTable rebuilds a budgets table created before budget scopes
// existed (keyed by user_path only) into the scoped shape.
//
// The rebuild also absorbs the two columns that older releases added with
// ALTER TABLE — source and last_reset_at — by substituting their defaults when
// the old table predates them.
//
// Only the column introspection differs per engine — SQLite has PRAGMA
// table_info, PostgreSQL has information_schema. The rebuild itself is the
// same statements in the same order on both.
func migratePreScopeTable(ctx context.Context, db sqlx.DB) error {
	columns, err := budgetColumns(ctx, db)
	if err != nil {
		return err
	}
	if columns["subject"] || !columns["user_path"] {
		return nil // already scoped, or the table does not exist yet
	}

	sourceExpr := "source"
	if !columns["source"] {
		sourceExpr = "''"
	}
	lastResetExpr := "last_reset_at"
	if !columns["last_reset_at"] {
		lastResetExpr = "NULL"
	}

	// One transaction: a crash mid-rebuild must not leave the table renamed
	// away, or the next startup would create a fresh empty budgets table and
	// orphan every limit. If concurrent replicas race here, one commits and the
	// others fail fast and see the migrated schema on restart.
	return db.InTx(ctx, func(q sqlx.Querier) error {
		statements := []string{
			`ALTER TABLE budgets RENAME TO budgets_pre_scope`,
			db.Dialect().ExpandTypes(sqlBudgetsSchema),
			`INSERT INTO budgets (scope, subject, period_seconds, amount, source, last_reset_at, created_at, updated_at)
				SELECT 'user_path', user_path, period_seconds, amount, ` + sourceExpr + `, ` + lastResetExpr + `, created_at, updated_at
				FROM budgets_pre_scope`,
			`DROP TABLE budgets_pre_scope`,
			// SQLite carried the old index along with the renamed table;
			// PostgreSQL drops it with the table, so this is a no-op there.
			`DROP INDEX IF EXISTS idx_budgets_user_path`,
		}
		for _, statement := range statements {
			if _, err := q.Exec(ctx, statement); err != nil {
				return fmt.Errorf("migrate budgets to scoped schema: %w", err)
			}
		}
		return nil
	})
}

// budgetColumns reports which columns the budgets table currently has, empty
// when the table does not exist.
func budgetColumns(ctx context.Context, db sqlx.DB) (map[string]bool, error) {
	var query string
	var args []any
	switch db.Dialect() {
	case sqlx.SQLite:
		query = `SELECT name FROM pragma_table_info('budgets')`
	case sqlx.PostgreSQL:
		query = `SELECT column_name FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = ?`
		args = []any{"budgets"}
	default:
		return nil, fmt.Errorf("unsupported dialect %q", db.Dialect())
	}

	rows, err := db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("inspect budgets schema: %w", err)
	}
	defer rows.Close()

	columns := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("inspect budgets schema: %w", err)
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("inspect budgets schema: %w", err)
	}
	return columns, nil
}
