package budget

import (
	"context"
	"fmt"
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

var sqlBudgetsTable = `CREATE TABLE IF NOT EXISTS budgets (
		user_path TEXT NOT NULL,
		period_seconds ` + sqlx.TypeInt64 + ` NOT NULL,
		amount ` + sqlx.TypeFloat + ` NOT NULL,
		source TEXT NOT NULL DEFAULT '',
		last_reset_at ` + sqlx.TypeInt64 + `,
		created_at ` + sqlx.TypeInt64 + ` NOT NULL,
		updated_at ` + sqlx.TypeInt64 + ` NOT NULL,
		PRIMARY KEY (user_path, period_seconds)
	)`

// sqlRest is applied after the budgets migrations.
var sqlRest = []string{
	`CREATE TABLE IF NOT EXISTS budget_settings (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL,
		updated_at ` + sqlx.TypeInt64 + ` NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_budgets_user_path ON budgets(user_path)`,
	`CREATE INDEX IF NOT EXISTS idx_budgets_period_seconds ON budgets(period_seconds)`,
}

// sqlMigrations backfill columns added after the table's first release.
var sqlMigrations = []string{
	`ALTER TABLE budgets ADD COLUMN source TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE budgets ADD COLUMN last_reset_at ` + sqlx.TypeInt64,
}

// upsertBudgetSQL preserves a manually edited budget against a config re-seed:
// a column is only overwritten when the incoming or stored row is manual, or
// when both are config-sourced.
const upsertBudgetSQL = `
	INSERT INTO budgets (user_path, period_seconds, amount, source, last_reset_at, created_at, updated_at)
	VALUES (?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(user_path, period_seconds) DO UPDATE SET
		amount = CASE WHEN excluded.source = ? OR budgets.source = ? THEN excluded.amount ELSE budgets.amount END,
		source = CASE WHEN excluded.source = ? OR budgets.source = ? THEN excluded.source ELSE budgets.source END,
		updated_at = CASE WHEN excluded.source = ? OR budgets.source = ? THEN excluded.updated_at ELSE budgets.updated_at END
`

// NewSQLStore creates the budget tables and indexes if needed.
func NewSQLStore(ctx context.Context, db sqlx.DB) (*SQLStore, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection is required")
	}
	if err := db.Schema(ctx, sqlBudgetsTable); err != nil {
		return nil, fmt.Errorf("failed to create budgets table: %w", err)
	}
	if err := sqlx.AddColumns(ctx, db, sqlMigrations...); err != nil {
		return nil, fmt.Errorf("failed to migrate budgets table: %w", err)
	}
	if err := db.Schema(ctx, sqlRest...); err != nil {
		return nil, fmt.Errorf("failed to create budget tables: %w", err)
	}
	return &SQLStore{db: db}, nil
}

func (s *SQLStore) ListBudgets(ctx context.Context) ([]Budget, error) {
	rows, err := s.db.Query(ctx, `
		SELECT user_path, period_seconds, amount, source, last_reset_at, created_at, updated_at
		FROM budgets
		ORDER BY user_path ASC, period_seconds ASC
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

func (s *SQLStore) DeleteBudget(ctx context.Context, userPath string, periodSeconds int64) error {
	userPath, err := NormalizeUserPath(userPath)
	if err != nil {
		return err
	}
	if periodSeconds <= 0 {
		return fmt.Errorf("period_seconds must be greater than 0")
	}
	affected, err := s.db.Exec(ctx, `
		DELETE FROM budgets
		WHERE user_path = ? AND period_seconds = ?
	`, userPath, periodSeconds)
	if err != nil {
		return fmt.Errorf("delete budget %s/%d: %w", userPath, periodSeconds, err)
	}
	if affected == 0 {
		return fmt.Errorf("%w: %s/%d", ErrNotFound, userPath, periodSeconds)
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
				conditions = append(conditions, `(user_path = ? AND period_seconds = ?)`)
				args = append(args, budget.UserPath, budget.PeriodSeconds)
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

func (s *SQLStore) ResetBudget(ctx context.Context, userPath string, periodSeconds int64, at time.Time) error {
	userPath, err := NormalizeUserPath(userPath)
	if err != nil {
		return err
	}
	if periodSeconds <= 0 {
		return fmt.Errorf("period_seconds must be greater than 0")
	}
	affected, err := s.db.Exec(ctx, `
		UPDATE budgets
		SET last_reset_at = ?, updated_at = ?
		WHERE user_path = ? AND period_seconds = ?
	`, at.UTC().Unix(), at.UTC().Unix(), userPath, periodSeconds)
	if err != nil {
		return fmt.Errorf("reset budget %s/%d: %w", userPath, periodSeconds, err)
	}
	if affected == 0 {
		return fmt.Errorf("%w: %s/%d", ErrNotFound, userPath, periodSeconds)
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

// SumUsageCost totals uncached spend for a budget's subtree over a window.
//
// This is the one budget statement that cannot be shared: SQLite keeps the
// usage timestamp as text and must convert it, while PostgreSQL compares a
// real timestamp column and has to quote "usage" as a reserved word.
func (s *SQLStore) SumUsageCost(ctx context.Context, userPath string, start, end time.Time) (float64, bool, error) {
	userPath, err := NormalizeUserPath(userPath)
	if err != nil {
		return 0, false, err
	}
	userPathExpr := usagePathMatchesBudgetExpr("user_path")

	var query string
	var args []any
	switch s.db.Dialect() {
	case sqlx.SQLite:
		epoch := "unixepoch(REPLACE(timestamp, ' ', 'T'))"
		query = `SELECT SUM(total_cost) FROM usage
			WHERE ` + epoch + ` >= unixepoch(?)
				AND ` + epoch + ` < unixepoch(?)
				AND (` + userPathExpr + ` = ? OR ` + userPathExpr + ` LIKE ? ESCAPE '\')
				AND (cache_type IS NULL OR cache_type = '')`
		args = []any{
			start.UTC().Format(time.RFC3339Nano),
			end.UTC().Format(time.RFC3339Nano),
			userPath,
			usagePathLikePattern(userPath),
		}
	case sqlx.PostgreSQL:
		query = `SELECT SUM(total_cost) FROM "usage"
			WHERE timestamp >= ?
				AND timestamp < ?
				AND (` + userPathExpr + ` = ? OR ` + userPathExpr + ` LIKE ? ESCAPE '\')
				AND (cache_type IS NULL OR cache_type = '')`
		args = []any{start.UTC(), end.UTC(), userPath, usagePathLikePattern(userPath)}
	default:
		return 0, false, fmt.Errorf("unsupported dialect %q", s.db.Dialect())
	}

	var total *float64
	if err := s.db.QueryRow(ctx, query, args...).Scan(&total); err != nil {
		return 0, false, fmt.Errorf("sum usage cost: %w", err)
	}
	if total == nil {
		return 0, false, nil
	}
	return *total, true, nil
}

func (s *SQLStore) Close() error {
	return nil
}

func upsertBudgets(ctx context.Context, q sqlx.Querier, budgets []Budget) error {
	for _, budget := range budgets {
		_, err := q.Exec(ctx, upsertBudgetSQL,
			budget.UserPath,
			budget.PeriodSeconds,
			budget.Amount,
			budget.Source,
			sqlutil.UnixOrNil(budget.LastResetAt),
			budget.CreatedAt.Unix(),
			budget.UpdatedAt.Unix(),
			SourceManual, SourceConfig,
			SourceManual, SourceConfig,
			SourceManual, SourceConfig,
		)
		if err != nil {
			return fmt.Errorf("upsert budget %s/%d: %w", budget.UserPath, budget.PeriodSeconds, err)
		}
	}
	return nil
}

func scanSQLBudget(scanner sqlx.Row) (Budget, error) {
	var budget Budget
	var lastResetAt *int64
	var createdAt, updatedAt int64
	if err := scanner.Scan(
		&budget.UserPath,
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
	budget.CreatedAt = time.Unix(createdAt, 0).UTC()
	budget.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return budget, nil
}
