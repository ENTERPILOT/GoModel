//go:build contract

// Contract tests in this file are intended to run with: -tags=contract -timeout=5m.
package contract

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

func TestGeminiReplayChatCompletion(t *testing.T) {
	testCases := []struct {
		name        string
		fixturePath string
	}{
		{name: "basic", fixturePath: "gemini/chat_completion.json"},
		{name: "params", fixturePath: "gemini/chat_with_params.json"},
		{name: "tools", fixturePath: "gemini/chat_with_tools.json"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			provider := newGeminiReplayProvider(t, map[string]replayRoute{
				replayKey(http.MethodPost, "/chat/completions"): jsonFixtureRoute(t, tc.fixturePath),
			})

			resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
				Model: "gemini-2.5-flash",
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

func TestGeminiReplayStreamChatCompletion(t *testing.T) {
	provider := newGeminiReplayProvider(t, map[string]replayRoute{
		replayKey(http.MethodPost, "/chat/completions"): sseFixtureRoute(t, "gemini/chat_completion_stream.txt"),
	})

	stream, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model: "gemini-2.5-flash",
		Messages: []core.Message{{
			Role:    "user",
			Content: "stream",
		}},
	})
	require.NoError(t, err)

	raw := readAllStream(t, stream)
	chunks, done := parseChatStream(t, raw)

	compareGoldenJSON(t, goldenPathForFixture("gemini/chat_completion_stream.txt"), map[string]any{
		"done":   done,
		"chunks": chunks,
		"text":   extractChatStreamText(chunks),
	})
}

func TestGeminiReplayListModels(t *testing.T) {
	provider := newGeminiReplayProvider(t, map[string]replayRoute{
		replayKey(http.MethodGet, "/models"): jsonFixtureRoute(t, "gemini/models.json"),
	})

	resp, err := provider.ListModels(context.Background())
	require.NoError(t, err)
	require.NotNil(t, resp)

	compareGoldenJSON(t, goldenPathForFixture("gemini/models.json"), resp)
}

func TestGeminiReplayResponses(t *testing.T) {
	if !goldenFileExists(t, "golden/gemini/responses.golden.json") {
		t.Fatalf("missing golden file golden/gemini/responses.golden.json; run `make record-api` then `RECORD=1 go test -v -tags=contract -timeout=5m ./tests/contract/...`")
	}

	provider := newGeminiReplayProvider(t, map[string]replayRoute{
		replayKey(http.MethodPost, "/chat/completions"): jsonFixtureRoute(t, "gemini/chat_completion.json"),
	})

	resp, err := provider.Responses(context.Background(), &core.ResponsesRequest{
		Model: "gemini-2.5-flash",
		Input: "hello",
	})
	require.NoError(t, err)
	require.NotNil(t, resp)

	compareGoldenJSON(t, "gemini/responses.golden.json", resp)
}

func TestGeminiReplayStreamResponses(t *testing.T) {
	provider := newGeminiReplayProvider(t, map[string]replayRoute{
		replayKey(http.MethodPost, "/chat/completions"): sseFixtureRoute(t, "gemini/chat_completion_stream.txt"),
	})

	stream, err := provider.StreamResponses(context.Background(), &core.ResponsesRequest{
		Model: "gemini-2.5-flash",
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

	compareGoldenJSON(t, "gemini/responses_stream.golden.json", map[string]any{
		"events": events,
		"text":   extractResponsesStreamText(events),
	})
}
