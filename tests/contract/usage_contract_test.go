//go:build contract

package contract

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"gomodel/internal/core"
)

func TestProviderReplayChatUsageContract(t *testing.T) {
	tests := []struct {
		name        string
		model       string
		routePath   string
		fixturePath string
		provider    func(*testing.T, map[string]replayRoute) core.Provider
	}{
		{
			name:        "openai",
			model:       "gpt-4o-mini",
			routePath:   "/chat/completions",
			fixturePath: "openai/chat_completion.json",
			provider:    newOpenAIReplayProvider,
		},
		{
			name:        "anthropic",
			model:       "claude-sonnet-4-20250514",
			routePath:   "/messages",
			fixturePath: "anthropic/messages.json",
			provider:    newAnthropicReplayProvider,
		},
		{
			name:        "gemini",
			model:       "gemini-2.5-flash",
			routePath:   "/chat/completions",
			fixturePath: "gemini/chat_completion.json",
			provider:    newGeminiReplayProvider,
		},
		{
			name:        "groq",
			model:       "llama-3.3-70b-versatile",
			routePath:   "/chat/completions",
			fixturePath: "groq/chat_completion.json",
			provider:    newGroqReplayProvider,
		},
		{
			name:        "xai",
			model:       "grok-3-mini",
			routePath:   "/chat/completions",
			fixturePath: "xai/chat_completion.json",
			provider:    newXAIReplayProvider,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := tt.provider(t, map[string]replayRoute{
				replayKey(http.MethodPost, tt.routePath): jsonFixtureRoute(t, tt.fixturePath),
			})

			resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
				Model: tt.model,
				Messages: []core.Message{{
					Role:    "user",
					Content: "hello",
				}},
			})
			require.NoError(t, err)
			require.NotNil(t, resp)
			require.Greater(t, resp.Usage.PromptTokens, 0, "prompt tokens should be normalized")
			require.Greater(t, resp.Usage.CompletionTokens, 0, "completion tokens should be normalized")
			require.GreaterOrEqual(t, resp.Usage.TotalTokens, resp.Usage.PromptTokens+resp.Usage.CompletionTokens)
			require.NotEmpty(t, resp.Model)
		})
	}
}
