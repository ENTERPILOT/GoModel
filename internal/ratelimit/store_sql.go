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
		per_child ` + sqlx.TypeBool + ` NOT NULL DEFAULT FALSE,
		period_seconds ` + sqlx.TypeInt64 + ` NOT NULL,
		max_requests ` + sqlx.TypeInt64 + `,
		max_tokens ` + sqlx.TypeInt64 + `,
		source TEXT NOT NULL DEFAULT '',
		created_at ` + sqlx.TypeInt64 + ` NOT NULL,
		updated_at ` + sqlx.TypeInt64 + ` NOT NULL,
		PRIMARY KEY (scope, subject, period_seconds)
	)`

const sqlRateLimitCountersSchema = `
	CREATE TABLE IF NOT EXISTS rate_limit_counters (
		scope TEXT NOT NULL,
		subject TEXT NOT NULL,
		partition TEXT NOT NULL DEFAULT '',
		period_seconds ` + sqlx.TypeInt64 + ` NOT NULL,
		requests_window_start ` + sqlx.TypeInt64 + ` NOT NULL DEFAULT 0,
		requests_current ` + sqlx.TypeInt64 + ` NOT NULL DEFAULT 0,
		requests_previous ` + sqlx.TypeInt64 + ` NOT NULL DEFAULT 0,
		tokens_window_start ` + sqlx.TypeInt64 + ` NOT NULL DEFAULT 0,
		tokens_current ` + sqlx.TypeInt64 + ` NOT NULL DEFAULT 0,
		tokens_previous ` + sqlx.TypeInt64 + ` NOT NULL DEFAULT 0,
		updated_at ` + sqlx.TypeInt64 + ` NOT NULL,
		PRIMARY KEY (scope, subject, partition, period_seconds)
	)`

// SQLStore stores rate limit rules in a SQL database.
type SQLStore struct {
	db sqlx.DB
}

// upsertRuleSQL preserves a manually edited rule against a config re-seed:
// a column is only overwritten when the incoming or stored row is manual, or
// when both are config-sourced.
const upsertRuleSQL = `
	INSERT INTO rate_limits (scope, subject, per_child, period_seconds, max_requests, max_tokens, source, created_at, updated_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(scope, subject, period_seconds) DO UPDATE SET
		per_child = CASE WHEN excluded.source = ? OR rate_limits.source = ? THEN excluded.per_child ELSE rate_limits.per_child END,
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
	if err := sqlx.AddColumns(ctx, db,
		`ALTER TABLE rate_limits ADD COLUMN per_child `+sqlx.TypeBool+` NOT NULL DEFAULT FALSE`,
	); err != nil {
		return nil, fmt.Errorf("failed to migrate rate_limits table: %w", err)
	}
	if err := db.Schema(ctx, `CREATE INDEX IF NOT EXISTS idx_rate_limits_subject ON rate_limits(scope, subject)`); err != nil {
		return nil, fmt.Errorf("failed to create rate limit index: %w", err)
	}
	if err := db.Schema(ctx, sqlRateLimitCountersSchema); err != nil {
		return nil, fmt.Errorf("failed to create rate_limit_counters table: %w", err)
	}
	return &SQLStore{db: db}, nil
}

func (s *SQLStore) ListRules(ctx context.Context) ([]Rule, error) {
	rows, err := s.db.Query(ctx, `
		SELECT scope, subject, per_child, period_seconds, max_requests, max_tokens, source, created_at, updated_at
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

func (s *SQLStore) LoadCounters(ctx context.Context) ([]WindowSnapshot, error) {
	rows, err := s.db.Query(ctx, `
		SELECT scope, subject, partition, period_seconds,
			requests_window_start, requests_current, requests_previous,
			tokens_window_start, tokens_current, tokens_previous
		FROM rate_limit_counters
	`)
	if err != nil {
		return nil, fmt.Errorf("list rate limit counters: %w", err)
	}
	defer rows.Close()

	var snapshots []WindowSnapshot
	for rows.Next() {
		var snap WindowSnapshot
		if err := rows.Scan(
			&snap.Scope, &snap.Subject, &snap.Partition, &snap.PeriodSeconds,
			&snap.RequestsWindowStart, &snap.RequestsCurrent, &snap.RequestsPrevious,
			&snap.TokensWindowStart, &snap.TokensCurrent, &snap.TokensPrevious,
		); err != nil {
			return nil, fmt.Errorf("scan rate limit counter: %w", err)
		}
		snapshots = append(snapshots, snap)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate rate limit counters: %w", err)
	}
	return snapshots, nil
}

const upsertCounterSQL = `
	INSERT INTO rate_limit_counters (
		scope, subject, partition, period_seconds,
		requests_window_start, requests_current, requests_previous,
		tokens_window_start, tokens_current, tokens_previous, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(scope, subject, partition, period_seconds) DO UPDATE SET
		requests_window_start = excluded.requests_window_start,
		requests_current = excluded.requests_current,
		requests_previous = excluded.requests_previous,
		tokens_window_start = excluded.tokens_window_start,
		tokens_current = excluded.tokens_current,
		tokens_previous = excluded.tokens_previous,
		updated_at = excluded.updated_at
`

func (s *SQLStore) SaveCounters(ctx context.Context, snapshots []WindowSnapshot) error {
	now := time.Now().Unix()
	return s.db.InTx(ctx, func(q sqlx.Querier) error {
		for _, snap := range snapshots {
			if _, err := q.Exec(ctx, upsertCounterSQL,
				snap.Scope, snap.Subject, snap.Partition, snap.PeriodSeconds,
				snap.RequestsWindowStart, snap.RequestsCurrent, snap.RequestsPrevious,
				snap.TokensWindowStart, snap.TokensCurrent, snap.TokensPrevious, now,
			); err != nil {
				return fmt.Errorf("upsert rate limit counter: %w", err)
			}
		}
		return deleteOrphanCounters(ctx, q, snapshots)
	})
}

func deleteOrphanCounters(ctx context.Context, q sqlx.Querier, snapshots []WindowSnapshot) error {
	if len(snapshots) == 0 {
		if _, err := q.Exec(ctx, `DELETE FROM rate_limit_counters`); err != nil {
			return fmt.Errorf("clear rate limit counters: %w", err)
		}
		return nil
	}
	var query strings.Builder
	query.WriteString(`DELETE FROM rate_limit_counters WHERE NOT (`)
	args := make([]any, 0, len(snapshots)*4)
	for i, snap := range snapshots {
		if i > 0 {
			query.WriteString(` OR `)
		}
		query.WriteString(`(scope = ? AND subject = ? AND partition = ? AND period_seconds = ?)`)
		args = append(args, snap.Scope, snap.Subject, snap.Partition, snap.PeriodSeconds)
	}
	query.WriteString(`)`)
	if _, err := q.Exec(ctx, query.String(), args...); err != nil {
		return fmt.Errorf("prune rate limit counters: %w", err)
	}
	return nil
}

func (s *SQLStore) DeleteCounter(ctx context.Context, scope RuleScope, subject string, periodSeconds int64) error {
	_, err := s.db.Exec(ctx, `
		DELETE FROM rate_limit_counters
		WHERE scope = ? AND subject = ? AND period_seconds = ?
	`, scope, subject, periodSeconds)
	if err != nil {
		return fmt.Errorf("delete rate limit counters %s %s/%d: %w", scope, subject, periodSeconds, err)
	}
	return nil
}

func (s *SQLStore) DeleteAllCounters(ctx context.Context) error {
	if _, err := s.db.Exec(ctx, `DELETE FROM rate_limit_counters`); err != nil {
		return fmt.Errorf("delete all rate limit counters: %w", err)
	}
	return nil
}

func (s *SQLStore) Close() error {
	return nil
}

func upsertRules(ctx context.Context, q sqlx.Querier, rules []Rule) error {
	for _, rule := range rules {
		_, err := q.Exec(ctx, upsertRuleSQL,
			rule.Scope,
			rule.Subject,
			rule.PerChild,
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
		&rule.PerChild,
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
