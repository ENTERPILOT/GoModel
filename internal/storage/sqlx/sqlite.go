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

func (s *sqliteDB) InTx(ctx context.Context, fn func(Querier) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err := fn(&sqliteQuerier{tx: tx}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	committed = true
	return nil
}

// sqliteQuerier adapts an open database/sql transaction to Querier.
type sqliteQuerier struct {
	tx *sql.Tx
}

func (s *sqliteQuerier) Exec(ctx context.Context, query string, args ...any) (int64, error) {
	return sqlExec(ctx, s.tx, query, args...)
}

func (s *sqliteQuerier) Query(ctx context.Context, query string, args ...any) (Rows, error) {
	return sqlQuery(ctx, s.tx, query, args...)
}

func (s *sqliteQuerier) QueryRow(ctx context.Context, query string, args ...any) Row {
	return sqlQueryRow(ctx, s.tx, query, args...)
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
