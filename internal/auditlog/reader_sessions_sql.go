package auditlog

import (
	"context"
	"fmt"

	"github.com/enterpilot/gomodel/internal/storage/sqlutil"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// auditThreadKey groups entries into threads: the session id when present,
// otherwise the entry's own id, which makes sessionless entries singleton
// threads in the same page. The cast matters: PostgreSQL databases created
// before the unified schema have a uuid id column, and COALESCE cannot match
// uuid against the text session_id without it.
const auditThreadKey = `COALESCE(NULLIF(session_id, ''), CAST(id AS TEXT))`

// GetSessions returns a paginated list of audit sessions ordered by latest
// activity. One window-function pass ranks each thread's entries by recency
// and aggregates its count and time span; the outer query keeps each thread's
// newest entry. Works identically on SQLite and PostgreSQL.
func (r *SQLReader) GetSessions(ctx context.Context, params LogQueryParams) (*SessionListResult, error) {
	limit, offset := clampLimitOffset(params.Limit, params.Offset)

	conditions, args, err := r.logFilters(params)
	if err != nil {
		return nil, err
	}
	where := sqlutil.BuildWhereClause(conditions)

	var total int
	if err := r.db.QueryRow(ctx,
		"SELECT COUNT(DISTINCT "+auditThreadKey+") FROM audit_logs"+where, args...,
	).Scan(&total); err != nil {
		return nil, fmt.Errorf("failed to count audit sessions: %w", err)
	}

	query := `WITH ranked AS (
		SELECT ` + logColumns + `,
			ROW_NUMBER() OVER (PARTITION BY ` + auditThreadKey + ` ORDER BY timestamp DESC, id DESC) AS rn,
			COUNT(*) OVER (PARTITION BY ` + auditThreadKey + `) AS entry_count,
			MIN(timestamp) OVER (PARTITION BY ` + auditThreadKey + `) AS first_ts,
			MAX(timestamp) OVER (PARTITION BY ` + auditThreadKey + `) AS last_ts
		FROM audit_logs` + where + `
	)
	SELECT ` + logColumns + `, entry_count, first_ts, last_ts
	FROM ranked WHERE rn = 1
	ORDER BY last_ts DESC, id DESC LIMIT ? OFFSET ?`

	rows, err := r.db.Query(ctx, query, append(append([]any(nil), args...), limit, offset)...)
	if err != nil {
		return nil, fmt.Errorf("failed to query audit sessions: %w", err)
	}
	defer rows.Close()

	sessions := make([]SessionSummary, 0)
	for rows.Next() {
		summary, err := scanSQLSessionSummary(rows)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, *summary)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating audit session rows: %w", err)
	}
	return &SessionListResult{Sessions: sessions, Total: total, Limit: limit, Offset: offset}, nil
}

// sessionSummaryScanner adapts a session row to the log-entry scanner: the
// leading columns are exactly a log entry, followed by the three aggregates.
type sessionSummaryScanner struct {
	row     sqlx.Row
	count   *int
	firstTS *sqlx.Timestamp
	lastTS  *sqlx.Timestamp
}

func (s sessionSummaryScanner) Scan(dest ...any) error {
	return s.row.Scan(append(dest, s.count, s.firstTS, s.lastTS)...)
}

func scanSQLSessionSummary(row sqlx.Row) (*SessionSummary, error) {
	var summary SessionSummary
	var firstTS, lastTS sqlx.Timestamp
	entry, err := scanSQLLogEntry(sessionSummaryScanner{
		row:     row,
		count:   &summary.Count,
		firstTS: &firstTS,
		lastTS:  &lastTS,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to scan audit session row: %w", err)
	}
	summary.SessionID = entry.SessionID
	summary.FirstTimestamp = firstTS.Time
	summary.LastTimestamp = lastTS.Time
	summary.Latest = *entry
	return &summary, nil
}
