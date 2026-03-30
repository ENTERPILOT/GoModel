package usage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// SQLiteReader implements UsageReader for SQLite databases.
type SQLiteReader struct {
	db *sql.DB
}

// NewSQLiteReader creates a new SQLite usage reader.
func NewSQLiteReader(db *sql.DB) (*SQLiteReader, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection is required")
	}
	return &SQLiteReader{db: db}, nil
}

// GetSummary returns aggregated usage statistics for the given query parameters.
func (r *SQLiteReader) GetSummary(ctx context.Context, params UsageQueryParams) (*UsageSummary, error) {
	conditions, args := sqliteDateRangeConditions(params)
	userPath, err := normalizeUsageUserPathFilter(params.UserPath)
	if err != nil {
		return nil, err
	}
	if userPath != "" {
		conditions = append(conditions, "(user_path = ? OR user_path LIKE ? ESCAPE '\\')")
		args = append(args, userPath, usageUserPathSubtreePattern(userPath))
	}
	where := buildWhereClause(conditions)

	costCols := `, SUM(input_cost), SUM(output_cost), SUM(total_cost)`
	query := `SELECT COUNT(*), COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0), COALESCE(SUM(total_tokens), 0)` + costCols + `
			FROM usage` + where

	summary := &UsageSummary{}
	err = r.db.QueryRowContext(ctx, query, args...).Scan(
		&summary.TotalRequests, &summary.TotalInput, &summary.TotalOutput, &summary.TotalTokens,
		&summary.TotalInputCost, &summary.TotalOutputCost, &summary.TotalCost,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query usage summary: %w", err)
	}

	return summary, nil
}

// GetUsageByModel returns token and cost totals grouped by model and provider.
func (r *SQLiteReader) GetUsageByModel(ctx context.Context, params UsageQueryParams) ([]ModelUsage, error) {
	conditions, args := sqliteDateRangeConditions(params)
	userPath, err := normalizeUsageUserPathFilter(params.UserPath)
	if err != nil {
		return nil, err
	}
	if userPath != "" {
		conditions = append(conditions, "(user_path = ? OR user_path LIKE ? ESCAPE '\\')")
		args = append(args, userPath, usageUserPathSubtreePattern(userPath))
	}
	where := buildWhereClause(conditions)

	costCols := `, SUM(input_cost), SUM(output_cost), SUM(total_cost)`
	query := `SELECT model, provider, COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0)` + costCols + `
			FROM usage` + where + ` GROUP BY model, provider`

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query usage by model: %w", err)
	}
	defer rows.Close()

	result := make([]ModelUsage, 0)
	for rows.Next() {
		var m ModelUsage
		if err := rows.Scan(&m.Model, &m.Provider, &m.InputTokens, &m.OutputTokens, &m.InputCost, &m.OutputCost, &m.TotalCost); err != nil {
			return nil, fmt.Errorf("failed to scan usage by model row: %w", err)
		}
		result = append(result, m)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating usage by model rows: %w", err)
	}

	return result, nil
}

// GetUsageLog returns a paginated list of individual usage log entries.
func (r *SQLiteReader) GetUsageLog(ctx context.Context, params UsageLogParams) (*UsageLogResult, error) {
	limit, offset := clampLimitOffset(params.Limit, params.Offset)

	conditions, args := sqliteDateRangeConditions(params.UsageQueryParams)
	userPath, err := normalizeUsageUserPathFilter(params.UserPath)
	if err != nil {
		return nil, err
	}

	if params.Model != "" {
		conditions = append(conditions, "model = ?")
		args = append(args, params.Model)
	}
	if params.Provider != "" {
		conditions = append(conditions, "provider = ?")
		args = append(args, params.Provider)
	}
	if userPath != "" {
		conditions = append(conditions, "(user_path = ? OR user_path LIKE ? ESCAPE '\\')")
		args = append(args, userPath, usageUserPathSubtreePattern(userPath))
	}
	if params.Search != "" {
		conditions = append(conditions, "(model LIKE ? ESCAPE '\\' OR provider LIKE ? ESCAPE '\\' OR request_id LIKE ? ESCAPE '\\' OR provider_id LIKE ? ESCAPE '\\')")
		s := "%" + escapeLikeWildcards(params.Search) + "%"
		args = append(args, s, s, s, s)
	}

	where := buildWhereClause(conditions)

	// Count total
	var total int
	countQuery := "SELECT COUNT(*) FROM usage" + where
	if err := r.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("failed to count usage log entries: %w", err)
	}

	// Fetch page
	dataQuery := `SELECT id, request_id, provider_id, timestamp, model, provider, endpoint, user_path,
		input_tokens, output_tokens, total_tokens, COALESCE(input_cost, 0), COALESCE(output_cost, 0), COALESCE(total_cost, 0), raw_data, COALESCE(costs_calculation_caveat, '')
		FROM usage` + where + ` ORDER BY ` + sqliteTimestampEpochExpr() + ` DESC, id DESC LIMIT ? OFFSET ?`
	dataArgs := append(append([]any(nil), args...), limit, offset)

	rows, err := r.db.QueryContext(ctx, dataQuery, dataArgs...)
	if err != nil {
		return nil, fmt.Errorf("failed to query usage log: %w", err)
	}
	defer rows.Close()

	entries := make([]UsageLogEntry, 0)
	for rows.Next() {
		var e UsageLogEntry
		var ts string
		var caveat *string
		var rawDataJSON *string
		var userPath sql.NullString
		if err := rows.Scan(&e.ID, &e.RequestID, &e.ProviderID, &ts, &e.Model, &e.Provider, &e.Endpoint, &userPath,
			&e.InputTokens, &e.OutputTokens, &e.TotalTokens, &e.InputCost, &e.OutputCost, &e.TotalCost, &rawDataJSON, &caveat); err != nil {
			return nil, fmt.Errorf("failed to scan usage log row: %w", err)
		}
		if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
			e.Timestamp = t
		} else if t, err := time.Parse("2006-01-02 15:04:05.999999999-07:00", ts); err == nil {
			e.Timestamp = t
		} else if t, err := time.Parse("2006-01-02T15:04:05Z", ts); err == nil {
			e.Timestamp = t
		} else {
			slog.Warn("failed to parse timestamp", "request_id", e.RequestID, "raw_timestamp", ts)
		}
		if rawDataJSON != nil && *rawDataJSON != "" {
			if err := json.Unmarshal([]byte(*rawDataJSON), &e.RawData); err != nil {
				slog.Warn("failed to unmarshal raw_data JSON", "request_id", e.RequestID, "error", err)
			}
		}
		if userPath.Valid {
			e.UserPath = userPath.String
		}
		if caveat != nil {
			e.CostsCalculationCaveat = *caveat
		}
		entries = append(entries, e)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating usage log rows: %w", err)
	}

	return &UsageLogResult{
		Entries: entries,
		Total:   total,
		Limit:   limit,
		Offset:  offset,
	}, nil
}

func sqliteTimestampTextExpr() string {
	return "REPLACE(timestamp, ' ', 'T')"
}

func sqliteTimestampEpochExpr() string {
	return "unixepoch(" + sqliteTimestampTextExpr() + ")"
}

// sqliteDateRangeConditions returns WHERE conditions and args for a date range.
// Stored timestamps may be RFC3339 UTC text or legacy space-separated offset text,
// so we normalize them and compare using epoch seconds to preserve absolute ordering.
func sqliteDateRangeConditions(params UsageQueryParams) (conditions []string, args []any) {
	if !params.StartDate.IsZero() {
		conditions = append(conditions, sqliteTimestampEpochExpr()+" >= ?")
		args = append(args, params.StartDate.UTC().Unix())
	}
	if !params.EndDate.IsZero() {
		conditions = append(conditions, sqliteTimestampEpochExpr()+" < ?")
		args = append(args, usageEndExclusive(params).UTC().Unix())
	}
	return conditions, args
}

func sqliteGroupExpr(interval string) string {
	return sqliteGroupExprWithOffset(interval, 0)
}

func sqliteGroupExprWithOffset(interval string, offsetMinutes int) string {
	modifier := sqliteOffsetModifier(offsetMinutes)
	timestampExpr := sqliteTimestampTextExpr()

	switch interval {
	case "weekly":
		if modifier == "" {
			return fmt.Sprintf(`strftime('%%G-W%%V', %s)`, timestampExpr)
		}
		return fmt.Sprintf(`strftime('%%G-W%%V', %s, '%s')`, timestampExpr, modifier)
	case "monthly":
		if modifier == "" {
			return fmt.Sprintf(`strftime('%%Y-%%m', %s)`, timestampExpr)
		}
		return fmt.Sprintf(`strftime('%%Y-%%m', %s, '%s')`, timestampExpr, modifier)
	case "yearly":
		if modifier == "" {
			return fmt.Sprintf(`strftime('%%Y', %s)`, timestampExpr)
		}
		return fmt.Sprintf(`strftime('%%Y', %s, '%s')`, timestampExpr, modifier)
	default:
		if modifier == "" {
			return fmt.Sprintf(`DATE(%s)`, timestampExpr)
		}
		return fmt.Sprintf(`DATE(%s, '%s')`, timestampExpr, modifier)
	}
}

// GetDailyUsage returns usage statistics grouped by time period (daily, weekly, monthly, yearly).
func (r *SQLiteReader) GetDailyUsage(ctx context.Context, params UsageQueryParams) ([]DailyUsage, error) {
	groupExpr, groupArgs, err := r.sqliteGroupExpr(ctx, params)
	if err != nil {
		return nil, err
	}

	conditions, args := sqliteDateRangeConditions(params)
	userPath, err := normalizeUsageUserPathFilter(params.UserPath)
	if err != nil {
		return nil, err
	}
	if userPath != "" {
		conditions = append(conditions, "(user_path = ? OR user_path LIKE ? ESCAPE '\\')")
		args = append(args, userPath, usageUserPathSubtreePattern(userPath))
	}
	where := buildWhereClause(conditions)

	costCols := `, SUM(input_cost), SUM(output_cost), SUM(total_cost)`
	query := `WITH usage_periods AS (
		SELECT ` + groupExpr + ` AS period,
			input_tokens, output_tokens, total_tokens, input_cost, output_cost, total_cost
		FROM usage` + where + `
	)
	SELECT period, COUNT(*), COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0), COALESCE(SUM(total_tokens), 0)` + costCols + `
		FROM usage_periods GROUP BY period ORDER BY period`

	queryArgs := append(groupArgs, args...)

	rows, err := r.db.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return nil, fmt.Errorf("failed to query daily usage: %w", err)
	}
	defer rows.Close()

	result := make([]DailyUsage, 0)
	for rows.Next() {
		var d DailyUsage
		if err := rows.Scan(&d.Date, &d.Requests, &d.InputTokens, &d.OutputTokens, &d.TotalTokens, &d.InputCost, &d.OutputCost, &d.TotalCost); err != nil {
			return nil, fmt.Errorf("failed to scan daily usage row: %w", err)
		}
		result = append(result, d)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating daily usage rows: %w", err)
	}

	return result, nil
}

func sqliteOffsetModifier(offsetMinutes int) string {
	if offsetMinutes == 0 {
		return ""
	}
	return fmt.Sprintf("%+d minutes", offsetMinutes)
}

type sqliteTimeZoneSegment struct {
	Until         time.Time
	OffsetMinutes int
}

func (r *SQLiteReader) sqliteGroupExpr(ctx context.Context, params UsageQueryParams) (string, []any, error) {
	interval := params.Interval
	if interval == "" {
		interval = "daily"
	}

	location := usageLocation(params)
	if location == time.UTC {
		return sqliteGroupExpr(interval), nil, nil
	}

	rangeStart, rangeEnd, ok, err := r.sqliteGroupingRange(ctx, params)
	if err != nil {
		return "", nil, err
	}
	if !ok {
		return sqliteGroupExpr(interval), nil, nil
	}

	segments := sqliteTimeZoneSegments(rangeStart, rangeEnd, location)
	if len(segments) == 0 {
		return sqliteGroupExpr(interval), nil, nil
	}
	if len(segments) == 1 {
		return sqliteGroupExprWithOffset(interval, segments[0].OffsetMinutes), nil, nil
	}

	var builder strings.Builder
	args := make([]any, 0, len(segments)-1)
	builder.WriteString("CASE")
	for _, segment := range segments {
		expr := sqliteGroupExprWithOffset(interval, segment.OffsetMinutes)
		if segment.Until.IsZero() {
			builder.WriteString(" ELSE ")
			builder.WriteString(expr)
			continue
		}

		builder.WriteString(" WHEN ")
		builder.WriteString(sqliteTimestampEpochExpr())
		builder.WriteString(" < ? THEN ")
		builder.WriteString(expr)
		args = append(args, segment.Until.UTC().Unix())
	}
	builder.WriteString(" END")

	return builder.String(), args, nil
}

func (r *SQLiteReader) sqliteGroupingRange(ctx context.Context, params UsageQueryParams) (time.Time, time.Time, bool, error) {
	if !params.StartDate.IsZero() && !params.EndDate.IsZero() {
		return params.StartDate.UTC(), usageEndExclusive(params).UTC(), true, nil
	}

	var minTS, maxTS sql.NullInt64
	conditions, args := sqliteDateRangeConditions(params)
	userPath, err := normalizeUsageUserPathFilter(params.UserPath)
	if err != nil {
		return time.Time{}, time.Time{}, false, err
	}
	if userPath != "" {
		conditions = append(conditions, "(user_path = ? OR user_path LIKE ? ESCAPE '\\')")
		args = append(args, userPath, usageUserPathSubtreePattern(userPath))
	}
	query := `SELECT MIN(` + sqliteTimestampEpochExpr() + `), MAX(` + sqliteTimestampEpochExpr() + `) FROM usage` + buildWhereClause(conditions)
	if err := r.db.QueryRowContext(ctx, query, args...).Scan(&minTS, &maxTS); err != nil {
		return time.Time{}, time.Time{}, false, fmt.Errorf("failed to determine sqlite usage range: %w", err)
	}
	if !minTS.Valid || !maxTS.Valid {
		return time.Time{}, time.Time{}, false, nil
	}

	start := time.Unix(minTS.Int64, 0).UTC()
	end := time.Unix(maxTS.Int64, 0).UTC()
	return start, end.Add(time.Second), true, nil
}

func sqliteTimeZoneSegments(startUTC time.Time, endUTC time.Time, location *time.Location) []sqliteTimeZoneSegment {
	if location == nil || !endUTC.After(startUTC) {
		return nil
	}

	segments := make([]sqliteTimeZoneSegment, 0, 4)
	current := startUTC.UTC()
	currentOffset := sqliteOffsetMinutes(current, location)

	for current.Before(endUTC) {
		transition, ok := sqliteNextOffsetTransition(current, endUTC, location, currentOffset)
		if !ok {
			segments = append(segments, sqliteTimeZoneSegment{OffsetMinutes: currentOffset})
			break
		}

		segments = append(segments, sqliteTimeZoneSegment{
			Until:         transition.UTC(),
			OffsetMinutes: currentOffset,
		})
		current = transition.UTC()
		currentOffset = sqliteOffsetMinutes(current, location)
	}

	return segments
}

func sqliteNextOffsetTransition(startUTC time.Time, endUTC time.Time, location *time.Location, startOffset int) (time.Time, bool) {
	for windowStart := startUTC.UTC(); windowStart.Before(endUTC); {
		windowEnd := windowStart.Add(time.Hour)
		if windowEnd.After(endUTC) {
			windowEnd = endUTC
		}

		sample := windowEnd.Add(-time.Second)
		if sample.Before(windowStart) {
			sample = windowStart
		}

		if sqliteOffsetMinutes(sample, location) != startOffset {
			for candidate := windowStart; candidate.Before(windowEnd); candidate = candidate.Add(time.Second) {
				if sqliteOffsetMinutes(candidate, location) != startOffset {
					return candidate, true
				}
			}
		}

		windowStart = windowEnd
	}

	return time.Time{}, false
}

func sqliteOffsetMinutes(ts time.Time, location *time.Location) int {
	_, offsetSeconds := ts.In(location).Zone()
	return offsetSeconds / 60
}
