package ratelimit

import (
	"context"
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
	if pool == nil {
		return nil, fmt.Errorf("connection pool is required")
	}
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS rate_limits (
			user_path TEXT NOT NULL,
			period_seconds BIGINT NOT NULL,
			max_requests BIGINT,
			max_tokens BIGINT,
			source TEXT NOT NULL DEFAULT '',
			created_at BIGINT NOT NULL,
			updated_at BIGINT NOT NULL,
			PRIMARY KEY (user_path, period_seconds)
		)
	`); err != nil {
		return nil, fmt.Errorf("failed to create rate_limits table: %w", err)
	}
	return &PostgreSQLStore{pool: pool}, nil
}

func (s *PostgreSQLStore) ListRules(ctx context.Context) ([]Rule, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT user_path, period_seconds, max_requests, max_tokens, source, created_at, updated_at
		FROM rate_limits
		ORDER BY user_path ASC, period_seconds ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list rate limit rules: %w", err)
	}
	defer rows.Close()

	rules := make([]Rule, 0)
	for rows.Next() {
		rule, err := scanPostgreSQLRule(rows)
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

func (s *PostgreSQLStore) UpsertRules(ctx context.Context, rules []Rule) error {
	rules, err := normalizeRulesForUpsert(rules)
	if err != nil {
		return err
	}
	if len(rules) == 0 {
		return nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin rate limit upsert: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if err := upsertPostgreSQLRules(ctx, tx, rules); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit rate limit upsert: %w", err)
	}
	return nil
}

func (s *PostgreSQLStore) DeleteRule(ctx context.Context, userPath string, periodSeconds int64) error {
	userPath, err := NormalizeUserPath(userPath)
	if err != nil {
		return err
	}
	if periodSeconds < 0 {
		return fmt.Errorf("period_seconds must be 0 (concurrent) or greater")
	}
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM rate_limits
		WHERE user_path = $1 AND period_seconds = $2
	`, userPath, periodSeconds)
	if err != nil {
		return fmt.Errorf("delete rate limit rule %s/%d: %w", userPath, periodSeconds, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: %s/%d", ErrNotFound, userPath, periodSeconds)
	}
	return nil
}

func (s *PostgreSQLStore) ReplaceConfigRules(ctx context.Context, rules []Rule) error {
	rules, err := normalizeRulesForUpsert(rules)
	if err != nil {
		return err
	}
	for i := range rules {
		rules[i].Source = SourceConfig
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin config rate limit replace: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if len(rules) == 0 {
		if _, err := tx.Exec(ctx, `DELETE FROM rate_limits WHERE source = $1`, SourceConfig); err != nil {
			return fmt.Errorf("delete old config rate limit rules: %w", err)
		}
	} else {
		conditions := make([]string, 0, len(rules))
		args := make([]any, 0, 1+len(rules)*2)
		args = append(args, SourceConfig)
		for _, rule := range rules {
			base := len(args) + 1
			conditions = append(conditions, fmt.Sprintf(`(user_path = $%d AND period_seconds = $%d)`, base, base+1))
			args = append(args, rule.UserPath, rule.PeriodSeconds)
		}
		query := `DELETE FROM rate_limits WHERE source = $1 AND NOT (` + strings.Join(conditions, " OR ") + `)`
		if _, err := tx.Exec(ctx, query, args...); err != nil {
			return fmt.Errorf("delete old config rate limit rules: %w", err)
		}
	}
	if err := upsertPostgreSQLRules(ctx, tx, rules); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit config rate limit replace: %w", err)
	}
	return nil
}

func (s *PostgreSQLStore) Close() error {
	return nil
}

func upsertPostgreSQLRules(ctx context.Context, tx pgx.Tx, rules []Rule) error {
	for _, rule := range rules {
		_, err := tx.Exec(ctx, `
			INSERT INTO rate_limits (user_path, period_seconds, max_requests, max_tokens, source, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (user_path, period_seconds) DO UPDATE SET
				max_requests = CASE WHEN excluded.source = $8 OR rate_limits.source = $9 THEN excluded.max_requests ELSE rate_limits.max_requests END,
				max_tokens = CASE WHEN excluded.source = $8 OR rate_limits.source = $9 THEN excluded.max_tokens ELSE rate_limits.max_tokens END,
				source = CASE WHEN excluded.source = $8 OR rate_limits.source = $9 THEN excluded.source ELSE rate_limits.source END,
				updated_at = CASE WHEN excluded.source = $8 OR rate_limits.source = $9 THEN excluded.updated_at ELSE rate_limits.updated_at END
		`,
			rule.UserPath,
			rule.PeriodSeconds,
			rule.MaxRequests,
			rule.MaxTokens,
			rule.Source,
			rule.CreatedAt.Unix(),
			rule.UpdatedAt.Unix(),
			SourceManual,
			SourceConfig,
		)
		if err != nil {
			return fmt.Errorf("upsert rate limit rule %s/%d: %w", rule.UserPath, rule.PeriodSeconds, err)
		}
	}
	return nil
}

func scanPostgreSQLRule(row pgx.Row) (Rule, error) {
	var rule Rule
	var maxRequests *int64
	var maxTokens *int64
	var createdAt int64
	var updatedAt int64
	if err := row.Scan(
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
	rule.MaxRequests = maxRequests
	rule.MaxTokens = maxTokens
	rule.CreatedAt = time.Unix(createdAt, 0).UTC()
	rule.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return rule, nil
}
