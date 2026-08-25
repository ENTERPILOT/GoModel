//go:build contract

// Contract replay tests for Gemini's native API mode: recorded native payloads
// (generateContent, streamGenerateContent, batchEmbedContents, models) are
// replayed through the real adapter and the normalized output is golden-pinned.
package contract

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

func TestGeminiNativeReplayChatCompletion(t *testing.T) {
	provider := newGeminiNativeReplayProvider(t, map[string]replayRoute{
		replayKey(http.MethodPost, "/models/gemini-2.5-flash:generateContent"): jsonFixtureRoute(t, "gemini/native_chat_completion.json"),
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

	compareGoldenJSON(t, goldenPathForFixture("gemini/native_chat_completion.json"), resp)
}

func TestGeminiNativeReplayStreamChatCompletion(t *testing.T) {
	provider := newGeminiNativeReplayProvider(t, map[string]replayRoute{
		replayKey(http.MethodPost, "/models/gemini-2.5-flash:streamGenerateContent?alt=sse"): sseFixtureRoute(t, "gemini/native_chat_completion_stream.txt"),
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

	compareGoldenJSON(t, goldenPathForFixture("gemini/native_chat_completion_stream.txt"), map[string]any{
		"done":   done,
		"chunks": chunks,
		"text":   extractChatStreamText(chunks),
	})
}

func TestGeminiNativeReplayEmbeddings(t *testing.T) {
	provider := newGeminiNativeReplayProvider(t, map[string]replayRoute{
		replayKey(http.MethodPost, "/models/gemini-embedding-001:batchEmbedContents"): jsonFixtureRoute(t, "gemini/native_embeddings.json"),
	})

	dimensions := 8
	resp, err := provider.Embeddings(context.Background(), &core.EmbeddingRequest{
		Model:      "gemini-embedding-001",
		Input:      []string{"hello world", "second input"},
		Dimensions: &dimensions,
	})
	require.NoError(t, err)
	require.NotNil(t, resp)

	compareGoldenJSON(t, goldenPathForFixture("gemini/native_embeddings.json"), resp)
}

func TestGeminiNativeReplayCreateImage(t *testing.T) {
	provider := newGeminiNativeReplayProvider(t, map[string]replayRoute{
		replayKey(http.MethodPost, "/models/gemini-2.5-flash-image:generateContent"): jsonFixtureRoute(t, "gemini/native_image_generation.json"),
	})

	resp, err := provider.CreateImage(context.Background(), &core.ImageGenerationRequest{
		Model:  "gemini-2.5-flash-image",
		Prompt: "A tiny solid red square on a white background",
	})
	require.NoError(t, err)
	require.NotNil(t, resp)

	compareGoldenJSON(t, goldenPathForFixture("gemini/native_image_generation.json"), resp)
}

func TestGeminiNativeReplayCreateImageEdit(t *testing.T) {
	// Edits share the generateContent response shape; the recorded generation
	// fixture pins the same normalization for the edit entry point.
	provider := newGeminiNativeReplayProvider(t, map[string]replayRoute{
		replayKey(http.MethodPost, "/models/gemini-2.5-flash-image:generateContent"): jsonFixtureRoute(t, "gemini/native_image_generation.json"),
	})

	resp, err := provider.CreateImageEdit(context.Background(), &core.ImageEditRequest{
		Model:  "gemini-2.5-flash-image",
		Prompt: "make the square red",
		Images: []core.ImageFile{{Filename: "square.png", ContentType: "image/png", Data: []byte{0x89, 0x50, 0x4e, 0x47}}},
	})
	require.NoError(t, err)
	require.NotNil(t, resp)

	compareGoldenJSON(t, "gemini/native_image_edit.golden.json", resp)
}

func TestGeminiNativeReplayListModels(t *testing.T) {
	provider := newGeminiNativeReplayProvider(t, map[string]replayRoute{
		replayKey(http.MethodGet, "/models"): jsonFixtureRoute(t, "gemini/native_models.json"),
	})

	resp, err := provider.ListModels(context.Background())
	require.NoError(t, err)
	require.NotNil(t, resp)

	compareGoldenJSON(t, goldenPathForFixture("gemini/native_models.json"), resp)
}
