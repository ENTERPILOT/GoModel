package budget

import (
	"context"
	"fmt"
	"strconv"
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
		CREATE TABLE IF NOT EXISTS budgets (
			user_path TEXT NOT NULL,
			period_seconds BIGINT NOT NULL,
			amount DOUBLE PRECISION NOT NULL,
			source TEXT NOT NULL DEFAULT '',
			last_reset_at BIGINT,
			created_at BIGINT NOT NULL,
			updated_at BIGINT NOT NULL,
			PRIMARY KEY (user_path, period_seconds)
		)
	`); err != nil {
		return nil, fmt.Errorf("failed to create budgets table: %w", err)
	}
	for _, migration := range []string{
		`ALTER TABLE budgets ADD COLUMN IF NOT EXISTS source TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE budgets ADD COLUMN IF NOT EXISTS last_reset_at BIGINT`,
	} {
		if _, err := pool.Exec(ctx, migration); err != nil {
			return nil, fmt.Errorf("failed to migrate budgets table: %w", err)
		}
	}
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS budget_settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL,
			updated_at BIGINT NOT NULL
		)
	`); err != nil {
		return nil, fmt.Errorf("failed to create budget_settings table: %w", err)
	}
	for _, index := range []string{
		`CREATE INDEX IF NOT EXISTS idx_budgets_user_path ON budgets(user_path)`,
		`CREATE INDEX IF NOT EXISTS idx_budgets_period_seconds ON budgets(period_seconds)`,
	} {
		if _, err := pool.Exec(ctx, index); err != nil {
			return nil, fmt.Errorf("failed to create budget index: %w", err)
		}
	}
	return &PostgreSQLStore{pool: pool}, nil
}

func (s *PostgreSQLStore) ListBudgets(ctx context.Context) ([]Budget, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT user_path, period_seconds, amount, source, last_reset_at, created_at, updated_at
		FROM budgets
		ORDER BY user_path ASC, period_seconds ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list budgets: %w", err)
	}
	defer rows.Close()

	budgets := make([]Budget, 0)
	for rows.Next() {
		budget, err := scanPostgreSQLBudget(rows)
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

func (s *PostgreSQLStore) UpsertBudgets(ctx context.Context, budgets []Budget) error {
	budgets, err := normalizeBudgetsForUpsert(budgets)
	if err != nil {
		return err
	}
	if len(budgets) == 0 {
		return nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin budget upsert: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	for _, budget := range budgets {
		_, err := tx.Exec(ctx, `
			INSERT INTO budgets (user_path, period_seconds, amount, source, last_reset_at, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (user_path, period_seconds) DO UPDATE SET
				amount = excluded.amount,
				source = excluded.source,
				updated_at = excluded.updated_at
		`,
			budget.UserPath,
			budget.PeriodSeconds,
			budget.Amount,
			budget.Source,
			unixOrNil(budget.LastResetAt),
			budget.CreatedAt.Unix(),
			budget.UpdatedAt.Unix(),
		)
		if err != nil {
			return fmt.Errorf("upsert budget %s/%d: %w", budget.UserPath, budget.PeriodSeconds, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit budget upsert: %w", err)
	}
	return nil
}

func (s *PostgreSQLStore) ReplaceConfigBudgets(ctx context.Context, budgets []Budget) error {
	budgets, err := normalizeBudgetsForUpsert(budgets)
	if err != nil {
		return err
	}
	for i := range budgets {
		budgets[i].Source = "config"
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin config budget replace: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if len(budgets) == 0 {
		if _, err := tx.Exec(ctx, `DELETE FROM budgets WHERE source = 'config'`); err != nil {
			return fmt.Errorf("delete old config budgets: %w", err)
		}
	} else {
		conditions := make([]string, 0, len(budgets))
		args := make([]any, 0, len(budgets)*2)
		for i, budget := range budgets {
			base := i*2 + 1
			conditions = append(conditions, fmt.Sprintf(`(user_path = $%d AND period_seconds = $%d)`, base, base+1))
			args = append(args, budget.UserPath, budget.PeriodSeconds)
		}
		query := `DELETE FROM budgets WHERE source = 'config' AND NOT (` + strings.Join(conditions, " OR ") + `)`
		if _, err := tx.Exec(ctx, query, args...); err != nil {
			return fmt.Errorf("delete old config budgets: %w", err)
		}
	}
	if err := upsertPostgreSQLBudgets(ctx, tx, budgets); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit config budget replace: %w", err)
	}
	return nil
}

func (s *PostgreSQLStore) GetSettings(ctx context.Context) (Settings, error) {
	rows, err := s.pool.Query(ctx, `SELECT key, value, updated_at FROM budget_settings`)
	if err != nil {
		return Settings{}, fmt.Errorf("get budget settings: %w", err)
	}
	defer rows.Close()

	return scanSettingsRows(rows)
}

func (s *PostgreSQLStore) SaveSettings(ctx context.Context, settings Settings) (Settings, error) {
	if err := ValidateSettings(settings); err != nil {
		return Settings{}, err
	}
	settings.UpdatedAt = time.Now().UTC()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Settings{}, fmt.Errorf("begin budget settings save: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	for key, value := range settingsKeyValues(settings) {
		if _, err := tx.Exec(ctx, `
			INSERT INTO budget_settings (key, value, updated_at)
			VALUES ($1, $2, $3)
			ON CONFLICT (key) DO UPDATE SET
				value = excluded.value,
				updated_at = excluded.updated_at
		`, key, strconv.Itoa(value), settings.UpdatedAt.Unix()); err != nil {
			return Settings{}, fmt.Errorf("save budget setting %s: %w", key, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Settings{}, fmt.Errorf("commit budget settings save: %w", err)
	}
	return settings, nil
}

func (s *PostgreSQLStore) ResetAllBudgets(ctx context.Context, at time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE budgets SET last_reset_at = $1, updated_at = $2`, at.UTC().Unix(), at.UTC().Unix())
	if err != nil {
		return fmt.Errorf("reset all budgets: %w", err)
	}
	return nil
}

func (s *PostgreSQLStore) SumUsageCost(ctx context.Context, userPath string, start, end time.Time) (float64, bool, error) {
	userPath, err := NormalizeUserPath(userPath)
	if err != nil {
		return 0, false, err
	}
	userPathExpr := usagePathMatchesBudgetExpr("user_path")
	query := `SELECT SUM(total_cost) FROM "usage"
		WHERE timestamp >= $1
			AND timestamp < $2
			AND (` + userPathExpr + ` = $3 OR ` + userPathExpr + ` LIKE $4 ESCAPE '\')
			AND (cache_type IS NULL OR cache_type = '')`
	var total *float64
	if err := s.pool.QueryRow(ctx, query, start.UTC(), end.UTC(), userPath, usagePathLikePattern(userPath)).Scan(&total); err != nil {
		return 0, false, fmt.Errorf("sum usage cost: %w", err)
	}
	if total == nil {
		return 0, false, nil
	}
	return *total, true, nil
}

func (s *PostgreSQLStore) Close() error {
	return nil
}

func upsertPostgreSQLBudgets(ctx context.Context, tx pgx.Tx, budgets []Budget) error {
	for _, budget := range budgets {
		_, err := tx.Exec(ctx, `
			INSERT INTO budgets (user_path, period_seconds, amount, source, last_reset_at, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (user_path, period_seconds) DO UPDATE SET
				amount = excluded.amount,
				source = excluded.source,
				updated_at = excluded.updated_at
		`,
			budget.UserPath,
			budget.PeriodSeconds,
			budget.Amount,
			budget.Source,
			unixOrNil(budget.LastResetAt),
			budget.CreatedAt.Unix(),
			budget.UpdatedAt.Unix(),
		)
		if err != nil {
			return fmt.Errorf("upsert budget %s/%d: %w", budget.UserPath, budget.PeriodSeconds, err)
		}
	}
	return nil
}

func scanPostgreSQLBudget(row pgx.Row) (Budget, error) {
	var budget Budget
	var lastResetAt *int64
	var createdAt int64
	var updatedAt int64
	if err := row.Scan(
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
	if lastResetAt != nil {
		t := time.Unix(*lastResetAt, 0).UTC()
		budget.LastResetAt = &t
	}
	budget.CreatedAt = time.Unix(createdAt, 0).UTC()
	budget.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return budget, nil
}
