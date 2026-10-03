package usage

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/enterpilot/gomodel/internal/storage/sqlutil"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

const defaultRecalculationBatchSize = 500

// RecalculatePricing updates matching usage rows with costs computed from the
// supplied pricing resolver. Rows are read in id order, one batch at a time,
// inside a single transaction.
func (s *SQLStore) RecalculatePricing(ctx context.Context, params RecalculatePricingParams, resolver PricingResolver) (RecalculatePricingResult, error) {
	if err := recalculatePricingUnavailable(resolver); err != nil {
		return RecalculatePricingResult{}, err
	}
	params = normalizedRecalculatePricingParams(params)

	result := RecalculatePricingResult{}
	err := s.db.InTx(ctx, func(q sqlx.Querier) error {
		lastID := ""
		for {
			entries, err := s.recalculationEntries(ctx, q, params, lastID)
			if err != nil {
				return err
			}
			if len(entries) == 0 {
				return nil
			}
			for _, entry := range entries {
				update := recalculateEntryCosts(entry, resolver)
				if _, err := q.Exec(ctx, `
					UPDATE usage
					SET input_cost = ?, output_cost = ?, total_cost = ?, rewrite_cost_saved = ?, cost_source = ?, costs_calculation_caveat = ?
					WHERE id = ?
				`,
					nullableFloat(update.InputCost),
					nullableFloat(update.OutputCost),
					nullableFloat(update.TotalCost),
					nullableFloat(update.RewriteCostSaved),
					update.CostSource,
					update.Caveat,
					update.ID,
				); err != nil {
					return fmt.Errorf("update usage cost %s: %w", update.ID, err)
				}
				updateRecalculatePricingResult(&result, update)
			}
			lastID = entries[len(entries)-1].ID
		}
	})
	if err != nil {
		return RecalculatePricingResult{}, fmt.Errorf("recalculate usage pricing: %w", err)
	}
	return finalizeRecalculatePricingResult(result), nil
}

func (s *SQLStore) recalculationEntries(ctx context.Context, q sqlx.Querier, params RecalculatePricingParams, lastID string) ([]recalculationEntry, error) {
	conditions, args, err := s.dialect.usageConditions(params.UsageQueryParams)
	if err != nil {
		return nil, err
	}
	if lastID != "" {
		conditions = append(conditions, "id > ?")
		args = append(args, lastID)
	}
	batchSize := s.recalculationBatchSize
	if batchSize <= 0 {
		batchSize = defaultRecalculationBatchSize
	}
	args = append(args, batchSize)

	rows, err := q.Query(ctx, `
		SELECT id, timestamp, model, provider, provider_name, endpoint, input_tokens, output_tokens, rewrite_tokens_saved, raw_data, COALESCE(costs_calculation_caveat, '')
		FROM usage`+sqlutil.BuildWhereClause(conditions)+`
		ORDER BY id
		LIMIT ?`+s.dialect.lockRows, args...)
	if err != nil {
		return nil, fmt.Errorf("query usage costs for recalculation: %w", err)
	}
	defer rows.Close()

	entries := make([]recalculationEntry, 0)
	for rows.Next() {
		var entry recalculationEntry
		var timestamp usageTimestamp
		var providerName, rawData *string
		if err := rows.Scan(
			&entry.ID,
			&timestamp,
			&entry.Model,
			&entry.Provider,
			&providerName,
			&entry.Endpoint,
			&entry.InputTokens,
			&entry.OutputTokens,
			&entry.RewriteTokensSaved,
			&rawData,
			&entry.Caveat,
		); err != nil {
			return nil, fmt.Errorf("scan usage cost row: %w", err)
		}
		if timestamp.Valid {
			entry.Timestamp = timestamp.Time
		} else {
			slog.Warn("failed to parse usage timestamp for pricing recalculation; using base rates", "id", entry.ID, "raw_timestamp", timestamp.Raw)
		}
		if providerName != nil {
			entry.ProviderName = *providerName
		}
		if rawData != nil {
			entry.RawData = rawDataFromJSON(*rawData, entry.ID)
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate usage costs for recalculation: %w", err)
	}
	return entries, nil
}
