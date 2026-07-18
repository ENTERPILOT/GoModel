package headerpolicy

import (
	"context"
	"fmt"
	"time"

	"github.com/goccy/go-json"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgreSQLStore persists header policies independently from guardrails.
type PostgreSQLStore struct{ pool *pgxpool.Pool }

func NewPostgreSQLStore(ctx context.Context, pool *pgxpool.Pool) (*PostgreSQLStore, error) {
	if ctx == nil {
		return nil, fmt.Errorf("context is required")
	}
	if pool == nil {
		return nil, fmt.Errorf("connection pool is required")
	}
	statements := []string{
		`CREATE TABLE IF NOT EXISTS header_policy_definitions (
			name TEXT PRIMARY KEY,
			description TEXT NOT NULL DEFAULT '',
			config JSONB NOT NULL,
			created_at BIGINT NOT NULL,
			updated_at BIGINT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_header_policy_definitions_updated_at ON header_policy_definitions(updated_at DESC)`,
	}
	for _, statement := range statements {
		if _, err := pool.Exec(ctx, statement); err != nil {
			return nil, fmt.Errorf("initialize header policy definitions table: %w", err)
		}
	}
	return &PostgreSQLStore{pool: pool}, nil
}

func (s *PostgreSQLStore) List(ctx context.Context) ([]Definition, error) {
	rows, err := s.pool.Query(ctx, `SELECT name, description, config, created_at, updated_at FROM header_policy_definitions ORDER BY name ASC`)
	if err != nil {
		return nil, fmt.Errorf("list header policies: %w", err)
	}
	defer rows.Close()
	return collectDefinitions(rows, scanPostgreSQLDefinition)
}

func (s *PostgreSQLStore) Get(ctx context.Context, name string) (*Definition, error) {
	definition, err := scanPostgreSQLDefinition(s.pool.QueryRow(ctx,
		`SELECT name, description, config, created_at, updated_at FROM header_policy_definitions WHERE name = $1`,
		normalizeDefinitionName(name),
	))
	if err != nil {
		return nil, storeNotFound(err, pgx.ErrNoRows)
	}
	return &definition, nil
}

func (s *PostgreSQLStore) Upsert(ctx context.Context, definition Definition) error {
	definition, err := normalizeDefinition(definition)
	if err != nil {
		return err
	}
	config, err := json.Marshal(persistedFromDefinition(definition))
	if err != nil {
		return fmt.Errorf("marshal header policy %q: %w", definition.Name, err)
	}
	now := time.Now().UTC()
	if definition.CreatedAt.IsZero() {
		definition.CreatedAt = now
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO header_policy_definitions (name, description, config, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT(name) DO UPDATE SET description = excluded.description, config = excluded.config, updated_at = excluded.updated_at
	`, definition.Name, definition.Description, config, definition.CreatedAt.Unix(), now.Unix())
	if err != nil {
		return fmt.Errorf("upsert header policy: %w", err)
	}
	return nil
}

func (s *PostgreSQLStore) UpsertMany(ctx context.Context, definitions []Definition) error {
	if len(definitions) == 0 {
		return nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin header policy upsert transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	now := time.Now().UTC()
	for _, definition := range definitions {
		definition, err = normalizeDefinition(definition)
		if err != nil {
			return err
		}
		config, err := json.Marshal(persistedFromDefinition(definition))
		if err != nil {
			return fmt.Errorf("marshal header policy %q: %w", definition.Name, err)
		}
		if definition.CreatedAt.IsZero() {
			definition.CreatedAt = now
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO header_policy_definitions (name, description, config, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT(name) DO UPDATE SET description = excluded.description, config = excluded.config, updated_at = excluded.updated_at
		`, definition.Name, definition.Description, config, definition.CreatedAt.Unix(), now.Unix()); err != nil {
			return fmt.Errorf("upsert header policy %q: %w", definition.Name, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit header policy upsert transaction: %w", err)
	}
	return nil
}

func (s *PostgreSQLStore) Delete(ctx context.Context, name string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM header_policy_definitions WHERE name = $1`, normalizeDefinitionName(name))
	if err != nil {
		return fmt.Errorf("delete header policy: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgreSQLStore) Close() error { return nil }

func scanPostgreSQLDefinition(scanner definitionScanner) (Definition, error) {
	var definition Definition
	var configJSON []byte
	var createdAt, updatedAt int64
	if err := scanner.Scan(&definition.Name, &definition.Description, &configJSON, &createdAt, &updatedAt); err != nil {
		return Definition{}, err
	}
	var persisted persistedDefinition
	if err := json.Unmarshal(configJSON, &persisted); err != nil {
		return Definition{}, fmt.Errorf("decode stored header policy %q: %w", definition.Name, err)
	}
	definition, err := definitionFromPersisted(definition.Name, definition.Description, persisted)
	if err != nil {
		return Definition{}, err
	}
	definition.CreatedAt = time.Unix(createdAt, 0).UTC()
	definition.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return definition, nil
}
