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
// activity. Works identically on SQLite and PostgreSQL.
//
// The window pass ranks and aggregates threads over ID AND TIMESTAMP ONLY —
// carrying the full entry (its `data` blob above all) through the partition
// sort meant every audit body in the window got materialized and sorted just
// to discard all but one row per thread. The outer join re-reads the complete
// rows for the page's 25 winners, by primary key.
//
// All four window functions share one named window so the partition is sorted
// once rather than per aggregate. The explicit full frame is what lets the
// aggregates share the ranking window's ORDER BY: without it they would be
// running totals over the frame ending at the current row. ROW_NUMBER ignores
// the frame, as specified. And since the partition is ordered by timestamp
// DESC, the ranked row IS its thread's newest entry, so its own timestamp is
// the thread's last_ts and no MAX() window is needed.
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

	// The correlated count deliberately applies no active list filters: the
	// dashboard expands a head with an unfiltered session_id query, so both
	// operations describe the same complete session.
	query := `WITH ranked AS (
		SELECT id, timestamp AS last_ts,
			ROW_NUMBER() OVER thread AS rn,
			COUNT(*) OVER thread AS entry_count,
			MIN(timestamp) OVER thread AS first_ts
		FROM audit_logs` + where + `
		WINDOW thread AS (
			PARTITION BY ` + auditThreadKey + `
			ORDER BY timestamp DESC, id DESC
			ROWS BETWEEN UNBOUNDED PRECEDING AND UNBOUNDED FOLLOWING
		)
	), heads AS (
		SELECT id, entry_count, first_ts, last_ts
		FROM ranked WHERE rn = 1
		ORDER BY last_ts DESC, id DESC LIMIT ? OFFSET ?
	)
	SELECT ` + qualifiedLogColumns("l") + `, h.entry_count,
		CASE WHEN NULLIF(l.session_id, '') IS NULL THEN 1 ELSE (
			SELECT COUNT(*) FROM audit_logs session_entries
			WHERE session_entries.session_id = l.session_id
		) END AS total_count,
		h.first_ts, h.last_ts
	FROM heads h JOIN audit_logs l ON l.id = h.id
	ORDER BY h.last_ts DESC, h.id DESC`

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
	total   *int
	firstTS *sqlx.Timestamp
	lastTS  *sqlx.Timestamp
}

func (s sessionSummaryScanner) Scan(dest ...any) error {
	return s.row.Scan(append(dest, s.count, s.total, s.firstTS, s.lastTS)...)
}

func scanSQLSessionSummary(row sqlx.Row) (*SessionSummary, error) {
	var summary SessionSummary
	var firstTS, lastTS sqlx.Timestamp
	entry, err := scanSQLLogEntry(sessionSummaryScanner{
		row:     row,
		count:   &summary.Count,
		total:   &summary.TotalCount,
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
