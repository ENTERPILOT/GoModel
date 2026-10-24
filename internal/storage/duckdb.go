//go:build duckdb

package storage

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "github.com/duckdb/duckdb-go/v2" // DuckDB driver (cgo)
)

type duckdbStorage struct {
	db *sql.DB
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
	return &duckdbStorage{db: db}, nil
}

func (s *duckdbStorage) DuckDB() *sql.DB { return s.db }

func (s *duckdbStorage) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

func (s *duckdbStorage) Close() error { return s.db.Close() }
