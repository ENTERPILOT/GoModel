package failover

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type SQLiteStore struct {
	db *sql.DB
}

func NewSQLiteStore(db *sql.DB) (*SQLiteStore, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection is required")
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS failover_rules (
			source TEXT PRIMARY KEY,
			targets TEXT NOT NULL DEFAULT '[]',
			description TEXT NOT NULL DEFAULT '',
			enabled INTEGER NOT NULL DEFAULT 1,
			managed_source TEXT NOT NULL DEFAULT 'dashboard',
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)
	`); err != nil {
		return nil, fmt.Errorf("failed to create failover_rules table: %w", err)
	}
	for _, stmt := range []string{
		`CREATE INDEX IF NOT EXISTS idx_failover_rules_enabled ON failover_rules(enabled)`,
		`CREATE INDEX IF NOT EXISTS idx_failover_rules_updated_at ON failover_rules(updated_at DESC)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			return nil, fmt.Errorf("failed to create failover_rules index: %w", err)
		}
	}
	return &SQLiteStore{db: db}, nil
}

func (s *SQLiteStore) List(ctx context.Context) ([]Rule, error) {
	rows, err := s.db.QueryContext(ctx, `
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
		rule, err := scanSQLiteRule(rows)
		return rule, true, err
	}, rows.Err)
}

func (s *SQLiteStore) Get(ctx context.Context, source string) (*Rule, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT source, targets, description, enabled, managed_source, created_at, updated_at
		FROM failover_rules
		WHERE source = ?
	`, strings.TrimSpace(source))
	rule, err := scanSQLiteRule(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &rule, nil
}

const sqliteUpsertRuleSQL = `
	INSERT INTO failover_rules (
		source, targets, description, enabled, managed_source, created_at, updated_at
	)
	VALUES (?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(source) DO UPDATE SET
		targets = excluded.targets,
		description = excluded.description,
		enabled = excluded.enabled,
		managed_source = excluded.managed_source,
		updated_at = excluded.updated_at
`

func sqliteUpsertArgs(rule Rule) ([]any, error) {
	stampUpsert(&rule)
	targetsJSON, err := encodeTargets(rule.Targets)
	if err != nil {
		return nil, err
	}
	return []any{
		strings.TrimSpace(rule.Source),
		targetsJSON,
		rule.Description,
		boolToSQLite(rule.Enabled),
		rule.ManagedSource,
		rule.CreatedAt.Unix(),
		rule.UpdatedAt.Unix(),
	}, nil
}

func (s *SQLiteStore) Upsert(ctx context.Context, rule Rule) error {
	args, err := sqliteUpsertArgs(rule)
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, sqliteUpsertRuleSQL, args...); err != nil {
		return fmt.Errorf("upsert failover rule: %w", err)
	}
	return nil
}

func (s *SQLiteStore) Delete(ctx context.Context, source string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM failover_rules WHERE source = ?`, strings.TrimSpace(source))
	if err != nil {
		return fmt.Errorf("delete failover rule: %w", err)
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

func (s *SQLiteStore) DeleteAll(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM failover_rules`); err != nil {
		return fmt.Errorf("delete failover rules: %w", err)
	}
	return nil
}

func (s *SQLiteStore) Close() error { return nil }

func scanSQLiteRule(scanner interface{ Scan(dest ...any) error }) (Rule, error) {
	var rule Rule
	var targets string
	var enabled int
	var createdAt int64
	var updatedAt int64
	if err := scanner.Scan(
		&rule.Source,
		&targets,
		&rule.Description,
		&enabled,
		&rule.ManagedSource,
		&createdAt,
		&updatedAt,
	); err != nil {
		return Rule{}, err
	}
	var err error
	if rule.Targets, err = decodeTargets([]byte(targets)); err != nil {
		return Rule{}, err
	}
	rule.Enabled = enabled != 0
	rule.CreatedAt = time.Unix(createdAt, 0).UTC()
	rule.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return rule, nil
}

func boolToSQLite(v bool) int {
	if v {
		return 1
	}
	return 0
}
