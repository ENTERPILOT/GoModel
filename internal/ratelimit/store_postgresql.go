package ratelimit

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// postgresRateLimitsSchema is the one source of the table shape, shared by
// fresh installs and the pre-scope migration rebuild.
const postgresRateLimitsSchema = `
	CREATE TABLE IF NOT EXISTS rate_limits (
		scope TEXT NOT NULL DEFAULT 'user_path',
		subject TEXT NOT NULL,
		period_seconds BIGINT NOT NULL,
		max_requests BIGINT,
		max_tokens BIGINT,
		source TEXT NOT NULL DEFAULT '',
		created_at BIGINT NOT NULL,
		updated_at BIGINT NOT NULL,
		PRIMARY KEY (scope, subject, period_seconds)
	)`

type PostgreSQLStore struct {
	pool *pgxpool.Pool
}

func NewPostgreSQLStore(ctx context.Context, pool *pgxpool.Pool) (*PostgreSQLStore, error) {
	if pool == nil {
		return nil, fmt.Errorf("connection pool is required")
	}
	if err := migratePostgreSQLPreScopeTable(ctx, pool); err != nil {
		return nil, err
	}
	if _, err := pool.Exec(ctx, postgresRateLimitsSchema); err != nil {
		return nil, fmt.Errorf("failed to create rate_limits table: %w", err)
	}
	return &PostgreSQLStore{pool: pool}, nil
}

// migratePostgreSQLPreScopeTable rebuilds a rate_limits table created before
// rule scopes existed (keyed by user_path only) into the scoped shape.
func migratePostgreSQLPreScopeTable(ctx context.Context, pool *pgxpool.Pool) error {
	var hasSubject, hasUserPath bool
	rows, err := pool.Query(ctx, `
		SELECT column_name FROM information_schema.columns
		WHERE table_name = 'rate_limits' AND table_schema = current_schema()
	`)
	if err != nil {
		return fmt.Errorf("inspect rate_limits schema: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return fmt.Errorf("inspect rate_limits schema: %w", err)
		}
		switch name {
		case "subject":
			hasSubject = true
		case "user_path":
			hasUserPath = true
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("inspect rate_limits schema: %w", err)
	}
	if hasSubject || !hasUserPath {
		return nil // already scoped, or table does not exist yet
	}
	// One transaction (PostgreSQL DDL is transactional): a crash mid-rebuild
	// must not leave the table renamed away, or the next startup would create
	// a fresh empty rate_limits and orphan every rule. If concurrent replicas
	// race here, one commits and the others fail-fast and see the migrated
	// schema on restart.
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin rate_limits scoped migration: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	for _, stmt := range []string{
		`ALTER TABLE rate_limits RENAME TO rate_limits_pre_scope`,
		postgresRateLimitsSchema,
		`INSERT INTO rate_limits (scope, subject, period_seconds, max_requests, max_tokens, source, created_at, updated_at)
			SELECT 'user_path', user_path, period_seconds, max_requests, max_tokens, source, created_at, updated_at
			FROM rate_limits_pre_scope`,
		`DROP TABLE rate_limits_pre_scope`,
	} {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("migrate rate_limits to scoped schema: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit rate_limits scoped migration: %w", err)
	}
	return nil
}

func (s *PostgreSQLStore) ListRules(ctx context.Context) ([]Rule, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT scope, subject, period_seconds, max_requests, max_tokens, source, created_at, updated_at
		FROM rate_limits
		ORDER BY scope ASC, subject ASC, period_seconds ASC
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

func (s *PostgreSQLStore) DeleteRule(ctx context.Context, scope RuleScope, subject string, periodSeconds int64) error {
	scope, subject, err := normalizeRuleKey(scope, subject, periodSeconds)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM rate_limits
		WHERE scope = $1 AND subject = $2 AND period_seconds = $3
	`, string(scope), subject, periodSeconds)
	if err != nil {
		return fmt.Errorf("delete rate limit rule %s %s/%d: %w", scope, subject, periodSeconds, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: %s %s/%d", ErrNotFound, scope, subject, periodSeconds)
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
		args := make([]any, 0, 1+len(rules)*3)
		args = append(args, SourceConfig)
		for _, rule := range rules {
			base := len(args) + 1
			conditions = append(conditions, fmt.Sprintf(`(scope = $%d AND subject = $%d AND period_seconds = $%d)`, base, base+1, base+2))
			args = append(args, string(rule.Scope), rule.Subject, rule.PeriodSeconds)
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
			INSERT INTO rate_limits (scope, subject, period_seconds, max_requests, max_tokens, source, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (scope, subject, period_seconds) DO UPDATE SET
				max_requests = CASE WHEN excluded.source = $9 OR rate_limits.source = $10 THEN excluded.max_requests ELSE rate_limits.max_requests END,
				max_tokens = CASE WHEN excluded.source = $9 OR rate_limits.source = $10 THEN excluded.max_tokens ELSE rate_limits.max_tokens END,
				source = CASE WHEN excluded.source = $9 OR rate_limits.source = $10 THEN excluded.source ELSE rate_limits.source END,
				updated_at = CASE WHEN excluded.source = $9 OR rate_limits.source = $10 THEN excluded.updated_at ELSE rate_limits.updated_at END
		`,
			string(rule.Scope),
			rule.Subject,
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
			return fmt.Errorf("upsert rate limit rule %s %s/%d: %w", rule.Scope, rule.Subject, rule.PeriodSeconds, err)
		}
	}
	return nil
}

func scanPostgreSQLRule(row pgx.Row) (Rule, error) {
	var rule Rule
	var scope string
	var maxRequests *int64
	var maxTokens *int64
	var createdAt int64
	var updatedAt int64
	if err := row.Scan(
		&scope,
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
	rule.Scope = RuleScope(scope)
	rule.MaxRequests = maxRequests
	rule.MaxTokens = maxTokens
	rule.CreatedAt = time.Unix(createdAt, 0).UTC()
	rule.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return rule, nil
}
