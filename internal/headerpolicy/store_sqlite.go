package headerpolicy

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/goccy/go-json"
)

// SQLiteStore persists header policies independently from guardrails.
type SQLiteStore struct{ db *sql.DB }

func NewSQLiteStore(ctx context.Context, db *sql.DB) (*SQLiteStore, error) {
	if ctx == nil {
		return nil, fmt.Errorf("context is required")
	}
	if db == nil {
		return nil, fmt.Errorf("database connection is required")
	}
	statements := []string{
		`CREATE TABLE IF NOT EXISTS header_policy_definitions (
			name TEXT PRIMARY KEY,
			description TEXT NOT NULL DEFAULT '',
			config JSON NOT NULL,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_header_policy_definitions_updated_at ON header_policy_definitions(updated_at DESC)`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return nil, fmt.Errorf("initialize header policy definitions table: %w", err)
		}
	}
	return &SQLiteStore{db: db}, nil
}

func (s *SQLiteStore) List(ctx context.Context) ([]Definition, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name, description, config, created_at, updated_at FROM header_policy_definitions ORDER BY name ASC`)
	if err != nil {
		return nil, fmt.Errorf("list header policies: %w", err)
	}
	defer rows.Close()
	return collectDefinitions(rows, scanSQLiteDefinition)
}

func (s *SQLiteStore) Get(ctx context.Context, name string) (*Definition, error) {
	definition, err := scanSQLiteDefinition(s.db.QueryRowContext(ctx,
		`SELECT name, description, config, created_at, updated_at FROM header_policy_definitions WHERE name = ?`,
		normalizeDefinitionName(name),
	))
	if err != nil {
		return nil, storeNotFound(err, sql.ErrNoRows)
	}
	return &definition, nil
}

func (s *SQLiteStore) Upsert(ctx context.Context, definition Definition) error {
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
	definition.UpdatedAt = now
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO header_policy_definitions (name, description, config, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET description = excluded.description, config = excluded.config, updated_at = excluded.updated_at
	`, definition.Name, definition.Description, string(config), definition.CreatedAt.Unix(), definition.UpdatedAt.Unix())
	if err != nil {
		return fmt.Errorf("upsert header policy: %w", err)
	}
	return nil
}

func (s *SQLiteStore) UpsertMany(ctx context.Context, definitions []Definition) error {
	if len(definitions) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin header policy upsert transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
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
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO header_policy_definitions (name, description, config, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(name) DO UPDATE SET description = excluded.description, config = excluded.config, updated_at = excluded.updated_at
		`, definition.Name, definition.Description, string(config), definition.CreatedAt.Unix(), now.Unix()); err != nil {
			return fmt.Errorf("upsert header policy %q: %w", definition.Name, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit header policy upsert transaction: %w", err)
	}
	return nil
}

func (s *SQLiteStore) Delete(ctx context.Context, name string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM header_policy_definitions WHERE name = ?`, normalizeDefinitionName(name))
	if err != nil {
		return fmt.Errorf("delete header policy: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete header policy rows affected: %w", err)
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLiteStore) Close() error { return nil }

func scanSQLiteDefinition(scanner definitionScanner) (Definition, error) {
	var definition Definition
	var configJSON string
	var createdAt, updatedAt int64
	if err := scanner.Scan(&definition.Name, &definition.Description, &configJSON, &createdAt, &updatedAt); err != nil {
		return Definition{}, err
	}
	var persisted persistedDefinition
	if err := json.Unmarshal([]byte(configJSON), &persisted); err != nil {
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
