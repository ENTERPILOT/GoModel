package ratelimit

import (
	"context"
	"fmt"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// migratePreScopeTable rebuilds a rate_limits table created before rule scopes
// existed (keyed by user_path only) into the scoped shape.
//
// Only the column introspection differs per engine — SQLite has PRAGMA
// table_info, PostgreSQL has information_schema. The rebuild itself is the
// same statements in the same order on both.
func migratePreScopeTable(ctx context.Context, db sqlx.DB) error {
	columns, err := rateLimitColumns(ctx, db)
	if err != nil {
		return err
	}
	if columns["subject"] || !columns["user_path"] {
		return nil // already scoped, or the table does not exist yet
	}

	// One transaction: a crash mid-rebuild must not leave the table renamed
	// away, or the next startup would create a fresh empty rate_limits and
	// orphan every rule. If concurrent replicas race here, one commits and the
	// others fail fast and see the migrated schema on restart.
	return db.InTx(ctx, func(q sqlx.Querier) error {
		statements := []string{
			`ALTER TABLE rate_limits RENAME TO rate_limits_pre_scope`,
			db.Dialect().ExpandTypes(sqlRateLimitsSchema),
			`INSERT INTO rate_limits (scope, subject, period_seconds, max_requests, max_tokens, source, created_at, updated_at)
				SELECT 'user_path', user_path, period_seconds, max_requests, max_tokens, source, created_at, updated_at
				FROM rate_limits_pre_scope`,
			`DROP TABLE rate_limits_pre_scope`,
			// SQLite carried the old index along with the renamed table;
			// PostgreSQL drops it with the table, so this is a no-op there.
			`DROP INDEX IF EXISTS idx_rate_limits_user_path`,
		}
		for _, statement := range statements {
			if _, err := q.Exec(ctx, statement); err != nil {
				return fmt.Errorf("migrate rate_limits to scoped schema: %w", err)
			}
		}
		return nil
	})
}

// rateLimitColumns reports which columns the rate_limits table currently has,
// empty when the table does not exist.
func rateLimitColumns(ctx context.Context, db sqlx.DB) (map[string]bool, error) {
	var query string
	var args []any
	switch db.Dialect() {
	case sqlx.SQLite:
		query = `SELECT name FROM pragma_table_info('rate_limits')`
	case sqlx.PostgreSQL:
		query = `SELECT column_name FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = ?`
		args = []any{"rate_limits"}
	default:
		return nil, fmt.Errorf("unsupported dialect %q", db.Dialect())
	}

	rows, err := db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("inspect rate_limits schema: %w", err)
	}
	defer rows.Close()

	columns := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("inspect rate_limits schema: %w", err)
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("inspect rate_limits schema: %w", err)
	}
	return columns, nil
}
