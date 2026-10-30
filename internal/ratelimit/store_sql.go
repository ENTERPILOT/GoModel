package ratelimit

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/enterpilot/gomodel/internal/storage/sqlutil"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// sqlRateLimitsSchema is the one source of the table shape, shared by fresh
// installs and the pre-scope migration rebuild.
const sqlRateLimitsSchema = `
	CREATE TABLE IF NOT EXISTS rate_limits (
		scope TEXT NOT NULL DEFAULT 'user_path',
		subject TEXT NOT NULL,
		subject_display TEXT NOT NULL DEFAULT '',
		per_child ` + sqlx.TypeBool + ` NOT NULL DEFAULT FALSE,
		period_seconds ` + sqlx.TypeInt64 + ` NOT NULL,
		max_requests ` + sqlx.TypeInt64 + `,
		max_tokens ` + sqlx.TypeInt64 + `,
		source TEXT NOT NULL DEFAULT '',
		created_at ` + sqlx.TypeInt64 + ` NOT NULL,
		updated_at ` + sqlx.TypeInt64 + ` NOT NULL,
		counter_epoch TEXT NOT NULL DEFAULT '',
		PRIMARY KEY (scope, subject, period_seconds)
	)`

// sqlRateLimitWindowSnapshotsSchema holds one row per window per writer, so
// replicas sharing the database keep their own rows instead of overwriting
// each other's every flush. It replaces rate_limit_counters, whose key had no
// writer; see importLegacyCounters.
const sqlRateLimitWindowSnapshotsSchema = `
	CREATE TABLE IF NOT EXISTS rate_limit_window_snapshots (
		scope TEXT NOT NULL,
		subject TEXT NOT NULL,
		partition TEXT NOT NULL DEFAULT '',
		period_seconds ` + sqlx.TypeInt64 + ` NOT NULL,
		writer TEXT NOT NULL DEFAULT '',
		epoch TEXT NOT NULL DEFAULT '',
		requests_window_start ` + sqlx.TypeInt64 + ` NOT NULL DEFAULT 0,
		requests_current ` + sqlx.TypeInt64 + ` NOT NULL DEFAULT 0,
		requests_previous ` + sqlx.TypeInt64 + ` NOT NULL DEFAULT 0,
		tokens_window_start ` + sqlx.TypeInt64 + ` NOT NULL DEFAULT 0,
		tokens_current ` + sqlx.TypeInt64 + ` NOT NULL DEFAULT 0,
		tokens_previous ` + sqlx.TypeInt64 + ` NOT NULL DEFAULT 0,
		in_flight ` + sqlx.TypeInt64 + ` NOT NULL DEFAULT 0,
		updated_at ` + sqlx.TypeInt64 + ` NOT NULL,
		PRIMARY KEY (scope, subject, partition, period_seconds, writer)
	)`

// SQLStore stores rate limit rules in a SQL database.
type SQLStore struct {
	db sqlx.DB
	// legacyCounters records that the pre-writer rate_limit_counters table
	// exists, so resets clear it too: an older replica still running during
	// a rolling upgrade keeps writing there, and the next start imports it.
	legacyCounters bool
}

// upsertRuleSQL preserves a manually edited rule against a config re-seed:
// a column is only overwritten when the incoming or stored row is manual, or
// when both are config-sourced. The counter epoch the insert carries is kept
// only by a new row or an update that switches per_child; see
// Rule.CounterEpoch.
const upsertRuleSQL = `
	INSERT INTO rate_limits (scope, subject, subject_display, per_child, period_seconds, max_requests, max_tokens, source, created_at, updated_at, counter_epoch)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(scope, subject, period_seconds) DO UPDATE SET
		counter_epoch = CASE WHEN (excluded.source = ? OR rate_limits.source = ?) AND excluded.per_child <> rate_limits.per_child THEN excluded.counter_epoch ELSE rate_limits.counter_epoch END,
		subject_display = CASE WHEN excluded.source = ? OR rate_limits.source = ? THEN excluded.subject_display ELSE rate_limits.subject_display END,
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
		`ALTER TABLE rate_limits ADD COLUMN subject_display TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE rate_limits ADD COLUMN counter_epoch TEXT NOT NULL DEFAULT ''`,
	); err != nil {
		return nil, fmt.Errorf("failed to migrate rate_limits table: %w", err)
	}
	if err := db.Schema(ctx, `CREATE INDEX IF NOT EXISTS idx_rate_limits_subject ON rate_limits(scope, subject)`); err != nil {
		return nil, fmt.Errorf("failed to create rate limit index: %w", err)
	}
	if err := db.Schema(ctx, sqlRateLimitWindowSnapshotsSchema); err != nil {
		return nil, fmt.Errorf("failed to create rate_limit_window_snapshots table: %w", err)
	}
	if err := sqlx.AddColumns(ctx, db,
		`ALTER TABLE rate_limit_window_snapshots ADD COLUMN in_flight `+sqlx.TypeInt64+` NOT NULL DEFAULT 0`,
	); err != nil {
		return nil, fmt.Errorf("failed to migrate rate_limit_window_snapshots table: %w", err)
	}
	// Every replica's flush loads the rows saved since its previous load.
	if err := db.Schema(ctx, `CREATE INDEX IF NOT EXISTS idx_rate_limit_window_snapshots_updated_at ON rate_limit_window_snapshots(updated_at)`); err != nil {
		return nil, fmt.Errorf("failed to create rate limit counter index: %w", err)
	}
	legacy, err := importLegacyCounters(ctx, db)
	if err != nil {
		return nil, err
	}
	return &SQLStore{db: db, legacyCounters: legacy}, nil
}

func (s *SQLStore) ListRules(ctx context.Context) ([]Rule, error) {
	rows, err := s.db.Query(ctx, `
		SELECT scope, subject, subject_display, per_child, period_seconds, max_requests, max_tokens, source, created_at, updated_at, counter_epoch
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

func (s *SQLStore) LoadCounters(ctx context.Context, since int64) ([]WindowSnapshot, error) {
	rows, err := s.db.Query(ctx, `
		SELECT scope, subject, partition, period_seconds, writer, epoch,
			requests_window_start, requests_current, requests_previous,
			tokens_window_start, tokens_current, tokens_previous,
			in_flight, updated_at
		FROM rate_limit_window_snapshots
		WHERE updated_at >= ?
	`, since)
	if err != nil {
		return nil, fmt.Errorf("list rate limit counters: %w", err)
	}
	defer rows.Close()

	var snapshots []WindowSnapshot
	for rows.Next() {
		var snap WindowSnapshot
		if err := rows.Scan(
			&snap.Scope, &snap.Subject, &snap.Partition, &snap.PeriodSeconds, &snap.Writer, &snap.Epoch,
			&snap.RequestsWindowStart, &snap.RequestsCurrent, &snap.RequestsPrevious,
			&snap.TokensWindowStart, &snap.TokensCurrent, &snap.TokensPrevious,
			&snap.InFlight, &snap.UpdatedAt,
		); err != nil {
			// One unreadable row must not cost every other window its
			// restore: Start treats a load error as "do not persist this
			// generation". Same policy as the Mongo loader.
			slog.Warn("rate limit counters: skipping malformed snapshot", "error", err)
			continue
		}
		snapshots = append(snapshots, snap)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate rate limit counters: %w", err)
	}
	return snapshots, nil
}

const upsertCounterSQL = `
	INSERT INTO rate_limit_window_snapshots (
		scope, subject, partition, period_seconds, writer, epoch,
		requests_window_start, requests_current, requests_previous,
		tokens_window_start, tokens_current, tokens_previous, in_flight, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(scope, subject, partition, period_seconds, writer) DO UPDATE SET
		epoch = excluded.epoch,
		requests_window_start = excluded.requests_window_start,
		requests_current = excluded.requests_current,
		requests_previous = excluded.requests_previous,
		tokens_window_start = excluded.tokens_window_start,
		tokens_current = excluded.tokens_current,
		tokens_previous = excluded.tokens_previous,
		in_flight = excluded.in_flight,
		updated_at = excluded.updated_at
`

func (s *SQLStore) SaveCounters(ctx context.Context, snapshots []WindowSnapshot) error {
	now := time.Now().Unix()
	return s.db.InTx(ctx, func(q sqlx.Querier) error {
		for _, snap := range snapshots {
			if _, err := q.Exec(ctx, upsertCounterSQL,
				snap.Scope, snap.Subject, snap.Partition, snap.PeriodSeconds, snap.Writer, snap.Epoch,
				snap.RequestsWindowStart, snap.RequestsCurrent, snap.RequestsPrevious,
				snap.TokensWindowStart, snap.TokensCurrent, snap.TokensPrevious, snap.InFlight, now,
			); err != nil {
				return fmt.Errorf("upsert rate limit counter: %w", err)
			}
		}
		if _, err := q.Exec(ctx, pruneCountersSQL, PeriodConcurrent, inFlightRowRetention, now); err != nil {
			return fmt.Errorf("prune expired rate limit counters: %w", err)
		}
		return nil
	})
}

// pruneCountersSQL collects the rows nobody writes any more: a rule that was
// deleted, a window that went unchanged, or a writer that stopped — every
// restart leaves its predecessor's rows behind. A window row is saved
// whenever it counts something, so two periods without a write means every
// count it holds has rolled out, the same bound a load drops it on. An
// in-flight row has no window: it is kept for inFlightRowRetention.
const pruneCountersSQL = `
	DELETE FROM rate_limit_window_snapshots
	WHERE updated_at + CASE WHEN period_seconds = ? THEN ? ELSE 2 * period_seconds END < ?
`

// ResetCounter draws a new counter epoch for the rule and removes every
// writer's rows for it in one transaction, so no replica's row, including one
// written after the delete under the old epoch, brings a reset window back.
func (s *SQLStore) ResetCounter(ctx context.Context, scope RuleScope, subject string, periodSeconds int64) error {
	err := s.resetCounters(ctx, ` WHERE scope = ? AND subject = ? AND period_seconds = ?`, scope, subject, periodSeconds)
	if err != nil {
		return fmt.Errorf("reset rate limit counters %s %s/%d: %w", scope, subject, periodSeconds, err)
	}
	return nil
}

// ResetAllCounters gives every rule the same new epoch: rows are keyed by
// rule as well, so sharing it is safe.
func (s *SQLStore) ResetAllCounters(ctx context.Context) error {
	if err := s.resetCounters(ctx, ""); err != nil {
		return fmt.Errorf("reset all rate limit counters: %w", err)
	}
	return nil
}

// resetCounters clears the legacy table before the window table. A starting
// replica's import locks the legacy table first (see importLegacyCounters), so
// in this order a reset either waits for the import to commit and then deletes
// what it wrote, or deletes first and leaves the import nothing to copy. Even
// a row the import writes back carries the empty epoch, which the new one
// never matches.
func (s *SQLStore) resetCounters(ctx context.Context, where string, args ...any) error {
	var tables []string
	if s.legacyCounters {
		tables = append(tables, legacyCountersTable)
	}
	tables = append(tables, "rate_limit_window_snapshots")
	epochArgs := append([]any{uuid.NewString()}, args...)
	return s.db.InTx(ctx, func(q sqlx.Querier) error {
		if _, err := q.Exec(ctx, `UPDATE rate_limits SET counter_epoch = ?`+where, epochArgs...); err != nil {
			return err
		}
		for _, table := range tables {
			if _, err := q.Exec(ctx, `DELETE FROM `+table+where, args...); err != nil {
				return err
			}
		}
		return nil
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
			rule.SubjectDisplay,
			rule.PerChild,
			rule.PeriodSeconds,
			nullableInt64(rule.MaxRequests),
			nullableInt64(rule.MaxTokens),
			rule.Source,
			rule.CreatedAt.Unix(),
			rule.UpdatedAt.Unix(),
			uuid.NewString(),
			SourceManual, SourceConfig,
			SourceManual, SourceConfig,
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
		&rule.SubjectDisplay,
		&rule.PerChild,
		&rule.PeriodSeconds,
		&maxRequests,
		&maxTokens,
		&rule.Source,
		&createdAt,
		&updatedAt,
		&rule.CounterEpoch,
	); err != nil {
		return Rule{}, fmt.Errorf("scan rate limit rule: %w", err)
	}
	rule.MaxRequests = maxRequests
	rule.MaxTokens = maxTokens
	rule.CreatedAt = sqlutil.TimeFromUnix(createdAt)
	rule.UpdatedAt = sqlutil.TimeFromUnix(updatedAt)
	return rule, nil
}

func nullableInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}
