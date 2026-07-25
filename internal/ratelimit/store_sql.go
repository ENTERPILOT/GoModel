package ratelimit

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// sqlRateLimitsSchema is the one source of the table shape, shared by fresh
// installs and the pre-scope migration rebuild.
const sqlRateLimitsSchema = `
	CREATE TABLE IF NOT EXISTS rate_limits (
		scope TEXT NOT NULL DEFAULT 'user_path',
		subject TEXT NOT NULL,
		period_seconds ` + sqlx.TypeInt64 + ` NOT NULL,
		max_requests ` + sqlx.TypeInt64 + `,
		max_tokens ` + sqlx.TypeInt64 + `,
		source TEXT NOT NULL DEFAULT '',
		created_at ` + sqlx.TypeInt64 + ` NOT NULL,
		updated_at ` + sqlx.TypeInt64 + ` NOT NULL,
		PRIMARY KEY (scope, subject, period_seconds)
	)`

// SQLStore stores rate limit rules in a SQL database.
type SQLStore struct {
	db sqlx.DB
}

// upsertRuleSQL preserves a manually edited rule against a config re-seed:
// a column is only overwritten when the incoming or stored row is manual, or
// when both are config-sourced.
const upsertRuleSQL = `
	INSERT INTO rate_limits (scope, subject, period_seconds, max_requests, max_tokens, source, created_at, updated_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(scope, subject, period_seconds) DO UPDATE SET
		max_requests = CASE WHEN excluded.source = ? OR rate_limits.source = ? THEN excluded.max_requests ELSE rate_limits.max_requests END,
		max_tokens = CASE WHEN excluded.source = ? OR rate_limits.source = ? THEN excluded.max_tokens ELSE rate_limits.max_tokens END,
		source = CASE WHEN excluded.source = ? OR rate_limits.source = ? THEN excluded.source ELSE rate_limits.source END,
		updated_at = CASE WHEN excluded.source = ? OR rate_limits.source = ? THEN excluded.updated_at ELSE rate_limits.updated_at END
`

// NewSQLStore creates the rate_limits table and index if needed, after
// migrating any pre-scope table shape.
func NewSQLStore(ctx context.Context, db sqlx.DB) (*SQLStore, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection is required")
	}
	if err := migratePreScopeTable(ctx, db); err != nil {
		return nil, err
	}
	if err := db.Schema(ctx, sqlRateLimitsSchema); err != nil {
		return nil, fmt.Errorf("failed to create rate_limits table: %w", err)
	}
	if err := db.Schema(ctx, `CREATE INDEX IF NOT EXISTS idx_rate_limits_subject ON rate_limits(scope, subject)`); err != nil {
		return nil, fmt.Errorf("failed to create rate limit index: %w", err)
	}
	return &SQLStore{db: db}, nil
}

func (s *SQLStore) ListRules(ctx context.Context) ([]Rule, error) {
	rows, err := s.db.Query(ctx, `
		SELECT scope, subject, period_seconds, max_requests, max_tokens, source, created_at, updated_at
		FROM rate_limits
		ORDER BY scope ASC, subject ASC, period_seconds ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list rate limit rules: %w", err)
	}
	defer rows.Close()

	var rules []Rule
	for rows.Next() {
		rule, err := scanSQLRule(rows)
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

func (s *SQLStore) UpsertRules(ctx context.Context, rules []Rule) error {
	rules, err := normalizeRulesForUpsert(rules)
	if err != nil {
		return err
	}
	if len(rules) == 0 {
		return nil
	}
	return s.db.InTx(ctx, func(q sqlx.Querier) error {
		return upsertRules(ctx, q, rules)
	})
}

func (s *SQLStore) DeleteRule(ctx context.Context, scope RuleScope, subject string, periodSeconds int64) error {
	scope, subject, err := normalizeRuleKey(scope, subject, periodSeconds)
	if err != nil {
		return err
	}
	affected, err := s.db.Exec(ctx, `
		DELETE FROM rate_limits
		WHERE scope = ? AND subject = ? AND period_seconds = ?
	`, scope, subject, periodSeconds)
	if err != nil {
		return fmt.Errorf("delete rate limit rule %s %s/%d: %w", scope, subject, periodSeconds, err)
	}
	if affected == 0 {
		return fmt.Errorf("%w: %s %s/%d", ErrNotFound, scope, subject, periodSeconds)
	}
	return nil
}

func (s *SQLStore) ReplaceConfigRules(ctx context.Context, rules []Rule) error {
	rules, err := normalizeRulesForUpsert(rules)
	if err != nil {
		return err
	}
	for i := range rules {
		rules[i].Source = SourceConfig
	}

	return s.db.InTx(ctx, func(q sqlx.Querier) error {
		// Drop the config-sourced rules that are no longer declared, leaving
		// manually created ones alone.
		query := `DELETE FROM rate_limits WHERE source = ?`
		args := []any{SourceConfig}
		if len(rules) > 0 {
			conditions := make([]string, 0, len(rules))
			for _, rule := range rules {
				conditions = append(conditions, `(scope = ? AND subject = ? AND period_seconds = ?)`)
				args = append(args, rule.Scope, rule.Subject, rule.PeriodSeconds)
			}
			query += ` AND NOT (` + strings.Join(conditions, " OR ") + `)`
		}
		if _, err := q.Exec(ctx, query, args...); err != nil {
			return fmt.Errorf("delete old config rate limit rules: %w", err)
		}
		return upsertRules(ctx, q, rules)
	})
}

func (s *SQLStore) Close() error {
	return nil
}

func upsertRules(ctx context.Context, q sqlx.Querier, rules []Rule) error {
	for _, rule := range rules {
		_, err := q.Exec(ctx, upsertRuleSQL,
			rule.Scope,
			rule.Subject,
			rule.PeriodSeconds,
			nullableInt64(rule.MaxRequests),
			nullableInt64(rule.MaxTokens),
			rule.Source,
			rule.CreatedAt.Unix(),
			rule.UpdatedAt.Unix(),
			SourceManual, SourceConfig,
			SourceManual, SourceConfig,
			SourceManual, SourceConfig,
			SourceManual, SourceConfig,
		)
		if err != nil {
			return fmt.Errorf("upsert rate limit rule %s %s/%d: %w",
				rule.Scope, rule.Subject, rule.PeriodSeconds, err)
		}
	}
	return nil
}

func scanSQLRule(scanner sqlx.Row) (Rule, error) {
	var rule Rule
	var maxRequests, maxTokens *int64
	var createdAt, updatedAt int64
	if err := scanner.Scan(
		&rule.Scope,
		&rule.Subject,
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

func nullableInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}
