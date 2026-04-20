package modeloverrides

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgreSQLStore stores model overrides in PostgreSQL.
type PostgreSQLStore struct {
	pool *pgxpool.Pool
}

// NewPostgreSQLStore creates the model_overrides table and indexes if needed.
func NewPostgreSQLStore(ctx context.Context, pool *pgxpool.Pool) (*PostgreSQLStore, error) {
	if ctx == nil {
		return nil, fmt.Errorf("context is required")
	}
	if pool == nil {
		return nil, fmt.Errorf("connection pool is required")
	}

	_, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS model_overrides (
			selector TEXT PRIMARY KEY,
			provider_name TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			user_paths JSONB NOT NULL DEFAULT '[]'::jsonb,
			created_at BIGINT NOT NULL,
			updated_at BIGINT NOT NULL
		)
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to create model_overrides table: %w", err)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE model_overrides ADD COLUMN IF NOT EXISTS user_paths JSONB NOT NULL DEFAULT '[]'::jsonb`); err != nil {
		return nil, fmt.Errorf("failed to migrate model_overrides user_paths column: %w", err)
	}
	if _, err := pool.Exec(ctx, `CREATE INDEX IF NOT EXISTS idx_model_overrides_provider_name ON model_overrides(provider_name)`); err != nil {
		return nil, fmt.Errorf("failed to create model_overrides provider_name index: %w", err)
	}
	if _, err := pool.Exec(ctx, `CREATE INDEX IF NOT EXISTS idx_model_overrides_model ON model_overrides(model)`); err != nil {
		return nil, fmt.Errorf("failed to create model_overrides model index: %w", err)
	}
	if _, err := pool.Exec(ctx, `CREATE INDEX IF NOT EXISTS idx_model_overrides_updated_at ON model_overrides(updated_at DESC)`); err != nil {
		return nil, fmt.Errorf("failed to create model_overrides updated_at index: %w", err)
	}
	return &PostgreSQLStore{pool: pool}, nil
}

func (s *PostgreSQLStore) List(ctx context.Context) ([]Override, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT selector, provider_name, model, user_paths, created_at, updated_at
		FROM model_overrides
		ORDER BY selector ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list model overrides: %w", err)
	}
	defer rows.Close()
	return collectOverrides(func() (Override, bool, error) {
		if !rows.Next() {
			return Override{}, false, nil
		}
		override, err := scanPostgreSQLOverride(rows)
		return override, true, err
	}, rows.Err)
}

func (s *PostgreSQLStore) Upsert(ctx context.Context, override Override) error {
	override, pathsJSON, err := prepareOverrideUpsert(override)
	if err != nil {
		return err
	}

	_, err = s.pool.Exec(ctx, `
		INSERT INTO model_overrides (
			selector, provider_name, model, user_paths, created_at, updated_at
		)
		VALUES ($1, $2, $3, $4::jsonb, $5, $6)
		ON CONFLICT(selector) DO UPDATE SET
			provider_name = excluded.provider_name,
			model = excluded.model,
			user_paths = excluded.user_paths,
			updated_at = excluded.updated_at
	`,
		override.Selector,
		override.ProviderName,
		override.Model,
		pathsJSON,
		override.CreatedAt.Unix(),
		override.UpdatedAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("upsert model override: %w", err)
	}
	return nil
}

func (s *PostgreSQLStore) Delete(ctx context.Context, selector string) error {
	cmd, err := s.pool.Exec(ctx, `DELETE FROM model_overrides WHERE selector = $1`, strings.TrimSpace(selector))
	if err != nil {
		return fmt.Errorf("delete model override: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgreSQLStore) Close() error {
	return nil
}

func scanPostgreSQLOverride(scanner interface{ Scan(dest ...any) error }) (Override, error) {
	var override Override
	var userPaths []byte
	var createdAt int64
	var updatedAt int64
	if err := scanner.Scan(
		&override.Selector,
		&override.ProviderName,
		&override.Model,
		&userPaths,
		&createdAt,
		&updatedAt,
	); err != nil {
		if err == pgx.ErrNoRows {
			return Override{}, ErrNotFound
		}
		return Override{}, fmt.Errorf("scan model override: %w", err)
	}
	if err := json.Unmarshal(userPaths, &override.UserPaths); err != nil {
		return Override{}, fmt.Errorf("decode user_paths: %w", err)
	}
	override.CreatedAt = time.Unix(createdAt, 0).UTC()
	override.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return override, nil
}
