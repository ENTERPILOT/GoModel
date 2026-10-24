package usage

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

// gpt6LunaPricing is gpt-6-luna as the catalog prices it for chat.
func gpt6LunaPricing() *core.ModelPricing {
	return &core.ModelPricing{
		Currency:           "USD",
		InputPerMtok:       new(0.10),
		OutputPerMtok:      new(0.50),
		CachedInputPerMtok: new(0.01),
		CacheWritePerMtok:  new(0.125),
		Tiers: []core.ModelPricingTier{
			{UpToTokens: new(272000.0), InputPerMtok: new(0.10), OutputPerMtok: new(0.50)},
			{UpToTokens: new(922000.0), InputPerMtok: new(0.20), OutputPerMtok: new(0.75)},
		},
	}
}

// A Decisions call is billed for its input tokens only, every one at the
// input rate: OpenAI charges no output, cache-read, or cache-write tokens on
// /v1/decisions, though the same model is priced for chat. Long-context
// tiers still set the input rate.
func TestDecisionsPricing_BillsInputTokensOnly(t *testing.T) {
	tests := []struct {
		name      string
		input     int
		wantInput float64
	}{
		{name: "base tier", input: 100000, wantInput: 0.01},
		{name: "long-context tier", input: 500000, wantInput: 0.10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(`{"answers":[],"model":"gpt-6-luna","usage":{"input_tokens":` + strconv.Itoa(tt.input) +
				`,"input_tokens_details":{"cache_write_tokens":20000,"cached_tokens":50000},"output_tokens":100000,` +
				`"output_tokens_details":{"reasoning_tokens":40000},"total_tokens":` + strconv.Itoa(tt.input+100000) + `}}`)

			entry := extractFromCachedJSONBody(body, "req-1", "gpt-6-luna", "openai", "/v1/decisions", gpt6LunaPricing())
			require.NotNil(t, entry)
			require.NotNil(t, entry.InputCost)
			require.NotNil(t, entry.OutputCost)
			assert.InDelta(t, tt.wantInput, *entry.InputCost, 1e-9)
			assert.InDelta(t, 0, *entry.OutputCost, 1e-12)
		})
	}
}

// Other endpoints keep the model's own pricing.
func TestDecisionsPricing_OnlyAppliesToDecisions(t *testing.T) {
	body := []byte(`{"model":"gpt-6-luna","usage":{"input_tokens":100000,"output_tokens":100000}}`)
	entry := extractFromCachedJSONBody(body, "req-1", "gpt-6-luna", "openai", "/v1/responses", gpt6LunaPricing())
	require.NotNil(t, entry)
	require.NotNil(t, entry.OutputCost)
	assert.InDelta(t, 0.05, *entry.OutputCost, 1e-9)
}
