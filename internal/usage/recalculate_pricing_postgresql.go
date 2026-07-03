package usage

import (
	"gomodel/internal/storage/sqlutil"

	"context"
	"database/sql"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// RecalculatePricing updates matching PostgreSQL usage rows with costs computed
// from the supplied pricing resolver.
func (s *PostgreSQLStore) RecalculatePricing(ctx context.Context, params RecalculatePricingParams, resolver PricingResolver) (RecalculatePricingResult, error) {
	if err := recalculatePricingUnavailable(resolver); err != nil {
		return RecalculatePricingResult{}, err
	}
	params = normalizedRecalculatePricingParams(params)

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return RecalculatePricingResult{}, fmt.Errorf("begin postgres pricing recalculation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	entries, err := postgresRecalculationEntries(ctx, tx, params)
	if err != nil {
		return RecalculatePricingResult{}, err
	}
	if len(entries) == 0 {
		return finalizeRecalculatePricingResult(RecalculatePricingResult{}), nil
	}

	result := RecalculatePricingResult{}
	for _, entry := range entries {
		update := recalculateEntryCosts(entry, resolver)
		if _, err := tx.Exec(ctx, `
			UPDATE usage
			SET input_cost = $1, output_cost = $2, total_cost = $3, cost_source = $4, costs_calculation_caveat = $5
			WHERE id = $6::uuid
		`,
			nullableFloat(update.InputCost),
			nullableFloat(update.OutputCost),
			nullableFloat(update.TotalCost),
			update.CostSource,
			update.Caveat,
			update.ID,
		); err != nil {
			return RecalculatePricingResult{}, fmt.Errorf("update postgres usage cost %s: %w", update.ID, err)
		}
		updateRecalculatePricingResult(&result, update)
	}

	if err := tx.Commit(ctx); err != nil {
		return RecalculatePricingResult{}, fmt.Errorf("commit postgres pricing recalculation: %w", err)
	}
	return finalizeRecalculatePricingResult(result), nil
}

func postgresRecalculationEntries(ctx context.Context, tx pgx.Tx, params RecalculatePricingParams) ([]recalculationEntry, error) {
	conditions, args, _, err := pgUsageConditions(params.UsageQueryParams, 1)
	if err != nil {
		return nil, err
	}

	rows, err := tx.Query(ctx, `
		SELECT id::text, model, provider, provider_name, endpoint, input_tokens, output_tokens, raw_data::text
		FROM usage`+sqlutil.BuildWhereClause(conditions)+`
		FOR UPDATE`, args...)
	if err != nil {
		return nil, fmt.Errorf("query postgres usage costs for recalculation: %w", err)
	}
	defer rows.Close()

	entries := make([]recalculationEntry, 0)
	for rows.Next() {
		var entry recalculationEntry
		var providerName sql.NullString
		var rawData *string
		if err := rows.Scan(
			&entry.ID,
			&entry.Model,
			&entry.Provider,
			&providerName,
			&entry.Endpoint,
			&entry.InputTokens,
			&entry.OutputTokens,
			&rawData,
		); err != nil {
			return nil, fmt.Errorf("scan postgres usage cost row: %w", err)
		}
		if providerName.Valid {
			entry.ProviderName = providerName.String
		}
		if rawData != nil {
			entry.RawData = rawDataFromJSON(*rawData, entry.ID)
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate postgres usage costs for recalculation: %w", err)
	}
	return entries, nil
}
