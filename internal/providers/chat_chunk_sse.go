package providers

import (
	"log/slog"

	"github.com/goccy/go-json"
)

type chatChunkChoice struct {
	Index        int            `json:"index"`
	Delta        map[string]any `json:"delta"`
	FinishReason any            `json:"finish_reason"`
}

type chatChunkEnvelope struct {
	ID       string             `json:"id"`
	Object   string             `json:"object"`
	Created  int64              `json:"created"`
	Model    string             `json:"model"`
	Provider string             `json:"provider"`
	Choices  [1]chatChunkChoice `json:"choices"`
	Usage    map[string]any     `json:"usage,omitempty"`
}

// FormatChatChunkSSE renders a single-choice OpenAI chat.completion.chunk as
// one SSE data line. It defines the chunk envelope emitted by native-protocol
// stream converters (Anthropic, Bedrock) so the OpenAI-compatible wire shape
// lives in one place. A nil usage omits the member; finishReason may be nil.
func FormatChatChunkSSE(id string, created int64, model, provider string, delta map[string]any, finishReason any, usage map[string]any) string {
	chunk := chatChunkEnvelope{
		ID:       id,
		Object:   "chat.completion.chunk",
		Created:  created,
		Model:    model,
		Provider: provider,
		Choices: [1]chatChunkChoice{{
			Delta:        delta,
			FinishReason: finishReason,
		}},
		Usage: usage,
	}

	jsonData, err := json.Marshal(&chunk)
	if err != nil {
		slog.Error("failed to marshal chat completion chunk", "error", err, "id", id, "provider", provider)
		return ""
	}
	return "data: " + string(jsonData) + "\n\n"
}
