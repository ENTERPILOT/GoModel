package usage

import (
	"context"
	"database/sql"
	"testing"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
)

// skipDuckDB skips the DuckDB run of a shared suite: the usage store refuses
// that dialect (see checkDialect).
func skipDuckDB(t *testing.T, db sqlx.DB) {
	t.Helper()
	sqlxtest.SkipDuckDB(t, db, "the usage store has no DuckDB dialect")
}

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
