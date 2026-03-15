package aliases

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// SQLiteStore stores aliases in SQLite.
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLiteStore creates the aliases table and indexes if needed.
func NewSQLiteStore(db *sql.DB) (*SQLiteStore, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection is required")
	}

	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS aliases (
			name TEXT PRIMARY KEY,
			target_model TEXT NOT NULL,
			target_provider TEXT NOT NULL DEFAULT '',
			description TEXT NOT NULL DEFAULT '',
			enabled INTEGER NOT NULL DEFAULT 1,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to create aliases table: %w", err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_aliases_enabled ON aliases(enabled)`); err != nil {
		return nil, fmt.Errorf("failed to create aliases enabled index: %w", err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_aliases_updated_at ON aliases(updated_at DESC)`); err != nil {
		return nil, fmt.Errorf("failed to create aliases updated_at index: %w", err)
	}
	return &SQLiteStore{db: db}, nil
}

func (s *SQLiteStore) List(ctx context.Context) ([]Alias, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT name, target_model, target_provider, description, enabled, created_at, updated_at
		FROM aliases
		ORDER BY name ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list aliases: %w", err)
	}
	defer rows.Close()
	result, err := collectAliases(rows, scanSQLiteAlias)
	if err != nil {
		return nil, fmt.Errorf("iterate aliases: %w", err)
	}
	return result, nil
}

func (s *SQLiteStore) Get(ctx context.Context, name string) (*Alias, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT name, target_model, target_provider, description, enabled, created_at, updated_at
		FROM aliases
		WHERE name = ?
	`, normalizeName(name))
	alias, err := scanSQLiteAlias(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &alias, nil
}

func (s *SQLiteStore) Upsert(ctx context.Context, alias Alias) error {
	alias, err := normalizeAlias(alias)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Unix()
	if alias.CreatedAt.IsZero() {
		alias.CreatedAt = time.Unix(now, 0).UTC()
	}
	alias.UpdatedAt = time.Unix(now, 0).UTC()

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO aliases (name, target_model, target_provider, description, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			target_model = excluded.target_model,
			target_provider = excluded.target_provider,
			description = excluded.description,
			enabled = excluded.enabled,
			updated_at = excluded.updated_at
	`, alias.Name, alias.TargetModel, alias.TargetProvider, alias.Description, boolToSQLite(alias.Enabled), alias.CreatedAt.Unix(), alias.UpdatedAt.Unix())
	if err != nil {
		return fmt.Errorf("upsert alias: %w", err)
	}
	return nil
}

func (s *SQLiteStore) Delete(ctx context.Context, name string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM aliases WHERE name = ?`, normalizeName(name))
	if err != nil {
		return fmt.Errorf("delete alias: %w", err)
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

func (s *SQLiteStore) Close() error {
	return nil
}

func scanSQLiteAlias(scanner aliasScanner) (Alias, error) {
	var alias Alias
	var enabled int
	var createdAt int64
	var updatedAt int64
	if err := scanner.Scan(
		&alias.Name,
		&alias.TargetModel,
		&alias.TargetProvider,
		&alias.Description,
		&enabled,
		&createdAt,
		&updatedAt,
	); err != nil {
		return Alias{}, err
	}
	alias.Enabled = enabled != 0
	alias.CreatedAt = time.Unix(createdAt, 0).UTC()
	alias.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return alias, nil
}

func boolToSQLite(v bool) int {
	if v {
		return 1
	}
	return 0
}
