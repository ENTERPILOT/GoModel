package auditlog

import (
	"context"
	"fmt"
	"time"

	"github.com/enterpilot/gomodel/internal/storage/sqlutil"
)

// GetRequestStats returns time-bucketed status-class counts and per-provider
// latency aggregates for the dashboard charts.
func (r *SQLiteReader) GetRequestStats(ctx context.Context, params RequestStatsParams) (*RequestStats, error) {
	conditions, args := sqliteDateRangeConditions(params.QueryParams)
	where := sqlutil.BuildWhereClause(conditions)

	// Group by UTC hour and provider; foldRequestStats folds hours into the
	// requested bucket granularity. strftime normalizes stored timestamp
	// variants (space separator, fractional seconds, offsets) to UTC.
	query := `SELECT
		strftime('%Y-%m-%dT%H', REPLACE(timestamp, ' ', 'T')) AS hour,
		COALESCE(NULLIF(TRIM(provider_name), ''), TRIM(provider), '') AS prov,
		COUNT(*),
		SUM(CASE WHEN status_code BETWEEN 200 AND 299 THEN 1 ELSE 0 END),
		SUM(CASE WHEN status_code BETWEEN 400 AND 499 THEN 1 ELSE 0 END),
		SUM(CASE WHEN status_code >= 500 THEN 1 ELSE 0 END),
		COALESCE(SUM(CASE WHEN ` + sqliteStatsLatencyPredicate + ` THEN duration_ns ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN ` + sqliteStatsLatencyPredicate + ` THEN 1 ELSE 0 END), 0)
		FROM audit_logs` + where + `
		GROUP BY hour, prov`

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query audit request stats: %w", err)
	}
	defer rows.Close()

	stats := make([]statsRow, 0)
	for rows.Next() {
		var row statsRow
		var hour string
		if err := rows.Scan(&hour, &row.Provider, &row.Requests, &row.Status2xx, &row.Status4xx, &row.Status5xx, &row.DurationNsSum, &row.DurationCount); err != nil {
			return nil, fmt.Errorf("failed to scan audit request stats row: %w", err)
		}
		parsed, err := time.ParseInLocation(statsHourLayout, hour, time.UTC)
		if err != nil {
			return nil, fmt.Errorf("failed to parse audit request stats hour %q: %w", hour, err)
		}
		row.HourUTC = parsed
		stats = append(stats, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating audit request stats rows: %w", err)
	}

	return foldRequestStats(stats, params), nil
}

// Latency covers successful requests with a recorded duration that actually
// reached a provider (local response-cache hits complete in microseconds and
// would drag averages toward zero).
const sqliteStatsLatencyPredicate = `status_code BETWEEN 200 AND 299 AND duration_ns > 0
		AND (cache_type IS NULL OR cache_type = '')`
