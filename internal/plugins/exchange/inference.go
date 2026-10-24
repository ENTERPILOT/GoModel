package exchange

import (
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/pluginapi"
)

// ChatRequestFromMessages builds the chat request for a plugin's internal
// inference call. Parts a chat message cannot carry are reduced to their
// text.
func ChatRequestFromMessages(model string, msgs []pluginapi.Message, maxTokens int, temperature *float64) *core.ChatRequest {
	req := &core.ChatRequest{Model: model, Temperature: temperature}
	if maxTokens > 0 {
		req.MaxTokens = &maxTokens
	}
	req.Messages = make([]core.Message, 0, len(msgs))
	for _, m := range msgs {
		encoded, err := encodeChatMessage(m)
		if err != nil {
			encoded = core.Message{Role: string(m.Role), Content: m.Text(), ToolCallID: m.ToolCallID}
		}
		req.Messages = append(req.Messages, encoded)
	}
	return req
}
