//go:build contract

// Contract tests in this file are intended to run with: -tags=contract -timeout=5m.
package contract

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers/openai"
)

func newOpenAIReplayProvider(t *testing.T, routes map[string]replayRoute) core.Provider {
	t.Helper()

	client := newReplayHTTPClient(t, routes)
	provider := openai.NewWithHTTPClient("sk-test", client, llmclient.Hooks{})
	provider.SetBaseURL("https://replay.local")
	return provider
}

func TestOpenAIReplayChatCompletion(t *testing.T) {
	testCases := []struct {
		name         string
		fixturePath  string
		finishReason string
	}{
		{name: "basic", fixturePath: "openai/chat_completion.json", finishReason: "stop"},
		{name: "reasoning", fixturePath: "openai/chat_completion_reasoning.json"},
		{name: "json-mode", fixturePath: "openai/chat_json_mode.json"},
		{name: "params", fixturePath: "openai/chat_with_params.json", finishReason: "stop"},
		{name: "multi-turn", fixturePath: "openai/chat_multi_turn.json"},
		{name: "multimodal", fixturePath: "openai/chat_multimodal.json"},
		{name: "tools", fixturePath: "openai/chat_with_tools.json", finishReason: "tool_calls"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			provider := newOpenAIReplayProvider(t, map[string]replayRoute{
				replayKey(http.MethodPost, "/chat/completions"): jsonFixtureRoute(t, tc.fixturePath),
			})

			resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
				Model: "gpt-4o-mini",
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

func TestOpenAIReplayStreamChatCompletion(t *testing.T) {
	provider := newOpenAIReplayProvider(t, map[string]replayRoute{
		replayKey(http.MethodPost, "/chat/completions"): sseFixtureRoute(t, "openai/chat_completion_stream.txt"),
	})

	stream, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model: "gpt-4o-mini",
		Messages: []core.Message{{
			Role:    "user",
			Content: "stream",
		}},
	})
	require.NoError(t, err)

	raw := readAllStream(t, stream)
	chunks, done := parseChatStream(t, raw)

	compareGoldenJSON(t, goldenPathForFixture("openai/chat_completion_stream.txt"), map[string]any{
		"done":   done,
		"chunks": chunks,
		"text":   extractChatStreamText(chunks),
	})
}

func TestOpenAIReplayListModels(t *testing.T) {
	provider := newOpenAIReplayProvider(t, map[string]replayRoute{
		replayKey(http.MethodGet, "/models"): jsonFixtureRoute(t, "openai/models.json"),
	})

	resp, err := provider.ListModels(context.Background())
	require.NoError(t, err)
	require.NotNil(t, resp)

	compareGoldenJSON(t, goldenPathForFixture("openai/models.json"), resp)
}

func TestOpenAIReplayResponses(t *testing.T) {
	if !goldenFileExists(t, "openai/responses.json") {
		t.Fatalf("missing golden file openai/responses.json; run `make record-api` to create/update contract fixtures")
	}

	provider := newOpenAIReplayProvider(t, map[string]replayRoute{
		replayKey(http.MethodPost, "/responses"): jsonFixtureRoute(t, "openai/responses.json"),
	})

	resp, err := provider.Responses(context.Background(), &core.ResponsesRequest{
		Model: "gpt-4o-mini",
		Input: "hello",
	})
	require.NoError(t, err)
	require.NotNil(t, resp)

	compareGoldenJSON(t, "openai/responses.golden.json", resp)
}

func TestOpenAIReplayStreamResponses(t *testing.T) {
	if !goldenFileExists(t, "openai/responses_stream.txt") {
		t.Fatalf("missing golden file openai/responses_stream.txt; run `make record-api` to create/update contract fixtures")
	}

	provider := newOpenAIReplayProvider(t, map[string]replayRoute{
		replayKey(http.MethodPost, "/responses"): sseFixtureRoute(t, "openai/responses_stream.txt"),
	})

	stream, err := provider.StreamResponses(context.Background(), &core.ResponsesRequest{
		Model: "gpt-4o-mini",
		Input: "stream",
	})
	require.NoError(t, err)

	raw := readAllStream(t, stream)
	events := parseResponsesStream(t, raw)
	require.NotEmpty(t, events)

	require.True(t, hasResponsesEvent(events, "response.created"))
	require.True(t, hasResponsesEvent(events, "response.output_text.delta"))
	require.True(t, hasResponsesEvent(events, "response.completed"))
	require.NotEmpty(t, extractResponsesStreamText(events))

	compareGoldenJSON(t, "openai/responses_stream.golden.json", map[string]any{
		"events": events,
		"text":   extractResponsesStreamText(events),
	})
}
