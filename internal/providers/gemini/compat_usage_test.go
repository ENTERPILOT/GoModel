package gemini

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/enterpilot/gomodel/internal/usage"
)

const compatChatPath = "/v1/chat/completions"

// compatUsageCases are usage objects of Google's OpenAI-compatible endpoint.
// Costs are in millionths of a dollar at $0.30 input and $2.50 output per
// million tokens; reasoningRateCost adds a $3.50 reasoning rate.
var compatUsageCases = []struct {
	name              string
	usage             string
	output            int
	total             int
	reasoning         int
	cost              float64
	reasoningRateCost float64
	unchanged         bool
}{
	{
		// Recorded from gemini-2.5-flash: thinking is in the total only.
		name:   "thinking not itemised",
		usage:  `{"completion_tokens":4,"prompt_tokens":11,"total_tokens":33}`,
		output: 22, total: 33, reasoning: 18, cost: 58.3, reasoningRateCost: 76.3,
	},
	{
		// The shape Google documents, reasoning beside completion tokens.
		name:   "thinking itemised in completion_tokens_details",
		usage:  `{"prompt_tokens":27,"completion_tokens":45,"total_tokens":576,"completion_tokens_details":{"reasoning_tokens":504}}`,
		output: 549, total: 576, reasoning: 504, cost: 1380.6, reasoningRateCost: 1884.6,
	},
	{
		name:   "thinking itemised at the top level",
		usage:  `{"prompt_tokens":27,"completion_tokens":45,"reasoning_tokens":504,"total_tokens":576}`,
		output: 549, total: 576, reasoning: 504, cost: 1380.6, reasoningRateCost: 1884.6,
	},
	{
		name:   "itemised thinking bounds a total inflated by something else",
		usage:  `{"prompt_tokens":27,"completion_tokens":45,"total_tokens":600,"completion_tokens_details":{"reasoning_tokens":504}}`,
		output: 549, total: 600, reasoning: 504, cost: 1380.6, reasoningRateCost: 1884.6,
	},
	{
		name:   "itemised thinking already inside completion tokens",
		usage:  `{"prompt_tokens":27,"completion_tokens":549,"total_tokens":576,"completion_tokens_details":{"reasoning_tokens":504}}`,
		output: 549, total: 576, reasoning: 504, cost: 1380.6, reasoningRateCost: 1884.6, unchanged: true,
	},
	{
		// Recorded tool-call turn (chat_with_tools.json): nothing to fold.
		name:   "total explained by prompt and completion",
		usage:  `{"completion_tokens":19,"prompt_tokens":44,"total_tokens":63}`,
		output: 19, total: 63, cost: 60.7, reasoningRateCost: 60.7, unchanged: true,
	},
}

func compatUsagePricing(reasoningRate bool) *core.ModelPricing {
	input, output, reasoning := 0.30, 2.50, 3.50
	pricing := &core.ModelPricing{Currency: "USD", InputPerMtok: &input, OutputPerMtok: &output}
	if reasoningRate {
		pricing.ReasoningOutputPerMtok = &reasoning
	}
	return pricing
}

func newCompatUsageProvider(t *testing.T, baseURL string) *Provider {
	t.Helper()
	t.Setenv(useNativeAPIEnvVar, "false")
	return New(providers.ProviderConfig{APIKey: "key", BaseURL: baseURL}, providertest.Options(llmclient.Hooks{})).(*Provider)
}

func compatUsageRequest() *core.ChatRequest {
	return &core.ChatRequest{Model: "gemini-2.5-flash", Messages: []core.Message{{Role: "user", Content: "hi"}}}
}

func TestChatCompletion_CompatCountsThinkingTokens(t *testing.T) {
	for _, tc := range compatUsageCases {
		t.Run(tc.name, func(t *testing.T) {
			server, _ := providertest.JSONServer(t, 200, `{"id":"c-1","object":"chat.completion","model":"gemini-2.5-flash",`+
				`"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"Hi"}}],"usage":`+tc.usage+`}`)

			resp, err := newCompatUsageProvider(t, server.URL).ChatCompletion(context.Background(), compatUsageRequest())
			require.NoError(t, err)

			assert.Equal(t, tc.output, resp.Usage.CompletionTokens, "the client sees thinking in completion_tokens")
			for _, reasoningRate := range []bool{false, true} {
				entry := usage.ExtractFromChatResponse(resp, "req-1", "gemini", compatChatPath, compatUsagePricing(reasoningRate))
				assertCompatUsageEntry(t, tc.output, tc.total, tc.reasoning, wantCompatCost(tc.cost, tc.reasoningRateCost, reasoningRate), entry)
			}
		})
	}
}

func TestStreamChatCompletion_CompatCountsThinkingTokens(t *testing.T) {
	for _, tc := range compatUsageCases {
		t.Run(tc.name, func(t *testing.T) {
			body := `data: {"id":"c-1","object":"chat.completion.chunk","model":"gemini-2.5-flash",` +
				`"choices":[{"index":0,"delta":{"role":"assistant","content":"Hi"},"finish_reason":"stop"}],"usage":` + tc.usage + "}\n\n" +
				"data: [DONE]\n\n"
			server, _ := providertest.SSEServer(t, body)

			stream, err := newCompatUsageProvider(t, server.URL).StreamChatCompletion(context.Background(), compatUsageRequest())
			require.NoError(t, err)
			defer func() { _ = stream.Close() }()
			raw, err := io.ReadAll(stream)
			require.NoError(t, err)

			if tc.unchanged {
				assert.Equal(t, body, string(raw), "a usage chunk with nothing to fold is forwarded byte for byte")
			}
			chunks := parseOpenAIStreamChunks(t, string(raw))
			require.Len(t, chunks, 1)
			assert.Equal(t, "Hi", chunks[0]["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)["content"])
			for _, reasoningRate := range []bool{false, true} {
				// Reads the body with the observer that meters live streams.
				entry := usage.ExtractFromCachedResponseBody(raw, "req-1", "gemini-2.5-flash", "gemini", compatChatPath, "", compatUsagePricing(reasoningRate))
				assertCompatUsageEntry(t, tc.output, tc.total, tc.reasoning, wantCompatCost(tc.cost, tc.reasoningRateCost, reasoningRate), entry)
			}
		})
	}
}

func TestFoldCompatThinkingTokens_LeavesExplainedTotalsAlone(t *testing.T) {
	folded := core.Usage{PromptTokens: 11, CompletionTokens: 4, TotalTokens: 33}
	require.True(t, foldCompatThinkingTokens(&folded))

	tests := []struct {
		name  string
		usage core.Usage
	}{
		{name: "already folded", usage: folded},
		{name: "total not reported", usage: core.Usage{PromptTokens: 44, CompletionTokens: 19}},
		{name: "total below prompt plus completion", usage: core.Usage{PromptTokens: 44, CompletionTokens: 19, TotalTokens: 50}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.usage
			assert.False(t, foldCompatThinkingTokens(&got))
			assert.Equal(t, tt.usage, got)
		})
	}
}

func wantCompatCost(outputRateCost, reasoningRateCost float64, reasoningRate bool) float64 {
	if reasoningRate {
		return reasoningRateCost
	}
	return outputRateCost
}

func assertCompatUsageEntry(t *testing.T, output, total, reasoning int, microUSD float64, entry *usage.UsageEntry) {
	t.Helper()
	require.NotNil(t, entry)
	assert.Equal(t, output, entry.OutputTokens, "output tokens")
	assert.Equal(t, total, entry.TotalTokens, "total tokens")
	assert.GreaterOrEqual(t, entry.TotalTokens, entry.InputTokens+entry.OutputTokens, "no token is counted twice")
	if reasoning == 0 {
		assert.NotContains(t, entry.RawData, "completion_reasoning_tokens")
	} else {
		assert.Equal(t, reasoning, entry.RawData["completion_reasoning_tokens"], "reasoning tokens")
	}
	require.NotNil(t, entry.TotalCost)
	assert.InDelta(t, microUSD, *entry.TotalCost*1e6, 1e-6, "cost in millionths of a dollar")
}
