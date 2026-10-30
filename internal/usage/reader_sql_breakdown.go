package usage

import (
	"context"
	"fmt"

	"github.com/enterpilot/gomodel/internal/storage/sqlutil"
)

// GetUsageBreakdown returns a bounded two-dimension pivot of the filtered
// usage. One grouped query returns every (row, column) pair (plus one for
// billed cache hits); the top-N cut and the "other" bucket are folded in Go
// so every backend shares them.
func (r *SQLReader) GetUsageBreakdown(ctx context.Context, params UsageBreakdownParams) (*UsageBreakdown, error) {
	return readUsageBreakdown(ctx, params, r.breakdownGroups)
}

// GetUsageInvoice returns billed usage per customer, streamed from the same
// grouped query as the breakdown.
func (r *SQLReader) GetUsageInvoice(ctx context.Context, params InvoiceParams) (*Invoice, error) {
	return readUsageInvoice(ctx, params, r.breakdownGroupsEach)
}

// breakdownGroups returns every (row, column) aggregate of params, at most
// maxBreakdownGroups+1 of them.
func (r *SQLReader) breakdownGroups(ctx context.Context, params UsageBreakdownParams) ([]breakdownGroup, error) {
	groups := make([]breakdownGroup, 0)
	err := r.breakdownGroupsEach(ctx, params, maxBreakdownGroups+1, func(g breakdownGroup) error {
		groups = append(groups, g)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return groups, nil
}

// breakdownGroupsEach streams the (row, column) aggregates of params to fn,
// at most limit of them; limit 0 streams them all.
func (r *SQLReader) breakdownGroupsEach(ctx context.Context, params UsageBreakdownParams, limit int, fn func(breakdownGroup) error) error {
	rowExpr, rowArgs, err := r.breakdownKeyExpr(ctx, params, params.Rows)
	if err != nil {
		return err
	}
	columnExpr, columnArgs, err := r.breakdownKeyExpr(ctx, params, params.Columns)
	if err != nil {
		return err
	}

	// Breaking down by user path matches the filter against the same
	// root-normalized expression the keys come from, like the user-path
	// breakdown does.
	userPathExpr := "user_path"
	if params.usesDimension(BreakdownUserPath) {
		userPathExpr = usageGroupedUserPathSQL("user_path")
	}
	conditions, whereArgs, err := r.dialect.usageConditionsWithUserPathExpr(params.UsageQueryParams, userPathExpr)
	if err != nil {
		return err
	}
	from := "usage"
	if params.usesDimension(BreakdownLabel) {
		from += " " + r.dialect.labelLeftJoin
	}

	// The keys are computed once in the CTE: on SQLite the period expression
	// can carry bound arguments, which a GROUP BY repeating it would need twice.
	query := `WITH usage_breakdown AS (
		SELECT ` + rowExpr + ` AS row_key, ` + columnExpr + ` AS column_key,
			input_tokens, output_tokens, total_tokens, total_cost, billed_cost
		FROM ` + from + sqlutil.BuildWhereClause(conditions) + `
	)
	SELECT row_key, column_key, COUNT(*), COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
		COALESCE(SUM(total_tokens), 0), SUM(total_cost), SUM(billed_cost)
		FROM usage_breakdown GROUP BY row_key, column_key`

	args := make([]any, 0, len(rowArgs)+len(columnArgs)+len(whereArgs)+1)
	args = append(append(append(args, rowArgs...), columnArgs...), whereArgs...)
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}

	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("failed to query usage breakdown: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var g breakdownGroup
		if err := rows.Scan(&g.Row, &g.Column, &g.Requests, &g.InputTokens, &g.OutputTokens, &g.TotalTokens, &g.TotalCost, &g.BilledCost); err != nil {
			return fmt.Errorf("failed to scan usage breakdown row: %w", err)
		}
		if err := fn(g); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("error iterating usage breakdown rows: %w", err)
	}
	return nil
}

// breakdownKeyExpr returns the SQL expression yielding dim's key for each
// row (never NULL), plus any arguments it binds.
func (r *SQLReader) breakdownKeyExpr(ctx context.Context, params UsageBreakdownParams, dim BreakdownDimension) (string, []any, error) {
	switch dim {
	case BreakdownModel:
		return "COALESCE(model, '')", nil, nil
	case BreakdownProvider:
		return usageGroupedProviderNameSQL("provider_name", "provider"), nil, nil
	case breakdownModelProvider:
		return "COALESCE(model, '') || CAST(? AS TEXT) || COALESCE(" + usageGroupedProviderNameSQL("provider_name", "provider") + ", '')", []any{modelProviderSeparator}, nil
	case BreakdownUserPath:
		return usageGroupedUserPathSQL("user_path"), nil, nil
	case BreakdownLabel:
		return "COALESCE(" + r.dialect.labelValue + ", '')", nil, nil
	case BreakdownEndpoint:
		return "COALESCE(endpoint, '')", nil, nil
	case BreakdownAuthKey:
		return "COALESCE(auth_key_id, '')", nil, nil
	case BreakdownPeriod:
		return r.periodExpr(ctx, params.UsageQueryParams)
	default:
		return "CAST('' AS TEXT)", nil, nil
	}
}
