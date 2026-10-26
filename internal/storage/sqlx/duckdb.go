package sqlx

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"sync"
)

// duckdbSerialSequence backs every TypeSerialPK column on DuckDB, which has no
// SERIAL or AUTOINCREMENT. One shared sequence keeps the token expandable in
// place; ids stay unique per table, just not dense.
const duckdbSerialSequence = "sqlx_serial_pk"

// duckdbDB adapts a database/sql handle opened with the DuckDB driver to DB.
// DuckDB accepts `?` placeholders and reports sql.ErrNoRows like SQLite, so it
// reuses the database/sql helpers unchanged.
//
// DuckDB's concurrency control is optimistic: two writers touching the same
// row do not wait for each other, the later one fails with "Conflict on
// update". SQLite serializes writers (one connection) and PostgreSQL makes
// them wait on the row lock, and store code relies on that. writeMu restores
// it by serializing Exec and InTx in the process, while Query and QueryRow
// stay concurrent: DuckDB's MVCC lets reads run alongside the one writer.
type duckdbDB struct {
	db      *sql.DB
	writeMu sync.Mutex
}

// NewDuckDB wraps a DuckDB handle. The caller retains ownership.
func NewDuckDB(db *sql.DB) (DB, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection is required")
	}
	return &duckdbDB{db: db}, nil
}

func (d *duckdbDB) Dialect() Dialect { return DuckDB }

func (d *duckdbDB) Exec(ctx context.Context, query string, args ...any) (int64, error) {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	return sqlExec(ctx, d.db, query, args...)
}

func (d *duckdbDB) Query(ctx context.Context, query string, args ...any) (Rows, error) {
	return sqlQuery(ctx, d.db, query, args...)
}

func (d *duckdbDB) QueryRow(ctx context.Context, query string, args ...any) Row {
	return sqlQueryRow(ctx, d.db, query, args...)
}

func (d *duckdbDB) Schema(ctx context.Context, statements ...string) error {
	if _, err := d.Exec(ctx, `CREATE SEQUENCE IF NOT EXISTS `+duckdbSerialSequence); err != nil {
		return fmt.Errorf("create serial sequence: %w", err)
	}
	rewritten := make([]string, len(statements))
	for i, statement := range statements {
		rewritten[i] = duckdbForeignKey.ReplaceAllString(statement, "")
	}
	return execSchema(ctx, d, DuckDB, rewritten)
}

// duckdbForeignKey matches a trailing table-level FOREIGN KEY clause. DuckDB
// rejects ON DELETE CASCADE, and a plain foreign key would block deleting the
// parent row, so the clause is dropped and stores delete children explicitly
// (auditlog cleanup already does).
var duckdbForeignKey = regexp.MustCompile(`(?is),\s*FOREIGN KEY\s*\([^)]*\)\s*REFERENCES[^,)]*\([^)]*\)(\s*ON DELETE \w+)?`)

// InTx runs fn in a database/sql transaction, holding writeMu throughout.
func (d *duckdbDB) InTx(ctx context.Context, fn func(Querier) error) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err := fn(&duckdbQuerier{tx: tx}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	committed = true
	return nil
}

type duckdbQuerier struct {
	tx *sql.Tx
}

func (q *duckdbQuerier) Exec(ctx context.Context, query string, args ...any) (int64, error) {
	return sqlExec(ctx, q.tx, query, args...)
}

func (q *duckdbQuerier) Query(ctx context.Context, query string, args ...any) (Rows, error) {
	return sqlQuery(ctx, q.tx, query, args...)
}

func (q *duckdbQuerier) QueryRow(ctx context.Context, query string, args ...any) Row {
	return sqlQueryRow(ctx, q.tx, query, args...)
}
