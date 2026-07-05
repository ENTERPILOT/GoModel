package ratelimit

import (
	"context"
	"database/sql"
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
		CREATE TABLE IF NOT EXISTS rate_limits (
			user_path TEXT NOT NULL,
			period_seconds INTEGER NOT NULL,
			max_requests INTEGER,
			max_tokens INTEGER,
			source TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			PRIMARY KEY (user_path, period_seconds)
		)
	`); err != nil {
		return nil, fmt.Errorf("failed to create rate_limits table: %w", err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_rate_limits_user_path ON rate_limits(user_path)`); err != nil {
		return nil, fmt.Errorf("failed to create rate limit index: %w", err)
	}
	return &SQLiteStore{db: db}, nil
}

func (s *SQLiteStore) ListRules(ctx context.Context) ([]Rule, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT user_path, period_seconds, max_requests, max_tokens, source, created_at, updated_at
		FROM rate_limits
		ORDER BY user_path ASC, period_seconds ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list rate limit rules: %w", err)
	}
	defer rows.Close()

	var rules []Rule
	for rows.Next() {
		rule, err := scanSQLiteRule(rows)
		if err != nil {
			return nil, err
		}
		rules = append(rules, rule)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate rate limit rules: %w", err)
	}
	return rules, nil
}

func (s *SQLiteStore) UpsertRules(ctx context.Context, rules []Rule) error {
	rules, err := normalizeRulesForUpsert(rules)
	if err != nil {
		return err
	}
	if len(rules) == 0 {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin rate limit upsert: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if err := upsertSQLiteRules(ctx, tx, rules); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit rate limit upsert: %w", err)
	}
	return nil
}

func (s *SQLiteStore) DeleteRule(ctx context.Context, userPath string, periodSeconds int64) error {
	userPath, err := NormalizeUserPath(userPath)
	if err != nil {
		return err
	}
	if err := validatePeriodSeconds(periodSeconds); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM rate_limits
		WHERE user_path = ? AND period_seconds = ?
	`, userPath, periodSeconds)
	if err != nil {
		return fmt.Errorf("delete rate limit rule %s/%d: %w", userPath, periodSeconds, err)
	}
	affected, err := result.RowsAffected()
	if err == nil && affected == 0 {
		return fmt.Errorf("%w: %s/%d", ErrNotFound, userPath, periodSeconds)
	}
	return nil
}

func (s *SQLiteStore) ReplaceConfigRules(ctx context.Context, rules []Rule) error {
	rules, err := normalizeRulesForUpsert(rules)
	if err != nil {
		return err
	}
	for i := range rules {
		rules[i].Source = SourceConfig
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin config rate limit replace: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if len(rules) == 0 {
		if _, err := tx.ExecContext(ctx, `DELETE FROM rate_limits WHERE source = ?`, SourceConfig); err != nil {
			return fmt.Errorf("delete old config rate limit rules: %w", err)
		}
	} else {
		conditions := make([]string, 0, len(rules))
		args := make([]any, 0, 1+len(rules)*2)
		args = append(args, SourceConfig)
		for _, rule := range rules {
			conditions = append(conditions, `(user_path = ? AND period_seconds = ?)`)
			args = append(args, rule.UserPath, rule.PeriodSeconds)
		}
		query := `DELETE FROM rate_limits WHERE source = ? AND NOT (` + strings.Join(conditions, " OR ") + `)`
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return fmt.Errorf("delete old config rate limit rules: %w", err)
		}
	}
	if err := upsertSQLiteRules(ctx, tx, rules); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit config rate limit replace: %w", err)
	}
	return nil
}

func (s *SQLiteStore) Close() error {
	return nil
}

func upsertSQLiteRules(ctx context.Context, tx *sql.Tx, rules []Rule) error {
	if len(rules) == 0 {
		return nil
	}
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO rate_limits (user_path, period_seconds, max_requests, max_tokens, source, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_path, period_seconds) DO UPDATE SET
			max_requests = CASE WHEN excluded.source = ? OR rate_limits.source = ? THEN excluded.max_requests ELSE rate_limits.max_requests END,
			max_tokens = CASE WHEN excluded.source = ? OR rate_limits.source = ? THEN excluded.max_tokens ELSE rate_limits.max_tokens END,
			source = CASE WHEN excluded.source = ? OR rate_limits.source = ? THEN excluded.source ELSE rate_limits.source END,
			updated_at = CASE WHEN excluded.source = ? OR rate_limits.source = ? THEN excluded.updated_at ELSE rate_limits.updated_at END
	`)
	if err != nil {
		return fmt.Errorf("prepare rate limit upsert: %w", err)
	}
	defer stmt.Close()

	for _, rule := range rules {
		if _, err := stmt.ExecContext(
			ctx,
			rule.UserPath,
			rule.PeriodSeconds,
			nullableInt64(rule.MaxRequests),
			nullableInt64(rule.MaxTokens),
			rule.Source,
			rule.CreatedAt.Unix(),
			rule.UpdatedAt.Unix(),
			SourceManual,
			SourceConfig,
			SourceManual,
			SourceConfig,
			SourceManual,
			SourceConfig,
			SourceManual,
			SourceConfig,
		); err != nil {
			return fmt.Errorf("upsert rate limit rule %s/%d: %w", rule.UserPath, rule.PeriodSeconds, err)
		}
	}
	return nil
}

func scanSQLiteRule(scanner interface{ Scan(dest ...any) error }) (Rule, error) {
	var rule Rule
	var maxRequests sql.NullInt64
	var maxTokens sql.NullInt64
	var createdAt int64
	var updatedAt int64
	if err := scanner.Scan(
		&rule.UserPath,
		&rule.PeriodSeconds,
		&maxRequests,
		&maxTokens,
		&rule.Source,
		&createdAt,
		&updatedAt,
	); err != nil {
		return Rule{}, fmt.Errorf("scan rate limit rule: %w", err)
	}
	if maxRequests.Valid {
		value := maxRequests.Int64
		rule.MaxRequests = &value
	}
	if maxTokens.Valid {
		value := maxTokens.Int64
		rule.MaxTokens = &value
	}
	rule.CreatedAt = time.Unix(createdAt, 0).UTC()
	rule.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return rule, nil
}

func nullableInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}
