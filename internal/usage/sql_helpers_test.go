package usage

import (
	"context"
	"database/sql"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// newSQLiteStore and newSQLiteReader wrap a raw SQLite handle, for the tests
// that also seed or inspect the table with SQLite-specific SQL.
func newSQLiteStore(db *sql.DB, retentionDays int) (*SQLStore, error) {
	wrapped, err := sqlx.NewSQLite(db)
	if err != nil {
		return nil, err
	}
	return NewSQLStore(context.Background(), wrapped, retentionDays)
}

func newSQLiteReader(db *sql.DB) (*SQLReader, error) {
	wrapped, err := sqlx.NewSQLite(db)
	if err != nil {
		return nil, err
	}
	return NewSQLReader(wrapped)
}
