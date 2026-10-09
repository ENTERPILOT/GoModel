package usage

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/enterpilot/gomodel/internal/storage/sqlutil"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// SQLReader implements UsageReader for SQLite and PostgreSQL.
type SQLReader struct {
	db      sqlx.DB
	dialect usageDialect
}

// NewSQLReader creates a usage reader on a SQL database.
func NewSQLReader(db sqlx.DB) (*SQLReader, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection is required")
	}
	return &SQLReader{db: db, dialect: usageDialectFor(db.Dialect())}, nil
}

const usageCostColumns = `, SUM(input_cost), SUM(output_cost), SUM(total_cost)`

// GetSummary returns aggregated usage statistics for the given query parameters.
func (r *SQLReader) GetSummary(ctx context.Context, params UsageQueryParams) (*UsageSummary, error) {
	conditions, args, err := r.dialect.usageConditions(params)
	if err != nil {
		return nil, err
	}
	where := sqlutil.BuildWhereClause(conditions)

	savingsCols := `, COALESCE(SUM(rewrite_tokens_saved), 0), SUM(rewrite_cost_saved)`
	query := `SELECT COUNT(*), COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0), COALESCE(SUM(total_tokens), 0)` + usageCostColumns + savingsCols + `
			FROM usage` + where

	summary := &UsageSummary{}
	err = r.db.QueryRow(ctx, query, args...).Scan(
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
func (r *SQLReader) accumulateInputSegments(ctx context.Context, where string, args []any, summary *UsageSummary) error {
	rows, err := r.db.Query(ctx, `SELECT input_tokens, provider, raw_data FROM usage`+where, args...)
	if err != nil {
		return fmt.Errorf("failed to query usage input segments: %w", err)
	}
	defer rows.Close()
	return foldInputSegments(rows, summary)
}

// GetUsageByModel returns token and cost totals grouped by model and provider.
func (r *SQLReader) GetUsageByModel(ctx context.Context, params UsageQueryParams) ([]ModelUsage, error) {
	conditions, args, err := r.dialect.usageConditions(params)
	if err != nil {
		return nil, err
	}
	where := sqlutil.BuildWhereClause(conditions)
	providerNameExpr := usageGroupedProviderNameSQL("provider_name", "provider")

	query := `SELECT model, provider, ` + providerNameExpr + ` AS provider_name, COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0)` + usageCostColumns + `
			FROM usage` + where + ` GROUP BY model, provider, ` + providerNameExpr

	rows, err := r.db.Query(ctx, query, args...)
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

	stats, err := r.usageCacheStats(ctx, params, "user_path", nil, modelGroupKeys)
	if err != nil {
		return nil, err
	}
	result = applyModelCacheStats(result, stats)

	return result, nil
}

// usageCacheStats runs the second streaming pass behind the chart aggregates
// and folds cache figures per group. For uncached-mode requests (the
// default) it streams with cache mode "all" so local-cache rows are counted
// even though the aggregates exclude them.
func (r *SQLReader) usageCacheStats(ctx context.Context, params UsageQueryParams, userPathExpr string, extraConditions []string, keysFor groupKeysFunc) (map[string]*GroupCacheStats, error) {
	// The cache fields describe uncached-mode aggregates: for cached/all
	// modes the local tokens are already inside the aggregate sums (and the
	// provider split would not partition them), so the pass is skipped and
	// the fields stay zero.
	if normalizeCacheMode(params.CacheMode) != CacheModeUncached {
		return nil, nil
	}
	params.CacheMode = CacheModeAll
	conditions, args, err := r.dialect.usageConditionsWithUserPathExpr(params, userPathExpr)
	if err != nil {
		return nil, err
	}
	conditions = append(conditions, extraConditions...)
	where := sqlutil.BuildWhereClause(conditions)

	rows, err := r.db.Query(ctx, `SELECT model, provider, provider_name, user_path, labels, cache_type, input_tokens, output_tokens, raw_data, timestamp FROM usage`+where, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query usage cache stats: %w", err)
	}
	defer rows.Close()
	return foldUsageCacheRows(rows, keysFor)
}

// GetUsageByUserPath returns token and cost totals grouped by tracked user path.
func (r *SQLReader) GetUsageByUserPath(ctx context.Context, params UsageQueryParams) ([]UserPathUsage, error) {
	// Match the user-path filter against the same grouped (root-normalized)
	// expression the rows are grouped by.
	userPathExpr := usageGroupedUserPathSQL("user_path")
	conditions, args, err := r.dialect.usageConditionsWithUserPathExpr(params, userPathExpr)
	if err != nil {
		return nil, err
	}
	where := sqlutil.BuildWhereClause(conditions)

	query := `SELECT ` + userPathExpr + ` AS user_path, COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0), COALESCE(SUM(total_tokens), 0)` + usageCostColumns + `
			FROM usage` + where + ` GROUP BY ` + userPathExpr

	rows, err := r.db.Query(ctx, query, args...)
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

	stats, err := r.usageCacheStats(ctx, params, userPathExpr, nil, userPathGroupKeys)
	if err != nil {
		return nil, err
	}
	result = applyUserPathCacheStats(result, stats)

	return result, nil
}

// GetUsageByLabel returns token and cost totals grouped by request label.
// Each row's labels array is expanded, so a row with several labels
// contributes its totals to each of them; unlabelled rows are omitted.
func (r *SQLReader) GetUsageByLabel(ctx context.Context, params UsageQueryParams) ([]LabelUsage, error) {
	conditions, args, err := r.dialect.usageConditions(params)
	if err != nil {
		return nil, err
	}
	conditions = append(conditions, r.dialect.hasLabels)
	where := sqlutil.BuildWhereClause(conditions)

	label := r.dialect.labelValue
	query := `SELECT ` + label + ` AS label, COUNT(*), COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0), COALESCE(SUM(total_tokens), 0)` + usageCostColumns + `
			FROM usage, ` + r.dialect.labelSource + where + ` GROUP BY ` + label + ` ORDER BY label`

	rows, err := r.db.Query(ctx, query, args...)
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

	stats, err := r.usageCacheStats(ctx, params, "user_path", []string{r.dialect.hasLabels}, labelGroupKeys)
	if err != nil {
		return nil, err
	}
	result = applyLabelCacheStats(result, stats)

	return result, nil
}

// GetUsageBySession returns request, token, and cost totals grouped by the
// detected session id and its tracked user path. Legacy rows without a session
// id are omitted.
func (r *SQLReader) GetUsageBySession(ctx context.Context, params SessionUsageParams) (*SessionUsageResult, error) {
	countQuery, query, args, dataArgs, limit, offset, err := r.dialect.sessionUsageQueries(params)
	if err != nil {
		return nil, err
	}

	var total int
	if err := r.db.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("failed to count usage sessions: %w", err)
	}

	rows, err := r.db.Query(ctx, query, dataArgs...)
	if err != nil {
		return nil, fmt.Errorf("failed to query usage by session: %w", err)
	}
	defer rows.Close()

	result := make([]SessionUsage, 0)
	for rows.Next() {
		var session SessionUsage
		if err := rows.Scan(&session.SessionID, &session.UserPath, &session.Requests,
			&session.InputTokens, &session.OutputTokens, &session.TotalTokens,
			&session.InputCost, &session.OutputCost, &session.TotalCost); err != nil {
			return nil, fmt.Errorf("failed to scan usage by session row: %w", err)
		}
		result = append(result, session)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating usage by session rows: %w", err)
	}
	return &SessionUsageResult{Entries: result, Total: total, Limit: limit, Offset: offset}, nil
}

// sessionUsageQueries builds matching bounded data and count queries.
func (d usageDialect) sessionUsageQueries(params SessionUsageParams) (countQuery, dataQuery string, args, dataArgs []any, limit, offset int, err error) {
	limit, offset = clampLimitOffset(params.Limit, params.Offset)
	queryParams := params.UsageQueryParams
	queryParams.CacheMode = CacheModeAll
	conditions, args, err := d.usageConditions(queryParams)
	if err != nil {
		return "", "", nil, nil, 0, 0, err
	}
	conditions = append(conditions, "session_id IS NOT NULL", "TRIM(session_id) <> ''")
	where := sqlutil.BuildWhereClause(conditions)
	userPathExpr := usageGroupedUserPathSQL("user_path")
	groupBy := " GROUP BY session_id, " + userPathExpr

	countQuery = `SELECT COUNT(*) FROM (SELECT 1 FROM usage` + where + groupBy + `) AS sessions`
	dataQuery = `SELECT session_id, ` + userPathExpr + ` AS user_path, COUNT(*),
		COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0), COALESCE(SUM(total_tokens), 0),
		` + providerSessionCostSQL("input_cost") + `,
		` + providerSessionCostSQL("output_cost") + `,
		` + providerSessionCostSQL("total_cost") + `
		FROM usage` + where + groupBy + ` ORDER BY MAX(` + d.timeExpr + `) DESC, session_id ASC, user_path ASC LIMIT ? OFFSET ?`
	dataArgs = append(append([]any(nil), args...), limit, offset)
	return countQuery, dataQuery, args, dataArgs, limit, offset, nil
}

const usageLogColumns = `id, request_id, provider_id, timestamp, model, provider, provider_name, endpoint, user_path, session_id, cache_type, labels,
		input_tokens, output_tokens, total_tokens, input_cost, output_cost, total_cost, COALESCE(cost_source, ''), raw_data, COALESCE(costs_calculation_caveat, ''),
		COALESCE(rewrite_tokens_saved, 0), rewrite_cost_saved`

// GetUsageLog returns a paginated list of individual usage log entries.
func (r *SQLReader) GetUsageLog(ctx context.Context, params UsageLogParams) (*UsageLogResult, error) {
	limit, offset := clampLimitOffset(params.Limit, params.Offset)

	conditions, args, err := r.dialect.usageConditions(params.UsageQueryParams)
	if err != nil {
		return nil, err
	}

	if params.Search != "" {
		like := r.dialect.like + ` ? ESCAPE '\'`
		conditions = append(conditions, "(model "+like+" OR provider "+like+" OR provider_name "+like+" OR request_id "+like+" OR provider_id "+like+" OR session_id "+like+")")
		s := "%" + sqlutil.EscapeLikeWildcards(params.Search) + "%"
		args = append(args, s, s, s, s, s, s)
	}

	where := sqlutil.BuildWhereClause(conditions)

	var total int
	if err := r.db.QueryRow(ctx, "SELECT COUNT(*) FROM usage"+where, args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("failed to count usage log entries: %w", err)
	}

	dataQuery := `SELECT ` + usageLogColumns + `
		FROM usage` + where + ` ORDER BY ` + r.dialect.timeExpr + ` DESC, id DESC LIMIT ? OFFSET ?`
	dataArgs := append(append([]any(nil), args...), limit, offset)

	rows, err := r.db.Query(ctx, dataQuery, dataArgs...)
	if err != nil {
		return nil, fmt.Errorf("failed to query usage log: %w", err)
	}
	defer rows.Close()

	entries, err := scanUsageLogEntries(rows)
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
func (r *SQLReader) GetUsageByRequestIDs(ctx context.Context, requestIDs []string) (map[string][]UsageLogEntry, error) {
	requestIDs = compactNonEmptyStrings(requestIDs)
	if len(requestIDs) == 0 {
		return map[string][]UsageLogEntry{}, nil
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(requestIDs)), ",")
	args := make([]any, 0, len(requestIDs))
	for _, requestID := range requestIDs {
		args = append(args, requestID)
	}

	query := `SELECT ` + usageLogColumns + `
		FROM usage WHERE request_id IN (` + placeholders + `) ORDER BY ` + r.dialect.timeExpr + ` DESC, id DESC`

	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query usage by request IDs: %w", err)
	}
	defer rows.Close()

	entries, err := scanUsageLogEntries(rows)
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

// GetDailyUsage returns usage statistics grouped by time period (daily, weekly, monthly, yearly).
func (r *SQLReader) GetDailyUsage(ctx context.Context, params UsageQueryParams) ([]DailyUsage, error) {
	groupExpr, groupArgs, err := r.periodExpr(ctx, params)
	if err != nil {
		return nil, err
	}

	conditions, args, err := r.dialect.usageConditions(params)
	if err != nil {
		return nil, err
	}
	where := sqlutil.BuildWhereClause(conditions)

	// The period is computed once in the CTE: on SQLite it can carry bound
	// arguments, which a GROUP BY repeating the expression would need twice.
	query := `WITH usage_periods AS (
		SELECT ` + groupExpr + ` AS period,
			input_tokens, output_tokens, total_tokens, input_cost, output_cost, total_cost
		FROM usage` + where + `
	)
	SELECT period, COUNT(*), COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0), COALESCE(SUM(total_tokens), 0)` + usageCostColumns + `
		FROM usage_periods GROUP BY period ORDER BY period`

	queryArgs := append(groupArgs, args...)

	rows, err := r.db.Query(ctx, query, queryArgs...)
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

	// Second pass: fold the per-period prompt-cache split from raw_data (the
	// GROUP BY above cannot also return per-row raw_data), reusing the same
	// group expression so the period keys line up.
	splitRows, err := r.db.Query(ctx, `SELECT `+groupExpr+` AS period, cache_type, input_tokens, provider, raw_data FROM usage`+where, queryArgs...)
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
func (r *SQLReader) GetCacheOverview(ctx context.Context, params UsageQueryParams) (*CacheOverview, error) {
	params.CacheMode = CacheModeCached

	conditions, args, err := r.dialect.usageConditions(params)
	if err != nil {
		return nil, err
	}
	where := sqlutil.BuildWhereClause(conditions)

	summaryQuery := `SELECT COUNT(*),
		COALESCE(SUM(CASE WHEN cache_type = '` + CacheTypeExact + `' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN cache_type = '` + CacheTypeSemantic + `' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(input_tokens), 0),
		COALESCE(SUM(output_tokens), 0),
		COALESCE(SUM(total_tokens), 0),
		SUM(total_cost)
		FROM usage` + where

	overview := &CacheOverview{}
	if err := r.db.QueryRow(ctx, summaryQuery, args...).Scan(
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

	groupExpr, groupArgs, err := r.periodExpr(ctx, params)
	if err != nil {
		return nil, err
	}
	dailyQuery := `WITH usage_periods AS (
		SELECT ` + groupExpr + ` AS period,
			cache_type, input_tokens, output_tokens, total_tokens, total_cost
		FROM usage` + where + `
	)
	SELECT period,
		COUNT(*),
		COALESCE(SUM(CASE WHEN cache_type = '` + CacheTypeExact + `' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN cache_type = '` + CacheTypeSemantic + `' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(input_tokens), 0),
		COALESCE(SUM(output_tokens), 0),
		COALESCE(SUM(total_tokens), 0),
		SUM(total_cost)
		FROM usage_periods GROUP BY period ORDER BY period`
	queryArgs := append(groupArgs, args...)

	rows, err := r.db.Query(ctx, dailyQuery, queryArgs...)
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
func (r *SQLReader) GetTokenThroughput(ctx context.Context, gran ThroughputGranularity, end time.Time, offset int64) (*TokenThroughput, error) {
	acc := newThroughputAccumulator(gran, end, offset)
	bucketSeconds, first, upper := throughputWindow(gran, end, offset)

	where := sqlutil.BuildWhereClause([]string{r.dialect.timeExpr + " >= ?", r.dialect.timeExpr + " < ?"})
	args := []any{r.dialect.timeArg(time.Unix(first, 0)), r.dialect.timeArg(time.Unix(upper, 0))}

	query := `SELECT ` + r.dialect.bucketExpr(offset, bucketSeconds) + ` AS bucket, cache_type, input_tokens, output_tokens, total_tokens, provider, raw_data
		FROM usage` + where

	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query token throughput: %w", err)
	}
	defer rows.Close()

	if err := foldThroughput(rows, acc); err != nil {
		return nil, err
	}
	return acc.result(gran), nil
}
