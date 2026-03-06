//go:build contract

// Contract tests in this file are intended to run with: -tags=contract -timeout=5m.
package contract

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"gomodel/internal/core"
	"gomodel/internal/llmclient"
	"gomodel/internal/providers/anthropic"
)

func newAnthropicReplayProvider(t *testing.T, routes map[string]replayRoute) core.Provider {
	t.Helper()

	client := newReplayHTTPClient(t, routes)
	provider := anthropic.NewWithHTTPClient("sk-ant-test", client, llmclient.Hooks{})
	provider.SetBaseURL("https://replay.local")
	return provider
}

func TestAnthropicReplayChatCompletion(t *testing.T) {
	testCases := []struct {
		name         string
		fixturePath  string
		finishReason string
	}{
		{name: "basic", fixturePath: "anthropic/messages.json"},
		{name: "with-params", fixturePath: "anthropic/messages_with_params.json"},
		{name: "with-tools", fixturePath: "anthropic/messages_with_tools.json", finishReason: "tool_calls"},
		{name: "extended-thinking", fixturePath: "anthropic/messages_extended_thinking.json"},
		{name: "multi-turn", fixturePath: "anthropic/messages_multi_turn.json"},
		{name: "multimodal", fixturePath: "anthropic/messages_multimodal.json"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			provider := newAnthropicReplayProvider(t, map[string]replayRoute{
				replayKey(http.MethodPost, "/messages"): jsonFixtureRoute(t, tc.fixturePath),
			})

			resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
				Model: "claude-sonnet-4-20250514",
				Messages: []core.Message{{
					Role:    "user",
					Content: "hello",
				}},
			})
			require.NoError(t, err)
			require.NotNil(t, resp)
			require.NotEmpty(t, resp.Choices)

			if tc.finishReason != "" {
				require.Equal(t, tc.finishReason, resp.Choices[0].FinishReason)
			}
			compareGoldenJSON(t, goldenPathForFixture(tc.fixturePath), resp)
		})
	}
}

func TestAnthropicReplayStreamChatCompletion(t *testing.T) {
	provider := newAnthropicReplayProvider(t, map[string]replayRoute{
		replayKey(http.MethodPost, "/messages"): sseFixtureRoute(t, "anthropic/messages_stream.txt"),
	})

	stream, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model: "claude-sonnet-4-20250514",
		Messages: []core.Message{{
			Role:    "user",
			Content: "stream",
		}},
	})
	require.NoError(t, err)

	raw := readAllStream(t, stream)
	chunks, done := parseChatStream(t, raw)

	compareGoldenJSON(t, goldenPathForFixture("anthropic/messages_stream.txt"), map[string]any{
		"done":   done,
		"chunks": chunks,
		"text":   extractChatStreamText(chunks),
	})
}

func TestAnthropicReplayResponses(t *testing.T) {
	provider := newAnthropicReplayProvider(t, map[string]replayRoute{
		replayKey(http.MethodPost, "/messages"): jsonFixtureRoute(t, "anthropic/messages.json"),
	})

	resp, err := provider.Responses(context.Background(), &core.ResponsesRequest{
		Model: "claude-sonnet-4-20250514",
		Input: "hello",
	})
	require.NoError(t, err)
	require.NotNil(t, resp)

	compareGoldenJSON(t, "anthropic/responses.golden.json", resp)
}

func TestAnthropicReplayStreamResponses(t *testing.T) {
	provider := newAnthropicReplayProvider(t, map[string]replayRoute{
		replayKey(http.MethodPost, "/messages"): sseFixtureRoute(t, "anthropic/messages_stream.txt"),
	})

	stream, err := provider.StreamResponses(context.Background(), &core.ResponsesRequest{
		Model: "claude-sonnet-4-20250514",
		Input: "stream",
	})
	require.NoError(t, err)

	raw := readAllStream(t, stream)
	events := parseResponsesStream(t, raw)
	require.True(t, hasResponsesEvent(events, "response.created"))
	require.True(t, hasResponsesEvent(events, "response.output_text.delta"))
	require.True(t, hasResponsesEvent(events, "response.completed"))

	hasDone := false
	for _, event := range events {
		if event.Done {
			hasDone = true
			break
		}
	}
	require.True(t, hasDone, "responses stream should terminate with [DONE]")

	compareGoldenJSON(t, "anthropic/responses_stream.golden.json", map[string]any{
		"events": events,
		"text":   extractResponsesStreamText(events),
	})
}
