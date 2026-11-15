//go:build contract

// Contract replay tests for thinking tokens on Gemini's OpenAI-compatible
// endpoint, which counts them in total_tokens but not in completion_tokens.
// The adapter folds them into the output count, as the native path does, so
// the gateway prices them.
package contract

import (
	"context"
	"math"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/usage"
)

const geminiNativeGeneratePath = "/models/gemini-2.5-flash:generateContent"

// TestGeminiReplayThinkingUsageCost pins what the gateway records for each
// recorded response: token counts, raw usage data and cost at the given
// dollars per million input and output tokens.
func TestGeminiReplayThinkingUsageCost(t *testing.T) {
	flash25 := [2]float64{0.30, 2.50}
	flash3 := [2]float64{0.50, 3.00}

	tests := []struct {
		name        string
		fixturePath string
		native      bool
		rates       [2]float64
	}{
		{name: "thinking", fixturePath: "gemini/chat_with_thinking.json", rates: flash25},
		{name: "thinking_truncated", fixturePath: "gemini/chat_with_thinking_truncated.json", rates: flash3},
		{name: "thinking_stream", fixturePath: "gemini/chat_with_thinking_stream.txt", rates: flash25},
		{name: "no_thinking", fixturePath: "gemini/chat_with_tools.json", rates: flash25},
		{name: "native", fixturePath: "gemini/native_chat_completion.json", native: true, rates: flash25},
	}

	recorded := make(map[string]any, len(tests))
	for _, tt := range tests {
		pricing := &core.ModelPricing{Currency: "USD", InputPerMtok: &tt.rates[0], OutputPerMtok: &tt.rates[1]}
		entry := replayGeminiUsageEntry(t, tt.fixturePath, tt.native, pricing)
		require.NotNil(t, entry, tt.name)
		require.NotNil(t, entry.TotalCost, tt.name)

		recorded[tt.name] = map[string]any{
			"fixture":              tt.fixturePath,
			"input_per_mtok":       tt.rates[0],
			"output_per_mtok":      tt.rates[1],
			"input_tokens":         entry.InputTokens,
			"output_tokens":        entry.OutputTokens,
			"total_tokens":         entry.TotalTokens,
			"raw_data":             entry.RawData,
			"total_cost_micro_usd": math.Round(*entry.TotalCost*1e10) / 1e4,
			"costs_caveat":         entry.CostsCalculationCaveat,
		}
	}

	compareGoldenJSON(t, "gemini/thinking_usage_cost.golden.json", recorded)
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
