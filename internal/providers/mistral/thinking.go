package mistral

import (
	"bytes"
	"strings"

	"github.com/goccy/go-json"
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

// contentKey is the member hasContentArray looks for.
var contentKey = []byte(`"content"`)

// hasContentArray is the cheap pre-check for the chunk flattening: only a
// payload with a "content" member whose value is an array needs a rewrite.
// Plain string deltas, which make up most of a stream, skip the decode. A
// false positive (the byte pattern inside a string value) only costs the
// decode.
func hasContentArray(data []byte) bool {
	for {
		i := bytes.Index(data, contentKey)
		if i < 0 {
			return false
		}
		data = data[i+len(contentKey):]
		rest := bytes.TrimLeft(data, " \t\r\n")
		if len(rest) == 0 || rest[0] != ':' {
			continue
		}
		rest = bytes.TrimLeft(rest[1:], " \t\r\n")
		if len(rest) > 0 && rest[0] == '[' {
			return true
		}
	}
}

// flattenThinkingChunks rewrites every choices[].message.content /
// choices[].delta.content chunk array in a chat completion payload (buffered
// or single SSE chunk) into a plain string content plus a reasoning_content
// member. It returns the original data when no content member is an array,
// and reports whether a rewrite happened.
func flattenThinkingChunks(data []byte) ([]byte, bool) {
	if !hasContentArray(data) {
		return data, false
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return data, false
	}
	choicesRaw, ok := top["choices"]
	if !ok {
		return data, false
	}
	var choices []map[string]json.RawMessage
	if err := json.Unmarshal(choicesRaw, &choices); err != nil {
		return data, false
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
		return data, false
	}
	encodedChoices, err := json.Marshal(choices)
	if err != nil {
		return data, false
	}
	top["choices"] = encodedChoices
	out, err := json.Marshal(top)
	if err != nil {
		return data, false
	}
	return out, true
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
