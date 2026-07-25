package sqlx

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// postgresDB adapts a pgx connection pool to DB. Queries arrive with `?`
// placeholders and are rebound to $1, $2, ... on the way through.
type postgresDB struct {
	pool *pgxpool.Pool
}

// NewPostgreSQL wraps a pgx pool. The caller retains ownership of the pool.
func NewPostgreSQL(pool *pgxpool.Pool) (DB, error) {
	if pool == nil {
		return nil, fmt.Errorf("connection pool is required")
	}
	return &postgresDB{pool: pool}, nil
}

func (p *postgresDB) Dialect() Dialect { return PostgreSQL }

func (p *postgresDB) Exec(ctx context.Context, query string, args ...any) (int64, error) {
	return pgExec(ctx, p.pool, query, args...)
}

func (p *postgresDB) Query(ctx context.Context, query string, args ...any) (Rows, error) {
	return pgQuery(ctx, p.pool, query, args...)
}

func (p *postgresDB) QueryRow(ctx context.Context, query string, args ...any) Row {
	return pgQueryRow(ctx, p.pool, query, args...)
}

func (p *postgresDB) Schema(ctx context.Context, statements ...string) error {
	return execSchema(ctx, p, PostgreSQL, statements)
}

func (p *postgresDB) InTx(ctx context.Context, fn func(Querier) error) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			// Rollback on an already-finished transaction is a no-op error.
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()
	if err := fn(&postgresQuerier{tx: tx}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	committed = true
	return nil
}

// postgresQuerier adapts an open pgx transaction to Querier.
type postgresQuerier struct {
	tx pgx.Tx
}

func (p *postgresQuerier) Exec(ctx context.Context, query string, args ...any) (int64, error) {
	return pgExec(ctx, p.tx, query, args...)
}

func (p *postgresQuerier) Query(ctx context.Context, query string, args ...any) (Rows, error) {
	return pgQuery(ctx, p.tx, query, args...)
}

func (p *postgresQuerier) QueryRow(ctx context.Context, query string, args ...any) Row {
	return pgQueryRow(ctx, p.tx, query, args...)
}

// pgExecutor is the subset of *pgxpool.Pool and pgx.Tx this adapter needs.
type pgExecutor interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func pgExec(ctx context.Context, e pgExecutor, query string, args ...any) (int64, error) {
	tag, err := e.Exec(ctx, rebind(query), args...)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func pgQuery(ctx context.Context, e pgExecutor, query string, args ...any) (Rows, error) {
	rows, err := e.Query(ctx, rebind(query), args...)
	if err != nil {
		return nil, err
	}
	return &postgresRows{rows: rows}, nil
}

func pgQueryRow(ctx context.Context, e pgExecutor, query string, args ...any) Row {
	return &postgresRow{row: e.QueryRow(ctx, rebind(query), args...)}
}

type postgresRow struct {
	row pgx.Row
}

func (r *postgresRow) Scan(dest ...any) error {
	err := r.row.Scan(dest...)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNoRows
	}
	return err
}

type postgresRows struct {
	rows pgx.Rows
}

func (r *postgresRows) Next() bool          { return r.rows.Next() }
func (r *postgresRows) Scan(d ...any) error { return r.rows.Scan(d...) }
func (r *postgresRows) Err() error          { return r.rows.Err() }
func (r *postgresRows) Close()              { r.rows.Close() }
