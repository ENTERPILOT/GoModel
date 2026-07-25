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
// It replaces ResolveBackend for stores that have been unified. ResolveBackend
// remains for the readers and stores that still branch per SQL dialect.
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
