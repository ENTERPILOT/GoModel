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
	"gomodel/internal/providers/kimi"
)

func newKimiReplayProvider(t *testing.T, routes map[string]replayRoute) core.Provider {
	t.Helper()

	client := newReplayHTTPClient(t, routes)
	provider := kimi.NewWithHTTPClient("kimi-test", "", client, llmclient.Hooks{}, nil, "")
	provider.SetBaseURL("https://replay.local")
	return provider
}

func TestKimiReplayChatCompletion(t *testing.T) {
	testCases := []struct {
		name        string
		fixturePath string
	}{
		{name: "basic", fixturePath: "kimi/chat_completion.json"},
		{name: "params", fixturePath: "kimi/chat_with_params.json"},
		{name: "tools", fixturePath: "kimi/chat_with_tools.json"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			provider := newKimiReplayProvider(t, map[string]replayRoute{
				replayKey(http.MethodPost, "/chat/completions"): jsonFixtureRoute(t, tc.fixturePath),
			})

			resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
				Model: "kimi-k2-0711",
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

func TestKimiReplayStreamChatCompletion(t *testing.T) {
	provider := newKimiReplayProvider(t, map[string]replayRoute{
		replayKey(http.MethodPost, "/chat/completions"): sseFixtureRoute(t, "kimi/chat_completion_stream.txt"),
	})

	stream, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model: "kimi-k2-0711",
		Messages: []core.Message{{
			Role:    "user",
			Content: "stream",
		}},
	})
	require.NoError(t, err)

	raw := readAllStream(t, stream)
	chunks, done := parseChatStream(t, raw)

	compareGoldenJSON(t, goldenPathForFixture("kimi/chat_completion_stream.txt"), map[string]any{
		"done":   done,
		"chunks": chunks,
		"text":   extractChatStreamText(chunks),
	})
}

func TestKimiReplayListModels(t *testing.T) {
	provider := newKimiReplayProvider(t, map[string]replayRoute{
		replayKey(http.MethodGet, "/models"): jsonFixtureRoute(t, "kimi/models.json"),
	})

	resp, err := provider.ListModels(context.Background())
	require.NoError(t, err)
	require.NotNil(t, resp)

	compareGoldenJSON(t, goldenPathForFixture("kimi/models.json"), resp)
}

func TestKimiReplayEmbeddings(t *testing.T) {
	provider := newKimiReplayProvider(t, map[string]replayRoute{
		replayKey(http.MethodPost, "/embeddings"): jsonFixtureRoute(t, "kimi/embeddings.json"),
	})

	resp, err := provider.Embeddings(context.Background(), &core.EmbeddingRequest{
		Model: "kimi-embedding",
		Input: "hello",
	})
	require.NoError(t, err)
	require.NotNil(t, resp)

	compareGoldenJSON(t, goldenPathForFixture("kimi/embeddings.json"), resp)
}