package gemini

import (
	"context"
	"io"
	"testing"

	"github.com/goccy/go-json"
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
		name:   "itemised thinking larger than the excess moves only the excess",
		usage:  `{"prompt_tokens":27,"completion_tokens":45,"total_tokens":172,"completion_tokens_details":{"reasoning_tokens":504}}`,
		output: 145, total: 172, reasoning: 504, cost: 370.6, reasoningRateCost: 874.6,
	},
	{
		// An explicit zero says the excess is something other than thinking.
		name:   "zero thinking itemised in completion_tokens_details",
		usage:  `{"prompt_tokens":27,"completion_tokens":45,"total_tokens":100,"completion_tokens_details":{"reasoning_tokens":0}}`,
		output: 45, total: 100, cost: 120.6, reasoningRateCost: 120.6, unchanged: true,
	},
	{
		name:   "zero thinking itemised at the top level",
		usage:  `{"prompt_tokens":27,"completion_tokens":45,"reasoning_tokens":0,"total_tokens":100}`,
		output: 45, total: 100, cost: 120.6, reasoningRateCost: 120.6, unchanged: true,
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

func compatChatBody(usage string) string {
	return `{"id":"c-1","object":"chat.completion","model":"gemini-2.5-flash",` +
		`"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"Hi"}}],"usage":` + usage + `}`
}

func compatStreamBody(usage string) string {
	return `data: {"id":"c-1","object":"chat.completion.chunk","model":"gemini-2.5-flash",` +
		`"choices":[{"index":0,"delta":{"role":"assistant","content":"Hi"},"finish_reason":"stop"}],"usage":` + usage + "}\n\n" +
		"data: [DONE]\n\n"
}

func compatChatCompletion(t *testing.T, body string) *core.ChatResponse {
	t.Helper()
	server, _ := providertest.JSONServer(t, 200, body)
	resp, err := newCompatUsageProvider(t, server.URL).ChatCompletion(context.Background(), compatUsageRequest())
	require.NoError(t, err)
	return resp
}

func compatStreamChatCompletion(t *testing.T, body string) string {
	t.Helper()
	server, _ := providertest.SSEServer(t, body)
	stream, err := newCompatUsageProvider(t, server.URL).StreamChatCompletion(context.Background(), compatUsageRequest())
	require.NoError(t, err)
	defer func() { _ = stream.Close() }()
	raw, err := io.ReadAll(stream)
	require.NoError(t, err)
	return string(raw)
}

func TestChatCompletion_CompatCountsThinkingTokens(t *testing.T) {
	for _, tc := range compatUsageCases {
		t.Run(tc.name, func(t *testing.T) {
			resp := compatChatCompletion(t, compatChatBody(tc.usage))

			assert.Equal(t, tc.output, resp.Usage.CompletionTokens, "the client sees thinking in completion_tokens")
			for _, reasoningRate := range []bool{false, true} {
				entry := usage.ExtractFromChatResponse(resp, "req-1", "gemini", compatChatPath, compatUsagePricing(reasoningRate))
				assertCompatUsageEntry(t, tc.output, tc.total, tc.reasoning, wantCompatCost(tc.cost, tc.reasoningRateCost, reasoningRate), entry)
			}

			// The adapter's own output, read by the adapter again, as when
			// one gateway is the upstream of another.
			once, err := json.Marshal(resp)
			require.NoError(t, err)
			twice, err := json.Marshal(compatChatCompletion(t, string(once)).Usage)
			require.NoError(t, err)
			folded, err := json.Marshal(resp.Usage)
			require.NoError(t, err)
			assert.JSONEq(t, string(folded), string(twice), "folding twice changes nothing")
		})
	}
}

func TestStreamChatCompletion_CompatCountsThinkingTokens(t *testing.T) {
	for _, tc := range compatUsageCases {
		t.Run(tc.name, func(t *testing.T) {
			body := compatStreamBody(tc.usage)
			raw := compatStreamChatCompletion(t, body)

			if tc.unchanged {
				assert.Equal(t, body, raw, "a usage chunk with nothing to fold is forwarded byte for byte")
			}
			chunks := parseOpenAIStreamChunks(t, raw)
			require.Len(t, chunks, 1)
			assert.Equal(t, "Hi", chunks[0]["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)["content"])
			for _, reasoningRate := range []bool{false, true} {
				// Reads the body with the observer that meters live streams.
				entry := usage.ExtractFromCachedResponseBody([]byte(raw), "req-1", "gemini-2.5-flash", "gemini", compatChatPath, "", compatUsagePricing(reasoningRate))
				assertCompatUsageEntry(t, tc.output, tc.total, tc.reasoning, wantCompatCost(tc.cost, tc.reasoningRateCost, reasoningRate), entry)
			}
			assert.Equal(t, raw, compatStreamChatCompletion(t, raw), "folding twice changes nothing")
		})
	}
}

// Responses run through the chat path, so their usage is folded too and
// reported under the Responses names.
func TestResponses_CompatCountsThinkingTokens(t *testing.T) {
	server, _ := providertest.JSONServer(t, 200, compatChatBody(`{"completion_tokens":4,"prompt_tokens":11,"total_tokens":33}`))

	resp, err := newCompatUsageProvider(t, server.URL).Responses(context.Background(), &core.ResponsesRequest{Model: "gemini-2.5-flash", Input: "hi"})
	require.NoError(t, err)
	require.NotNil(t, resp.Usage)

	encoded, err := json.Marshal(resp.Usage)
	require.NoError(t, err)
	assert.JSONEq(t, `{"input_tokens":11,"output_tokens":22,"total_tokens":33,"output_tokens_details":`+
		`{"reasoning_tokens":18,"audio_tokens":0,"accepted_prediction_tokens":0,"rejected_prediction_tokens":0}}`, string(encoded))
}

func TestFoldCompatThinkingTokens_LeavesUnexplainedCountsAlone(t *testing.T) {
	negative := -5
	tests := []struct {
		name     string
		usage    core.Usage
		itemised *int
	}{
		{name: "total not reported", usage: core.Usage{PromptTokens: 44, CompletionTokens: 19}},
		{name: "total below prompt plus completion", usage: core.Usage{PromptTokens: 44, CompletionTokens: 19, TotalTokens: 50}},
		{name: "negative itemised count", usage: core.Usage{PromptTokens: 11, CompletionTokens: 4, TotalTokens: 33}, itemised: &negative},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.usage
			assert.False(t, foldCompatThinkingTokens(&got, tt.itemised))
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
