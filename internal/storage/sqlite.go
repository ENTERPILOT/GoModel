package storage

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // Pure Go SQLite driver
)

// sqliteStorage implements Storage for SQLite
type sqliteStorage struct {
	db *sql.DB
}

// NewSQLite creates a new SQLite storage connection.
// It enables WAL mode for better concurrent read/write performance.
func NewSQLite(cfg SQLiteConfig) (Storage, error) {
	if cfg.Path == "" {
		cfg.Path = DefaultSQLitePath
	}

	// Ensure directory exists
	dir := filepath.Dir(cfg.Path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create directory %s: %w", dir, err)
	}

	// Open database with WAL mode and busy timeout
	dsn := fmt.Sprintf("%s?_journal=WAL&_busy_timeout=5000&_synchronous=NORMAL", cfg.Path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open SQLite database: %w", err)
	}

	// Serialize all SQLite access through a single connection.
	// SQLite only permits one writer at a time. With multiple open connections,
	// concurrent flush loops (audit log + usage tracking) cause SQLITE_BUSY errors.
	// A single connection lets database/sql queue callers in Go, eliminating contention.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	// Verify connection
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to ping SQLite database: %w", err)
	}

	// Set busy_timeout explicitly as defense-in-depth. The DSN parameter may not be
	// honored reliably by the pure-Go driver across pooled connections.
	if _, err := db.Exec("PRAGMA busy_timeout = 5000"); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to set busy_timeout pragma: %w", err)
	}

	return &sqliteStorage{db: db}, nil
}

func (s *sqliteStorage) Type() string {
	return TypeSQLite
}

func (s *sqliteStorage) SQLiteDB() *sql.DB {
	return s.db
}

func (s *sqliteStorage) PostgreSQLPool() any {
	return nil
}

func (s *sqliteStorage) MongoDatabase() any {
	return nil
}

func (s *sqliteStorage) Close() error {
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}
