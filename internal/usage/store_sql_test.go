package usage

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

func TestBuildUsageInsert(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	inputCost := 0.1
	outputCost := 0.2
	totalCost := 0.3
	rewriteCostSaved := 0.05

	query, args := buildUsageInsert(sqlx.PostgreSQL, []*UsageEntry{
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
	wantQuery := "INSERT INTO usage (id, request_id, provider_id, timestamp, model, provider, provider_name, endpoint, user_path, session_id, cache_type, labels, input_tokens, output_tokens, total_tokens, rewrite_tokens_saved, rewrite_cost_saved, raw_data, input_cost, output_cost, total_cost, cost_source, costs_calculation_caveat) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?), (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (id) DO NOTHING"
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
	require.Equal(t, now, args[3])
	require.JSONEq(t, `{"cached_tokens":3}`, args[17].(string))
	require.Nil(t, args[33])
	require.Nil(t, args[34])
	require.Equal(t, 0, args[38])
	require.Nil(t, args[39].(*float64), "rewrite_cost_saved")
	require.Nil(t, args[40], "raw_data")
}

func TestBuildUsageInsertWritesSQLiteTimestampsAsRFC3339Text(t *testing.T) {
	at := time.Date(2026, 1, 16, 12, 0, 0, 123, time.FixedZone("CET", 3600))
	_, args := buildUsageInsert(sqlx.SQLite, []*UsageEntry{{ID: "usage-1", Timestamp: at}})
	require.Equal(t, "2026-01-16T11:00:00.000000123Z", args[3])
}

func TestPostgreSQLSessionUsageQueriesArePagedAndExcludeCachedCost(t *testing.T) {
	countQuery, dataQuery, args, dataArgs, limit, offset, err := usageDialectFor(sqlx.PostgreSQL).sessionUsageQueries(SessionUsageParams{
		SessionID: "scoped-session", CacheMode: CacheModeUncached,
		Limit:  25,
		Offset: 10,
	})
	require.NoError(t, err)
	require.Equal(t, 25, limit)
	require.Equal(t, 10, offset)
	require.Contains(t, countQuery, "GROUP BY session_id")
	require.NotContains(t, countQuery, "cache_type")

	for _, fragment := range []string{
		"session_id = ?",
		"COUNT(CASE WHEN (cache_type IS NULL OR cache_type = '') THEN 1 END)",
		"ORDER BY MAX(timestamp) DESC, session_id ASC, user_path ASC",
		"LIMIT ? OFFSET ?",
	} {
		require.Contains(t, dataQuery, fragment)
	}
	require.Equal(t, []any{"scoped-session"}, args)
	require.Equal(t, []any{"scoped-session", 25, 10}, dataArgs)
}
