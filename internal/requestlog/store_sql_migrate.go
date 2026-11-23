package requestlog

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// The request log was stored as the "audit log" up to v0.2.5. These are the
// names that release created, which adoptLegacyTables moves off.
//
// The indexes keep their idx_audit_ names. SQLite cannot rename an index, and
// rebuilding every one of them at the first start took about 40 seconds per
// million rows when measured, long enough for a liveness probe to stop the
// gateway before it finished.
const (
	legacyRequestLogTable = "audit_logs"
	legacyAttemptsTable   = "audit_log_attempts"
	legacyAttemptsLogID   = "audit_log_id"
)

const (
	attemptsTable = "request_log_attempts"
	attemptsLogID = "request_log_id"
)

const (
	relationTable = "table"
	relationView  = "view"
)

// adoptLegacyStorage renames the legacy tables under the schema lock, for the
// readers: they work without a store when request logging is turned off, and
// must still find the history an older release wrote. Rows in legacy tables an
// older binary recreated after the rename are the store's to move.
func adoptLegacyStorage(ctx context.Context, db sqlx.DB) error {
	// Checked before taking the lock, which another replica can hold for as
	// long as an index build runs: with nothing to rename, a reader does not
	// wait for it.
	legacy, err := relationKind(ctx, db, db.Dialect(), legacyRequestLogTable)
	if err != nil || legacy != relationTable {
		return err
	}
	current, err := relationKind(ctx, db, db.Dialect(), requestLogTable)
	if err != nil || current != "" {
		return err
	}
	return withSchemaLock(ctx, db, func(session sqlx.DB) error { return adoptLegacyTables(ctx, session, false) })
}

// adoptLegacyTables moves a database written by v0.2.5 or older onto the
// request_logs names. It runs on every start and does nothing once the legacy
// tables are gone. The caller holds the schema lock (see withSchemaLock).
//
// A legacy table is renamed in place, which is immediate. That has to happen
// before the tables are created, or there would be an empty request_logs in
// the way.
//
// When request_logs exists as well, an older binary ran against the database
// after the rename (a rollback) and recreated the legacy tables: with absorb,
// its rows are moved over and the tables dropped. That needs both request log
// tables in their current shape, so only the store asks for it, once it has
// created and migrated them.
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
		slog.Info("requestlog: moved the audit_logs tables to request_logs")
	}
	return nil
}

// renameLegacyTables renames the legacy tables and the attempt table's foreign
// key column. Their indexes follow them.
func renameLegacyTables(ctx context.Context, q sqlx.Querier, dialect sqlx.Dialect) error {
	attempts, err := relationKind(ctx, q, dialect, legacyAttemptsTable)
	if err != nil {
		return err
	}
	statements := []string{"ALTER TABLE " + legacyRequestLogTable + " RENAME TO " + requestLogTable}
	if attempts == relationTable {
		statements = append(statements,
			"ALTER TABLE "+legacyAttemptsTable+" RENAME TO "+attemptsTable,
			"ALTER TABLE "+attemptsTable+" RENAME COLUMN "+legacyAttemptsLogID+" TO "+attemptsLogID,
		)
	}
	if dialect == sqlx.PostgreSQL {
		renames, err := postgresConstraintRenames(ctx, q)
		if err != nil {
			return err
		}
		statements = append(statements, renames...)
	}
	return execAll(ctx, q, statements)
}

// postgresConstraintRenames returns the statements that rename the constraints
// and the sequence PostgreSQL named after the legacy tables, so an upgraded
// schema matches a fresh one. They run after the tables are renamed, and
// renaming a primary key or unique constraint renames its index with it.
func postgresConstraintRenames(ctx context.Context, q sqlx.Querier) ([]string, error) {
	rows, err := q.Query(ctx, `SELECT t.relname::text, c.conname::text FROM pg_constraint c
		JOIN pg_class t ON t.oid = c.conrelid
		WHERE t.relnamespace = current_schema()::regnamespace AND t.relname IN (?, ?)`,
		legacyRequestLogTable, legacyAttemptsTable)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var statements []string
	for rows.Next() {
		var table, name string
		if err := rows.Scan(&table, &name); err != nil {
			return nil, err
		}
		// audit_logs_pkey, audit_log_attempts_audit_log_id_fkey: the table's
		// name, then the column's.
		rest, ok := strings.CutPrefix(name, table+"_")
		if !ok {
			continue
		}
		renamed := requestLogTable
		if table == legacyAttemptsTable {
			renamed = attemptsTable
			rest = strings.Replace(rest, legacyAttemptsLogID, attemptsLogID, 1)
		}
		statements = append(statements, "ALTER TABLE "+renamed+" RENAME CONSTRAINT "+name+" TO "+renamed+"_"+rest)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return append(statements,
		"ALTER SEQUENCE IF EXISTS "+legacyAttemptsTable+"_id_seq RENAME TO "+attemptsTable+"_id_seq"), nil
}

// absorbLegacyTables moves the rows of legacy tables that exist next to the
// request log tables into them, then drops the legacy tables. Rows are matched
// on the columns both sides have, so an older binary's narrower table is fine.
func absorbLegacyTables(ctx context.Context, q sqlx.Querier, dialect sqlx.Dialect) error {
	attempts, err := relationKind(ctx, q, dialect, legacyAttemptsTable)
	if err != nil {
		return err
	}
	var statements []string
	if dialect == sqlx.PostgreSQL {
		// A replica on the older release may be writing these tables. Without
		// the lock, a row it commits between the copy and the drop is lost;
		// with it, that write waits and then goes through the views. Entries
		// before attempts, the order the writers take them in.
		lock := "LOCK TABLE " + legacyRequestLogTable
		if attempts == relationTable {
			lock += ", " + legacyAttemptsTable
		}
		statements = append(statements, lock+" IN ACCESS EXCLUSIVE MODE")
	}

	columns, err := sharedColumns(ctx, q, dialect, legacyRequestLogTable, requestLogTable)
	if err != nil {
		return err
	}
	list := strings.Join(columns, ", ")
	// "WHERE 1 = 1" keeps SQLite from parsing ON CONFLICT as a join constraint
	// of the SELECT.
	statements = append(statements,
		"INSERT INTO "+requestLogTable+" ("+list+") SELECT "+list+" FROM "+legacyRequestLogTable+
			" WHERE 1 = 1 ON CONFLICT (id) DO NOTHING")

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
	return execAll(ctx, q, append(statements, "DROP TABLE "+legacyRequestLogTable))
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
		err := execAll(ctx, q, []string{
			"DROP VIEW IF EXISTS " + legacyAttemptsTable,
			"DROP VIEW IF EXISTS " + legacyRequestLogTable,
		})
		if err != nil {
			return err
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
		view, err := sqlx.ColumnNames(ctx, q, sqlx.PostgreSQL, pair[0])
		if err != nil {
			return false, err
		}
		table, err := sqlx.ColumnNames(ctx, q, sqlx.PostgreSQL, pair[1])
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
	columns, err := sqlx.ColumnNames(ctx, q, sqlx.PostgreSQL, attemptsTable)
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
	return execAll(ctx, q, statements)
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

// sharedColumns lists the columns of from that into has as well.
func sharedColumns(ctx context.Context, q sqlx.Querier, dialect sqlx.Dialect, from, into string) ([]string, error) {
	columns, err := sqlx.ColumnNames(ctx, q, dialect, from)
	if err != nil {
		return nil, err
	}
	target, err := sqlx.ColumnNames(ctx, q, dialect, into)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(columns, func(column string) bool { return !slices.Contains(target, column) }), nil
}

func execAll(ctx context.Context, q sqlx.Querier, statements []string) error {
	for _, statement := range statements {
		if _, err := q.Exec(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
