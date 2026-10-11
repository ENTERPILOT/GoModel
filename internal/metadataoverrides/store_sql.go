package metadataoverrides

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/storage/sqlutil"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// SQLStore stores model metadata overrides in SQLite or PostgreSQL.
type SQLStore struct {
	db sqlx.DB
}

var sqlSchema = []string{
	`CREATE TABLE IF NOT EXISTS model_metadata_overrides (
		selector TEXT PRIMARY KEY,
		provider_name TEXT NOT NULL DEFAULT '',
		model TEXT NOT NULL DEFAULT '',
		metadata TEXT NOT NULL DEFAULT '{}',
		created_at ` + sqlx.TypeInt64 + ` NOT NULL,
		updated_at ` + sqlx.TypeInt64 + ` NOT NULL
	)`,
}

// NewSQLStore creates the model_metadata_overrides table if needed.
func NewSQLStore(ctx context.Context, db sqlx.DB) (*SQLStore, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection is required")
	}
	if err := db.Schema(ctx, sqlSchema...); err != nil {
		return nil, fmt.Errorf("failed to create model_metadata_overrides table: %w", err)
	}
	return &SQLStore{db: db}, nil
}

func (s *SQLStore) List(ctx context.Context) ([]Override, error) {
	rows, err := s.db.Query(ctx, `
		SELECT selector, provider_name, model, metadata, created_at, updated_at
		FROM model_metadata_overrides
		ORDER BY selector ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list model metadata overrides: %w", err)
	}
	defer rows.Close()

	result := make([]Override, 0)
	for rows.Next() {
		var override Override
		var metadata []byte
		var createdAt, updatedAt int64
		if err := rows.Scan(&override.Selector, &override.ProviderName, &override.Model, &metadata, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan model metadata override: %w", err)
		}
		if err := json.Unmarshal(metadata, &override.Metadata); err != nil {
			return nil, fmt.Errorf("decode model metadata override %q: %w", override.Selector, err)
		}
		override.CreatedAt = sqlutil.TimeFromUnix(createdAt)
		override.UpdatedAt = sqlutil.TimeFromUnix(updatedAt)
		result = append(result, override)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate model metadata overrides: %w", err)
	}
	return result, nil
}

func (s *SQLStore) Upsert(ctx context.Context, override Override) error {
	metadata, err := json.Marshal(override.Metadata)
	if err != nil {
		return fmt.Errorf("encode metadata: %w", err)
	}
	now := time.Now().UTC().Unix()
	_, err = s.db.Exec(ctx, `
		INSERT INTO model_metadata_overrides (
			selector, provider_name, model, metadata, created_at, updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(selector) DO UPDATE SET
			provider_name = excluded.provider_name,
			model = excluded.model,
			metadata = excluded.metadata,
			updated_at = excluded.updated_at
	`, override.Selector, override.ProviderName, override.Model, string(metadata), now, now)
	if err != nil {
		return fmt.Errorf("upsert model metadata override: %w", err)
	}
	return nil
}

func (s *SQLStore) Delete(ctx context.Context, selector string) error {
	affected, err := s.db.Exec(ctx,
		`DELETE FROM model_metadata_overrides WHERE selector = ?`, strings.TrimSpace(selector))
	if err != nil {
		return fmt.Errorf("delete model metadata override: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLStore) Close() error {
	return nil
}
