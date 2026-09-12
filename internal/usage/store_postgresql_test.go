package usage

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBuildUsageInsert(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	inputCost := 0.1
	outputCost := 0.2
	totalCost := 0.3
	rewriteCostSaved := 0.05

	query, args := buildUsageInsert([]*UsageEntry{
		{
			ID:                     "usage-1",
			RequestID:              "req-1",
			ProviderID:             "provider-1",
			Timestamp:              now,
			Model:                  "gpt-4o-mini",
			Provider:               "openai",
			ProviderName:           "primary-openai",
			Endpoint:               "/v1/chat/completions",
			SessionID:              "session-1",
			CacheType:              CacheTypeExact,
			Labels:                 []string{"alpha", "prod"},
			InputTokens:            10,
			OutputTokens:           5,
			TotalTokens:            15,
			RewriteTokensSaved:     42,
			RewriteCostSaved:       &rewriteCostSaved,
			RawData:                map[string]any{"cached_tokens": 3},
			InputCost:              &inputCost,
			OutputCost:             &outputCost,
			TotalCost:              &totalCost,
			CostSource:             CostSourceModelPricing,
			CostsCalculationCaveat: "none",
		},
		{
			ID:                     "usage-2",
			RequestID:              "req-2",
			ProviderID:             "provider-2",
			Timestamp:              now.Add(time.Second),
			Model:                  "gpt-4.1",
			Provider:               "openai",
			Endpoint:               "/v1/responses",
			CacheType:              "unexpected-cache-type",
			InputTokens:            20,
			OutputTokens:           8,
			TotalTokens:            28,
			RawData:                nil,
			InputCost:              nil,
			OutputCost:             nil,
			TotalCost:              nil,
			CostsCalculationCaveat: "missing pricing for tool tokens",
		},
	})

	normalized := strings.Join(strings.Fields(query), " ")
	wantQuery := "INSERT INTO usage (id, request_id, provider_id, timestamp, model, provider, provider_name, endpoint, user_path, session_id, cache_type, labels, input_tokens, output_tokens, total_tokens, rewrite_tokens_saved, rewrite_cost_saved, raw_data, input_cost, output_cost, total_cost, cost_source, costs_calculation_caveat) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23), ($24, $25, $26, $27, $28, $29, $30, $31, $32, $33, $34, $35, $36, $37, $38, $39, $40, $41, $42, $43, $44, $45, $46) ON CONFLICT (id) DO NOTHING"
	require.Equal(t, wantQuery, normalized)

	require.Len(t, args, 46)
	require.Equal(t, "usage-1", args[0])
	require.Equal(t, "primary-openai", args[6])
	require.Equal(t, CostSourceModelPricing, args[21])
	require.Equal(t, "usage-2", args[23])
	require.Equal(t, "session-1", args[9])
	require.Equal(t, CacheTypeExact, args[10])
	require.Equal(t, `["alpha","prod"]`, args[11])
	require.Equal(t, 42, args[15])
	require.Equal(t, &rewriteCostSaved, args[16])
	require.JSONEq(t, `{"cached_tokens":3}`, string(args[17].([]byte)))
	require.Nil(t, args[33])
	require.Nil(t, args[34])
	require.Equal(t, 0, args[38])
	require.Nil(t, args[39].(*float64), "rewrite_cost_saved")
	rawData, ok := args[40].([]byte)
	require.True(t, ok, "args[40] has type %T, want []byte", args[40])
	require.Nil(t, rawData)
}

func TestUsageInsertMaxRowsPerQueryRespectsPostgresLimit(t *testing.T) {
	got := usageInsertMaxRowsPerQuery * usageInsertColumnCount
	require.LessOrEqual(t, got, postgresMaxBindParameters)
}
