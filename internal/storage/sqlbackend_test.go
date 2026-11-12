package storage

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
)

// fakeDuckDBStorage is a DuckDBStorage without the cgo driver.
type fakeDuckDBStorage struct{ db sqlx.DB }

func (s fakeDuckDBStorage) DuckDB() sqlx.DB { return s.db }
func (fakeDuckDBStorage) Close() error      { return nil }

// Every store on a DuckDB backend gets the backend's own sqlx.DB. A wrapper
// per store would give each its own write lock, and DuckDB fails conflicting
// writers instead of making them wait.
func TestResolveSQLBackendSharesDuckDBWrapper(t *testing.T) {
	shared := sqlxtest.NewSQLite(t)
	store := fakeDuckDBStorage{db: shared}

	for range 2 {
		got, err := ResolveSQLBackend(context.Background(), store,
			func(db sqlx.DB) (sqlx.DB, error) { return db, nil }, nil)
		require.NoError(t, err)
		assert.Same(t, shared, got)
	}
}
