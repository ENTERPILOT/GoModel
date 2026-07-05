package ratelimit

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// sqliteRateLimitsSchema is the one source of the table shape, shared by
// fresh installs and the pre-scope migration rebuild.
const sqliteRateLimitsSchema = `
	CREATE TABLE IF NOT EXISTS rate_limits (
		scope TEXT NOT NULL DEFAULT 'user_path',
		subject TEXT NOT NULL,
		period_seconds INTEGER NOT NULL,
		max_requests INTEGER,
		max_tokens INTEGER,
		source TEXT NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL,
		PRIMARY KEY (scope, subject, period_seconds)
	)`

type SQLiteStore struct {
	db *sql.DB
}

func NewSQLiteStore(db *sql.DB) (*SQLiteStore, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection is required")
	}
	if err := migrateSQLitePreScopeTable(db); err != nil {
		return nil, err
	}
	if _, err := db.Exec(sqliteRateLimitsSchema); err != nil {
		return nil, fmt.Errorf("failed to create rate_limits table: %w", err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_rate_limits_subject ON rate_limits(scope, subject)`); err != nil {
		return nil, fmt.Errorf("failed to create rate limit index: %w", err)
	}
	return &SQLiteStore{db: db}, nil
}

// migrateSQLitePreScopeTable rebuilds a rate_limits table created before rule
// scopes existed (keyed by user_path only) into the scoped shape.
func migrateSQLitePreScopeTable(db *sql.DB) error {
	var hasUserPath bool
	rows, err := db.Query(`PRAGMA table_info(rate_limits)`)
	if err != nil {
		return fmt.Errorf("inspect rate_limits schema: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			return fmt.Errorf("inspect rate_limits schema: %w", err)
		}
		if name == "subject" {
			return nil // already scoped
		}
		if name == "user_path" {
			hasUserPath = true
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("inspect rate_limits schema: %w", err)
	}
	if !hasUserPath {
		return nil // table does not exist yet
	}
	// One transaction: a crash mid-rebuild must not leave the table renamed
	// away, or the next startup would create a fresh empty rate_limits and
	// orphan every rule.
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin rate_limits scoped migration: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	for _, stmt := range []string{
		`ALTER TABLE rate_limits RENAME TO rate_limits_pre_scope`,
		sqliteRateLimitsSchema,
		`INSERT INTO rate_limits (scope, subject, period_seconds, max_requests, max_tokens, source, created_at, updated_at)
			SELECT 'user_path', user_path, period_seconds, max_requests, max_tokens, source, created_at, updated_at
			FROM rate_limits_pre_scope`,
		`DROP TABLE rate_limits_pre_scope`,
		`DROP INDEX IF EXISTS idx_rate_limits_user_path`,
	} {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("migrate rate_limits to scoped schema: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit rate_limits scoped migration: %w", err)
	}
	return nil
}

func (s *SQLiteStore) ListRules(ctx context.Context) ([]Rule, error) {
	rows, err := s.db.QueryContext(ctx, `
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

func (s *SQLiteStore) DeleteRule(ctx context.Context, scope RuleScope, subject string, periodSeconds int64) error {
	scope, subject, err := normalizeRuleKey(scope, subject, periodSeconds)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM rate_limits
		WHERE scope = ? AND subject = ? AND period_seconds = ?
	`, scope, subject, periodSeconds)
	if err != nil {
		return fmt.Errorf("delete rate limit rule %s %s/%d: %w", scope, subject, periodSeconds, err)
	}
	affected, err := result.RowsAffected()
	if err == nil && affected == 0 {
		return fmt.Errorf("%w: %s %s/%d", ErrNotFound, scope, subject, periodSeconds)
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
		args := make([]any, 0, 1+len(rules)*3)
		args = append(args, SourceConfig)
		for _, rule := range rules {
			conditions = append(conditions, `(scope = ? AND subject = ? AND period_seconds = ?)`)
			args = append(args, rule.Scope, rule.Subject, rule.PeriodSeconds)
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
		INSERT INTO rate_limits (scope, subject, period_seconds, max_requests, max_tokens, source, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(scope, subject, period_seconds) DO UPDATE SET
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
			rule.Scope,
			rule.Subject,
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
			return fmt.Errorf("upsert rate limit rule %s %s/%d: %w", rule.Scope, rule.Subject, rule.PeriodSeconds, err)
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
