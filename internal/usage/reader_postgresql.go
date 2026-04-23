package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgreSQLReader implements UsageReader for PostgreSQL databases.
type PostgreSQLReader struct {
	pool *pgxpool.Pool
}

type pgxRows interface {
	Next() bool
	Scan(dest ...any) error
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
	conditions, args, _, err := pgUsageConditions(params, 1)
	if err != nil {
		return nil, err
	}
	where := buildWhereClause(conditions)

	costCols := `, SUM(input_cost), SUM(output_cost), SUM(total_cost)`
	query := `SELECT COUNT(*), COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0), COALESCE(SUM(total_tokens), 0)` + costCols + `
			FROM "usage"` + where

	summary := &UsageSummary{}
	err = r.pool.QueryRow(ctx, query, args...).Scan(
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
	conditions, args, _, err := pgUsageConditions(params, 1)
	if err != nil {
		return nil, err
	}
	where := buildWhereClause(conditions)
	providerNameExpr := usageGroupedProviderNameSQL("provider_name", "provider")

	costCols := `, SUM(input_cost), SUM(output_cost), SUM(total_cost)`
	query := `SELECT model, provider, ` + providerNameExpr + ` AS provider_name, COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0)` + costCols + `
			FROM "usage"` + where + ` GROUP BY model, provider, ` + providerNameExpr

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query usage by model: %w", err)
	}
	defer rows.Close()

	result := make([]ModelUsage, 0)
	for rows.Next() {
		var m ModelUsage
		if err := rows.Scan(&m.Model, &m.Provider, &m.ProviderName, &m.InputTokens, &m.OutputTokens, &m.InputCost, &m.OutputCost, &m.TotalCost); err != nil {
			return nil, fmt.Errorf("failed to scan usage by model row: %w", err)
		}
		result = append(result, m)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating usage by model rows: %w", err)
	}

	return result, nil
}

// GetUsageByUserPath returns token and cost totals grouped by tracked user path.
func (r *PostgreSQLReader) GetUsageByUserPath(ctx context.Context, params UsageQueryParams) ([]UserPathUsage, error) {
	userPathExpr := usageGroupedUserPathSQL("user_path")
	conditions, args, _, err := pgUsageByUserPathConditions(params, userPathExpr, 1)
	if err != nil {
		return nil, err
	}
	where := buildWhereClause(conditions)

	costCols := `, SUM(input_cost), SUM(output_cost), SUM(total_cost)`
	query := `SELECT ` + userPathExpr + ` AS user_path, COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0), COALESCE(SUM(total_tokens), 0)` + costCols + `
			FROM "usage"` + where + ` GROUP BY ` + userPathExpr

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query usage by user path: %w", err)
	}
	defer rows.Close()

	result := make([]UserPathUsage, 0)
	for rows.Next() {
		var u UserPathUsage
		if err := rows.Scan(&u.UserPath, &u.InputTokens, &u.OutputTokens, &u.TotalTokens, &u.InputCost, &u.OutputCost, &u.TotalCost); err != nil {
			return nil, fmt.Errorf("failed to scan usage by user path row: %w", err)
		}
		result = append(result, u)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating usage by user path rows: %w", err)
	}

	return result, nil
}

// GetUsageLog returns a paginated list of individual usage log entries.
func (r *PostgreSQLReader) GetUsageLog(ctx context.Context, params UsageLogParams) (*UsageLogResult, error) {
	limit, offset := clampLimitOffset(params.Limit, params.Offset)

	conditions, args, argIdx, err := pgUsageConditions(params.UsageQueryParams, 1)
	if err != nil {
		return nil, err
	}

	if params.Model != "" {
		conditions = append(conditions, fmt.Sprintf("model = $%d", argIdx))
		args = append(args, params.Model)
		argIdx++
	}
	if params.Provider != "" {
		conditions = append(conditions, fmt.Sprintf("(provider = $%d OR provider_name = $%d)", argIdx, argIdx+1))
		args = append(args, params.Provider, params.Provider)
		argIdx += 2
	}
	if params.Search != "" {
		s := "%" + escapeLikeWildcards(params.Search) + "%"
		conditions = append(conditions, fmt.Sprintf("(model ILIKE $%d ESCAPE '\\' OR provider ILIKE $%d ESCAPE '\\' OR provider_name ILIKE $%d ESCAPE '\\' OR request_id ILIKE $%d ESCAPE '\\' OR provider_id ILIKE $%d ESCAPE '\\')", argIdx, argIdx, argIdx, argIdx, argIdx))
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
	dataQuery := fmt.Sprintf(`SELECT id, request_id, provider_id, timestamp, model, provider, provider_name, endpoint, user_path, cache_type,
		input_tokens, output_tokens, total_tokens, COALESCE(input_cost, 0), COALESCE(output_cost, 0), COALESCE(total_cost, 0), raw_data, COALESCE(costs_calculation_caveat, '')
		FROM "usage"%s ORDER BY timestamp DESC LIMIT $%d OFFSET $%d`, where, argIdx, argIdx+1)
	dataArgs := append(append([]any(nil), args...), limit, offset)

	rows, err := r.pool.Query(ctx, dataQuery, dataArgs...)
	if err != nil {
		return nil, fmt.Errorf("failed to query usage log: %w", err)
	}
	defer rows.Close()

	entries, err := scanPostgreSQLUsageLogEntries(rows)
	if err != nil {
		return nil, err
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

// GetUsageByRequestIDs returns usage entries grouped by request ID.
func (r *PostgreSQLReader) GetUsageByRequestIDs(ctx context.Context, requestIDs []string) (map[string][]UsageLogEntry, error) {
	requestIDs = compactNonEmptyStrings(requestIDs)
	if len(requestIDs) == 0 {
		return map[string][]UsageLogEntry{}, nil
	}

	args := make([]any, 0, len(requestIDs))
	placeholders := make([]string, 0, len(requestIDs))
	for idx, requestID := range requestIDs {
		args = append(args, requestID)
		placeholders = append(placeholders, fmt.Sprintf("$%d", idx+1))
	}

	query := fmt.Sprintf(`SELECT id, request_id, provider_id, timestamp, model, provider, provider_name, endpoint, user_path, cache_type,
		input_tokens, output_tokens, total_tokens, COALESCE(input_cost, 0), COALESCE(output_cost, 0), COALESCE(total_cost, 0), raw_data, COALESCE(costs_calculation_caveat, '')
		FROM "usage" WHERE request_id IN (%s) ORDER BY timestamp DESC, id DESC`, strings.Join(placeholders, ", "))

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query usage by request IDs: %w", err)
	}
	defer rows.Close()

	entries, err := scanPostgreSQLUsageLogEntries(rows)
	if err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating usage by request ID rows: %w", err)
	}

	grouped := make(map[string][]UsageLogEntry, len(requestIDs))
	for _, entry := range entries {
		grouped[entry.RequestID] = append(grouped[entry.RequestID], entry)
	}
	return grouped, nil
}

func scanPostgreSQLUsageLogEntries(rows pgxRows) ([]UsageLogEntry, error) {
	entries := make([]UsageLogEntry, 0)
	for rows.Next() {
		var e UsageLogEntry
		var rawDataJSON *string
		var providerName *string
		var userPath *string
		var cacheType *string
		if err := rows.Scan(&e.ID, &e.RequestID, &e.ProviderID, &e.Timestamp, &e.Model, &e.Provider, &providerName, &e.Endpoint, &userPath, &cacheType,
			&e.InputTokens, &e.OutputTokens, &e.TotalTokens, &e.InputCost, &e.OutputCost, &e.TotalCost, &rawDataJSON, &e.CostsCalculationCaveat); err != nil {
			return nil, fmt.Errorf("failed to scan usage log row: %w", err)
		}
		if rawDataJSON != nil && *rawDataJSON != "" {
			if err := json.Unmarshal([]byte(*rawDataJSON), &e.RawData); err != nil {
				slog.Warn("failed to unmarshal raw_data JSON", "request_id", e.RequestID, "error", err)
			}
		}
		if userPath != nil {
			e.UserPath = *userPath
		}
		if providerName != nil {
			e.ProviderName = displayUsageProviderName(*providerName, e.Provider)
		} else {
			e.ProviderName = displayUsageProviderName("", e.Provider)
		}
		if cacheType != nil {
			e.CacheType = normalizeCacheType(*cacheType)
		}
		entries = append(entries, e)
	}
	return entries, nil
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
		args = append(args, usageEndExclusive(params).UTC())
		nextIdx++
	}
	return conditions, args, nextIdx
}

func pgGroupExpr(interval string, timeZone string) string {
	zoneLiteral := pgQuoteLiteral(timeZone)

	switch interval {
	case "weekly":
		return fmt.Sprintf(`to_char(DATE_TRUNC('week', timestamp AT TIME ZONE %s), 'IYYY-"W"IW')`, zoneLiteral)
	case "monthly":
		return fmt.Sprintf(`to_char(DATE_TRUNC('month', timestamp AT TIME ZONE %s), 'YYYY-MM')`, zoneLiteral)
	case "yearly":
		return fmt.Sprintf(`to_char(DATE_TRUNC('year', timestamp AT TIME ZONE %s), 'YYYY')`, zoneLiteral)
	default:
		return fmt.Sprintf(`to_char(DATE(timestamp AT TIME ZONE %s), 'YYYY-MM-DD')`, zoneLiteral)
	}
}

// GetDailyUsage returns usage statistics grouped by time period (daily, weekly, monthly, yearly).
func (r *PostgreSQLReader) GetDailyUsage(ctx context.Context, params UsageQueryParams) ([]DailyUsage, error) {
	interval := params.Interval
	if interval == "" {
		interval = "daily"
	}
	groupExpr := pgGroupExpr(interval, usageTimeZone(params))

	conditions, args, _, err := pgUsageConditions(params, 1)
	if err != nil {
		return nil, err
	}
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

// GetCacheOverview returns cached-only aggregates for the admin dashboard.
func (r *PostgreSQLReader) GetCacheOverview(ctx context.Context, params UsageQueryParams) (*CacheOverview, error) {
	params.CacheMode = CacheModeCached

	conditions, args, _, err := pgUsageConditions(params, 3)
	if err != nil {
		return nil, err
	}
	where := buildWhereClause(conditions)
	queryArgs := append([]any{CacheTypeExact, CacheTypeSemantic}, args...)

	summaryQuery := `SELECT COUNT(*),
		COALESCE(SUM(CASE WHEN cache_type = $1 THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN cache_type = $2 THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(input_tokens), 0),
		COALESCE(SUM(output_tokens), 0),
		COALESCE(SUM(total_tokens), 0),
		SUM(total_cost)
		FROM "usage"` + where

	overview := &CacheOverview{}
	if err := r.pool.QueryRow(ctx, summaryQuery, queryArgs...).Scan(
		&overview.Summary.TotalHits,
		&overview.Summary.ExactHits,
		&overview.Summary.SemanticHits,
		&overview.Summary.TotalInput,
		&overview.Summary.TotalOutput,
		&overview.Summary.TotalTokens,
		&overview.Summary.TotalSavedCost,
	); err != nil {
		return nil, fmt.Errorf("failed to query cache overview summary: %w", err)
	}

	interval := params.Interval
	if interval == "" {
		interval = "daily"
	}
	groupExpr := pgGroupExpr(interval, usageTimeZone(params))
	dailyQuery := fmt.Sprintf(`SELECT %s as period,
		COUNT(*),
		COALESCE(SUM(CASE WHEN cache_type = $1 THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN cache_type = $2 THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(input_tokens), 0),
		COALESCE(SUM(output_tokens), 0),
		COALESCE(SUM(total_tokens), 0),
		SUM(total_cost)
		FROM "usage"%s GROUP BY %s ORDER BY period`, groupExpr, where, groupExpr)

	rows, err := r.pool.Query(ctx, dailyQuery, queryArgs...)
	if err != nil {
		return nil, fmt.Errorf("failed to query cache overview daily: %w", err)
	}
	defer rows.Close()

	overview.Daily = make([]CacheOverviewDaily, 0)
	for rows.Next() {
		var d CacheOverviewDaily
		if err := rows.Scan(&d.Date, &d.Hits, &d.ExactHits, &d.SemanticHits, &d.InputTokens, &d.OutputTokens, &d.TotalTokens, &d.SavedCost); err != nil {
			return nil, fmt.Errorf("failed to scan cache overview daily row: %w", err)
		}
		overview.Daily = append(overview.Daily, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating cache overview daily rows: %w", err)
	}

	return overview, nil
}

func pgQuoteLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func pgUsageConditions(params UsageQueryParams, argIdx int) (conditions []string, args []any, nextIdx int, err error) {
	conditions, args, nextIdx = pgDateRangeConditions(params, argIdx)
	userPath, err := normalizeUsageUserPathFilter(params.UserPath)
	if err != nil {
		return nil, nil, 0, err
	}
	if userPath != "" {
		conditions = append(conditions, fmt.Sprintf("(user_path = $%d OR user_path LIKE $%d ESCAPE '\\')", nextIdx, nextIdx+1))
		args = append(args, userPath, usageUserPathSubtreePattern(userPath))
		nextIdx += 2
	}
	if condition := pgCacheModeCondition(params.CacheMode); condition != "" {
		conditions = append(conditions, condition)
	}
	return conditions, args, nextIdx, nil
}

func pgUsageByUserPathConditions(params UsageQueryParams, userPathExpr string, argIdx int) (conditions []string, args []any, nextIdx int, err error) {
	conditions, args, nextIdx = pgDateRangeConditions(params, argIdx)
	userPath, err := normalizeUsageUserPathFilter(params.UserPath)
	if err != nil {
		return nil, nil, 0, err
	}
	if userPath != "" {
		conditions = append(conditions, fmt.Sprintf("(%s = $%d OR %s LIKE $%d ESCAPE '\\')", userPathExpr, nextIdx, userPathExpr, nextIdx+1))
		args = append(args, userPath, usageUserPathSubtreePattern(userPath))
		nextIdx += 2
	}
	if condition := pgCacheModeCondition(params.CacheMode); condition != "" {
		conditions = append(conditions, condition)
	}
	return conditions, args, nextIdx, nil
}

func pgCacheModeCondition(mode string) string {
	switch normalizeCacheMode(mode) {
	case CacheModeCached:
		return "(cache_type = '" + CacheTypeExact + "' OR cache_type = '" + CacheTypeSemantic + "')"
	case CacheModeAll:
		return ""
	default:
		return "(cache_type IS NULL OR cache_type = '')"
	}
}
