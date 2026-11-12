package sqlx

import (
	"context"
	"fmt"
	"strings"
)

// execSchema runs DDL statements in order with portable type tokens expanded.
//
// On SQLite the statements run outside a transaction: it cannot run every
// schema change transactionally, and `CREATE TABLE IF NOT EXISTS` / `CREATE
// INDEX IF NOT EXISTS` are individually idempotent, so a partial application
// is safe to retry on the next start. PostgreSQL wraps them in a locked
// transaction instead; see postgresDB.Schema.
func execSchema(ctx context.Context, q Querier, dialect Dialect, statements []string) error {
	for _, statement := range statements {
		if _, err := q.Exec(ctx, dialect.ExpandTypes(statement)); err != nil {
			return fmt.Errorf("apply schema statement: %w", err)
		}
	}
	return nil
}

// AddColumns applies `ALTER TABLE <table> ADD COLUMN <column> ...` migrations,
// expanding type tokens and running only those whose column is missing.
//
// Store constructors call this on every start, so on an up-to-date schema it
// must not run any ALTER: PostgreSQL takes an ACCESS EXCLUSIVE lock for
// `ALTER TABLE ... ADD COLUMN IF NOT EXISTS` even when the column exists, and
// a waiting ALTER queues every later read and write on the table behind
// whatever long query it is waiting for. Each table's columns are read once
// (see Columns) and applied migrations are skipped.
//
// A statement that does not have that shape always runs. The duplicate-column
// error is still tolerated, and PostgreSQL still gets the IF NOT EXISTS
// spelling (see addColumnStatement), because replicas starting together can
// all see a column missing and race to add it.
func AddColumns(ctx context.Context, db DB, statements ...string) error {
	pending, err := missingColumnStatements(ctx, db, statements)
	if err != nil {
		return err
	}
	dialect := db.Dialect()
	for _, statement := range pending {
		_, err := db.Exec(ctx, dialect.addColumnStatement(statement))
		if err != nil && !IsDuplicateColumnError(err) {
			return fmt.Errorf("apply migration %q: %w", statement, err)
		}
	}
	return nil
}

// missingColumnStatements drops the statements whose column already exists,
// reading each table's columns at most once.
func missingColumnStatements(ctx context.Context, db DB, statements []string) ([]string, error) {
	existing := make(map[string]map[string]bool)
	pending := make([]string, 0, len(statements))
	for _, statement := range statements {
		table, column, ok := parseAddColumn(statement)
		if !ok {
			pending = append(pending, statement)
			continue
		}
		columns, seen := existing[table]
		if !seen {
			var err error
			if columns, err = Columns(ctx, db, table); err != nil {
				return nil, err
			}
			existing[table] = columns
		}
		if !columns[column] {
			pending = append(pending, statement)
		}
	}
	return pending, nil
}

// parseAddColumn extracts the lowercased table and column from an
// `ALTER TABLE <table> ADD COLUMN [IF NOT EXISTS] <column> ...` statement.
// ok is false for any other shape, and for quoted or schema-qualified names.
func parseAddColumn(statement string) (table, column string, ok bool) {
	f := strings.Fields(statement)
	if len(f) < 6 || !keywords(f[:2], "ALTER", "TABLE") || !keywords(f[3:5], "ADD", "COLUMN") {
		return "", "", false
	}
	table, column = f[2], f[5]
	if len(f) >= 9 && keywords(f[5:8], "IF", "NOT", "EXISTS") {
		column = f[8]
	}
	if !isPlainIdentifier(table) || !isPlainIdentifier(column) {
		return "", "", false
	}
	return strings.ToLower(table), strings.ToLower(column), true
}

func keywords(fields []string, want ...string) bool {
	for i, w := range want {
		if !strings.EqualFold(fields[i], w) {
			return false
		}
	}
	return true
}

// isPlainIdentifier reports whether name is an unquoted SQL identifier, which
// both engines match case-insensitively.
func isPlainIdentifier(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		switch {
		case r == '_', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// Columns reports table's current column names, lowercased; a missing table
// has none. On PostgreSQL the table is looked up in current_schema(), where
// unqualified store SQL resolves it.
//
// The SQLite query interpolates table, so it must be a fixed identifier.
func Columns(ctx context.Context, db DB, table string) (map[string]bool, error) {
	var query string
	var args []any
	switch db.Dialect() {
	case SQLite:
		query = fmt.Sprintf(`SELECT name FROM pragma_table_info('%s')`, table)
	case PostgreSQL:
		query = `SELECT column_name FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = ?`
		args = []any{table}
	default:
		return nil, fmt.Errorf("unsupported dialect %q", db.Dialect())
	}

	rows, err := db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("inspect %s columns: %w", table, err)
	}
	defer rows.Close()

	columns := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("inspect %s columns: %w", table, err)
		}
		columns[strings.ToLower(name)] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("inspect %s columns: %w", table, err)
	}
	return columns, nil
}

// addColumnStatement expands type tokens and, on PostgreSQL, turns
// `ADD COLUMN` into `ADD COLUMN IF NOT EXISTS`.
func (d Dialect) addColumnStatement(statement string) string {
	statement = d.ExpandTypes(statement)
	if d != PostgreSQL || strings.Contains(statement, "IF NOT EXISTS") {
		return statement
	}
	return strings.Replace(statement, "ADD COLUMN ", "ADD COLUMN IF NOT EXISTS ", 1)
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
