package budget

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/enterpilot/gomodel/internal/storage/sqlutil"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

const (
	settingDailyResetHour     = "daily_reset_hour"
	settingDailyResetMinute   = "daily_reset_minute"
	settingWeeklyResetWeekday = "weekly_reset_weekday"
	settingWeeklyResetHour    = "weekly_reset_hour"
	settingWeeklyResetMinute  = "weekly_reset_minute"
	settingMonthlyResetDay    = "monthly_reset_day"
	settingMonthlyResetHour   = "monthly_reset_hour"
	settingMonthlyResetMinute = "monthly_reset_minute"
)

// SQLStore stores budgets and budget settings in a SQL database.
type SQLStore struct {
	db sqlx.DB
}

// sqlBudgetsSchema is shared by fresh installs and the pre-scope migration
// rebuild.
var sqlBudgetsSchema = `CREATE TABLE IF NOT EXISTS budgets (
		scope TEXT NOT NULL DEFAULT 'user_path',
		subject TEXT NOT NULL,
		per_child ` + sqlx.TypeBool + ` NOT NULL DEFAULT FALSE,
		period_seconds ` + sqlx.TypeInt64 + ` NOT NULL,
		amount ` + sqlx.TypeFloat + ` NOT NULL,
		source TEXT NOT NULL DEFAULT '',
		last_reset_at ` + sqlx.TypeInt64 + `,
		created_at ` + sqlx.TypeInt64 + ` NOT NULL,
		updated_at ` + sqlx.TypeInt64 + ` NOT NULL,
		PRIMARY KEY (scope, subject, period_seconds)
	)`

// sqlRest is applied after the budgets table exists.
var sqlRest = []string{
	`CREATE TABLE IF NOT EXISTS budget_settings (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL,
		updated_at ` + sqlx.TypeInt64 + ` NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_budgets_subject ON budgets(scope, subject)`,
	`CREATE INDEX IF NOT EXISTS idx_budgets_period_seconds ON budgets(period_seconds)`,
}

// upsertBudgetSQL preserves a manually edited budget against a config re-seed:
// a column is only overwritten when the incoming or stored row is manual, or
// when both are config-sourced.
const upsertBudgetSQL = `
	INSERT INTO budgets (scope, subject, per_child, period_seconds, amount, source, last_reset_at, created_at, updated_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(scope, subject, period_seconds) DO UPDATE SET
		per_child = CASE WHEN excluded.source = ? OR budgets.source = ? THEN excluded.per_child ELSE budgets.per_child END,
		amount = CASE WHEN excluded.source = ? OR budgets.source = ? THEN excluded.amount ELSE budgets.amount END,
		source = CASE WHEN excluded.source = ? OR budgets.source = ? THEN excluded.source ELSE budgets.source END,
		updated_at = CASE WHEN excluded.source = ? OR budgets.source = ? THEN excluded.updated_at ELSE budgets.updated_at END
`

// NewSQLStore creates the budget tables and indexes if needed.
func NewSQLStore(ctx context.Context, db sqlx.DB) (*SQLStore, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection is required")
	}
	if err := migratePreScopeTable(ctx, db); err != nil {
		return nil, err
	}
	if err := db.Schema(ctx, sqlBudgetsSchema); err != nil {
		return nil, fmt.Errorf("failed to create budgets table: %w", err)
	}
	if err := sqlx.AddColumns(ctx, db,
		`ALTER TABLE budgets ADD COLUMN per_child `+sqlx.TypeBool+` NOT NULL DEFAULT FALSE`,
	); err != nil {
		return nil, fmt.Errorf("failed to migrate budgets table: %w", err)
	}
	if err := db.Schema(ctx, sqlRest...); err != nil {
		return nil, fmt.Errorf("failed to create budget tables: %w", err)
	}
	return &SQLStore{db: db}, nil
}

func (s *SQLStore) ListBudgets(ctx context.Context) ([]Budget, error) {
	rows, err := s.db.Query(ctx, `
		SELECT scope, subject, per_child, period_seconds, amount, source, last_reset_at, created_at, updated_at
		FROM budgets
		ORDER BY scope ASC, subject ASC, period_seconds ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list budgets: %w", err)
	}
	defer rows.Close()

	var budgets []Budget
	for rows.Next() {
		budget, err := scanSQLBudget(rows)
		if err != nil {
			return nil, err
		}
		budgets = append(budgets, budget)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate budgets: %w", err)
	}
	return budgets, nil
}

func (s *SQLStore) UpsertBudgets(ctx context.Context, budgets []Budget) error {
	budgets, err := normalizeBudgetsForUpsert(budgets)
	if err != nil {
		return err
	}
	if len(budgets) == 0 {
		return nil
	}
	return s.db.InTx(ctx, func(q sqlx.Querier) error {
		return upsertBudgets(ctx, q, budgets)
	})
}

func (s *SQLStore) DeleteBudget(ctx context.Context, scope Scope, subject string, periodSeconds int64) error {
	scope, subject, err := normalizeBudgetKey(scope, subject, periodSeconds)
	if err != nil {
		return err
	}
	affected, err := s.db.Exec(ctx, `
		DELETE FROM budgets
		WHERE scope = ? AND subject = ? AND period_seconds = ?
	`, scope, subject, periodSeconds)
	if err != nil {
		return fmt.Errorf("delete budget %s %s/%d: %w", scope, subject, periodSeconds, err)
	}
	if affected == 0 {
		return fmt.Errorf("%w: %s %s/%d", ErrNotFound, scope, subject, periodSeconds)
	}
	return nil
}

func (s *SQLStore) ReplaceConfigBudgets(ctx context.Context, budgets []Budget) error {
	budgets, err := normalizeBudgetsForUpsert(budgets)
	if err != nil {
		return err
	}
	for i := range budgets {
		budgets[i].Source = SourceConfig
	}

	return s.db.InTx(ctx, func(q sqlx.Querier) error {
		// Drop the config-sourced budgets that are no longer declared, leaving
		// manually created ones alone.
		query := `DELETE FROM budgets WHERE source = ?`
		args := []any{SourceConfig}
		if len(budgets) > 0 {
			conditions := make([]string, 0, len(budgets))
			for _, budget := range budgets {
				conditions = append(conditions, `(scope = ? AND subject = ? AND period_seconds = ?)`)
				args = append(args, budget.Scope, budget.Subject, budget.PeriodSeconds)
			}
			query += ` AND NOT (` + strings.Join(conditions, " OR ") + `)`
		}
		if _, err := q.Exec(ctx, query, args...); err != nil {
			return fmt.Errorf("delete old config budgets: %w", err)
		}
		return upsertBudgets(ctx, q, budgets)
	})
}

func (s *SQLStore) GetSettings(ctx context.Context) (Settings, error) {
	rows, err := s.db.Query(ctx, `SELECT key, value, updated_at FROM budget_settings`)
	if err != nil {
		return Settings{}, fmt.Errorf("get budget settings: %w", err)
	}
	defer rows.Close()
	return scanSettingsRows(rows)
}

func (s *SQLStore) SaveSettings(ctx context.Context, settings Settings) (Settings, error) {
	if err := ValidateSettings(settings); err != nil {
		return Settings{}, err
	}
	settings.UpdatedAt = time.Now().UTC()
	values := settingsKeyValues(settings)

	err := s.db.InTx(ctx, func(q sqlx.Querier) error {
		for key, value := range values {
			_, err := q.Exec(ctx, `
				INSERT INTO budget_settings (key, value, updated_at)
				VALUES (?, ?, ?)
				ON CONFLICT(key) DO UPDATE SET
					value = excluded.value,
					updated_at = excluded.updated_at
			`, key, strconv.Itoa(value), settings.UpdatedAt.Unix())
			if err != nil {
				return fmt.Errorf("save budget setting %s: %w", key, err)
			}
		}
		return nil
	})
	if err != nil {
		return Settings{}, err
	}
	return settings, nil
}

func (s *SQLStore) ResetBudget(ctx context.Context, scope Scope, subject string, periodSeconds int64, at time.Time) error {
	scope, subject, err := normalizeBudgetKey(scope, subject, periodSeconds)
	if err != nil {
		return err
	}
	affected, err := s.db.Exec(ctx, `
		UPDATE budgets
		SET last_reset_at = ?, updated_at = ?
		WHERE scope = ? AND subject = ? AND period_seconds = ?
	`, at.UTC().Unix(), at.UTC().Unix(), scope, subject, periodSeconds)
	if err != nil {
		return fmt.Errorf("reset budget %s %s/%d: %w", scope, subject, periodSeconds, err)
	}
	if affected == 0 {
		return fmt.Errorf("%w: %s %s/%d", ErrNotFound, scope, subject, periodSeconds)
	}
	return nil
}

func (s *SQLStore) ResetAllBudgets(ctx context.Context, at time.Time) error {
	_, err := s.db.Exec(ctx,
		`UPDATE budgets SET last_reset_at = ?, updated_at = ?`, at.UTC().Unix(), at.UTC().Unix())
	if err != nil {
		return fmt.Errorf("reset all budgets: %w", err)
	}
	return nil
}

// spendChunkSize caps how many windows share one statement. Each window binds
// up to four parameters, keeping a chunk well inside the 999-parameter limit
// of older SQLite builds.
const spendChunkSize = 64

// SumSpend totals uncached spend for every window with one scan per chunk.
//
// A budget check matches several budgets at once — a user-path subtree plus a
// budget per request label — and running one statement each would scan the
// usage table once per budget. Conditional aggregation collapses them into a
// single pass: the outer WHERE spans the union of the windows, and each window
// gets its own SUM(CASE ...) column.
func (s *SQLStore) SumSpend(ctx context.Context, windows []SpendWindow) ([]Spend, error) {
	if len(windows) == 0 {
		return nil, nil
	}
	if dialect := s.db.Dialect(); dialect != sqlx.SQLite && dialect != sqlx.PostgreSQL {
		return nil, fmt.Errorf("unsupported dialect %q", dialect)
	}
	spends := make([]Spend, 0, len(windows))
	for chunk := range slices.Chunk(windows, spendChunkSize) {
		part, err := s.sumSpendChunk(ctx, chunk)
		if err != nil {
			return nil, err
		}
		spends = append(spends, part...)
	}
	return spends, nil
}

func (s *SQLStore) sumSpendChunk(ctx context.Context, windows []SpendWindow) ([]Spend, error) {
	// SQLite keeps the usage timestamp as text and must convert it, while
	// PostgreSQL compares a real timestamp column and has to quote "usage" as a
	// reserved word.
	sqlite := s.db.Dialect() == sqlx.SQLite
	timeExpr, timeBound, table := "timestamp", "?", `"usage"`
	timeArg := func(t time.Time) any { return t.UTC() }
	if sqlite {
		timeExpr, timeBound, table = "unixepoch(REPLACE(timestamp, ' ', 'T'))", "unixepoch(?)", "usage"
		timeArg = func(t time.Time) any { return t.UTC().Format(time.RFC3339Nano) }
	}

	columns := make([]string, 0, len(windows))
	args := make([]any, 0, len(windows)*4+2)
	for _, window := range windows {
		match, matchArgs, err := spendSubjectMatch(window, sqlite)
		if err != nil {
			return nil, err
		}
		columns = append(columns, "SUM(CASE WHEN "+timeExpr+" >= "+timeBound+
			" AND "+timeExpr+" < "+timeBound+" AND "+match+" THEN total_cost END)")
		args = append(args, timeArg(window.Start), timeArg(window.End))
		args = append(args, matchArgs...)
	}
	query := `SELECT ` + strings.Join(columns, ", ") + ` FROM ` + table +
		` WHERE ` + timeExpr + ` >= ` + timeBound +
		` AND ` + timeExpr + ` < ` + timeBound +
		` AND (cache_type IS NULL OR cache_type = '')`
	minStart, maxEnd := spendBounds(windows)
	args = append(args, timeArg(minStart), timeArg(maxEnd))

	totals := make([]*float64, len(windows))
	dest := make([]any, len(windows))
	for i := range totals {
		dest[i] = &totals[i]
	}
	if err := s.db.QueryRow(ctx, query, args...).Scan(dest...); err != nil {
		return nil, fmt.Errorf("sum usage cost: %w", err)
	}

	spends := make([]Spend, len(windows))
	for i, total := range totals {
		// A NULL sum means the window matched no priced usage at all, which is
		// what keeps an untouched budget from blocking.
		if total != nil {
			spends[i] = Spend{Total: *total, HasUsage: true}
		}
	}
	return spends, nil
}

// spendSubjectMatch renders the usage-row predicate for one budget subject.
func spendSubjectMatch(window SpendWindow, sqlite bool) (string, []any, error) {
	if window.Scope == ScopeLabel {
		subject, err := NormalizeSubject(ScopeLabel, window.Subject)
		if err != nil {
			return "", nil, err
		}
		if sqlite {
			return `(labels IS NOT NULL AND EXISTS (SELECT 1 FROM json_each(usage.labels) WHERE json_each.value = ?))`,
				[]any{subject}, nil
		}
		// The function form, not the `?` operator, so the placeholder rewriter
		// never has to tell the two apart.
		return `jsonb_exists(labels, ?)`, []any{subject}, nil
	}
	userPath, err := NormalizeUserPath(window.Subject)
	if err != nil {
		return "", nil, err
	}
	pathExpr := usagePathMatchesBudgetExpr("user_path")
	return `(` + pathExpr + ` = ? OR ` + pathExpr + ` LIKE ? ESCAPE '\')`,
		[]any{userPath, usagePathLikePattern(userPath)}, nil
}

// usagePathMatchesBudgetExpr normalizes a stored user path the way budget
// matching expects: missing and blank paths count as the root.
func usagePathMatchesBudgetExpr(column string) string {
	return "COALESCE(NULLIF(TRIM(" + column + "), ''), '/')"
}

func usagePathLikePattern(userPath string) string {
	if userPath == "/" {
		return "/%"
	}
	return escapeLikeWildcards(userPath) + "/%"
}

func escapeLikeWildcards(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}

func (s *SQLStore) Close() error {
	return nil
}

func upsertBudgets(ctx context.Context, q sqlx.Querier, budgets []Budget) error {
	for _, budget := range budgets {
		_, err := q.Exec(ctx, upsertBudgetSQL,
			budget.Scope,
			budget.Subject,
			budget.PerChild,
			budget.PeriodSeconds,
			budget.Amount,
			budget.Source,
			sqlutil.UnixOrNil(budget.LastResetAt),
			budget.CreatedAt.Unix(),
			budget.UpdatedAt.Unix(),
			SourceManual, SourceConfig,
			SourceManual, SourceConfig,
			SourceManual, SourceConfig,
			SourceManual, SourceConfig,
		)
		if err != nil {
			return fmt.Errorf("upsert budget %s %s/%d: %w", budget.Scope, budget.Subject, budget.PeriodSeconds, err)
		}
	}
	return nil
}

func scanSQLBudget(scanner sqlx.Row) (Budget, error) {
	var budget Budget
	var lastResetAt *int64
	var createdAt, updatedAt int64
	if err := scanner.Scan(
		&budget.Scope,
		&budget.Subject,
		&budget.PerChild,
		&budget.PeriodSeconds,
		&budget.Amount,
		&budget.Source,
		&lastResetAt,
		&createdAt,
		&updatedAt,
	); err != nil {
		return Budget{}, fmt.Errorf("scan budget: %w", err)
	}
	budget.LastResetAt = sqlutil.TimeFromUnixPtr(lastResetAt)
	budget.CreatedAt = sqlutil.TimeFromUnix(createdAt)
	budget.UpdatedAt = sqlutil.TimeFromUnix(updatedAt)
	return budget, nil
}
