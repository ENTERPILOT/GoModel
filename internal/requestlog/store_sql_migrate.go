package requestlog

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// The request log was stored as the "audit log" up to v0.2.5. These are the
// names that release created, which adoptLegacyTables moves off.
const (
	legacyRequestLogTable = "audit_logs"
	legacyAttemptsTable   = "audit_log_attempts"
	legacyAttemptsLogID   = "audit_log_id"
	legacyIndexPrefix     = "idx_audit_"
	legacyAttemptIndexes  = "idx_audit_attempts_"
)

const (
	attemptsTable         = "request_log_attempts"
	attemptsLogID         = "request_log_id"
	requestLogIndexPrefix = "idx_request_logs_"
	attemptIndexPrefix    = "idx_request_log_attempts_"
)

// legacyAttemptsSequence backs the attempt table's id column on PostgreSQL,
// which named it after the legacy table.
const legacyAttemptsSequence = "audit_log_attempts_id_seq"

const (
	relationTable = "table"
	relationView  = "view"
)

// adoptLegacyStorage runs adoptLegacyTables under the schema lock, for the
// readers: they work without a store when request logging is turned off, and
// must still find the history an older release wrote.
func adoptLegacyStorage(ctx context.Context, db sqlx.DB) error {
	// Checked before taking the lock, which another replica can hold for as
	// long as an index build runs: on a database with nothing to move, a
	// reader does not wait for it.
	legacy, err := relationKind(ctx, db, db.Dialect(), legacyRequestLogTable)
	if err != nil || legacy != relationTable {
		return err
	}
	return withSchemaLock(ctx, db, func(session sqlx.DB) error { return adoptLegacyTables(ctx, session, true) })
}

// adoptLegacyTables moves a database written by v0.2.5 or older onto the
// request_logs names. It runs on every start and does nothing once the legacy
// tables are gone. The caller holds the schema lock (see withSchemaLock).
//
// A legacy table is renamed in place, with its indexes. That has to happen
// before the tables are created, or there would be an empty request_logs in
// the way.
//
// When request_logs exists as well, an older binary ran against the database
// after the rename (a rollback) and recreated the legacy tables: with absorb,
// its rows are moved over and the tables dropped. That needs both request log
// tables in place, so the store asks for it only once it has created and
// migrated them; a start interrupted between the rename and that point may
// have left one missing.
//
// PostgreSQL then gets views under the legacy names. Replicas still on the
// older release read and write through them for the rest of a rolling
// upgrade, and a rolled-back binary finds its tables. SQLite has one process
// per database and cannot write through a view, so it gets none.
func adoptLegacyTables(ctx context.Context, db sqlx.DB, absorb bool) error {
	dialect := db.Dialect()
	legacy, err := relationKind(ctx, db, dialect, legacyRequestLogTable)
	if err != nil {
		return err
	}
	if legacy != relationTable {
		return nil // never existed, or already a compatibility view
	}

	started := time.Now()
	moved := false
	err = db.InTx(ctx, func(q sqlx.Querier) error {
		current, err := relationKind(ctx, q, dialect, requestLogTable)
		if err != nil {
			return err
		}
		switch {
		case current == "":
			err = renameLegacyTables(ctx, q, dialect)
		case absorb:
			var ready bool
			if ready, err = absorbTargetsReady(ctx, q, dialect); err != nil || !ready {
				return err
			}
			err = absorbLegacyTables(ctx, q, dialect)
		default:
			return nil
		}
		if err != nil {
			return err
		}
		moved = true
		if dialect != sqlx.PostgreSQL {
			return nil
		}
		return createLegacyViews(ctx, q)
	})
	if err != nil {
		return fmt.Errorf("failed to move the audit_logs tables to request_logs: %w", err)
	}
	if moved {
		slog.Info("requestlog: moved the audit_logs tables to request_logs", "duration", time.Since(started).Round(time.Millisecond))
	}
	return nil
}

// absorbTargetsReady reports whether every legacy table has its request log
// table to move rows into.
func absorbTargetsReady(ctx context.Context, q sqlx.Querier, dialect sqlx.Dialect) (bool, error) {
	legacyAttempts, err := relationKind(ctx, q, dialect, legacyAttemptsTable)
	if err != nil || legacyAttempts != relationTable {
		return err == nil, err
	}
	attempts, err := relationKind(ctx, q, dialect, attemptsTable)
	return attempts == relationTable, err
}

// renameLegacyTables renames the legacy tables, the attempt table's foreign
// key column and every index on them.
func renameLegacyTables(ctx context.Context, q sqlx.Querier, dialect sqlx.Dialect) error {
	attempts, err := relationKind(ctx, q, dialect, legacyAttemptsTable)
	if err != nil {
		return err
	}
	statements := []string{"ALTER TABLE " + legacyRequestLogTable + " RENAME TO " + requestLogTable}
	tables := []string{requestLogTable}
	if attempts == relationTable {
		statements = append(statements,
			"ALTER TABLE "+legacyAttemptsTable+" RENAME TO "+attemptsTable,
			"ALTER TABLE "+attemptsTable+" RENAME COLUMN "+legacyAttemptsLogID+" TO "+attemptsLogID,
		)
		tables = append(tables, attemptsTable)
	}
	for _, statement := range statements {
		if _, err := q.Exec(ctx, statement); err != nil {
			return err
		}
	}
	if dialect == sqlx.PostgreSQL {
		return renamePostgresIndexes(ctx, q, tables)
	}
	return rebuildSQLiteIndexes(ctx, q, tables)
}

// renamePostgresIndexes renames the legacy indexes, and the constraints and
// the sequence PostgreSQL named after the legacy tables, so an upgraded schema
// matches a fresh one. Renaming a primary key or unique constraint renames
// its index with it.
func renamePostgresIndexes(ctx context.Context, q sqlx.Querier, tables []string) error {
	names, err := queryStrings(ctx, q,
		`SELECT indexname FROM pg_indexes WHERE schemaname = current_schema() AND tablename IN (`+placeholders(len(tables))+`)`,
		stringArgs(tables)...)
	if err != nil {
		return err
	}
	var statements []string
	for _, name := range names {
		if renamed, ok := renamedIndex(name); ok {
			statements = append(statements, "ALTER INDEX "+name+" RENAME TO "+renamed)
		}
	}

	for _, table := range tables {
		constraints, err := queryStrings(ctx, q, `SELECT c.conname::text FROM pg_constraint c
			JOIN pg_class t ON t.oid = c.conrelid
			WHERE t.relnamespace = current_schema()::regnamespace AND t.relname = ?`, table)
		if err != nil {
			return err
		}
		for _, name := range constraints {
			if renamed, ok := renamedConstraint(name); ok {
				statements = append(statements, "ALTER TABLE "+table+" RENAME CONSTRAINT "+name+" TO "+renamed)
			}
		}
	}
	statements = append(statements,
		"ALTER SEQUENCE IF EXISTS "+legacyAttemptsSequence+" RENAME TO "+attemptsTable+"_id_seq")

	for _, statement := range statements {
		if _, err := q.Exec(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

// renamedConstraint maps a constraint name PostgreSQL derived from a legacy
// table (audit_logs_pkey, audit_log_attempts_audit_log_id_fkey) to the name it
// derives from the renamed one. ok is false for any other name.
func renamedConstraint(name string) (renamed string, ok bool) {
	if rest, found := strings.CutPrefix(name, legacyAttemptsTable+"_"); found {
		return attemptsTable + "_" + strings.Replace(rest, legacyAttemptsLogID, attemptsLogID, 1), true
	}
	if rest, found := strings.CutPrefix(name, legacyRequestLogTable+"_"); found {
		return requestLogTable + "_" + rest, true
	}
	return "", false
}

// rebuildSQLiteIndexes gives the legacy indexes their new names. SQLite
// cannot rename an index, so each is dropped and rebuilt from its own
// definition, which the table rename already pointed at the new table.
func rebuildSQLiteIndexes(ctx context.Context, q sqlx.Querier, tables []string) error {
	rows, err := q.Query(ctx,
		`SELECT name, sql FROM sqlite_master WHERE type = 'index' AND sql IS NOT NULL AND tbl_name IN (`+placeholders(len(tables))+`)`,
		stringArgs(tables)...)
	if err != nil {
		return err
	}
	type index struct{ name, renamed, definition string }
	var indexes []index
	for rows.Next() {
		var name, definition string
		if err := rows.Scan(&name, &definition); err != nil {
			rows.Close()
			return err
		}
		if renamed, ok := renamedIndex(name); ok {
			indexes = append(indexes, index{name: name, renamed: renamed, definition: definition})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, index := range indexes {
		definition := strings.Replace(index.definition, index.name, index.renamed, 1)
		if definition == index.definition {
			return fmt.Errorf("index %s: its definition does not name it", index.name)
		}
		if _, err := q.Exec(ctx, "DROP INDEX "+index.name); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, definition); err != nil {
			return fmt.Errorf("rebuild index %s as %s: %w", index.name, index.renamed, err)
		}
	}
	return nil
}

// renamedIndex maps a legacy index name to its request log name. ok is false
// for a name without the legacy prefix.
func renamedIndex(name string) (renamed string, ok bool) {
	if rest, found := strings.CutPrefix(name, legacyAttemptIndexes); found {
		return attemptIndexPrefix + rest, true
	}
	if rest, found := strings.CutPrefix(name, legacyIndexPrefix); found {
		return requestLogIndexPrefix + rest, true
	}
	return "", false
}

// absorbLegacyTables moves the rows of legacy tables that exist next to the
// request log tables into them, then drops the legacy tables. Rows are matched
// on the columns both sides have, so an older binary's narrower table is fine.
func absorbLegacyTables(ctx context.Context, q sqlx.Querier, dialect sqlx.Dialect) error {
	columns, err := sharedColumns(ctx, q, dialect, legacyRequestLogTable, requestLogTable)
	if err != nil {
		return err
	}
	list := strings.Join(columns, ", ")
	// "WHERE 1 = 1" keeps SQLite from parsing ON CONFLICT as a join constraint
	// of the SELECT.
	statements := []string{
		"INSERT INTO " + requestLogTable + " (" + list + ") SELECT " + list + " FROM " + legacyRequestLogTable +
			" WHERE 1 = 1 ON CONFLICT (id) DO NOTHING",
	}

	attempts, err := relationKind(ctx, q, dialect, legacyAttemptsTable)
	if err != nil {
		return err
	}
	if attempts == relationTable {
		columns, err := sharedColumns(ctx, q, dialect, legacyAttemptsTable, attemptsTable)
		if err != nil {
			return err
		}
		// The row id is local to each table, and the foreign key changed name.
		columns = slices.DeleteFunc(columns, func(column string) bool { return column == "id" })
		list := strings.Join(columns, ", ")
		statements = append(statements,
			"INSERT INTO "+attemptsTable+" ("+attemptsLogID+", "+list+") SELECT "+legacyAttemptsLogID+", "+list+
				" FROM "+legacyAttemptsTable+" WHERE 1 = 1 ON CONFLICT ("+attemptsLogID+", seq) DO NOTHING",
			"DROP TABLE "+legacyAttemptsTable,
		)
	}
	statements = append(statements, "DROP TABLE "+legacyRequestLogTable)
	for _, statement := range statements {
		if _, err := q.Exec(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

// ensureLegacyViews brings the PostgreSQL views under the legacy names up to
// date with the request log tables, on a database that has them. It runs after
// the tables' own migrations: the views adoptLegacyTables created cover the
// columns the tables had at that moment, and a table created or widened since
// (a database older than the attempts table, a column this release adds) would
// leave an older binary looking for a relation or a column it expects.
func ensureLegacyViews(ctx context.Context, db sqlx.DB) error {
	if db.Dialect() != sqlx.PostgreSQL {
		return nil
	}
	legacy, err := relationKind(ctx, db, sqlx.PostgreSQL, legacyRequestLogTable)
	if err != nil || legacy != relationView {
		return err
	}
	current, err := legacyViewsCurrent(ctx, db)
	if err != nil || current {
		return err
	}
	// One transaction, so an older replica never finds the names missing.
	err = db.InTx(ctx, func(q sqlx.Querier) error {
		for _, statement := range []string{
			"DROP VIEW IF EXISTS " + legacyAttemptsTable,
			"DROP VIEW IF EXISTS " + legacyRequestLogTable,
		} {
			if _, err := q.Exec(ctx, statement); err != nil {
				return err
			}
		}
		return createLegacyViews(ctx, q)
	})
	if err != nil {
		return fmt.Errorf("failed to refresh the audit_logs views: %w", err)
	}
	return nil
}

// legacyViewsCurrent reports whether each legacy view shows every column of
// its table.
func legacyViewsCurrent(ctx context.Context, q sqlx.Querier) (bool, error) {
	for _, pair := range [][2]string{{legacyRequestLogTable, requestLogTable}, {legacyAttemptsTable, attemptsTable}} {
		view, err := tableColumns(ctx, q, sqlx.PostgreSQL, pair[0])
		if err != nil {
			return false, err
		}
		table, err := tableColumns(ctx, q, sqlx.PostgreSQL, pair[1])
		if err != nil {
			return false, err
		}
		for i, column := range table {
			if column == attemptsLogID {
				table[i] = legacyAttemptsLogID
			}
		}
		if !slices.Equal(view, table) {
			return false, nil
		}
	}
	return true, nil
}

// createLegacyViews exposes the request log tables under their legacy names.
// Both views are simple enough for PostgreSQL to update through, including
// the ON CONFLICT clauses the older release writes with.
//
// They pin the columns they select: drop the views before dropping or
// retyping one of those columns. They can go once no supported upgrade starts
// from a release that used the legacy names.
func createLegacyViews(ctx context.Context, q sqlx.Querier) error {
	columns, err := tableColumns(ctx, q, sqlx.PostgreSQL, attemptsTable)
	if err != nil {
		return err
	}
	statements := []string{"CREATE VIEW " + legacyRequestLogTable + " AS SELECT * FROM " + requestLogTable}
	if len(columns) > 0 {
		for i, column := range columns {
			if column == attemptsLogID {
				columns[i] = attemptsLogID + " AS " + legacyAttemptsLogID
			}
		}
		statements = append(statements,
			"CREATE VIEW "+legacyAttemptsTable+" AS SELECT "+strings.Join(columns, ", ")+" FROM "+attemptsTable)
	}
	for _, statement := range statements {
		if _, err := q.Exec(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

// relationKind reports whether name is a table, a view or (empty) neither.
func relationKind(ctx context.Context, q sqlx.Querier, dialect sqlx.Dialect, name string) (string, error) {
	var query string
	switch dialect {
	case sqlx.SQLite:
		query = `SELECT type FROM sqlite_master WHERE name = ? AND type IN ('table', 'view')`
	case sqlx.PostgreSQL:
		// 'p' is a partitioned table, which an operator may have made of it.
		query = `SELECT CASE WHEN relkind IN ('r', 'p') THEN '` + relationTable + `' WHEN relkind IN ('v', 'm') THEN '` + relationView + `' ELSE '' END
			FROM pg_class WHERE relnamespace = current_schema()::regnamespace AND relname = ?`
	default:
		return "", fmt.Errorf("unsupported dialect %q", dialect)
	}
	var kind string
	err := q.QueryRow(ctx, query, name).Scan(&kind)
	if errors.Is(err, sqlx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("inspect %s: %w", name, err)
	}
	return kind, nil
}

// tableColumns lists table's columns in declaration order; none when it does
// not exist. The SQLite query interpolates table, so it must be a fixed name.
func tableColumns(ctx context.Context, q sqlx.Querier, dialect sqlx.Dialect, table string) ([]string, error) {
	switch dialect {
	case sqlx.SQLite:
		return queryStrings(ctx, q, fmt.Sprintf(`SELECT name FROM pragma_table_info('%s') ORDER BY cid`, table))
	case sqlx.PostgreSQL:
		return queryStrings(ctx, q, `SELECT column_name FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = ? ORDER BY ordinal_position`, table)
	default:
		return nil, fmt.Errorf("unsupported dialect %q", dialect)
	}
}

// sharedColumns lists the columns of from that into has as well.
func sharedColumns(ctx context.Context, q sqlx.Querier, dialect sqlx.Dialect, from, into string) ([]string, error) {
	columns, err := tableColumns(ctx, q, dialect, from)
	if err != nil {
		return nil, err
	}
	target, err := tableColumns(ctx, q, dialect, into)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(columns, func(column string) bool { return !slices.Contains(target, column) }), nil
}

func queryStrings(ctx context.Context, q sqlx.Querier, query string, args ...any) ([]string, error) {
	rows, err := q.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

func stringArgs(values []string) []any {
	args := make([]any, len(values))
	for i, value := range values {
		args[i] = value
	}
	return args
}
