package filestore

import (
	"context"
	"fmt"
	"time"

	"github.com/enterpilot/gomodel/internal/storage/sqlutil"
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

// List returns the mappings matching filter, newest first, after the cursor.
func (s *SQLStore) List(ctx context.Context, filter ListFilter, limit int, after string) ([]*StoredFile, error) {
	limit = listLimit(limit)
	var conditions []string
	var args []any
	switch filter.UserPath {
	case "":
	case "/":
		conditions = append(conditions, "user_path <> ''")
	default:
		conditions = append(conditions, "(user_path = ? OR user_path LIKE ? ESCAPE '\\')")
		args = append(args, filter.UserPath, sqlutil.EscapeLikeWildcards(filter.UserPath)+"/%")
	}
	if filter.ProviderType != "" {
		conditions = append(conditions, "provider_type = ?")
		args = append(args, filter.ProviderType)
	}
	if filter.Purpose != "" {
		conditions = append(conditions, "purpose = ?")
		args = append(args, filter.Purpose)
	}
	if after != "" {
		cursor, err := s.Get(ctx, after)
		if err != nil {
			return nil, err
		}
		if !filter.matches(cursor) {
			// A cursor outside the filter is indistinguishable from a missing one.
			return nil, ErrNotFound
		}
		conditions = append(conditions, "((created_at < ?) OR (created_at = ? AND id < ?))")
		args = append(args, cursor.CreatedAt, cursor.CreatedAt, cursor.ID)
	}
	args = append(args, limit)

	rows, err := s.db.Query(ctx, `
		SELECT id, provider_type, purpose, filename, bytes, created_at, user_path
		FROM file_mappings`+sqlutil.BuildWhereClause(conditions)+`
		ORDER BY created_at DESC, id DESC
		LIMIT ?
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("list file mappings: %w", err)
	}
	defer rows.Close()
	items := make([]*StoredFile, 0, limit)
	for rows.Next() {
		file, err := scanStoredFile(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, file)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate file mappings: %w", err)
	}
	return items, nil
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
