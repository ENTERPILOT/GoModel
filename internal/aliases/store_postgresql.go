package aliases

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgreSQLStore stores aliases in PostgreSQL.
type PostgreSQLStore struct {
	pool *pgxpool.Pool
}

// NewPostgreSQLStore creates the aliases table and indexes if needed.
func NewPostgreSQLStore(ctx context.Context, pool *pgxpool.Pool) (*PostgreSQLStore, error) {
	if ctx == nil {
		return nil, fmt.Errorf("context is required")
	}
	if pool == nil {
		return nil, fmt.Errorf("connection pool is required")
	}

	_, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS aliases (
			name TEXT PRIMARY KEY,
			target_model TEXT NOT NULL,
			target_provider TEXT NOT NULL DEFAULT '',
			description TEXT NOT NULL DEFAULT '',
			enabled BOOLEAN NOT NULL DEFAULT TRUE,
			created_at BIGINT NOT NULL,
			updated_at BIGINT NOT NULL
		)
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to create aliases table: %w", err)
	}
	if _, err := pool.Exec(ctx, `CREATE INDEX IF NOT EXISTS idx_aliases_enabled ON aliases(enabled)`); err != nil {
		return nil, fmt.Errorf("failed to create aliases enabled index: %w", err)
	}
	if _, err := pool.Exec(ctx, `CREATE INDEX IF NOT EXISTS idx_aliases_updated_at ON aliases(updated_at DESC)`); err != nil {
		return nil, fmt.Errorf("failed to create aliases updated_at index: %w", err)
	}
	return &PostgreSQLStore{pool: pool}, nil
}

func (s *PostgreSQLStore) List(ctx context.Context) ([]Alias, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT name, target_model, target_provider, description, enabled, created_at, updated_at
		FROM aliases
		ORDER BY name ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list aliases: %w", err)
	}
	defer rows.Close()
	result, err := collectAliases(rows, scanPostgreSQLAlias)
	if err != nil {
		return nil, fmt.Errorf("iterate aliases: %w", err)
	}
	return result, nil
}

func (s *PostgreSQLStore) Get(ctx context.Context, name string) (*Alias, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT name, target_model, target_provider, description, enabled, created_at, updated_at
		FROM aliases
		WHERE name = $1
	`, normalizeName(name))
	alias, err := scanPostgreSQLAlias(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &alias, nil
}

func (s *PostgreSQLStore) Upsert(ctx context.Context, alias Alias) error {
	alias, err := normalizeAlias(alias)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Unix()
	if alias.CreatedAt.IsZero() {
		alias.CreatedAt = time.Unix(now, 0).UTC()
	}
	alias.UpdatedAt = time.Unix(now, 0).UTC()

	_, err = s.pool.Exec(ctx, `
		INSERT INTO aliases (name, target_model, target_provider, description, enabled, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT(name) DO UPDATE SET
			target_model = excluded.target_model,
			target_provider = excluded.target_provider,
			description = excluded.description,
			enabled = excluded.enabled,
			updated_at = excluded.updated_at
	`, alias.Name, alias.TargetModel, alias.TargetProvider, alias.Description, alias.Enabled, alias.CreatedAt.Unix(), alias.UpdatedAt.Unix())
	if err != nil {
		return fmt.Errorf("upsert alias: %w", err)
	}
	return nil
}

func (s *PostgreSQLStore) Delete(ctx context.Context, name string) error {
	cmd, err := s.pool.Exec(ctx, `DELETE FROM aliases WHERE name = $1`, normalizeName(name))
	if err != nil {
		return fmt.Errorf("delete alias: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgreSQLStore) Close() error {
	return nil
}

func scanPostgreSQLAlias(scanner aliasScanner) (Alias, error) {
	var alias Alias
	var createdAt int64
	var updatedAt int64
	if err := scanner.Scan(
		&alias.Name,
		&alias.TargetModel,
		&alias.TargetProvider,
		&alias.Description,
		&alias.Enabled,
		&createdAt,
		&updatedAt,
	); err != nil {
		return Alias{}, err
	}
	alias.CreatedAt = time.Unix(createdAt, 0).UTC()
	alias.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return alias, nil
}
