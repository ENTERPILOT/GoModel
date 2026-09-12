package usage

import (
	"testing"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

// A Responses body replayed from the cache carries cache reads in the OpenAI
// shape; the cache-hit entry must still price them at the cached-input rate.
func TestExtractFromCachedResponseBody_AnthropicResponsesCachedTokens(t *testing.T) {
	cachedRate := 0.30
	inputRate := 3.0
	outputRate := 15.0

	body, err := json.Marshal(&core.ResponsesResponse{
		ID:     "resp_cache",
		Object: "response",
		Model:  "claude-haiku-4-5",
		Status: "completed",
		Usage: &core.ResponsesUsage{
			InputTokens:         20,
			OutputTokens:        10,
			TotalTokens:         30,
			PromptTokensDetails: &core.PromptTokensDetails{CachedTokens: 100},
		},
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	entry := ExtractFromCachedResponseBody(body, "req-cache", "claude-haiku-4-5", "anthropic", "/v1/responses", CacheTypeExact, &core.ModelPricing{
		InputPerMtok:       &inputRate,
		OutputPerMtok:      &outputRate,
		CachedInputPerMtok: &cachedRate,
	})
	if entry == nil {
		t.Fatal("expected non-nil entry")
	}
	if entry.RawData["prompt_cached_tokens"] != 100 {
		t.Fatalf("RawData = %+v, want prompt_cached_tokens 100", entry.RawData)
	}
	// 20 input at 3/Mtok + 100 cache reads at 0.30/Mtok.
	wantInput := 20*inputRate/1_000_000 + 100*cachedRate/1_000_000
	if entry.InputCost == nil || *entry.InputCost != wantInput {
		t.Fatalf("InputCost = %v, want %v", entry.InputCost, wantInput)
	}
}
