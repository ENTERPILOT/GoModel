package failover

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgreSQLStore struct {
	pool *pgxpool.Pool
}

func NewPostgreSQLStore(ctx context.Context, pool *pgxpool.Pool) (*PostgreSQLStore, error) {
	if ctx == nil {
		return nil, fmt.Errorf("context is required")
	}
	if pool == nil {
		return nil, fmt.Errorf("connection pool is required")
	}
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS failover_rules (
			primary_model TEXT PRIMARY KEY,
			fallback_models JSONB NOT NULL DEFAULT '[]'::jsonb,
			enabled BOOLEAN NOT NULL DEFAULT TRUE,
			managed_source TEXT NOT NULL DEFAULT 'dashboard',
			created_at BIGINT NOT NULL,
			updated_at BIGINT NOT NULL
		)
	`); err != nil {
		return nil, fmt.Errorf("failed to create failover_rules table: %w", err)
	}
	if _, err := pool.Exec(ctx, `
		DO $$
		BEGIN
			IF EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = current_schema() AND table_name = 'failover_rules' AND column_name = 'source'
			) AND NOT EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = current_schema() AND table_name = 'failover_rules' AND column_name = 'primary_model'
			) THEN
				ALTER TABLE failover_rules RENAME COLUMN source TO primary_model;
			END IF;
			-- Trim padded primary keys whenever the column exists, so rules
			-- migrated by an earlier (non-trimming) version stay reachable by the
			-- trim-normalizing Get/Delete lookups. Runs independently of the
			-- rename above and is a no-op once values are already trimmed.
			IF EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = current_schema() AND table_name = 'failover_rules' AND column_name = 'primary_model'
			) THEN
				UPDATE failover_rules
				SET primary_model = btrim(primary_model)
				WHERE primary_model <> btrim(primary_model);
			END IF;
			IF EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = current_schema() AND table_name = 'failover_rules' AND column_name = 'targets'
			) AND NOT EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = current_schema() AND table_name = 'failover_rules' AND column_name = 'fallback_models'
			) THEN
				ALTER TABLE failover_rules RENAME COLUMN targets TO fallback_models;
			END IF;
			IF EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = current_schema() AND table_name = 'failover_rules' AND column_name = 'description'
			) THEN
				ALTER TABLE failover_rules DROP COLUMN description;
			END IF;
		END $$;
	`); err != nil {
		return nil, fmt.Errorf("failed to migrate failover_rules table: %w", err)
	}
	for _, stmt := range []string{
		`CREATE INDEX IF NOT EXISTS idx_failover_rules_enabled ON failover_rules(enabled)`,
		`CREATE INDEX IF NOT EXISTS idx_failover_rules_updated_at ON failover_rules(updated_at DESC)`,
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			return nil, fmt.Errorf("failed to create failover_rules index: %w", err)
		}
	}
	return &PostgreSQLStore{pool: pool}, nil
}

func (s *PostgreSQLStore) List(ctx context.Context) ([]Rule, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT primary_model, fallback_models, enabled, managed_source, created_at, updated_at
		FROM failover_rules
		ORDER BY primary_model ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list failover mappings: %w", err)
	}
	defer rows.Close()
	return collectRules(func() (Rule, bool, error) {
		if !rows.Next() {
			return Rule{}, false, nil
		}
		rule, err := scanPostgreSQLRule(rows)
		return rule, true, err
	}, rows.Err)
}

func (s *PostgreSQLStore) Get(ctx context.Context, source string) (*Rule, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT primary_model, fallback_models, enabled, managed_source, created_at, updated_at
		FROM failover_rules
		WHERE primary_model = $1
	`, strings.TrimSpace(source))
	rule, err := scanPostgreSQLRule(row)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &rule, nil
}

const postgresUpsertRuleSQL = `
	INSERT INTO failover_rules (
		primary_model, fallback_models, enabled, managed_source, created_at, updated_at
	)
	VALUES ($1, $2::jsonb, $3, $4, $5, $6)
	ON CONFLICT(primary_model) DO UPDATE SET
		fallback_models = excluded.fallback_models,
		enabled = excluded.enabled,
		managed_source = excluded.managed_source,
		updated_at = excluded.updated_at
`

func postgresUpsertArgs(rule Rule) ([]any, error) {
	stampUpsert(&rule)
	targetsJSON, err := encodeTargets(rule.Targets)
	if err != nil {
		return nil, err
	}
	return []any{
		strings.TrimSpace(rule.Source),
		targetsJSON,
		rule.Enabled,
		rule.ManagedSource,
		rule.CreatedAt.Unix(),
		rule.UpdatedAt.Unix(),
	}, nil
}

func (s *PostgreSQLStore) Upsert(ctx context.Context, rule Rule) error {
	args, err := postgresUpsertArgs(rule)
	if err != nil {
		return err
	}
	if _, err := s.pool.Exec(ctx, postgresUpsertRuleSQL, args...); err != nil {
		return fmt.Errorf("upsert failover mapping: %w", err)
	}
	return nil
}

func (s *PostgreSQLStore) Delete(ctx context.Context, source string) error {
	cmd, err := s.pool.Exec(ctx, `DELETE FROM failover_rules WHERE primary_model = $1`, strings.TrimSpace(source))
	if err != nil {
		return fmt.Errorf("delete failover mapping: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgreSQLStore) DeleteAll(ctx context.Context) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM failover_rules`); err != nil {
		return fmt.Errorf("delete failover mappings: %w", err)
	}
	return nil
}

func (s *PostgreSQLStore) Close() error { return nil }

func scanPostgreSQLRule(scanner interface{ Scan(dest ...any) error }) (Rule, error) {
	var rule Rule
	var targets []byte
	var createdAt int64
	var updatedAt int64
	if err := scanner.Scan(
		&rule.Source,
		&targets,
		&rule.Enabled,
		&rule.ManagedSource,
		&createdAt,
		&updatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Rule{}, ErrNotFound
		}
		return Rule{}, fmt.Errorf("scan failover mapping: %w", err)
	}
	var err error
	if rule.Targets, err = decodeTargets(targets); err != nil {
		return Rule{}, err
	}
	rule.CreatedAt = time.Unix(createdAt, 0).UTC()
	rule.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return rule, nil
}
