package providers

import (
	"strings"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/require"
)

func decodeChunkSSE(t *testing.T, line string) map[string]any {
	t.Helper()
	payload, ok := strings.CutPrefix(line, "data: ")
	require.True(t, ok)
	require.True(t, strings.HasSuffix(payload, "\n\n"), "chunk = %q, want data: <json>\\n\\n framing", line)

	var chunk map[string]any
	err := json.Unmarshal([]byte(strings.TrimSuffix(payload, "\n\n")), &chunk)
	require.NoError(t, err)

	return chunk
}

func TestFormatChatChunkSSE(t *testing.T) {
	chunk := decodeChunkSSE(t, FormatChatChunkSSE(
		"chunk-1", 1700000000, "claude-3", "anthropic",
		map[string]any{"content": "hi"}, nil, nil,
	))
	got := chunk["id"]
	require.Equal(t, "chunk-1", got)
	got = chunk["object"]
	require.Equal(t, "chat.completion.chunk", got)
	got = chunk["created"]
	require.Equal(t, float64(1700000000), got)
	got = chunk["model"]
	require.Equal(t, "claude-3", got)
	got = chunk["provider"]
	require.Equal(t, "anthropic", got)
	_, present := chunk["usage"]
	require.False(t, present)

	choices, ok := chunk["choices"].([]any)
	require.True(t, ok)
	require.Len(t, choices, 1)

	choice := choices[0].(map[string]any)
	got = choice["index"]
	require.Equal(t, float64(0), got)
	require.Nil(t, choice["finish_reason"])

	delta, _ := choice["delta"].(map[string]any)
	got = delta["content"]
	require.Equal(t, "hi", got)
}

func TestFormatChatChunkSSEWithFinishReasonAndUsage(t *testing.T) {
	chunk := decodeChunkSSE(t, FormatChatChunkSSE(
		"chunk-2", 1700000000, "claude-3", "anthropic",
		map[string]any{}, "stop",
		map[string]any{"prompt_tokens": 5, "completion_tokens": 2, "total_tokens": 7},
	))

	choice := chunk["choices"].([]any)[0].(map[string]any)
	got := choice["finish_reason"]
	require.Equal(t, "stop", got)

	usage, ok := chunk["usage"].(map[string]any)
	require.True(t, ok, "usage = %#v, want object", chunk["usage"])
	got = usage["total_tokens"]
	require.Equal(t, float64(7), got)
}
