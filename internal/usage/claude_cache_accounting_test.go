package usage

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/enterpilot/gomodel/internal/core"
)

// One Claude request (200k uncached prompt tokens, 100k cache reads, 50k
// cache writes) as each row shape records it. Rows recorded before
// input_tokens included the cache, and native Anthropic rows, carry the
// Anthropic-named counts on top of input_tokens; translated rows now carry
// OpenAI-named counts inside it.
var claudeCacheRowShapes = []struct {
	name     string
	provider string
	input    int
	raw      map[string]any
}{
	{"anthropic, Anthropic names", "anthropic", 200_000, map[string]any{
		"cache_read_input_tokens": 100_000, "cache_creation_input_tokens": 50_000}},
	{"anthropic, older Responses row with both names", "anthropic", 200_000, map[string]any{
		"cache_read_input_tokens": 100_000, "prompt_cached_tokens": 100_000, "cache_creation_input_tokens": 50_000}},
	{"anthropic, OpenAI names", "anthropic", 350_000, map[string]any{
		"prompt_cached_tokens": 100_000, "prompt_cache_write_tokens": 50_000}},
	{"bedrock, Anthropic names", "bedrock", 200_000, map[string]any{
		"cache_read_input_tokens": 100_000, "cache_creation_input_tokens": 50_000, "cache_write_input_tokens": 50_000}},
	{"bedrock, OpenAI names", "bedrock", 350_000, map[string]any{
		"prompt_cached_tokens": 100_000, "prompt_cache_write_tokens": 50_000}},
}

func TestClaudeCacheRowShapes_PriceTheSame(t *testing.T) {
	pricing := &core.ModelPricing{
		InputPerMtok:       new(3.0),
		OutputPerMtok:      new(15.0),
		CachedInputPerMtok: new(0.30),
		CacheWritePerMtok:  new(3.75),
	}
	for _, shape := range claudeCacheRowShapes {
		t.Run(shape.name, func(t *testing.T) {
			result := CalculateGranularCost(shape.input, 100_000, shape.raw, shape.provider, pricing)

			// 200k * 3.0/1M + 100k * 0.30/1M + 50k * 3.75/1M
			assertCostNear(t, "InputCost", result.InputCost, 0.8175)
			assert.Empty(t, result.Caveat)
		})
	}
}

func TestClaudeCacheRowShapes_SplitTheSame(t *testing.T) {
	for _, shape := range claudeCacheRowShapes {
		t.Run(shape.name, func(t *testing.T) {
			uncached, cached, cacheWrite := EntryInputSegments(UsageLogEntry{
				Provider: shape.provider, InputTokens: shape.input, RawData: shape.raw,
			})

			assert.Equal(t, []int64{200_000, 100_000, 50_000}, []int64{uncached, cached, cacheWrite})
		})
	}
}

func TestEntryInputSegments_OpenAINamesInsideInput(t *testing.T) {
	tests := []struct {
		name  string
		input int
		raw   map[string]any
		want  []int64
	}{
		{"cache write is part of the prompt", 120, map[string]any{"prompt_cached_tokens": 80, "prompt_cache_write_tokens": 30}, []int64{10, 80, 30}},
		// A Claude Responses body cached before input_tokens included the
		// cache reports more cache reads than input; they come on top of it.
		{"cache count larger than input", 20, map[string]any{"prompt_cached_tokens": 100}, []int64{20, 100, 0}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			uncached, cached, cacheWrite := EntryInputSegments(UsageLogEntry{
				Provider: "anthropic", InputTokens: tt.input, RawData: tt.raw,
			})

			assert.Equal(t, tt.want, []int64{uncached, cached, cacheWrite})
		})
	}
}
