package auditlog

import (
	"context"
	"fmt"
	"time"

	"github.com/enterpilot/gomodel/internal/storage/sqlutil"
)

// GetRequestStats returns time-bucketed status-class counts and per-provider
// latency aggregates for the dashboard charts.
func (r *SQLReader) GetRequestStats(ctx context.Context, params RequestStatsParams) (*RequestStats, error) {
	var conditions []string
	var args []any
	if !params.StartDate.IsZero() {
		conditions = append(conditions, "timestamp >= ?")
		args = append(args, r.dialect.timestampBound(params.StartDate))
	}
	if !params.EndDate.IsZero() {
		conditions = append(conditions, "timestamp < ?")
		args = append(args, r.dialect.timestampBound(params.EndDate.AddDate(0, 0, 1)))
	}

	// Group by UTC hour and provider; foldRequestStats folds hours into the
	// requested bucket granularity.
	//
	// Latency covers successful requests with a recorded duration that
	// actually reached a provider: local response-cache hits complete in
	// microseconds and would drag averages toward zero.
	const latencyPredicate = `status_code BETWEEN 200 AND 299 AND duration_ns > 0
		AND (cache_type IS NULL OR cache_type = '')`

	query := `SELECT
		` + r.dialect.statsHour + ` AS hour,
		COALESCE(NULLIF(TRIM(provider_name), ''), TRIM(provider), '') AS prov,
		COUNT(*),
		SUM(CASE WHEN status_code BETWEEN 200 AND 299 THEN 1 ELSE 0 END),
		SUM(CASE WHEN status_code BETWEEN 400 AND 499 THEN 1 ELSE 0 END),
		SUM(CASE WHEN status_code >= 500 THEN 1 ELSE 0 END),
		COALESCE(SUM(CASE WHEN ` + latencyPredicate + ` THEN duration_ns ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN ` + latencyPredicate + ` THEN 1 ELSE 0 END), 0)
		FROM audit_logs` + sqlutil.BuildWhereClause(conditions) + `
		GROUP BY hour, prov`

	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query audit request stats: %w", err)
	}
	defer rows.Close()

	stats := make([]statsRow, 0)
	for rows.Next() {
		var row statsRow
		var hour statsHour
		if err := rows.Scan(&hour, &row.Provider, &row.Requests, &row.Status2xx,
			&row.Status4xx, &row.Status5xx, &row.DurationNsSum, &row.DurationCount); err != nil {
			return nil, fmt.Errorf("failed to scan audit request stats row: %w", err)
		}
		if !hour.valid {
			return nil, fmt.Errorf("failed to parse audit request stats hour %q", hour.raw)
		}
		row.HourUTC = hour.Time
		stats = append(stats, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating audit request stats rows: %w", err)
	}

	return foldRequestStats(stats, params), nil
}

// statsHour scans the hour a stats row groups under. SQLite formats it as text
// with strftime; PostgreSQL's date_trunc over "AT TIME ZONE 'UTC'" yields a
// timestamp *without* time zone holding UTC wall-clock values, so the location
// is pinned rather than trusted from the driver.
type statsHour struct {
	time.Time
	valid bool
	raw   string
}

func (h *statsHour) Scan(src any) error {
	switch value := src.(type) {
	case time.Time:
		h.Time = time.Date(value.Year(), value.Month(), value.Day(), value.Hour(), 0, 0, 0, time.UTC)
		h.valid = true
		return nil
	case string:
		return h.parse(value)
	case []byte:
		return h.parse(string(value))
	default:
		return fmt.Errorf("cannot scan %T into a stats hour", src)
	}
}

func (h *statsHour) parse(raw string) error {
	h.raw = raw
	parsed, err := time.ParseInLocation(statsHourLayout, raw, time.UTC)
	if err != nil {
		return nil // reported by the caller, which has the row context
	}
	h.Time, h.valid = parsed, true
	return nil
}
