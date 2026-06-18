package pricingoverrides

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/goccy/go-json"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgreSQLStore stores model pricing overrides in PostgreSQL.
type PostgreSQLStore struct {
	pool *pgxpool.Pool
}

// NewPostgreSQLStore creates the model_pricing_overrides table and indexes if needed.
func NewPostgreSQLStore(ctx context.Context, pool *pgxpool.Pool) (*PostgreSQLStore, error) {
	if ctx == nil {
		return nil, fmt.Errorf("context is required")
	}
	if pool == nil {
		return nil, fmt.Errorf("connection pool is required")
	}

	_, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS model_pricing_overrides (
			selector TEXT PRIMARY KEY,
			provider_name TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			pricing JSONB NOT NULL DEFAULT '{}'::jsonb,
			created_at BIGINT NOT NULL,
			updated_at BIGINT NOT NULL
		)
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to create model_pricing_overrides table: %w", err)
	}
	if _, err := pool.Exec(ctx, `CREATE INDEX IF NOT EXISTS idx_model_pricing_overrides_provider_name ON model_pricing_overrides(provider_name)`); err != nil {
		return nil, fmt.Errorf("failed to create model_pricing_overrides provider_name index: %w", err)
	}
	if _, err := pool.Exec(ctx, `CREATE INDEX IF NOT EXISTS idx_model_pricing_overrides_model ON model_pricing_overrides(model)`); err != nil {
		return nil, fmt.Errorf("failed to create model_pricing_overrides model index: %w", err)
	}
	if _, err := pool.Exec(ctx, `CREATE INDEX IF NOT EXISTS idx_model_pricing_overrides_updated_at ON model_pricing_overrides(updated_at DESC)`); err != nil {
		return nil, fmt.Errorf("failed to create model_pricing_overrides updated_at index: %w", err)
	}
	return &PostgreSQLStore{pool: pool}, nil
}

func (s *PostgreSQLStore) List(ctx context.Context) ([]Override, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT selector, provider_name, model, pricing, created_at, updated_at
		FROM model_pricing_overrides
		ORDER BY selector ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list model pricing overrides: %w", err)
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
	override, pricingJSON, err := prepareOverrideUpsert(override)
	if err != nil {
		return err
	}

	_, err = s.pool.Exec(ctx, `
		INSERT INTO model_pricing_overrides (
			selector, provider_name, model, pricing, created_at, updated_at
		)
		VALUES ($1, $2, $3, $4::jsonb, $5, $6)
		ON CONFLICT(selector) DO UPDATE SET
			provider_name = excluded.provider_name,
			model = excluded.model,
			pricing = excluded.pricing,
			updated_at = excluded.updated_at
	`,
		override.Selector,
		override.ProviderName,
		override.Model,
		pricingJSON,
		override.CreatedAt.Unix(),
		override.UpdatedAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("upsert model pricing override: %w", err)
	}
	return nil
}

func (s *PostgreSQLStore) Delete(ctx context.Context, selector string) error {
	cmd, err := s.pool.Exec(ctx, `DELETE FROM model_pricing_overrides WHERE selector = $1`, strings.TrimSpace(selector))
	if err != nil {
		return fmt.Errorf("delete model pricing override: %w", err)
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
	var pricing []byte
	var createdAt int64
	var updatedAt int64
	if err := scanner.Scan(
		&override.Selector,
		&override.ProviderName,
		&override.Model,
		&pricing,
		&createdAt,
		&updatedAt,
	); err != nil {
		return Override{}, fmt.Errorf("scan model pricing override: %w", err)
	}
	if err := json.Unmarshal(pricing, &override.Pricing); err != nil {
		return Override{}, fmt.Errorf("decode pricing: %w", err)
	}
	override.CreatedAt = time.Unix(createdAt, 0).UTC()
	override.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return override, nil
}
