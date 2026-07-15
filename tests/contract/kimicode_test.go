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
	"github.com/enterpilot/gomodel/internal/providers/kimicode"
)

func newKimicodeReplayProvider(t *testing.T, routes map[string]replayRoute) core.Provider {
	t.Helper()

	client := newReplayHTTPClient(t, routes)
	provider := kimicode.NewWithHTTPClient("kimi-test", "https://replay.local", client, llmclient.Hooks{})
	provider.SetBaseURL("https://replay.local")
	return provider
}

func TestKimicodeReplayChatCompletion(t *testing.T) {
	provider := newKimicodeReplayProvider(t, map[string]replayRoute{
		replayKey(http.MethodPost, "/chat/completions"): jsonFixtureRoute(t, "kimicode/chat_completion.json"),
	})

	resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model: "kimi-for-coding",
		Messages: []core.Message{{
			Role:    "user",
			Content: "hello",
		}},
	})
	require.NoError(t, err)
	require.NotNil(t, resp)

	compareGoldenJSON(t, goldenPathForFixture("kimicode/chat_completion.json"), resp)
}

func TestKimicodeReplayStreamChatCompletion(t *testing.T) {
	provider := newKimicodeReplayProvider(t, map[string]replayRoute{
		replayKey(http.MethodPost, "/chat/completions"): sseFixtureRoute(t, "kimicode/chat_completion_stream.txt"),
	})

	stream, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model: "kimi-for-coding",
		Messages: []core.Message{{
			Role:    "user",
			Content: "stream",
		}},
	})
	require.NoError(t, err)

	raw := readAllStream(t, stream)
	chunks, done := parseChatStream(t, raw)

	compareGoldenJSON(t, goldenPathForFixture("kimicode/chat_completion_stream.txt"), map[string]any{
		"done":   done,
		"chunks": chunks,
		"text":   extractChatStreamText(chunks),
	})
}

func TestKimicodeReplayListModels(t *testing.T) {
	provider := newKimicodeReplayProvider(t, map[string]replayRoute{
		replayKey(http.MethodGet, "/models"): jsonFixtureRoute(t, "kimicode/models.json"),
	})

	resp, err := provider.ListModels(context.Background())
	require.NoError(t, err)
	require.NotNil(t, resp)

	compareGoldenJSON(t, goldenPathForFixture("kimicode/models.json"), resp)
}

func TestKimicodeReplayEmbeddings(t *testing.T) {
	provider := newKimicodeReplayProvider(t, map[string]replayRoute{
		replayKey(http.MethodPost, "/embeddings"): jsonFixtureRoute(t, "kimicode/embeddings.json"),
	})

	resp, err := provider.Embeddings(context.Background(), &core.EmbeddingRequest{
		Model: "bge_m3_embed",
		Input: []string{"hello world"},
	})
	require.NoError(t, err)
	require.NotNil(t, resp)

	compareGoldenJSON(t, goldenPathForFixture("kimicode/embeddings.json"), resp)
}

func TestKimicodeReplayChatCompletionError(t *testing.T) {
	provider := newKimicodeReplayProvider(t, map[string]replayRoute{
		replayKey(http.MethodPost, "/chat/completions"): {
			statusCode:  http.StatusBadRequest,
			contentType: "application/json",
			body:        []byte(`{"error":{"message":"invalid request"}}`),
		},
	})

	_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model: "kimi-for-coding",
		Messages: []core.Message{{
			Role:    "user",
			Content: "hello",
		}},
	})
	require.Error(t, err)
}
