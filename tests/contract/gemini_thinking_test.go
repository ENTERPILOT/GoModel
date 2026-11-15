//go:build contract

// Contract replay tests for thinking tokens on Gemini's OpenAI-compatible
// endpoint, which counts them in total_tokens but not in completion_tokens.
// The adapter folds them into the output count, as the native path does, so
// the gateway prices them.
package contract

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/usage"
)

const geminiNativeGeneratePath = "/models/gemini-2.5-flash:generateContent"

func TestGeminiReplayThinkingUsageCost(t *testing.T) {
	// Dollars per million tokens (input, output).
	flash25 := [2]float64{0.30, 2.50}
	flash3 := [2]float64{0.50, 3.00}

	tests := []struct {
		name        string
		fixturePath string
		native      bool
		rates       [2]float64
		input       int
		output      int
		total       int
		reasoning   int
		// Cost in millionths of a dollar, and what the upstream numbers
		// priced as they arrive would have come to.
		cost       float64
		unfoldCost float64
	}{
		{name: "thinking", fixturePath: "gemini/chat_with_thinking.json", rates: flash25, input: 11, output: 22, total: 33, reasoning: 18, cost: 58.3, unfoldCost: 13.3},
		{name: "thinking truncated", fixturePath: "gemini/chat_with_thinking_truncated.json", rates: flash3, input: 11, output: 46, total: 57, reasoning: 46, cost: 143.5, unfoldCost: 5.5},
		{name: "thinking stream", fixturePath: "gemini/chat_with_thinking_stream.txt", rates: flash25, input: 11, output: 25, total: 36, reasoning: 21, cost: 65.8, unfoldCost: 13.3},
		{name: "no thinking", fixturePath: "gemini/chat_with_tools.json", rates: flash25, input: 44, output: 19, total: 63, cost: 60.7, unfoldCost: 60.7},
		{name: "native", fixturePath: "gemini/native_chat_completion.json", native: true, rates: flash25, input: 11, output: 25, total: 36, reasoning: 21, cost: 65.8, unfoldCost: 65.8},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pricing := &core.ModelPricing{Currency: "USD", InputPerMtok: &tt.rates[0], OutputPerMtok: &tt.rates[1]}
			entry := replayGeminiUsageEntry(t, tt.fixturePath, tt.native, pricing)

			require.NotNil(t, entry)
			assert.Equal(t, tt.input, entry.InputTokens)
			assert.Equal(t, tt.output, entry.OutputTokens)
			assert.Equal(t, tt.total, entry.TotalTokens)
			if tt.reasoning == 0 {
				assert.NotContains(t, entry.RawData, "completion_reasoning_tokens")
			} else {
				assert.Equal(t, tt.reasoning, entry.RawData["completion_reasoning_tokens"])
			}
			require.NotNil(t, entry.TotalCost)
			assert.InDelta(t, tt.cost, *entry.TotalCost*1e6, 1e-6)
			assert.GreaterOrEqual(t, tt.cost, tt.unfoldCost, "folding never lowers a cost")
		})
	}
}

// replayGeminiUsageEntry replays a fixture through the adapter and meters the
// result the way the gateway does: a buffered response from the decoded
// response, a stream with the observer that reads its usage chunk.
func replayGeminiUsageEntry(t *testing.T, fixturePath string, native bool, pricing *core.ModelPricing) *usage.UsageEntry {
	t.Helper()
	const model, endpoint = "gemini-2.5-flash", "/v1/chat/completions"
	req := &core.ChatRequest{Model: model, Messages: []core.Message{{Role: "user", Content: "hello"}}}

	if strings.HasSuffix(fixturePath, ".txt") {
		provider := newGeminiReplayProvider(t, map[string]replayRoute{
			replayKey(http.MethodPost, "/chat/completions"): sseFixtureRoute(t, fixturePath),
		})
		stream, err := provider.StreamChatCompletion(context.Background(), req)
		require.NoError(t, err)
		return usage.ExtractFromCachedResponseBody(readAllStream(t, stream), "req-1", model, "gemini", endpoint, "", pricing)
	}

	routePath := "/chat/completions"
	if native {
		routePath = geminiNativeGeneratePath
	}
	provider := newGeminiReplayProviderWithMode(t, map[string]replayRoute{
		replayKey(http.MethodPost, routePath): jsonFixtureRoute(t, fixturePath),
	}, native)
	resp, err := provider.ChatCompletion(context.Background(), req)
	require.NoError(t, err)
	return usage.ExtractFromChatResponse(resp, "req-1", "gemini", endpoint, pricing)
}
