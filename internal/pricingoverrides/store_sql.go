package pricingoverrides

import (
	"context"
	"fmt"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/storage/sqlutil"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// SQLStore stores model pricing overrides in a SQL database.
type SQLStore struct {
	db sqlx.DB
}

var sqlSchema = []string{
	`CREATE TABLE IF NOT EXISTS model_pricing_overrides (
		selector TEXT PRIMARY KEY,
		provider_name TEXT NOT NULL DEFAULT '',
		model TEXT NOT NULL DEFAULT '',
		pricing TEXT NOT NULL DEFAULT '{}',
		created_at ` + sqlx.TypeInt64 + ` NOT NULL,
		updated_at ` + sqlx.TypeInt64 + ` NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_model_pricing_overrides_provider_name ON model_pricing_overrides(provider_name)`,
	`CREATE INDEX IF NOT EXISTS idx_model_pricing_overrides_model ON model_pricing_overrides(model)`,
	`CREATE INDEX IF NOT EXISTS idx_model_pricing_overrides_updated_at ON model_pricing_overrides(updated_at DESC)`,
}

// NewSQLStore creates the model_pricing_overrides table and indexes if needed.
func NewSQLStore(ctx context.Context, db sqlx.DB) (*SQLStore, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection is required")
	}
	if err := db.Schema(ctx, sqlSchema...); err != nil {
		return nil, fmt.Errorf("failed to create model_pricing_overrides table: %w", err)
	}
	return &SQLStore{db: db}, nil
}

func (s *SQLStore) List(ctx context.Context) ([]Override, error) {
	rows, err := s.db.Query(ctx, `
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
		override, err := scanSQLOverride(rows)
		return override, true, err
	}, rows.Err)
}

func (s *SQLStore) Upsert(ctx context.Context, override Override) error {
	override, pricingJSON, err := prepareOverrideUpsert(override)
	if err != nil {
		return err
	}

	_, err = s.db.Exec(ctx, `
		INSERT INTO model_pricing_overrides (
			selector, provider_name, model, pricing, created_at, updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?)
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

func (s *SQLStore) Delete(ctx context.Context, selector string) error {
	affected, err := s.db.Exec(ctx,
		`DELETE FROM model_pricing_overrides WHERE selector = ?`, strings.TrimSpace(selector))
	if err != nil {
		return fmt.Errorf("delete model pricing override: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLStore) Close() error {
	return nil
}

func scanSQLOverride(rows sqlx.Rows) (Override, error) {
	var override Override
	var pricing []byte
	var createdAt int64
	var updatedAt int64
	if err := rows.Scan(
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
	override.CreatedAt = sqlutil.TimeFromUnix(createdAt)
	override.UpdatedAt = sqlutil.TimeFromUnix(updatedAt)
	return override, nil
}
