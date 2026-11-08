package gemini

import (
	"bufio"
	"context"
	"io"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

func streamNativeChat(t *testing.T, upstream string) []map[string]any {
	t.Helper()
	t.Setenv(useNativeAPIEnvVar, "true")

	server, _ := providertest.SSEServer(t, upstream)
	provider := newTestProvider("test-api-key", nil, llmclient.Hooks{})
	provider.SetModelsURL(server.URL)

	body, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "gemini-2.5-flash-image",
		Messages: []core.Message{{Role: "user", Content: "draw a cat"}},
	})
	require.NoError(t, err)
	defer func() { _ = body.Close() }()

	raw, err := io.ReadAll(body)
	require.NoError(t, err)
	return parseOpenAIStreamChunks(t, string(raw))
}

// streamImageDeltas returns the images member of every chunk delta that has one.
func streamImageDeltas(t *testing.T, chunks []map[string]any) []any {
	t.Helper()

	var images []any
	for _, chunk := range chunks {
		choices, _ := chunk["choices"].([]any)
		for _, entry := range choices {
			choice, ok := entry.(map[string]any)
			require.True(t, ok, "choice = %#v", entry)
			delta, _ := choice["delta"].(map[string]any)
			if list, ok := delta["images"].([]any); ok {
				images = append(images, list...)
			}
		}
	}
	return images
}

func TestStreamChatCompletion_NativeStreamsGeneratedImages(t *testing.T) {
	chunks := streamNativeChat(t, `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Here is a cat: "}]}}]}

data: {"candidates":[{"content":{"role":"model","parts":[{"inlineData":{"mimeType":"image/png","data":"aW1n"},"thoughtSignature":"sig-a"}]},"finishReason":"STOP"}]}

`)

	images := streamImageDeltas(t, chunks)
	require.Len(t, images, 1, "chunks %#v", chunks)
	assert.Equal(t, map[string]any{
		"type": "image_url",
		"image_url": map[string]any{
			"url":           "data:image/png;base64,aW1n",
			"extra_content": map[string]any{"google": map[string]any{"thought_signature": "sig-a"}},
		},
	}, images[0])
}

func TestStreamChatCompletion_NativeSkipsNonOutputImages(t *testing.T) {
	// A thought image and a non-image blob are not generated images; the
	// text in the same event still streams.
	chunks := streamNativeChat(t, `data: {"candidates":[{"content":{"role":"model","parts":[{"inlineData":{"mimeType":"image/png","data":"dGhvdWdodA=="},"thought":true},{"inlineData":{"mimeType":"application/pdf","data":"cGRm"}},{"inline_data":{"mime_type":"image/png","data":""}},{"text":"done"}]},"finishReason":"STOP"}]}

`)

	assert.Empty(t, streamImageDeltas(t, chunks))
	require.NotEmpty(t, chunks)
	choices, _ := chunks[0]["choices"].([]any)
	require.Len(t, choices, 1)
	delta, _ := choices[0].(map[string]any)["delta"].(map[string]any)
	assert.Equal(t, "done", delta["content"])
}

func TestStreamResponses_NativeReturnsGeneratedImages(t *testing.T) {
	t.Setenv(useNativeAPIEnvVar, "true")

	server, _ := providertest.SSEServer(t, `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Here is a cat: "}]}}]}

data: {"candidates":[{"content":{"role":"model","parts":[{"inlineData":{"mimeType":"image/png","data":"aW1n"}}]},"finishReason":"STOP"}]}

`)
	provider := newTestProvider("test-api-key", nil, llmclient.Hooks{})
	provider.SetModelsURL(server.URL)

	body, err := provider.StreamResponses(context.Background(), &core.ResponsesRequest{
		Model: "gemini-2.5-flash-image",
		Input: "draw a cat",
	})
	require.NoError(t, err)
	defer func() { _ = body.Close() }()

	raw, err := io.ReadAll(body)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"type":"image_generation_call"`)
	assert.Contains(t, string(raw), `"result":"aW1n"`)
}

func TestStreamChatCompletion_NativeImagesGetTheirOwnChunk(t *testing.T) {
	// Text, an image and a tool call in one event: the image must not share
	// a delta with text or tool calls, which stream tools split and rewrite.
	chunks := streamNativeChat(t, `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Here: "},{"inlineData":{"mimeType":"image/png","data":"aW1n"}},{"functionCall":{"id":"call_1","name":"save","args":{}}}]},"finishReason":"STOP"}]}

`)

	var imageDeltas []map[string]any
	for _, chunk := range chunks {
		choices, _ := chunk["choices"].([]any)
		for _, entry := range choices {
			choice, ok := entry.(map[string]any)
			require.True(t, ok, "choice = %#v", entry)
			delta, _ := choice["delta"].(map[string]any)
			if _, ok := delta["images"]; ok {
				imageDeltas = append(imageDeltas, delta)
			}
		}
	}
	require.Len(t, imageDeltas, 1, "chunks %#v", chunks)
	assert.Len(t, imageDeltas[0], 1, "image delta carries only images: %#v", imageDeltas[0])
}

func TestStreamChatCompletion_NativeStreamsImageAboveFourMiB(t *testing.T) {
	// A high-resolution image arrives as one SSE line well above bufio's
	// usual caps; the stream must carry it rather than fail mid-way.
	data := strings.Repeat("A", 6<<20)
	chunks := streamNativeChat(t, `data: {"candidates":[{"content":{"role":"model","parts":[{"inlineData":{"mimeType":"image/png","data":"`+data+`"}}]},"finishReason":"STOP"}]}

`)

	images := streamImageDeltas(t, chunks)
	require.Len(t, images, 1)
	image, _ := images[0].(map[string]any)["image_url"].(map[string]any)
	assert.Equal(t, "data:image/png;base64,"+data, image["url"])
}

func TestStreamChatCompletion_NativeLineAboveCapFailsStream(t *testing.T) {
	t.Setenv(useNativeAPIEnvVar, "true")

	data := strings.Repeat("A", maxNativeStreamLineBytes)
	server, _ := providertest.SSEServer(t, `data: {"candidates":[{"content":{"role":"model","parts":[{"inlineData":{"mimeType":"image/png","data":"`+data+`"}}]},"finishReason":"STOP"}]}

`)
	provider := newTestProvider("test-api-key", nil, llmclient.Hooks{})
	provider.SetModelsURL(server.URL)

	body, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "gemini-2.5-flash-image",
		Messages: []core.Message{{Role: "user", Content: "draw a cat"}},
	})
	require.NoError(t, err)
	defer func() { _ = body.Close() }()

	_, err = io.ReadAll(body)
	require.ErrorIs(t, err, bufio.ErrTooLong)
}

// streamDeltaKinds lists, per chunk delta, "role", "content:<text>" or
// "images:<n>" in stream order.
func streamDeltaKinds(t *testing.T, chunks []map[string]any) []string {
	t.Helper()

	var kinds []string
	for _, chunk := range chunks {
		choices, _ := chunk["choices"].([]any)
		for _, entry := range choices {
			choice, ok := entry.(map[string]any)
			require.True(t, ok, "choice = %#v", entry)
			delta, _ := choice["delta"].(map[string]any)
			if _, ok := delta["role"]; ok {
				kinds = append(kinds, "role")
			}
			if text, ok := delta["content"].(string); ok {
				kinds = append(kinds, "content:"+text)
			}
			if list, ok := delta["images"].([]any); ok {
				kinds = append(kinds, "images:"+strconv.Itoa(len(list)))
			}
		}
	}
	return kinds
}

func TestStreamChatCompletion_NativeKeepsPartOrderAroundImages(t *testing.T) {
	tests := []struct {
		name  string
		parts string
		want  []string
	}{
		{
			name:  "text image text",
			parts: `{"text":"A"},{"inlineData":{"mimeType":"image/png","data":"aW1n"}},{"text":"B"}`,
			want:  []string{"role", "content:A", "images:1", "content:B"},
		},
		{
			name:  "image first",
			parts: `{"inlineData":{"mimeType":"image/png","data":"aW1n"}},{"inlineData":{"mimeType":"image/png","data":"dHdv"}},{"text":"B"}`,
			want:  []string{"role", "images:2", "content:B"},
		},
		{
			name:  "image only",
			parts: `{"inlineData":{"mimeType":"image/png","data":"aW1n"}}`,
			want:  []string{"role", "images:1"},
		},
		{
			name:  "thought image stays with the text around it",
			parts: `{"text":"A"},{"inlineData":{"mimeType":"image/png","data":"dGg="},"thought":true},{"text":"B"}`,
			want:  []string{"role", "content:AB"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chunks := streamNativeChat(t, `data: {"candidates":[{"content":{"role":"model","parts":[`+tt.parts+`]},"finishReason":"STOP"}]}

`)
			kinds := streamDeltaKinds(t, chunks)
			assert.Equal(t, tt.want, kinds, "chunks %#v", chunks)
			assert.Equal(t, "stop", lastFinishReason(t, chunks))
		})
	}
}

func lastFinishReason(t *testing.T, chunks []map[string]any) string {
	t.Helper()

	reason := ""
	for _, chunk := range chunks {
		choices, _ := chunk["choices"].([]any)
		for _, entry := range choices {
			choice, ok := entry.(map[string]any)
			require.True(t, ok, "choice = %#v", entry)
			if value, ok := choice["finish_reason"].(string); ok && value != "" {
				reason = value
			}
		}
	}
	return reason
}

// Splitting a candidate at an image must not let a generated tool call ID
// take one Gemini supplies later in the same event.
func TestStreamChatCompletion_NativeImageKeepsSuppliedToolCallIDs(t *testing.T) {
	chunks := streamNativeChat(t, `data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"first","args":{}}},{"inlineData":{"mimeType":"image/png","data":"aW1n"}},{"functionCall":{"id":"call_0","name":"second","args":{}}}]},"finishReason":"STOP"}]}

`)

	ids := map[string]string{}
	for _, chunk := range chunks {
		choices, _ := chunk["choices"].([]any)
		for _, entry := range choices {
			choice, ok := entry.(map[string]any)
			require.True(t, ok, "choice = %#v", entry)
			delta, _ := choice["delta"].(map[string]any)
			calls, _ := delta["tool_calls"].([]any)
			for _, raw := range calls {
				call, ok := raw.(map[string]any)
				require.True(t, ok, "tool call = %#v", raw)
				function, _ := call["function"].(map[string]any)
				name, _ := function["name"].(string)
				ids[name], _ = call["id"].(string)
			}
		}
	}
	assert.Equal(t, "call_0", ids["second"], "Gemini's own ID is kept")
	assert.NotEqual(t, "call_0", ids["first"])
	assert.NotEmpty(t, ids["first"])
	assert.Len(t, streamImageDeltas(t, chunks), 1)
}
