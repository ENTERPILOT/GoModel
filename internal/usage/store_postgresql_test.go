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
	markupPercent := 20.0
	billedCost := 0.36

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
			AuthKeyID:              "key-1",
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
			MarkupPercent:          &markupPercent,
			MarkupSelector:         "/",
			BilledCost:             &billedCost,
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
	wantQuery := "INSERT INTO usage (id, request_id, provider_id, timestamp, model, provider, provider_name, endpoint, user_path, session_id, cache_type, labels, input_tokens, output_tokens, total_tokens, rewrite_tokens_saved, rewrite_cost_saved, raw_data, input_cost, output_cost, total_cost, cost_source, costs_calculation_caveat, auth_key_id, markup_percent, markup_selector, billed_cost) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25, $26, $27), ($28, $29, $30, $31, $32, $33, $34, $35, $36, $37, $38, $39, $40, $41, $42, $43, $44, $45, $46, $47, $48, $49, $50, $51, $52, $53, $54) ON CONFLICT (id) DO NOTHING"
	require.Equal(t, wantQuery, normalized)

	require.Len(t, args, 54)
	require.Equal(t, "usage-1", args[0])
	require.Equal(t, "primary-openai", args[6])
	require.Equal(t, CostSourceModelPricing, args[21])
	require.Equal(t, "key-1", args[23])
	require.Equal(t, &markupPercent, args[24])
	require.Equal(t, "/", args[25])
	require.Equal(t, &billedCost, args[26])
	require.Equal(t, "usage-2", args[27])
	require.Equal(t, "session-1", args[9])
	require.Equal(t, CacheTypeExact, args[10])
	require.Equal(t, `["alpha","prod"]`, args[11])
	require.Equal(t, 42, args[15])
	require.Equal(t, &rewriteCostSaved, args[16])
	require.JSONEq(t, `{"cached_tokens":3}`, string(args[17].([]byte)))
	require.Nil(t, args[37])
	require.Nil(t, args[38])
	require.Equal(t, 0, args[42])
	require.Nil(t, args[43].(*float64), "rewrite_cost_saved")
	rawData, ok := args[44].([]byte)
	require.True(t, ok, "args[44] has type %T, want []byte", args[44])
	require.Empty(t, args[50], "auth_key_id")
	require.Nil(t, args[51].(*float64), "markup_percent")
	require.Nil(t, args[53].(*float64), "billed_cost")
	require.Nil(t, rawData)
}

func TestUsageInsertMaxRowsPerQueryRespectsPostgresLimit(t *testing.T) {
	got := usageInsertMaxRowsPerQuery * usageInsertColumnCount
	require.LessOrEqual(t, got, postgresMaxBindParameters)
}
