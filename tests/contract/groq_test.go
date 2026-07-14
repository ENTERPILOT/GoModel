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
	"github.com/enterpilot/gomodel/internal/providers/groq"
)

func newGroqReplayProvider(t *testing.T, routes map[string]replayRoute) core.Provider {
	t.Helper()

	client := newReplayHTTPClient(t, routes)
	provider := groq.NewWithHTTPClient("gsk-test", client, llmclient.Hooks{})
	provider.SetBaseURL("https://replay.local")
	return provider
}

func TestGroqReplayChatCompletion(t *testing.T) {
	testCases := []struct {
		name        string
		fixturePath string
	}{
		{name: "basic", fixturePath: "groq/chat_completion.json"},
		{name: "params", fixturePath: "groq/chat_with_params.json"},
		{name: "tools", fixturePath: "groq/chat_with_tools.json"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			provider := newGroqReplayProvider(t, map[string]replayRoute{
				replayKey(http.MethodPost, "/chat/completions"): jsonFixtureRoute(t, tc.fixturePath),
			})

			resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
				Model: "llama-3.3-70b-versatile",
				Messages: []core.Message{{
					Role:    "user",
					Content: "hello",
				}},
			})
			require.NoError(t, err)
			require.NotNil(t, resp)

			compareGoldenJSON(t, goldenPathForFixture(tc.fixturePath), resp)
		})
	}
}

func TestGroqReplayStreamChatCompletion(t *testing.T) {
	provider := newGroqReplayProvider(t, map[string]replayRoute{
		replayKey(http.MethodPost, "/chat/completions"): sseFixtureRoute(t, "groq/chat_completion_stream.txt"),
	})

	stream, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model: "llama-3.3-70b-versatile",
		Messages: []core.Message{{
			Role:    "user",
			Content: "stream",
		}},
	})
	require.NoError(t, err)

	raw := readAllStream(t, stream)
	chunks, done := parseChatStream(t, raw)

	compareGoldenJSON(t, goldenPathForFixture("groq/chat_completion_stream.txt"), map[string]any{
		"done":   done,
		"chunks": chunks,
		"text":   extractChatStreamText(chunks),
	})
}

func TestGroqReplayListModels(t *testing.T) {
	provider := newGroqReplayProvider(t, map[string]replayRoute{
		replayKey(http.MethodGet, "/models"): jsonFixtureRoute(t, "groq/models.json"),
	})

	resp, err := provider.ListModels(context.Background())
	require.NoError(t, err)
	require.NotNil(t, resp)

	compareGoldenJSON(t, goldenPathForFixture("groq/models.json"), resp)
}

func TestGroqReplayResponses(t *testing.T) {
	provider := newGroqReplayProvider(t, map[string]replayRoute{
		replayKey(http.MethodPost, "/chat/completions"): jsonFixtureRoute(t, "groq/chat_completion.json"),
	})

	resp, err := provider.Responses(context.Background(), &core.ResponsesRequest{
		Model: "llama-3.3-70b-versatile",
		Input: "hello",
	})
	require.NoError(t, err)
	require.NotNil(t, resp)

	compareGoldenJSON(t, "groq/responses.golden.json", resp)
}

func TestGroqReplayStreamResponses(t *testing.T) {
	provider := newGroqReplayProvider(t, map[string]replayRoute{
		replayKey(http.MethodPost, "/chat/completions"): sseFixtureRoute(t, "groq/chat_completion_stream.txt"),
	})

	stream, err := provider.StreamResponses(context.Background(), &core.ResponsesRequest{
		Model: "llama-3.3-70b-versatile",
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

	compareGoldenJSON(t, "groq/responses_stream.golden.json", map[string]any{
		"events": events,
		"text":   extractResponsesStreamText(events),
	})
}
