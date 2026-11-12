//go:build duckdb

package sqlxtest

import (
	"database/sql"
	"testing"

	_ "github.com/duckdb/duckdb-go/v2" // DuckDB driver for the in-memory test database

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

func init() { newDuckDB = NewDuckDB }

// NewDuckDB returns an empty in-memory DuckDB database for one test. Every
// connection from one sql.Open shares the same in-memory database.
func NewDuckDB(t *testing.T) sqlx.DB {
	t.Helper()

	raw, err := sql.Open("duckdb", "")
	if err != nil {
		t.Fatalf("open duckdb: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })

	db, err := sqlx.NewDuckDB(raw)
	if err != nil {
		t.Fatalf("wrap duckdb: %v", err)
	}
	return db
}
