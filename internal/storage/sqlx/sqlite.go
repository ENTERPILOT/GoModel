package sqlx

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// sqliteDB adapts a database/sql handle to DB.
type sqliteDB struct {
	db *sql.DB
}

// NewSQLite wraps a SQLite handle. The caller retains ownership: closing the
// returned DB is not this package's concern, matching how stores have always
// left connection lifecycle to the storage layer.
func NewSQLite(db *sql.DB) (DB, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection is required")
	}
	return &sqliteDB{db: db}, nil
}

func (s *sqliteDB) Dialect() Dialect { return SQLite }

func (s *sqliteDB) Exec(ctx context.Context, query string, args ...any) (int64, error) {
	return sqlExec(ctx, s.db, query, args...)
}

func (s *sqliteDB) Query(ctx context.Context, query string, args ...any) (Rows, error) {
	return sqlQuery(ctx, s.db, query, args...)
}

func (s *sqliteDB) QueryRow(ctx context.Context, query string, args ...any) Row {
	return sqlQueryRow(ctx, s.db, query, args...)
}

func (s *sqliteDB) Schema(ctx context.Context, statements ...string) error {
	return execSchema(ctx, s, SQLite, statements)
}

// InTx runs fn in a BEGIN IMMEDIATE transaction on a dedicated connection.
//
// database/sql's BeginTx can only start SQLite's default deferred
// transaction, which takes the write lock lazily. A read-then-write sequence —
// every InTx caller here reads a MAX or an existing row before writing — can
// then have two transactions both read, then collide when the second tries to
// upgrade, surfacing as SQLITE_BUSY rather than one waiting for the other.
// Taking the write lock up front serializes them properly.
func (s *sqliteDB) InTx(ctx context.Context, fn func(Querier) error) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			// Roll back on a live context: the caller's may already be done.
			_, _ = conn.ExecContext(context.WithoutCancel(ctx), `ROLLBACK`)
		}
	}()

	if err := fn(&sqliteQuerier{conn: conn}); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	committed = true
	return nil
}

// sqliteQuerier adapts a connection inside an open transaction to Querier.
type sqliteQuerier struct {
	conn *sql.Conn
}

func (s *sqliteQuerier) Exec(ctx context.Context, query string, args ...any) (int64, error) {
	return sqlExec(ctx, s.conn, query, args...)
}

func (s *sqliteQuerier) Query(ctx context.Context, query string, args ...any) (Rows, error) {
	return sqlQuery(ctx, s.conn, query, args...)
}

func (s *sqliteQuerier) QueryRow(ctx context.Context, query string, args ...any) Row {
	return sqlQueryRow(ctx, s.conn, query, args...)
}

// sqlExecutor is the subset of *sql.DB and *sql.Tx this adapter needs.
type sqlExecutor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func sqlExec(ctx context.Context, e sqlExecutor, query string, args ...any) (int64, error) {
	result, err := e.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("read rows affected: %w", err)
	}
	return affected, nil
}

func sqlQuery(ctx context.Context, e sqlExecutor, query string, args ...any) (Rows, error) {
	rows, err := e.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return &sqliteRows{rows: rows}, nil
}

func sqlQueryRow(ctx context.Context, e sqlExecutor, query string, args ...any) Row {
	return &sqliteRow{row: e.QueryRowContext(ctx, query, args...)}
}

type sqliteRow struct {
	row *sql.Row
}

func (r *sqliteRow) Scan(dest ...any) error {
	err := r.row.Scan(dest...)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNoRows
	}
	return err
}

type sqliteRows struct {
	rows *sql.Rows
}

func (r *sqliteRows) Next() bool          { return r.rows.Next() }
func (r *sqliteRows) Scan(d ...any) error { return r.rows.Scan(d...) }
func (r *sqliteRows) Err() error          { return r.rows.Err() }
func (r *sqliteRows) Close()              { _ = r.rows.Close() }
