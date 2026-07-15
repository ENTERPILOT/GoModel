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
	"github.com/enterpilot/gomodel/internal/providers/xai"
)

func newXAIReplayProvider(t *testing.T, routes map[string]replayRoute) core.Provider {
	t.Helper()

	client := newReplayHTTPClient(t, routes)
	provider := xai.NewWithHTTPClient("xai-test", client, llmclient.Hooks{})
	provider.SetBaseURL("https://replay.local")
	return provider
}

func TestXAIReplayChatCompletion(t *testing.T) {
	testCases := []struct {
		name        string
		fixturePath string
	}{
		{name: "basic", fixturePath: "xai/chat_completion.json"},
		{name: "params", fixturePath: "xai/chat_with_params.json"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			provider := newXAIReplayProvider(t, map[string]replayRoute{
				replayKey(http.MethodPost, "/chat/completions"): jsonFixtureRoute(t, tc.fixturePath),
			})

			resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
				Model: "grok-3-mini",
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

func TestXAIReplayStreamChatCompletion(t *testing.T) {
	provider := newXAIReplayProvider(t, map[string]replayRoute{
		replayKey(http.MethodPost, "/chat/completions"): sseFixtureRoute(t, "xai/chat_completion_stream.txt"),
	})

	stream, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model: "grok-3-mini",
		Messages: []core.Message{{
			Role:    "user",
			Content: "stream",
		}},
	})
	require.NoError(t, err)

	raw := readAllStream(t, stream)
	chunks, done := parseChatStream(t, raw)

	compareGoldenJSON(t, goldenPathForFixture("xai/chat_completion_stream.txt"), map[string]any{
		"done":   done,
		"chunks": chunks,
		"text":   extractChatStreamText(chunks),
	})
}

func TestXAIReplayListModels(t *testing.T) {
	provider := newXAIReplayProvider(t, map[string]replayRoute{
		replayKey(http.MethodGet, "/models"): jsonFixtureRoute(t, "xai/models.json"),
	})

	resp, err := provider.ListModels(context.Background())
	require.NoError(t, err)
	require.NotNil(t, resp)

	compareGoldenJSON(t, goldenPathForFixture("xai/models.json"), resp)
}

func TestXAIReplayResponses(t *testing.T) {
	if !goldenFileExists(t, "xai/responses.json") {
		t.Fatalf("missing golden file xai/responses.json; run `make record-api` to create/update contract fixtures")
	}

	provider := newXAIReplayProvider(t, map[string]replayRoute{
		replayKey(http.MethodPost, "/responses"): jsonFixtureRoute(t, "xai/responses.json"),
	})

	resp, err := provider.Responses(context.Background(), &core.ResponsesRequest{
		Model: "grok-3-mini",
		Input: "hello",
	})
	require.NoError(t, err)
	require.NotNil(t, resp)

	compareGoldenJSON(t, goldenPathForFixture("xai/responses.json"), resp)
}

func TestXAIReplayStreamResponses(t *testing.T) {
	if !goldenFileExists(t, "xai/responses_stream.txt") {
		t.Fatalf("missing golden file xai/responses_stream.txt; run `make record-api` to create/update contract fixtures")
	}

	provider := newXAIReplayProvider(t, map[string]replayRoute{
		replayKey(http.MethodPost, "/responses"): sseFixtureRoute(t, "xai/responses_stream.txt"),
	})

	stream, err := provider.StreamResponses(context.Background(), &core.ResponsesRequest{
		Model: "grok-3-mini",
		Input: "stream",
	})
	require.NoError(t, err)

	raw := readAllStream(t, stream)
	events := parseResponsesStream(t, raw)
	require.True(t, hasResponsesEvent(events, "response.created"))
	require.True(t, hasResponsesEvent(events, "response.output_text.delta"))
	require.True(t, hasResponsesEvent(events, "response.completed"))

	compareGoldenJSON(t, goldenPathForFixture("xai/responses_stream.txt"), map[string]any{
		"events": events,
		"text":   extractResponsesStreamText(events),
	})
}
