package mistral

import (
	"bytes"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
)

// Mistral content chunk types on reasoning models: the answer arrives as
// {"type":"text","text":...} chunks and the reasoning trace as
// {"type":"thinking","thinking":[{"type":"text","text":...}]} chunks.
const (
	chunkTypeText     = "text"
	chunkTypeThinking = "thinking"
	// reasoningContentKey is the standard OpenAI-compatible extension member
	// chat clients (SillyTavern, Open WebUI, ...) render as the thinking block.
	reasoningContentKey = "reasoning_content"
)

// flattenThinkingChunks rewrites every choices[].message.content /
// choices[].delta.content chunk array in a chat completion payload (buffered
// or single SSE chunk) into a plain string content plus a reasoning_content
// member. It returns data unchanged when no content member is an array.
func flattenThinkingChunks(data []byte) []byte {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return data
	}
	choicesRaw, ok := top["choices"]
	if !ok {
		return data
	}
	var choices []map[string]json.RawMessage
	if err := json.Unmarshal(choicesRaw, &choices); err != nil {
		return data
	}
	changed := false
	for _, choice := range choices {
		for _, member := range []string{"message", "delta"} {
			msgRaw, ok := choice[member]
			if !ok {
				continue
			}
			var msg map[string]json.RawMessage
			if err := json.Unmarshal(msgRaw, &msg); err != nil {
				continue
			}
			content, reasoning, ok := flattenContent(msg["content"])
			if !ok {
				continue
			}
			if reasoning != "" {
				encoded, err := json.Marshal(reasoning)
				if err != nil {
					continue
				}
				msg[reasoningContentKey] = encoded
			}
			msg["content"] = content
			encodedMsg, err := json.Marshal(msg)
			if err != nil {
				continue
			}
			choice[member] = encodedMsg
			changed = true
		}
	}
	if !changed {
		return data
	}
	encodedChoices, err := json.Marshal(choices)
	if err != nil {
		return data
	}
	top["choices"] = encodedChoices
	out, err := json.Marshal(top)
	if err != nil {
		return data
	}
	return out
}

// flattenContent converts one Mistral content member. A non-array content
// (plain string or null, which reasoning_effort "none" produces) is left
// alone: ok is false. An array is flattened: text chunks join into the
// returned content string, thinking chunks join into the returned reasoning
// string, and chunk types GoModel does not model are preserved verbatim in the
// content array so no upstream data is dropped.
func flattenContent(raw json.RawMessage) (content json.RawMessage, reasoning string, ok bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return nil, "", false
	}
	var chunks []json.RawMessage
	if err := json.Unmarshal(trimmed, &chunks); err != nil {
		return nil, "", false
	}
	var text, thinking strings.Builder
	var rest []json.RawMessage
	for _, chunkRaw := range chunks {
		var chunk map[string]json.RawMessage
		if err := json.Unmarshal(chunkRaw, &chunk); err != nil {
			rest = append(rest, chunkRaw)
			continue
		}
		switch rawString(chunk["type"]) {
		case chunkTypeText:
			text.WriteString(rawString(chunk["text"]))
		case chunkTypeThinking:
			thinking.WriteString(thinkingText(chunk["thinking"]))
		default:
			rest = append(rest, chunkRaw)
		}
	}
	content, err := encodeFlattenedContent(text.String(), rest)
	if err != nil {
		return nil, "", false
	}
	return content, thinking.String(), true
}

// encodeFlattenedContent renders the flattened content: a plain string when no
// unrecognized chunks remain, otherwise an array with the text part first.
func encodeFlattenedContent(text string, rest []json.RawMessage) (json.RawMessage, error) {
	if len(rest) == 0 {
		return json.Marshal(text)
	}
	parts := make([]json.RawMessage, 0, len(rest)+1)
	if text != "" {
		textPart, err := json.Marshal(map[string]any{"type": chunkTypeText, "text": text})
		if err != nil {
			return nil, err
		}
		parts = append(parts, textPart)
	}
	parts = append(parts, rest...)
	return json.Marshal(parts)
}

// thinkingText extracts the reasoning text from a ThinkChunk's thinking member:
// a list of text chunks per the API, tolerated as a plain string as well.
func thinkingText(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return ""
	}
	if trimmed[0] == '"' {
		return rawString(trimmed)
	}
	if trimmed[0] != '[' {
		return ""
	}
	var parts []json.RawMessage
	if err := json.Unmarshal(trimmed, &parts); err != nil {
		return ""
	}
	var sb strings.Builder
	for _, partRaw := range parts {
		trimmedPart := bytes.TrimSpace(partRaw)
		if len(trimmedPart) == 0 {
			continue
		}
		if trimmedPart[0] == '"' {
			sb.WriteString(rawString(trimmedPart))
			continue
		}
		var part map[string]json.RawMessage
		if err := json.Unmarshal(trimmedPart, &part); err != nil {
			continue
		}
		sb.WriteString(rawString(part["text"]))
	}
	return sb.String()
}

// rawString decodes a raw JSON string member; non-strings decode to "".
func rawString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var value string
	_ = json.Unmarshal(raw, &value)
	return value
}

// adaptChatRequest applies Mistral's chat dialect to an outgoing request:
//   - GoModel's nested reasoning object becomes the flat reasoning_effort
//     string. Mistral's API-wide enum matches the OpenAI-style levels, so the
//     value is passed through unchanged; which levels a model accepts is
//     model-specific (Mistral Large 4 takes low/medium/high, hosted GLM
//     models take low/high/max), exactly as with Mistral's own SDKs.
//   - A replayed assistant turn's reasoning_content member is dropped:
//     Mistral rejects the unknown member with a 422, and the answer text in
//     content is a valid assistant message on its own.
func adaptChatRequest(req *core.ChatRequest) (*core.ChatRequest, error) {
	if req == nil {
		return req, nil
	}
	adapted := req
	var err error
	if req.Reasoning != nil && strings.TrimSpace(req.Reasoning.Effort) != "" {
		adapted, err = providers.AdaptReasoningEffortRequest(adapted, strings.ToLower(strings.TrimSpace(req.Reasoning.Effort)))
		if err != nil {
			return nil, err
		}
	}
	return stripAssistantReasoning(adapted), nil
}

// stripAssistantReasoning returns a copy of req without the reasoning_content
// member on assistant messages, or req itself when there is nothing to strip.
func stripAssistantReasoning(req *core.ChatRequest) *core.ChatRequest {
	var adapted *core.ChatRequest
	for i, message := range req.Messages {
		if message.Role != "assistant" || message.ExtraFields.Lookup(reasoningContentKey) == nil {
			continue
		}
		if adapted == nil {
			copied := *req
			copied.Messages = append([]core.Message(nil), req.Messages...)
			adapted = &copied
		}
		adapted.Messages[i].ExtraFields = adapted.Messages[i].ExtraFields.Without(reasoningContentKey)
	}
	if adapted == nil {
		return req
	}
	return adapted
}
