package usage

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/require"
)

type staticTestPricingResolver map[string]*core.ModelPricing

func (r staticTestPricingResolver) ResolvePricing(model, providerType string) *core.ModelPricing {
	return r[providerType+"/"+model]
}

func TestSQLiteStoreRecalculatePricingUpdatesFilteredUsageCosts(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)

	defer db.Close()

	store, err := NewSQLiteStore(db, 0)
	require.NoError(t, err)

	oldCost := 99.0
	ctx := context.Background()
	err = store.WriteBatch(ctx, []*UsageEntry{
		{
			ID:           "usage-match",
			RequestID:    "req-match",
			ProviderID:   "provider-match",
			Timestamp:    time.Date(2026, 4, 12, 10, 0, 0, 0, time.UTC),
			Model:        "gpt-4o",
			Provider:     "openai",
			ProviderName: "primary-openai",
			Endpoint:     "/v1/chat/completions",
			UserPath:     "/team/alpha",
			InputTokens:  1_000_000,
			OutputTokens: 500_000,
			TotalTokens:  1_500_000,
			InputCost:    &oldCost,
			OutputCost:   &oldCost,
			TotalCost:    &oldCost,
		},
		{
			ID:          "usage-other-model",
			RequestID:   "req-other",
			ProviderID:  "provider-other",
			Timestamp:   time.Date(2026, 4, 12, 11, 0, 0, 0, time.UTC),
			Model:       "gpt-4o-mini",
			Provider:    "openai",
			Endpoint:    "/v1/chat/completions",
			UserPath:    "/team/alpha",
			InputTokens: 1_000_000,
			TotalTokens: 1_000_000,
			TotalCost:   &oldCost,
		},
	})
	require.NoError(t, err)

	inputRate := 2.0
	outputRate := 6.0
	result, err := store.RecalculatePricing(ctx, RecalculatePricingParams{
		StartDate: time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC),
		EndDate:   time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC),
		UserPath:  "/team",
		Provider:  "primary-openai",
		Model:     "gpt-4o",
	}, staticTestPricingResolver{
		"primary-openai/gpt-4o": {
			InputPerMtok:  &inputRate,
			OutputPerMtok: &outputRate,
		},
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), result.Matched)
	require.Equal(t, int64(1), result.Recalculated)
	require.Equal(t, int64(1), result.WithPricing)
	require.Equal(t, int64(0), result.WithoutPricing, "result = %+v, want one recalculated row with pricing", result)

	var inputCost, outputCost, totalCost float64
	err = db.QueryRow(`SELECT input_cost, output_cost, total_cost FROM usage WHERE id = 'usage-match'`).Scan(&inputCost, &outputCost, &totalCost)
	require.NoError(t, err)
	require.Equal(t, 2.0, inputCost)
	require.Equal(t, 3.0, outputCost)
	require.Equal(t, 5.0, totalCost)

	var otherTotal float64
	err = db.QueryRow(`SELECT total_cost FROM usage WHERE id = 'usage-other-model'`).Scan(&otherTotal)
	require.NoError(t, err)
	require.Equal(t, oldCost, otherTotal)
}

func TestSQLiteStoreRecalculatePricingFiltersByLabel(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)

	defer db.Close()

	store, err := NewSQLiteStore(db, 0)
	require.NoError(t, err)

	oldCost := 99.0
	ctx := context.Background()
	entry := func(id string, labels []string) *UsageEntry {
		return &UsageEntry{
			ID:          id,
			RequestID:   "req-" + id,
			ProviderID:  "provider-" + id,
			Timestamp:   time.Date(2026, 4, 12, 10, 0, 0, 0, time.UTC),
			Model:       "gpt-4o",
			Provider:    "openai",
			Endpoint:    "/v1/chat/completions",
			Labels:      labels,
			InputTokens: 1_000_000,
			TotalTokens: 1_000_000,
			InputCost:   &oldCost,
			TotalCost:   &oldCost,
		}
	}
	err = store.WriteBatch(ctx, []*UsageEntry{
		entry("usage-labelled", []string{"env:prod", "batch"}),
		entry("usage-other-label", []string{"env:staging"}),
		entry("usage-unlabelled", nil),
	})
	require.NoError(t, err)

	inputRate := 2.0
	// The padded label exercises normalizedRecalculatePricingParams trimming.
	result, err := store.RecalculatePricing(ctx, RecalculatePricingParams{
		Label: " env:prod ",
	}, staticTestPricingResolver{
		"openai/gpt-4o": {InputPerMtok: &inputRate},
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), result.Matched)
	require.Equal(t, int64(1), result.Recalculated, "result = %+v, want exactly the labelled row recalculated", result)

	var labelledCost, otherCost, unlabelledCost float64
	err = db.QueryRow(`SELECT total_cost FROM usage WHERE id = 'usage-labelled'`).Scan(&labelledCost)
	require.NoError(t, err)
	require.Equal(t, 2.0, labelledCost)
	err = db.QueryRow(`SELECT total_cost FROM usage WHERE id = 'usage-other-label'`).Scan(&otherCost)
	require.NoError(t, err)
	err = db.QueryRow(`SELECT total_cost FROM usage WHERE id = 'usage-unlabelled'`).Scan(&unlabelledCost)
	require.NoError(t, err)
	require.Equal(t, oldCost, otherCost)
	require.Equal(t, oldCost, unlabelledCost)
}

func TestSQLiteStoreRecalculatePricingProcessesBatches(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)

	defer db.Close()

	store, err := NewSQLiteStore(db, 0)
	require.NoError(t, err)

	store.recalculationBatchSize = 1

	ctx := context.Background()
	err = store.WriteBatch(ctx, []*UsageEntry{
		{
			ID:          "usage-1",
			RequestID:   "req-1",
			ProviderID:  "provider-1",
			Timestamp:   time.Date(2026, 4, 12, 10, 0, 0, 0, time.UTC),
			Model:       "gpt-4o",
			Provider:    "openai",
			Endpoint:    "/v1/chat/completions",
			InputTokens: 1_000_000,
		},
		{
			ID:           "usage-2",
			RequestID:    "req-2",
			ProviderID:   "provider-2",
			Timestamp:    time.Date(2026, 4, 12, 11, 0, 0, 0, time.UTC),
			Model:        "gpt-4o",
			Provider:     "openai",
			ProviderName: "primary-openai",
			Endpoint:     "/v1/chat/completions",
			InputTokens:  2_000_000,
		},
	})
	require.NoError(t, err)

	inputRate := 2.0
	result, err := store.RecalculatePricing(ctx, RecalculatePricingParams{
		Model: "gpt-4o",
	}, staticTestPricingResolver{
		"openai/gpt-4o": {
			InputPerMtok: &inputRate,
		},
		"primary-openai/gpt-4o": {
			InputPerMtok: &inputRate,
		},
	})
	require.NoError(t, err)
	require.Equal(t, int64(2), result.Matched)
	require.Equal(t, int64(2), result.Recalculated)
	require.Equal(t, int64(2), result.WithPricing, "result = %+v, want two recalculated rows with pricing", result)

	rows, err := db.Query(`SELECT id, input_cost FROM usage ORDER BY id`)
	require.NoError(t, err)

	defer rows.Close()

	got := map[string]float64{}
	for rows.Next() {
		var id string
		var inputCost float64
		err := rows.Scan(&id, &inputCost)
		require.NoError(t, err)

		got[id] = inputCost
	}
	err = rows.Err()
	require.NoError(t, err)
	require.Equal(t, 2.0, got["usage-1"])
	require.Equal(t, 4.0, got["usage-2"], "input costs = %+v, want usage-1=2 usage-2=4", got)
}
