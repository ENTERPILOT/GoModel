package providers

import (
	"log/slog"

	"github.com/goccy/go-json"
)

// FormatChatChunkSSE renders a single-choice OpenAI chat.completion.chunk as
// one SSE data line. It defines the chunk envelope emitted by native-protocol
// stream converters (Anthropic, Bedrock) so the OpenAI-compatible wire shape
// lives in one place. A nil usage omits the member; finishReason may be nil.
func FormatChatChunkSSE(id string, created int64, model, provider string, delta map[string]any, finishReason any, usage map[string]any) string {
	chunk := map[string]any{
		"id":       id,
		"object":   "chat.completion.chunk",
		"created":  created,
		"model":    model,
		"provider": provider,
		"choices": []map[string]any{
			{
				"index":         0,
				"delta":         delta,
				"finish_reason": finishReason,
			},
		},
	}
	if usage != nil {
		chunk["usage"] = usage
	}

	jsonData, err := json.Marshal(chunk)
	if err != nil {
		slog.Error("failed to marshal chat completion chunk", "error", err, "id", id, "provider", provider)
		return ""
	}
	return "data: " + string(jsonData) + "\n\n"
}
