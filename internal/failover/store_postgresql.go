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
			source TEXT PRIMARY KEY,
			targets JSONB NOT NULL DEFAULT '[]'::jsonb,
			description TEXT NOT NULL DEFAULT '',
			enabled BOOLEAN NOT NULL DEFAULT TRUE,
			managed_source TEXT NOT NULL DEFAULT 'dashboard',
			created_at BIGINT NOT NULL,
			updated_at BIGINT NOT NULL
		)
	`); err != nil {
		return nil, fmt.Errorf("failed to create failover_rules table: %w", err)
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
		SELECT source, targets, description, enabled, managed_source, created_at, updated_at
		FROM failover_rules
		ORDER BY source ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list failover rules: %w", err)
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
		SELECT source, targets, description, enabled, managed_source, created_at, updated_at
		FROM failover_rules
		WHERE source = $1
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
		source, targets, description, enabled, managed_source, created_at, updated_at
	)
	VALUES ($1, $2::jsonb, $3, $4, $5, $6, $7)
	ON CONFLICT(source) DO UPDATE SET
		targets = excluded.targets,
		description = excluded.description,
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
		rule.Description,
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
		return fmt.Errorf("upsert failover rule: %w", err)
	}
	return nil
}

func (s *PostgreSQLStore) Delete(ctx context.Context, source string) error {
	cmd, err := s.pool.Exec(ctx, `DELETE FROM failover_rules WHERE source = $1`, strings.TrimSpace(source))
	if err != nil {
		return fmt.Errorf("delete failover rule: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgreSQLStore) DeleteAll(ctx context.Context) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM failover_rules`); err != nil {
		return fmt.Errorf("delete failover rules: %w", err)
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
		&rule.Description,
		&rule.Enabled,
		&rule.ManagedSource,
		&createdAt,
		&updatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Rule{}, ErrNotFound
		}
		return Rule{}, fmt.Errorf("scan failover rule: %w", err)
	}
	var err error
	if rule.Targets, err = decodeTargets(targets); err != nil {
		return Rule{}, err
	}
	rule.CreatedAt = time.Unix(createdAt, 0).UTC()
	rule.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return rule, nil
}
