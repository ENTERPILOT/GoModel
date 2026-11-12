//go:build duckdb

package storage

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "github.com/duckdb/duckdb-go/v2" // DuckDB driver (cgo)

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

type duckdbStorage struct {
	db      *sql.DB
	wrapped sqlx.DB
}

// NewDuckDB opens a DuckDB database file. DuckDB takes an exclusive file lock,
// so only one gateway process can open it read-write.
func NewDuckDB(cfg DuckDBConfig) (DuckDBStorage, error) {
	if cfg.Path == "" {
		cfg.Path = DefaultDuckDBPath()
	}
	dir := filepath.Dir(cfg.Path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create directory %s: %w", dir, err)
	}
	db, err := sql.Open("duckdb", cfg.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to open DuckDB database: %w", err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("failed to open DuckDB database: %w", err)
	}
	wrapped, err := sqlx.NewDuckDB(db)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return &duckdbStorage{db: db, wrapped: wrapped}, nil
}

func (s *duckdbStorage) DuckDB() sqlx.DB { return s.wrapped }

func (s *duckdbStorage) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

func (s *duckdbStorage) Close() error { return s.db.Close() }
