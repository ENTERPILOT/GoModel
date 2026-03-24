package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgreSQLReader implements UsageReader for PostgreSQL databases.
type PostgreSQLReader struct {
	pool *pgxpool.Pool
}

// NewPostgreSQLReader creates a new PostgreSQL usage reader.
func NewPostgreSQLReader(pool *pgxpool.Pool) (*PostgreSQLReader, error) {
	if pool == nil {
		return nil, fmt.Errorf("connection pool is required")
	}
	return &PostgreSQLReader{pool: pool}, nil
}

// GetSummary returns aggregated usage statistics for the given query parameters.
func (r *PostgreSQLReader) GetSummary(ctx context.Context, params UsageQueryParams) (*UsageSummary, error) {
	conditions, args, _ := pgDateRangeConditions(params, 1)
	where := buildWhereClause(conditions)

	costCols := `, SUM(input_cost), SUM(output_cost), SUM(total_cost)`
	query := `SELECT COUNT(*), COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0), COALESCE(SUM(total_tokens), 0)` + costCols + `
			FROM "usage"` + where

	summary := &UsageSummary{}
	err := r.pool.QueryRow(ctx, query, args...).Scan(
		&summary.TotalRequests, &summary.TotalInput, &summary.TotalOutput, &summary.TotalTokens,
		&summary.TotalInputCost, &summary.TotalOutputCost, &summary.TotalCost,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query usage summary: %w", err)
	}

	return summary, nil
}

// GetUsageByModel returns token and cost totals grouped by model and provider.
func (r *PostgreSQLReader) GetUsageByModel(ctx context.Context, params UsageQueryParams) ([]ModelUsage, error) {
	conditions, args, _ := pgDateRangeConditions(params, 1)
	where := buildWhereClause(conditions)

	costCols := `, SUM(input_cost), SUM(output_cost), SUM(total_cost)`
	query := `SELECT model, provider, COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0)` + costCols + `
			FROM "usage"` + where + ` GROUP BY model, provider`

	rows, err := r.pool.Query(ctx, query, args...)
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
func (r *PostgreSQLReader) GetUsageLog(ctx context.Context, params UsageLogParams) (*UsageLogResult, error) {
	limit, offset := clampLimitOffset(params.Limit, params.Offset)

	conditions, args, argIdx := pgDateRangeConditions(params.UsageQueryParams, 1)

	if params.Model != "" {
		conditions = append(conditions, fmt.Sprintf("model = $%d", argIdx))
		args = append(args, params.Model)
		argIdx++
	}
	if params.Provider != "" {
		conditions = append(conditions, fmt.Sprintf("provider = $%d", argIdx))
		args = append(args, params.Provider)
		argIdx++
	}
	if params.Search != "" {
		s := "%" + escapeLikeWildcards(params.Search) + "%"
		conditions = append(conditions, fmt.Sprintf("(model ILIKE $%d ESCAPE '\\' OR provider ILIKE $%d ESCAPE '\\' OR request_id ILIKE $%d ESCAPE '\\' OR provider_id ILIKE $%d ESCAPE '\\')", argIdx, argIdx, argIdx, argIdx))
		args = append(args, s)
		argIdx++
	}

	where := buildWhereClause(conditions)

	// Count total
	var total int
	countQuery := `SELECT COUNT(*) FROM "usage"` + where
	if err := r.pool.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("failed to count usage log entries: %w", err)
	}

	// Fetch page
	dataQuery := fmt.Sprintf(`SELECT id, request_id, provider_id, timestamp, model, provider, endpoint,
		input_tokens, output_tokens, total_tokens, COALESCE(input_cost, 0), COALESCE(output_cost, 0), COALESCE(total_cost, 0), raw_data, COALESCE(costs_calculation_caveat, '')
		FROM "usage"%s ORDER BY timestamp DESC LIMIT $%d OFFSET $%d`, where, argIdx, argIdx+1)
	dataArgs := append(append([]any(nil), args...), limit, offset)

	rows, err := r.pool.Query(ctx, dataQuery, dataArgs...)
	if err != nil {
		return nil, fmt.Errorf("failed to query usage log: %w", err)
	}
	defer rows.Close()

	entries := make([]UsageLogEntry, 0)
	for rows.Next() {
		var e UsageLogEntry
		var rawDataJSON *string
		if err := rows.Scan(&e.ID, &e.RequestID, &e.ProviderID, &e.Timestamp, &e.Model, &e.Provider, &e.Endpoint,
			&e.InputTokens, &e.OutputTokens, &e.TotalTokens, &e.InputCost, &e.OutputCost, &e.TotalCost, &rawDataJSON, &e.CostsCalculationCaveat); err != nil {
			return nil, fmt.Errorf("failed to scan usage log row: %w", err)
		}
		if rawDataJSON != nil && *rawDataJSON != "" {
			if err := json.Unmarshal([]byte(*rawDataJSON), &e.RawData); err != nil {
				slog.Warn("failed to unmarshal raw_data JSON", "request_id", e.RequestID, "error", err)
			}
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

// pgDateRangeConditions returns WHERE conditions and args for a date range.
// argIdx is the starting $N placeholder index; nextIdx is the next available index.
func pgDateRangeConditions(params UsageQueryParams, argIdx int) (conditions []string, args []any, nextIdx int) {
	nextIdx = argIdx
	if !params.StartDate.IsZero() {
		conditions = append(conditions, fmt.Sprintf("timestamp >= $%d", nextIdx))
		args = append(args, params.StartDate.UTC())
		nextIdx++
	}
	if !params.EndDate.IsZero() {
		conditions = append(conditions, fmt.Sprintf("timestamp < $%d", nextIdx))
		args = append(args, params.EndDate.AddDate(0, 0, 1).UTC())
		nextIdx++
	}
	return conditions, args, nextIdx
}

func pgGroupExpr(interval string) string {
	switch interval {
	case "weekly":
		return `to_char(DATE_TRUNC('week', timestamp AT TIME ZONE 'UTC'), 'IYYY-"W"IW')`
	case "monthly":
		return `to_char(DATE_TRUNC('month', timestamp AT TIME ZONE 'UTC'), 'YYYY-MM')`
	case "yearly":
		return `to_char(DATE_TRUNC('year', timestamp AT TIME ZONE 'UTC'), 'YYYY')`
	default:
		return `to_char(DATE(timestamp AT TIME ZONE 'UTC'), 'YYYY-MM-DD')`
	}
}

// GetDailyUsage returns usage statistics grouped by time period (daily, weekly, monthly, yearly).
func (r *PostgreSQLReader) GetDailyUsage(ctx context.Context, params UsageQueryParams) ([]DailyUsage, error) {
	interval := params.Interval
	if interval == "" {
		interval = "daily"
	}
	groupExpr := pgGroupExpr(interval)

	conditions, args, _ := pgDateRangeConditions(params, 1)
	where := buildWhereClause(conditions)

	costCols := `, SUM(input_cost), SUM(output_cost), SUM(total_cost)`
	query := fmt.Sprintf(`SELECT %s as period, COUNT(*), COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0), COALESCE(SUM(total_tokens), 0)`+costCols+`
		FROM "usage"%s GROUP BY %s ORDER BY period`, groupExpr, where, groupExpr)

	rows, err := r.pool.Query(ctx, query, args...)
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
