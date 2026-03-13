package usage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
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
	where := buildWhereClause(conditions)

	costCols := `, COALESCE(SUM(input_cost),0), COALESCE(SUM(output_cost),0), COALESCE(SUM(total_cost),0)`
	query := `SELECT COUNT(*), COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0), COALESCE(SUM(total_tokens), 0)` + costCols + `
			FROM usage` + where

	summary := &UsageSummary{}
	err := r.db.QueryRowContext(ctx, query, args...).Scan(
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
	where := buildWhereClause(conditions)

	costCols := `, COALESCE(SUM(input_cost),0), COALESCE(SUM(output_cost),0), COALESCE(SUM(total_cost),0)`
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

	if params.Model != "" {
		conditions = append(conditions, "model = ?")
		args = append(args, params.Model)
	}
	if params.Provider != "" {
		conditions = append(conditions, "provider = ?")
		args = append(args, params.Provider)
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
	dataQuery := `SELECT id, request_id, provider_id, timestamp, model, provider, endpoint,
		input_tokens, output_tokens, total_tokens, COALESCE(input_cost, 0), COALESCE(output_cost, 0), COALESCE(total_cost, 0), raw_data, COALESCE(costs_calculation_caveat, '')
		FROM usage` + where + ` ORDER BY timestamp DESC LIMIT ? OFFSET ?`
	dataArgs := append(append([]interface{}(nil), args...), limit, offset)

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
		if err := rows.Scan(&e.ID, &e.RequestID, &e.ProviderID, &ts, &e.Model, &e.Provider, &e.Endpoint,
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

// sqliteDateRangeConditions returns WHERE conditions and args for a date range.
// Dates are formatted as "2006-01-02" strings for SQLite text comparison.
func sqliteDateRangeConditions(params UsageQueryParams) (conditions []string, args []interface{}) {
	if !params.StartDate.IsZero() {
		conditions = append(conditions, "timestamp >= ?")
		args = append(args, params.StartDate.UTC().Format("2006-01-02"))
	}
	if !params.EndDate.IsZero() {
		conditions = append(conditions, "timestamp < ?")
		args = append(args, params.EndDate.AddDate(0, 0, 1).UTC().Format("2006-01-02"))
	}
	return conditions, args
}

func sqliteGroupExpr(interval string) string {
	switch interval {
	case "weekly":
		return `strftime('%G-W%V', timestamp)`
	case "monthly":
		return `strftime('%Y-%m', timestamp)`
	case "yearly":
		return `strftime('%Y', timestamp)`
	default:
		return `DATE(timestamp)`
	}
}

// GetDailyUsage returns usage statistics grouped by time period (daily, weekly, monthly, yearly).
func (r *SQLiteReader) GetDailyUsage(ctx context.Context, params UsageQueryParams) ([]DailyUsage, error) {
	interval := params.Interval
	if interval == "" {
		interval = "daily"
	}
	groupExpr := sqliteGroupExpr(interval)

	conditions, args := sqliteDateRangeConditions(params)
	where := buildWhereClause(conditions)

	costCols := `, COALESCE(SUM(input_cost),0), COALESCE(SUM(output_cost),0), COALESCE(SUM(total_cost),0)`
	query := fmt.Sprintf(`SELECT %s as period, COUNT(*), COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0), COALESCE(SUM(total_tokens), 0)`+costCols+`
		FROM usage%s GROUP BY %s ORDER BY period`, groupExpr, where, groupExpr)

	rows, err := r.db.QueryContext(ctx, query, args...)
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
