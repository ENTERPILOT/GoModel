package budget

import (
	"context"
	"fmt"
	"time"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// sqlBudgetAlertsSchema holds one row per alert that fired. Its primary key
// is what makes an alert fire once across every instance on the database: the
// insert that creates the row wins.
var sqlBudgetAlertsSchema = `CREATE TABLE IF NOT EXISTS budget_alerts (
		scope TEXT NOT NULL,
		subject TEXT NOT NULL,
		effective_subject TEXT NOT NULL,
		period_seconds ` + sqlx.TypeInt64 + ` NOT NULL,
		period_start ` + sqlx.TypeInt64 + ` NOT NULL,
		threshold_percent ` + sqlx.TypeInt64 + ` NOT NULL,
		period_end ` + sqlx.TypeInt64 + ` NOT NULL,
		fired_at ` + sqlx.TypeInt64 + ` NOT NULL,
		PRIMARY KEY (scope, subject, effective_subject, period_seconds, period_start, threshold_percent)
	)`

func (s *SQLStore) ClaimAlert(ctx context.Context, claim AlertClaim) (bool, error) {
	affected, err := s.db.Exec(ctx, `
		INSERT INTO budget_alerts (scope, subject, effective_subject, period_seconds, period_start, threshold_percent, period_end, fired_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT DO NOTHING
	`, claim.Scope, claim.Subject, claim.EffectiveSubject, claim.PeriodSeconds,
		claim.PeriodStart.Unix(), claim.Threshold, claim.PeriodEnd.Unix(), time.Now().UTC().Unix())
	if err != nil {
		return false, fmt.Errorf("claim budget alert: %w", err)
	}
	return affected > 0, nil
}

func (s *SQLStore) PruneAlerts(ctx context.Context, cutoff time.Time) error {
	if _, err := s.db.Exec(ctx, `DELETE FROM budget_alerts WHERE period_end < ?`, cutoff.Unix()); err != nil {
		return fmt.Errorf("prune budget alerts: %w", err)
	}
	return nil
}

// SumSpendByUserPath groups the usage of one user-path window by recorded
// user path in a single scan, instead of one spend query per child.
func (s *SQLStore) SumSpendByUserPath(ctx context.Context, window SpendWindow, limit int) ([]PathSpend, error) {
	if window.Scope != ScopeUserPath {
		return nil, fmt.Errorf("spend by user path needs a user_path window, got %q", window.Scope)
	}
	// See sumSpendChunk for why the time expression differs per dialect.
	sqlite := s.db.Dialect() == sqlx.SQLite
	timeExpr, timeBound, table := "timestamp", "?", `"usage"`
	timeArg := func(t time.Time) any { return t.UTC() }
	if sqlite {
		timeExpr, timeBound, table = "unixepoch(REPLACE(timestamp, ' ', 'T'))", "unixepoch(?)", "usage"
		timeArg = func(t time.Time) any { return t.UTC().Format(time.RFC3339Nano) }
	}
	match, matchArgs, err := spendSubjectMatch(window, sqlite)
	if err != nil {
		return nil, err
	}
	cost := spendCostExpr(window)
	query := `SELECT ` + usagePathMatchesBudgetExpr("user_path") + `, SUM(` + cost + `) FROM ` + table +
		` WHERE ` + timeExpr + ` >= ` + timeBound + ` AND ` + timeExpr + ` < ` + timeBound + ` AND ` + match +
		` GROUP BY 1 HAVING SUM(` + cost + `) IS NOT NULL ORDER BY 2 DESC, 1 ASC LIMIT ?`
	args := append([]any{timeArg(window.Start), timeArg(window.End)}, matchArgs...)
	args = append(args, limit)

	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("sum usage cost by user path: %w", err)
	}
	defer rows.Close()

	var spends []PathSpend
	for rows.Next() {
		var spend PathSpend
		if err := rows.Scan(&spend.UserPath, &spend.Total); err != nil {
			return nil, fmt.Errorf("scan usage cost by user path: %w", err)
		}
		spends = append(spends, spend)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate usage cost by user path: %w", err)
	}
	return spends, nil
}
