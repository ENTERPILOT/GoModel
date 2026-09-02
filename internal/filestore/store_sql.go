package filestore

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// SQLStore stores file provider mappings in a SQL database.
type SQLStore struct {
	db sqlx.DB
}

var sqlSchema = []string{
	`CREATE TABLE IF NOT EXISTS file_mappings (
		id TEXT PRIMARY KEY,
		provider_type TEXT NOT NULL,
		purpose TEXT NOT NULL DEFAULT '',
		filename TEXT NOT NULL DEFAULT '',
		bytes ` + sqlx.TypeInt64 + ` NOT NULL DEFAULT 0,
		created_at ` + sqlx.TypeInt64 + ` NOT NULL DEFAULT 0,
		updated_at ` + sqlx.TypeInt64 + ` NOT NULL,
		user_path TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE INDEX IF NOT EXISTS idx_file_mappings_provider_type ON file_mappings(provider_type)`,
	`CREATE INDEX IF NOT EXISTS idx_file_mappings_created_at ON file_mappings(created_at DESC)`,
}

// NewSQLStore creates the file_mappings table and indexes if needed.
func NewSQLStore(ctx context.Context, db sqlx.DB) (*SQLStore, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection is required")
	}
	if err := db.Schema(ctx, sqlSchema...); err != nil {
		return nil, fmt.Errorf("failed to create file_mappings table: %w", err)
	}
	return &SQLStore{db: db}, nil
}

// Upsert creates or replaces a file mapping.
func (s *SQLStore) Upsert(ctx context.Context, file *StoredFile) error {
	normalized, err := normalizeStoredFile(file)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `
		INSERT INTO file_mappings (id, provider_type, purpose, filename, bytes, created_at, updated_at, user_path)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			provider_type = excluded.provider_type,
			purpose = excluded.purpose,
			filename = excluded.filename,
			bytes = excluded.bytes,
			updated_at = excluded.updated_at,
			user_path = excluded.user_path
	`, normalized.ID, normalized.ProviderType, normalized.Purpose, normalized.Filename,
		normalized.Bytes, normalized.CreatedAt, time.Now().Unix(), normalized.UserPath)
	if err != nil {
		return fmt.Errorf("upsert file mapping: %w", err)
	}
	return nil
}

// Get retrieves one file mapping by id.
func (s *SQLStore) Get(ctx context.Context, id string) (*StoredFile, error) {
	return scanStoredFile(s.db.QueryRow(ctx, `
		SELECT id, provider_type, purpose, filename, bytes, created_at, user_path
		FROM file_mappings
		WHERE id = ?
	`, id))
}

// GetMany retrieves the mappings present for the given ids in one query.
func (s *SQLStore) GetMany(ctx context.Context, ids []string) (map[string]*StoredFile, error) {
	result := make(map[string]*StoredFile, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.db.Query(ctx, `
		SELECT id, provider_type, purpose, filename, bytes, created_at, user_path
		FROM file_mappings
		WHERE id IN (`+placeholders+`)
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("query file mappings: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		file, err := scanStoredFile(rows)
		if err != nil {
			return nil, err
		}
		result[file.ID] = file
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate file mappings: %w", err)
	}
	return result, nil
}

// Delete removes one file mapping by id.
func (s *SQLStore) Delete(ctx context.Context, id string) error {
	affected, err := s.db.Exec(ctx, "DELETE FROM file_mappings WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete file mapping: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// Close is a no-op; connection lifecycle is managed by the storage layer.
func (s *SQLStore) Close() error {
	return nil
}
