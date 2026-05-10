package filestore

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgreSQLStore stores file provider mappings in PostgreSQL.
type PostgreSQLStore struct {
	pool *pgxpool.Pool
}

// NewPostgreSQLStore creates the file_mappings table and indexes if needed.
func NewPostgreSQLStore(ctx context.Context, pool *pgxpool.Pool) (*PostgreSQLStore, error) {
	if ctx == nil {
		return nil, fmt.Errorf("context is required")
	}
	if pool == nil {
		return nil, fmt.Errorf("connection pool is required")
	}
	_, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS file_mappings (
			id TEXT PRIMARY KEY,
			provider_type TEXT NOT NULL,
			purpose TEXT NOT NULL DEFAULT '',
			filename TEXT NOT NULL DEFAULT '',
			bytes BIGINT NOT NULL DEFAULT 0,
			created_at BIGINT NOT NULL DEFAULT 0,
			updated_at BIGINT NOT NULL,
			user_path TEXT NOT NULL DEFAULT ''
		)
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to create file_mappings table: %w", err)
	}
	if _, err := pool.Exec(ctx, "CREATE INDEX IF NOT EXISTS idx_file_mappings_provider_type ON file_mappings(provider_type)"); err != nil {
		return nil, fmt.Errorf("failed to create file_mappings provider index: %w", err)
	}
	if _, err := pool.Exec(ctx, "CREATE INDEX IF NOT EXISTS idx_file_mappings_created_at ON file_mappings(created_at DESC)"); err != nil {
		return nil, fmt.Errorf("failed to create file_mappings created_at index: %w", err)
	}
	return &PostgreSQLStore{pool: pool}, nil
}

// Upsert creates or replaces a file mapping.
func (s *PostgreSQLStore) Upsert(ctx context.Context, file *StoredFile) error {
	normalized, err := normalizeStoredFile(file)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO file_mappings (id, provider_type, purpose, filename, bytes, created_at, updated_at, user_path)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
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
func (s *PostgreSQLStore) Get(ctx context.Context, id string) (*StoredFile, error) {
	return scanStoredFile(s.pool.QueryRow(ctx, `
		SELECT id, provider_type, purpose, filename, bytes, created_at, user_path
		FROM file_mappings
		WHERE id = $1
	`, id), pgx.ErrNoRows)
}

// Delete removes one file mapping by id.
func (s *PostgreSQLStore) Delete(ctx context.Context, id string) error {
	cmd, err := s.pool.Exec(ctx, "DELETE FROM file_mappings WHERE id = $1", id)
	if err != nil {
		return fmt.Errorf("delete file mapping: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Close is a no-op; pool lifecycle is managed by storage layer.
func (s *PostgreSQLStore) Close() error {
	return nil
}
