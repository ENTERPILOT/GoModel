package usage

import (
	"gomodel/internal/storage/sqlutil"

	"context"
	"database/sql"
	"fmt"
)

const defaultSQLiteRecalculationBatchSize = 500

// RecalculatePricing updates matching SQLite usage rows with costs computed
// from the supplied pricing resolver.
func (s *SQLiteStore) RecalculatePricing(ctx context.Context, params RecalculatePricingParams, resolver PricingResolver) (RecalculatePricingResult, error) {
	if err := recalculatePricingUnavailable(resolver); err != nil {
		return RecalculatePricingResult{}, err
	}
	params = normalizedRecalculatePricingParams(params)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RecalculatePricingResult{}, fmt.Errorf("begin sqlite pricing recalculation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx, `
		UPDATE usage
		SET input_cost = ?, output_cost = ?, total_cost = ?, rewrite_cost_saved = ?, cost_source = ?, costs_calculation_caveat = ?
		WHERE id = ?
	`)
	if err != nil {
		return RecalculatePricingResult{}, fmt.Errorf("prepare sqlite pricing recalculation update: %w", err)
	}
	defer stmt.Close()

	result := RecalculatePricingResult{}
	lastID := ""
	for {
		entries, err := s.sqliteRecalculationEntries(ctx, tx, params, lastID, s.sqliteRecalculationBatchSize())
		if err != nil {
			return RecalculatePricingResult{}, err
		}
		if len(entries) == 0 {
			break
		}

		for _, entry := range entries {
			update := recalculateEntryCosts(entry, resolver)
			if _, err := stmt.ExecContext(ctx,
				nullableFloat(update.InputCost),
				nullableFloat(update.OutputCost),
				nullableFloat(update.TotalCost),
				nullableFloat(update.RewriteCostSaved),
				update.CostSource,
				update.Caveat,
				update.ID,
			); err != nil {
				return RecalculatePricingResult{}, fmt.Errorf("update sqlite usage cost %s: %w", update.ID, err)
			}
			updateRecalculatePricingResult(&result, update)
		}
		lastID = entries[len(entries)-1].ID
	}

	if err := tx.Commit(); err != nil {
		return RecalculatePricingResult{}, fmt.Errorf("commit sqlite pricing recalculation: %w", err)
	}
	return finalizeRecalculatePricingResult(result), nil
}

func (s *SQLiteStore) sqliteRecalculationBatchSize() int {
	if s.recalculationBatchSize > 0 {
		return s.recalculationBatchSize
	}
	return defaultSQLiteRecalculationBatchSize
}

func (s *SQLiteStore) sqliteRecalculationEntries(ctx context.Context, tx *sql.Tx, params RecalculatePricingParams, lastID string, limit int) ([]recalculationEntry, error) {
	conditions, args, err := sqliteUsageConditions(params.UsageQueryParams)
	if err != nil {
		return nil, err
	}
	if lastID != "" {
		conditions = append(conditions, "id > ?")
		args = append(args, lastID)
	}
	args = append(args, limit)

	rows, err := tx.QueryContext(ctx, `
		SELECT id, model, provider, provider_name, endpoint, input_tokens, output_tokens, rewrite_tokens_saved, raw_data
		FROM usage`+sqlutil.BuildWhereClause(conditions)+`
		ORDER BY id
		LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("query sqlite usage costs for recalculation: %w", err)
	}
	defer rows.Close()

	entries := make([]recalculationEntry, 0)
	for rows.Next() {
		var entry recalculationEntry
		var providerName sql.NullString
		var rawData sql.NullString
		if err := rows.Scan(
			&entry.ID,
			&entry.Model,
			&entry.Provider,
			&providerName,
			&entry.Endpoint,
			&entry.InputTokens,
			&entry.OutputTokens,
			&entry.RewriteTokensSaved,
			&rawData,
		); err != nil {
			return nil, fmt.Errorf("scan sqlite usage cost row: %w", err)
		}
		if providerName.Valid {
			entry.ProviderName = providerName.String
		}
		if rawData.Valid {
			entry.RawData = rawDataFromJSON(rawData.String, entry.ID)
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sqlite usage costs for recalculation: %w", err)
	}
	return entries, nil
}
