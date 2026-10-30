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
	columns, err := tableColumns(ctx, db, "rate_limits")
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

// legacyCountersTable held one window row per key, with no writer, before
// rate_limit_window_snapshots replaced it.
const legacyCountersTable = "rate_limit_counters"

// importLegacyCounters moves the rows of the pre-writer rate_limit_counters
// table into rate_limit_window_snapshots under the empty writer, so windows
// survive the upgrade, and reports whether that table exists.
//
// The table is emptied, not dropped: during a rolling upgrade an older replica
// still reads and writes it, and each later start imports what it wrote since.
// The copy overwrites an earlier import of the same key, because the legacy
// row is the newer one. Other writers of the legacy table, an older replica's
// flush or a reset, wait for the import to commit (see resetCounters).
func importLegacyCounters(ctx context.Context, db sqlx.DB) (bool, error) {
	columns, err := tableColumns(ctx, db, legacyCountersTable)
	if err != nil {
		return false, err
	}
	if len(columns) == 0 {
		return false, nil
	}
	err = db.InTx(ctx, func(q sqlx.Querier) error {
		// "WHERE 1 = 1" keeps SQLite from parsing ON CONFLICT as a join
		// constraint of the SELECT.
		statements := []string{
			`INSERT INTO rate_limit_window_snapshots (
				scope, subject, partition, period_seconds, writer,
				requests_window_start, requests_current, requests_previous,
				tokens_window_start, tokens_current, tokens_previous, updated_at
			)
			SELECT scope, subject, partition, period_seconds, '',
				requests_window_start, requests_current, requests_previous,
				tokens_window_start, tokens_current, tokens_previous, updated_at
			FROM ` + legacyCountersTable + ` WHERE 1 = 1
			ON CONFLICT(scope, subject, partition, period_seconds, writer) DO UPDATE SET
				requests_window_start = excluded.requests_window_start,
				requests_current = excluded.requests_current,
				requests_previous = excluded.requests_previous,
				tokens_window_start = excluded.tokens_window_start,
				tokens_current = excluded.tokens_current,
				tokens_previous = excluded.tokens_previous,
				updated_at = excluded.updated_at`,
			`DELETE FROM ` + legacyCountersTable,
		}
		if db.Dialect() == sqlx.PostgreSQL {
			// Hold off every other writer of the legacy table until commit:
			// otherwise a reset could slip in between the copy and the
			// delete, and the copy would write back rows the reset just
			// deleted. SQLite needs nothing, as InTx serializes writers.
			statements = append([]string{`LOCK TABLE ` + legacyCountersTable + ` IN SHARE ROW EXCLUSIVE MODE`}, statements...)
		}
		for _, statement := range statements {
			if _, err := q.Exec(ctx, statement); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return true, fmt.Errorf("import legacy rate limit counters: %w", err)
	}
	return true, nil
}

// tableColumns reports which columns a table currently has, empty when the
// table does not exist.
func tableColumns(ctx context.Context, db sqlx.DB, table string) (map[string]bool, error) {
	var query string
	var args []any
	switch db.Dialect() {
	case sqlx.SQLite:
		query = `SELECT name FROM pragma_table_info(?)`
		args = []any{table}
	case sqlx.PostgreSQL:
		query = `SELECT column_name FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = ?`
		args = []any{table}
	default:
		return nil, fmt.Errorf("unsupported dialect %q", db.Dialect())
	}

	rows, err := db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("inspect %s schema: %w", table, err)
	}
	defer rows.Close()

	columns := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("inspect %s schema: %w", table, err)
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("inspect %s schema: %w", table, err)
	}
	return columns, nil
}
