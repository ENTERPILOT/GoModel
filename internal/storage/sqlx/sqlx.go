// Package sqlx lets one store implementation serve both SQL backends.
//
// SQLite and PostgreSQL stores in this repository were historically written
// twice. Comparing the two halves, the differences were never about business
// logic — they were the driver handle type, the placeholder syntax, the
// no-rows sentinel, the Exec result shape, and a handful of DDL type names.
// This package absorbs exactly those five things and nothing else.
//
// Deliberately NOT abstracted:
//
//   - Value binding and scanning. Both drivers already agree: a Go bool binds
//     to SQLite INTEGER and PostgreSQL BOOLEAN, an INTEGER scans into *bool,
//     and TEXT/JSON/JSONB all scan into *[]byte. Stores bind and scan plain Go
//     types on both backends.
//   - Genuinely dialect-specific migrations. Rewriting a table is a different
//     operation in each engine; those stay behind a Dialect() check rather
//     than pretending to be portable.
//   - MongoDB. Document semantics are not a SQL dialect and stay hand-written.
package sqlx

import (
	"context"
	"errors"
)

// ErrNoRows is returned by Row.Scan when a query selected no rows. It replaces
// the driver-specific sql.ErrNoRows and pgx.ErrNoRows so store code has one
// sentinel to match with errors.Is.
var ErrNoRows = errors.New("sqlx: no rows in result set")

// Dialect identifies a SQL backend.
type Dialect string

const (
	SQLite     Dialect = "sqlite"
	PostgreSQL Dialect = "postgresql"
)

// Row is a single-row query result.
type Row interface {
	// Scan copies the row's columns into dest. It returns ErrNoRows when the
	// query selected nothing.
	Scan(dest ...any) error
}

// Rows is a multi-row query result. Callers must Close it, and must check Err
// after the Next loop ends.
type Rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
	Close()
}

// Querier runs statements against a database or an open transaction.
//
// Queries are written with `?` placeholders regardless of backend; the
// PostgreSQL adapter rewrites them to $1, $2, ... in order. A `?` inside a
// string literal, quoted identifier, or comment is left alone.
type Querier interface {
	// Exec runs a statement and reports how many rows it affected.
	Exec(ctx context.Context, query string, args ...any) (int64, error)

	// Query runs a query returning multiple rows.
	Query(ctx context.Context, query string, args ...any) (Rows, error)

	// QueryRow runs a query returning at most one row. Errors surface from
	// the returned Row's Scan.
	QueryRow(ctx context.Context, query string, args ...any) Row
}

// DB is a database handle shared by every SQL store.
type DB interface {
	Querier

	// Dialect reports the backend, for the rare statement that cannot be
	// written portably (schema migrations, mostly).
	Dialect() Dialect

	// Schema executes DDL statements in order, expanding portable type tokens
	// (see Dialect.ExpandTypes). It is what store constructors call to create
	// their tables and indexes.
	Schema(ctx context.Context, statements ...string) error

	// InTx runs fn inside a transaction, committing when fn returns nil and
	// rolling back otherwise. A panic also rolls back before unwinding.
	InTx(ctx context.Context, fn func(Querier) error) error
}
