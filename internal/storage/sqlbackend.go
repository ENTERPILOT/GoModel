package storage

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// ResolveSQLBackend dispatches a storage backend to one of two constructors:
// a single SQL one serving both SQLite and PostgreSQL through sqlx.DB, and a
// MongoDB one.
//
// Every SQL store is served through it; ResolveBackend is the dispatch
// underneath.
func ResolveSQLBackend[T any](
	ctx context.Context,
	store Storage,
	sqlStore func(sqlx.DB) (T, error),
	mongodb func(*mongo.Database) (T, error),
) (T, error) {
	return ResolveBackend[T](
		store,
		func(db *sql.DB) (T, error) {
			return withSQL(db, sqlx.NewSQLite, sqlStore)
		},
		func(pool *pgxpool.Pool) (T, error) {
			return withSQL(pool, sqlx.NewPostgreSQL, sqlStore)
		},
		mongodb,
	)
}

// withSQL wraps a driver handle as a sqlx.DB and hands it to the store
// constructor.
func withSQL[H any, T any](handle H, wrap func(H) (sqlx.DB, error), sqlStore func(sqlx.DB) (T, error)) (T, error) {
	var zero T
	if sqlStore == nil {
		return zero, fmt.Errorf("sql store constructor is nil")
	}
	db, err := wrap(handle)
	if err != nil {
		return zero, err
	}
	return sqlStore(db)
}

// ResolveSQLReadBackend is ResolveSQLBackend for stores that only read. On
// SQLite the store gets ReadSQL's read-only pool, so its queries never queue
// writes behind them; other backends get what ResolveSQLBackend gives.
func ResolveSQLReadBackend[T any](
	ctx context.Context,
	store Storage,
	sqlStore func(sqlx.DB) (T, error),
	mongodb func(*mongo.Database) (T, error),
) (T, error) {
	if sqlStore == nil {
		// ResolveSQLBackend errors only when an SQL backend is selected.
		return ResolveSQLBackend(ctx, store, nil, mongodb)
	}
	return ResolveSQLBackend(ctx, store, func(db sqlx.DB) (T, error) {
		return sqlStore(ReadSQL(store, db))
	}, mongodb)
}

// ReadSQL returns the handle for a store's read-only queries: SQLite's read
// pool (see SQLiteStorage.ReadDB), otherwise db itself. Writes, and reads that
// must see a write in the same transaction, stay on db.
func ReadSQL(store Storage, db sqlx.DB) sqlx.DB {
	sqlite, ok := store.(SQLiteStorage)
	if !ok {
		return db
	}
	reads, err := sqlx.NewSQLite(sqlite.ReadDB())
	if err != nil {
		return db
	}
	return reads
}
