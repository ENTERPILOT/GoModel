package budget

import (
	"context"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// sqlBudgetAlertsSchema holds one row per alert that fired. Its primary key
// is what makes an alert fire once across every instance on the database: the
// insert that creates the row wins.
var sqlBudgetAlertsSchema = `CREATE TABLE IF NOT EXISTS budget_alerts (
		scope TEXT NOT NULL,
		subject TEXT NOT NULL,
		effective_subject TEXT NOT NULL,
		budget_revision TEXT NOT NULL,
		period_seconds ` + sqlx.TypeInt64 + ` NOT NULL,
		period_start ` + sqlx.TypeInt64 + ` NOT NULL,
		threshold_percent ` + sqlx.TypeInt64 + ` NOT NULL,
		period_end ` + sqlx.TypeInt64 + ` NOT NULL,
		fired_at ` + sqlx.TypeInt64 + ` NOT NULL,
		PRIMARY KEY (scope, subject, effective_subject, budget_revision, period_seconds, period_start, threshold_percent)
	)`

func (s *SQLStore) ClaimAlert(ctx context.Context, claim AlertClaim) (bool, error) {
	affected, err := s.db.Exec(ctx, `
		INSERT INTO budget_alerts (scope, subject, effective_subject, budget_revision, period_seconds, period_start, threshold_percent, period_end, fired_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT DO NOTHING
	`, claim.Scope, claim.Subject, claim.EffectiveSubject, claim.Revision, claim.PeriodSeconds,
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

// SumSpendByChild groups the usage of one user-path window by the direct
// child of its subject in a single scan, instead of one spend query per
// child. The grouping happens in the database, so the limit counts children,
// not the paths below them.
func (s *SQLStore) SumSpendByChild(ctx context.Context, window SpendWindow, limit int) ([]ChildSpend, error) {
	if window.Scope != ScopeUserPath {
		return nil, fmt.Errorf("spend by child needs a user_path window, got %q", window.Scope)
	}
	subject, err := NormalizeUserPath(window.Subject)
	if err != nil {
		return nil, err
	}
	// See sumSpendChunk for why the time expression differs per dialect.
	sqlite := s.db.Dialect() == sqlx.SQLite
	timeExpr, timeBound, table := "timestamp", "?", `"usage"`
	timeArg := func(t time.Time) any { return t.UTC() }
	// segment is the first segment of rest, the path below the subject.
	segment := "split_part(rest, '/', 1)"
	if sqlite {
		timeExpr, timeBound, table = "unixepoch(REPLACE(timestamp, ' ', 'T'))", "unixepoch(?)", "usage"
		timeArg = func(t time.Time) any { return t.UTC().Format(time.RFC3339Nano) }
		segment = "CASE WHEN instr(rest, '/') > 0 THEN substr(rest, 1, instr(rest, '/') - 1) ELSE rest END"
	}
	match, matchArgs, err := spendSubjectMatch(window, sqlite)
	if err != nil {
		return nil, err
	}
	prefix := childPrefix(subject)
	path := usagePathMatchesBudgetExpr("user_path")
	query := `SELECT child, SUM(cost) FROM (` +
		`SELECT ` + segment + ` AS child, cost FROM (` +
		`SELECT substr(` + path + `, ?) AS rest, ` + spendCostExpr(window) + ` AS cost FROM ` + table +
		` WHERE ` + timeExpr + ` >= ` + timeBound + ` AND ` + timeExpr + ` < ` + timeBound + ` AND ` + match +
		` AND ` + path + ` <> ?` +
		`) paths) children GROUP BY child HAVING SUM(cost) IS NOT NULL ORDER BY 2 DESC, 1 ASC LIMIT ?`
	args := []any{utf8.RuneCountInString(prefix) + 1, timeArg(window.Start), timeArg(window.End)}
	args = append(args, matchArgs...)
	args = append(args, subject, limit)

	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("sum usage cost by child: %w", err)
	}
	defer rows.Close()

	var spends []ChildSpend
	for rows.Next() {
		var segment string
		var spend ChildSpend
		if err := rows.Scan(&segment, &spend.Total); err != nil {
			return nil, fmt.Errorf("scan usage cost by child: %w", err)
		}
		spend.Child = prefix + segment
		spends = append(spends, spend)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate usage cost by child: %w", err)
	}
	return spends, nil
}

// childPrefix is what every child path of subject starts with.
func childPrefix(subject string) string {
	if subject == "/" {
		return "/"
	}
	return subject + "/"
}
