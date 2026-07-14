package usage

import (
	"github.com/enterpilot/gomodel/internal/storage/sqlutil"

	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/goccy/go-json"

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
	where := sqlutil.BuildWhereClause(conditions)

	costCols := `, SUM(input_cost), SUM(output_cost), SUM(total_cost)`
	savingsCols := `, COALESCE(SUM(rewrite_tokens_saved), 0), SUM(rewrite_cost_saved)`
	query := `SELECT COUNT(*), COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0), COALESCE(SUM(total_tokens), 0)` + costCols + savingsCols + `
			FROM "usage"` + where

	summary := &UsageSummary{}
	err = r.pool.QueryRow(ctx, query, args...).Scan(
		&summary.TotalRequests, &summary.TotalInput, &summary.TotalOutput, &summary.TotalTokens,
		&summary.TotalInputCost, &summary.TotalOutputCost, &summary.TotalCost,
		&summary.RewriteTokensSaved, &summary.RewriteCostSaved,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query usage summary: %w", err)
	}

	if err := r.accumulateInputSegments(ctx, where, args, summary); err != nil {
		return nil, err
	}

	return summary, nil
}

// accumulateInputSegments streams the matched rows and folds each row's
// provider prompt-cache split into the summary. It runs a second pass (the
// aggregate above cannot also return per-row raw_data) over the same filter,
// selecting only the columns EntryInputSegments needs. This is the dashboard
// summary path, not a request hot path.
func (r *PostgreSQLReader) accumulateInputSegments(ctx context.Context, where string, args []any, summary *UsageSummary) error {
	rows, err := r.pool.Query(ctx, `SELECT input_tokens, provider, raw_data FROM "usage"`+where, args...)
	if err != nil {
		return fmt.Errorf("failed to query usage input segments: %w", err)
	}
	defer rows.Close()
	return foldInputSegments(rows, summary)
}

// GetUsageByModel returns token and cost totals grouped by model and provider.
func (r *PostgreSQLReader) GetUsageByModel(ctx context.Context, params UsageQueryParams) ([]ModelUsage, error) {
	conditions, args, _, err := pgUsageConditions(params, 1)
	if err != nil {
		return nil, err
	}
	where := sqlutil.BuildWhereClause(conditions)
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
	// Match the user-path filter against the same grouped (root-normalized)
	// expression the rows are grouped by.
	userPathExpr := usageGroupedUserPathSQL("user_path")
	conditions, args, _, err := pgUsageConditionsWithUserPathExpr(params, userPathExpr, 1)
	if err != nil {
		return nil, err
	}
	where := sqlutil.BuildWhereClause(conditions)

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

// GetUsageByLabel returns token and cost totals grouped by request label.
// jsonb_array_elements_text expands each row's labels JSONB array, so a row
// with several labels contributes its totals to each of them; rows with NULL
// or non-array labels are omitted by the jsonb_typeof guard.
func (r *PostgreSQLReader) GetUsageByLabel(ctx context.Context, params UsageQueryParams) ([]LabelUsage, error) {
	conditions, args, _, err := pgUsageConditions(params, 1)
	if err != nil {
		return nil, err
	}
	conditions = append(conditions, "jsonb_typeof(labels) = 'array'")
	where := sqlutil.BuildWhereClause(conditions)

	costCols := `, SUM(input_cost), SUM(output_cost), SUM(total_cost)`
	query := `SELECT label, COUNT(*), COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0), COALESCE(SUM(total_tokens), 0)` + costCols + `
			FROM "usage", jsonb_array_elements_text(labels) AS label` + where + ` GROUP BY label ORDER BY label`

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query usage by label: %w", err)
	}
	defer rows.Close()

	result := make([]LabelUsage, 0)
	for rows.Next() {
		var l LabelUsage
		if err := rows.Scan(&l.Label, &l.Requests, &l.InputTokens, &l.OutputTokens, &l.TotalTokens, &l.InputCost, &l.OutputCost, &l.TotalCost); err != nil {
			return nil, fmt.Errorf("failed to scan usage by label row: %w", err)
		}
		result = append(result, l)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating usage by label rows: %w", err)
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

	if params.Search != "" {
		s := "%" + sqlutil.EscapeLikeWildcards(params.Search) + "%"
		conditions = append(conditions, fmt.Sprintf("(model ILIKE $%d ESCAPE '\\' OR provider ILIKE $%d ESCAPE '\\' OR provider_name ILIKE $%d ESCAPE '\\' OR request_id ILIKE $%d ESCAPE '\\' OR provider_id ILIKE $%d ESCAPE '\\')", argIdx, argIdx, argIdx, argIdx, argIdx))
		args = append(args, s)
		argIdx++
	}

	where := sqlutil.BuildWhereClause(conditions)

	// Count total
	var total int
	countQuery := `SELECT COUNT(*) FROM "usage"` + where
	if err := r.pool.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("failed to count usage log entries: %w", err)
	}

	// Fetch page
	dataQuery := fmt.Sprintf(`SELECT id, request_id, provider_id, timestamp, model, provider, provider_name, endpoint, user_path, cache_type, labels,
		input_tokens, output_tokens, total_tokens, input_cost, output_cost, total_cost, COALESCE(cost_source, ''), raw_data, COALESCE(costs_calculation_caveat, ''),
		COALESCE(rewrite_tokens_saved, 0), rewrite_cost_saved
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

	query := fmt.Sprintf(`SELECT id, request_id, provider_id, timestamp, model, provider, provider_name, endpoint, user_path, cache_type, labels,
		input_tokens, output_tokens, total_tokens, input_cost, output_cost, total_cost, COALESCE(cost_source, ''), raw_data, COALESCE(costs_calculation_caveat, ''),
		COALESCE(rewrite_tokens_saved, 0), rewrite_cost_saved
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
		var labelsJSON *string
		if err := rows.Scan(&e.ID, &e.RequestID, &e.ProviderID, &e.Timestamp, &e.Model, &e.Provider, &providerName, &e.Endpoint, &userPath, &cacheType, &labelsJSON,
			&e.InputTokens, &e.OutputTokens, &e.TotalTokens, &e.InputCost, &e.OutputCost, &e.TotalCost, &e.CostSource, &rawDataJSON, &e.CostsCalculationCaveat,
			&e.RewriteTokensSaved, &e.RewriteCostSaved); err != nil {
			return nil, fmt.Errorf("failed to scan usage log row: %w", err)
		}
		if labelsJSON != nil {
			e.Labels = sqlutil.StringsFromJSON(*labelsJSON, e.RequestID)
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
	where := sqlutil.BuildWhereClause(conditions)

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
	rows.Close()

	// Second pass: fold the per-period prompt-cache split from raw_data, reusing
	// the same group expression so the period keys line up.
	splitQuery := fmt.Sprintf(`SELECT %s AS period, cache_type, input_tokens, provider, raw_data FROM "usage"%s`, groupExpr, where)
	splitRows, err := r.pool.Query(ctx, splitQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query daily input segments: %w", err)
	}
	defer splitRows.Close()
	splits, err := foldPeriodInputSegments(splitRows)
	if err != nil {
		return nil, err
	}
	applyDailyInputSplit(result, splits)

	return result, nil
}

// GetCacheOverview returns cached-only aggregates for the admin dashboard.
func (r *PostgreSQLReader) GetCacheOverview(ctx context.Context, params UsageQueryParams) (*CacheOverview, error) {
	params.CacheMode = CacheModeCached

	conditions, args, _, err := pgUsageConditions(params, 3)
	if err != nil {
		return nil, err
	}
	where := sqlutil.BuildWhereClause(conditions)
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

// GetTokenThroughput returns the trailing window of token-volume buckets for the
// overview live-throughput chart. Buckets are epoch-aligned; the prompt-cache
// split is folded in Go (it lives in raw_data), mirroring the summary path.
func (r *PostgreSQLReader) GetTokenThroughput(ctx context.Context, gran ThroughputGranularity, end time.Time, offset int64) (*TokenThroughput, error) {
	acc := newThroughputAccumulator(gran, end, offset)
	bucketSeconds, first, upper := throughputWindow(gran, end, offset)

	conditions := []string{"timestamp >= $1", "timestamp < $2"}
	args := []any{time.Unix(first, 0).UTC(), time.Unix(upper, 0).UTC()}
	where := sqlutil.BuildWhereClause(conditions)

	bucketExpr := fmt.Sprintf("(FLOOR((EXTRACT(EPOCH FROM timestamp) + %d) / %d) * %d - %d)::bigint", offset, bucketSeconds, bucketSeconds, offset)
	query := fmt.Sprintf(`SELECT %s AS bucket, cache_type, input_tokens, output_tokens, total_tokens, provider, raw_data
		FROM "usage"%s`, bucketExpr, where)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query token throughput: %w", err)
	}
	defer rows.Close()

	if err := foldThroughput(rows, acc); err != nil {
		return nil, err
	}
	return acc.result(gran), nil
}

func pgQuoteLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func pgUsageConditions(params UsageQueryParams, argIdx int) (conditions []string, args []any, nextIdx int, err error) {
	return pgUsageConditionsWithUserPathExpr(params, "user_path", argIdx)
}

func pgUsageConditionsWithUserPathExpr(params UsageQueryParams, userPathExpr string, argIdx int) (conditions []string, args []any, nextIdx int, err error) {
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
	if params.Model != "" {
		conditions = append(conditions, fmt.Sprintf("model = $%d", nextIdx))
		args = append(args, params.Model)
		nextIdx++
	}
	if params.Provider != "" {
		conditions = append(conditions, fmt.Sprintf("(provider = $%d OR provider_name = $%d)", nextIdx, nextIdx+1))
		args = append(args, params.Provider, params.Provider)
		nextIdx += 2
	}
	if params.Label != "" {
		// jsonb_exists matches a top-level array element; NULL labels yield
		// NULL which the WHERE clause treats as no match.
		conditions = append(conditions, fmt.Sprintf("jsonb_exists(labels, $%d)", nextIdx))
		args = append(args, params.Label)
		nextIdx++
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
