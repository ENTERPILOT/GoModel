package filestore

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// SQLiteStore stores file provider mappings in SQLite.
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLiteStore creates the file_mappings table and indexes if needed.
func NewSQLiteStore(db *sql.DB) (*SQLiteStore, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection is required")
	}
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS file_mappings (
			id TEXT PRIMARY KEY,
			provider_type TEXT NOT NULL,
			purpose TEXT NOT NULL DEFAULT '',
			filename TEXT NOT NULL DEFAULT '',
			bytes INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL DEFAULT 0,
			updated_at INTEGER NOT NULL,
			user_path TEXT NOT NULL DEFAULT ''
		)
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to create file_mappings table: %w", err)
	}
	if _, err := db.Exec("CREATE INDEX IF NOT EXISTS idx_file_mappings_provider_type ON file_mappings(provider_type)"); err != nil {
		return nil, fmt.Errorf("failed to create file_mappings provider index: %w", err)
	}
	if _, err := db.Exec("CREATE INDEX IF NOT EXISTS idx_file_mappings_created_at ON file_mappings(created_at DESC)"); err != nil {
		return nil, fmt.Errorf("failed to create file_mappings created_at index: %w", err)
	}
	return &SQLiteStore{db: db}, nil
}

// Upsert creates or replaces a file mapping.
func (s *SQLiteStore) Upsert(ctx context.Context, file *StoredFile) error {
	normalized, err := normalizeStoredFile(file)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO file_mappings (id, provider_type, purpose, filename, bytes, created_at, updated_at, user_path)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			provider_type = excluded.provider_type,
			purpose = excluded.purpose,
			filename = excluded.filename,
			bytes = excluded.bytes,
			updated_at = excluded.updated_at,
			user_path = excluded.user_path
	`, normalized.ID, normalized.ProviderType, normalized.Purpose, normalized.Filename, normalized.Bytes, normalized.CreatedAt, time.Now().Unix(), normalized.UserPath)
	if err != nil {
		return fmt.Errorf("upsert file mapping: %w", err)
	}
	return nil
}

// Get retrieves one file mapping by id.
func (s *SQLiteStore) Get(ctx context.Context, id string) (*StoredFile, error) {
	return scanStoredFile(s.db.QueryRowContext(ctx, `
		SELECT id, provider_type, purpose, filename, bytes, created_at, user_path
		FROM file_mappings
		WHERE id = ?
	`, id), sql.ErrNoRows)
}

// Delete removes one file mapping by id.
func (s *SQLiteStore) Delete(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM file_mappings WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete file mapping: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read delete rows affected: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// Close is a no-op; DB lifecycle is managed by storage layer.
func (s *SQLiteStore) Close() error {
	return nil
}
