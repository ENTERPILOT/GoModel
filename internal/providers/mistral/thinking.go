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
	// unknownChunksKey carries chunk types GoModel does not model (for example
	// Mistral's "reference" chunk) on the message/delta as a vendor member, so
	// they survive the round trip without breaking the typed content decode,
	// which rejects unsupported part types and would fail the whole response.
	unknownChunksKey = "mistral_content_chunks"
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
			content, reasoning, unknown, ok := flattenContent(msg["content"])
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
			if len(unknown) > 0 {
				encoded, err := json.Marshal(unknown)
				if err != nil {
					continue
				}
				msg[unknownChunksKey] = encoded
			} else {
				delete(msg, unknownChunksKey)
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
// string, and chunk types GoModel does not model are returned separately so
// the caller can keep them off the typed content decode (which would reject
// them and fail the whole response).
func flattenContent(raw json.RawMessage) (content json.RawMessage, reasoning string, unknown []json.RawMessage, ok bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return nil, "", nil, false
	}
	var chunks []json.RawMessage
	if err := json.Unmarshal(trimmed, &chunks); err != nil {
		return nil, "", nil, false
	}
	var text, thinking strings.Builder
	for _, chunkRaw := range chunks {
		var chunk map[string]json.RawMessage
		if err := json.Unmarshal(chunkRaw, &chunk); err != nil {
			unknown = append(unknown, chunkRaw)
			continue
		}
		switch rawString(chunk["type"]) {
		case chunkTypeText:
			text.WriteString(rawString(chunk["text"]))
		case chunkTypeThinking:
			thinking.WriteString(thinkingText(chunk["thinking"]))
		default:
			unknown = append(unknown, chunkRaw)
		}
	}
	content, err := json.Marshal(text.String())
	if err != nil {
		return nil, "", nil, false
	}
	return content, thinking.String(), unknown, true
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
	if req.Reasoning != nil {
		effort := strings.ToLower(strings.TrimSpace(req.Reasoning.Effort))
		if effort == "" {
			// Mistral has no nested reasoning parameter and rejects unknown
			// members with a 422, so an effort-less object (summary only) is
			// dropped rather than forwarded.
			adapted = providers.DropReasoning(adapted)
		} else {
			var err error
			adapted, err = providers.AdaptReasoningEffortRequest(adapted, effort)
			if err != nil {
				return nil, err
			}
		}
	}
	return stripAssistantReasoning(adapted), nil
}

// stripAssistantReasoning returns a copy of req without the members of a
// replayed assistant turn that Mistral rejects with a 422: the flat
// reasoning_content a client echoes back from an earlier reply, and the
// mistral_content_chunks vendor member this provider adds to it. The answer
// text in content is a valid assistant message on its own.
func stripAssistantReasoning(req *core.ChatRequest) *core.ChatRequest {
	var adapted *core.ChatRequest
	for i, message := range req.Messages {
		if message.Role != "assistant" {
			continue
		}
		reasoning := message.ExtraFields.Lookup(reasoningContentKey)
		chunks := message.ExtraFields.Lookup(unknownChunksKey)
		if reasoning == nil && chunks == nil {
			continue
		}
		if adapted == nil {
			copied := *req
			copied.Messages = append([]core.Message(nil), req.Messages...)
			adapted = &copied
		}
		adapted.Messages[i].ExtraFields = adapted.Messages[i].ExtraFields.Without(reasoningContentKey).Without(unknownChunksKey)
	}
	if adapted == nil {
		return req
	}
	return adapted
}
