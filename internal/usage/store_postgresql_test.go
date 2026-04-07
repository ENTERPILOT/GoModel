package usage

import (
	"strings"
	"testing"
	"time"
)

func TestBuildUsageInsert(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	inputCost := 0.1
	outputCost := 0.2
	totalCost := 0.3

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
			CacheType:              CacheTypeExact,
			InputTokens:            10,
			OutputTokens:           5,
			TotalTokens:            15,
			RawData:                map[string]any{"cached_tokens": 3},
			InputCost:              &inputCost,
			OutputCost:             &outputCost,
			TotalCost:              &totalCost,
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
	wantQuery := "INSERT INTO usage (id, request_id, provider_id, timestamp, model, provider, provider_name, endpoint, user_path, cache_type, input_tokens, output_tokens, total_tokens, raw_data, input_cost, output_cost, total_cost, costs_calculation_caveat) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18), ($19, $20, $21, $22, $23, $24, $25, $26, $27, $28, $29, $30, $31, $32, $33, $34, $35, $36) ON CONFLICT (id) DO NOTHING"
	if normalized != wantQuery {
		t.Fatalf("query = %q, want %q", normalized, wantQuery)
	}

	if got, want := len(args), 36; got != want {
		t.Fatalf("len(args) = %d, want %d", got, want)
	}
	if got := args[0]; got != "usage-1" {
		t.Fatalf("args[0] = %v, want usage-1", got)
	}
	if got := args[6]; got != "primary-openai" {
		t.Fatalf("args[6] = %v, want primary-openai", got)
	}
	if got := args[18]; got != "usage-2" {
		t.Fatalf("args[18] = %v, want usage-2", got)
	}
	if got := args[9]; got != CacheTypeExact {
		t.Fatalf("args[9] = %v, want %q", got, CacheTypeExact)
	}
	if got := string(args[13].([]byte)); got != `{"cached_tokens":3}` {
		t.Fatalf("args[13] = %q, want %q", got, `{"cached_tokens":3}`)
	}
	if got := args[27]; got != nil {
		t.Fatalf("args[27] = %v, want nil cache_type", got)
	}
	rawData, ok := args[31].([]byte)
	if !ok {
		t.Fatalf("args[31] has type %T, want []byte", args[31])
	}
	if rawData != nil {
		t.Fatalf("args[31] = %v, want nil raw_data", rawData)
	}
}

func TestUsageInsertMaxRowsPerQueryRespectsPostgresLimit(t *testing.T) {
	if got := usageInsertMaxRowsPerQuery * usageInsertColumnCount; got > postgresMaxBindParameters {
		t.Fatalf("bind parameters = %d, want <= %d", got, postgresMaxBindParameters)
	}
}
