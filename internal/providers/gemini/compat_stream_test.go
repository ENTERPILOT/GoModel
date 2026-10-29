package gemini

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

// Recorded from Google's OpenAI-compatible endpoint (gemini-4-argon, October
// 2026): streamed tool_calls carry no index and the turn finishes with "stop".
const compatToolCallStream = `data: {"choices":[{"delta":{"role":"assistant","tool_calls":[{"extra_content":{"google":{"thought_signature":"sig-1"}},"function":{"arguments":"{\"city\":\"Paris\"}","name":"get_weather"},"id":"call_1","type":"function"}]},"index":0}],"created":1,"id":"d-1","model":"gemini-4-argon","object":"chat.completion.chunk"}

data: {"choices":[{"delta":{"role":"assistant","tool_calls":[{"function":{"arguments":"{\"city\":\"Berlin\"}","name":"get_weather"},"id":"call_2","type":"function"}]},"index":0}],"created":1,"id":"d-1","model":"gemini-4-argon","object":"chat.completion.chunk"}

data: {"choices":[{"delta":{"role":"assistant"},"finish_reason":"stop","index":0}],"created":1,"id":"d-1","model":"gemini-4-argon","object":"chat.completion.chunk"}

data: [DONE]
`

func TestStreamChatCompletion_CompatRepairsToolCallChunks(t *testing.T) {
	t.Setenv(useNativeAPIEnvVar, "false")
	server, _ := providertest.SSEServer(t, compatToolCallStream)
	provider := New(providers.ProviderConfig{APIKey: "key", BaseURL: server.URL}, providertest.Options(llmclient.Hooks{})).(*Provider)

	stream, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "gemini-4-argon",
		Messages: []core.Message{{Role: "user", Content: "weather?"}},
	})
	require.NoError(t, err)
	defer func() { _ = stream.Close() }()
	raw, err := io.ReadAll(stream)
	require.NoError(t, err)

	chunks := parseOpenAIStreamChunks(t, string(raw))
	require.Len(t, chunks, 3)
	var indexes []any
	for _, chunk := range chunks[:2] {
		choice := chunk["choices"].([]any)[0].(map[string]any)
		for _, call := range choice["delta"].(map[string]any)["tool_calls"].([]any) {
			indexes = append(indexes, call.(map[string]any)["index"])
		}
	}
	assert.Equal(t, []any{float64(0), float64(1)}, indexes)
	first := chunks[0]["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)
	assert.Contains(t, first, "extra_content", "the thought signature survives the re-encode")
	last := chunks[2]["choices"].([]any)[0].(map[string]any)
	assert.Equal(t, "tool_calls", last["finish_reason"])
	assert.True(t, strings.HasSuffix(string(raw), "data: [DONE]\n"), "terminator is kept")
}

func TestStreamChatCompletion_CompatPassesTextChunksThrough(t *testing.T) {
	t.Setenv(useNativeAPIEnvVar, "false")
	const body = "data: {\"id\":\"d-1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"d-1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"
	server, _ := providertest.SSEServer(t, body)
	provider := New(providers.ProviderConfig{APIKey: "key", BaseURL: server.URL}, providertest.Options(llmclient.Hooks{})).(*Provider)

	stream, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "gemini-4-argon",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)
	defer func() { _ = stream.Close() }()
	raw, err := io.ReadAll(stream)
	require.NoError(t, err)
	assert.Equal(t, body, string(raw), "a text stream is forwarded byte for byte")
}

func TestRepairCompatChunkLine(t *testing.T) {
	choices := map[int]*compatChoiceState{}
	line, changed := repairCompatChunkLine(`data: {"choices":[{"index":1,"delta":{"tool_calls":[{"index":4,"id":"a"},{"id":"b"}]}}]}`, choices)
	require.True(t, changed)
	assert.Contains(t, line, `"index":4`, "an index the upstream set is kept")
	assert.Contains(t, line, `{"id":"b","index":0}`, "the next unindexed call takes the per-choice counter")

	line, changed = repairCompatChunkLine(`data: {"choices":[{"index":1,"delta":{"tool_calls":[{"index":1,"id":"c"},{"id":"d"}]}}]}`, choices)
	require.True(t, changed)
	assert.Contains(t, line, `{"id":"d","index":2}`, "an index the upstream used is skipped")

	line, changed = repairCompatChunkLine(`data: {"choices":[{"index":2,"delta":{"tool_calls":[{"id":"e"},{"index":0,"id":"f"}]}}]}`, choices)
	require.True(t, changed)
	assert.Contains(t, line, `{"id":"e","index":1}`, "an explicit index later in the chunk is reserved first")
	assert.Contains(t, line, `{"id":"f","index":0}`)

	_, changed = repairCompatChunkLine(`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`, choices)
	assert.False(t, changed, "choice 0 never saw tool calls")

	line, changed = repairCompatChunkLine(`data: {"choices":[{"index":1,"delta":{},"finish_reason":"stop"}]}`, choices)
	require.True(t, changed)
	assert.Contains(t, line, `"finish_reason":"tool_calls"`)

	_, changed = repairCompatChunkLine(`event: ping`, choices)
	assert.False(t, changed)
	_, changed = repairCompatChunkLine(`data: not json "tool_calls"`, choices)
	assert.False(t, changed)
}
