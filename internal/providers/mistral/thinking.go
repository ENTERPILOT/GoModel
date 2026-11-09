package mistral

import (
	"bytes"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
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

// chunkContentMarker is the cheap pre-check for the chunk flattening: a
// payload with no "content" member needs no rewrite. String deltas still
// carry a content member, so they take the full decode; the pre-check covers
// finish, usage, and tool-call chunks instead.
var chunkContentMarker = []byte(`"content"`)

// flattenThinkingChunks rewrites every choices[].message.content /
// choices[].delta.content chunk array in a chat completion payload (buffered
// or single SSE chunk) into a plain string content plus a reasoning_content
// member. It returns the original data when no content member is an array,
// and reports whether a rewrite happened.
func flattenThinkingChunks(data []byte) ([]byte, bool) {
	if !bytes.Contains(data, chunkContentMarker) {
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

// adaptChatRequest applies the request adaptations expressible in the typed
// request: GoModel's nested reasoning object becomes the flat reasoning_effort
// string. Mistral's API-wide enum matches the OpenAI-style levels, so the
// value is passed through unchanged; which levels a model accepts is
// model-specific (Mistral Large 4 takes low/medium/high, hosted GLM models
// take low/high/max), exactly as with Mistral's own SDKs. An effort-less
// reasoning object (summary only) is dropped: Mistral has no nested reasoning
// parameter and rejects unknown members with a 422.
func adaptChatRequest(req *core.ChatRequest) (*core.ChatRequest, error) {
	if req == nil {
		return req, nil
	}
	if req.Reasoning == nil {
		return req, nil
	}
	effort := strings.ToLower(strings.TrimSpace(req.Reasoning.Effort))
	if effort == "" {
		return providers.DropReasoning(req), nil
	}
	return providers.AdaptReasoningEffortRequest(req, effort)
}

// adaptChatWire is the client RequestMutator applying Mistral's request
// dialect on the marshaled chat body — the place the remaining quirks have to
// live, because the typed request cannot express a Mistral thinking chunk
// inside message content (chat_content.go rejects that part type). Body is
// swapped for the patched bytes as a json.RawMessage, which round-trips
// verbatim, so content type and retry replay are unaffected. Bodies without a
// messages member (embeddings, model listings) pass through entirely.
func adaptChatWire(r *llmclient.Request) {
	if r.Body == nil {
		return
	}
	raw, err := json.Marshal(r.Body)
	if err != nil {
		return // the client's own marshal would fail with the same error
	}
	if patched := adaptMistralChatBody(raw); patched != nil {
		r.Body = json.RawMessage(patched)
	}
}

// wirePatchMarkers lists the members adaptMistralChatBody rewrites; a body
// containing none of them needs no decode.
var wirePatchMarkers = [][]byte{
	[]byte(`"reasoning_content"`),
	[]byte(unknownChunksKey),
	[]byte(`"stream_options"`),
	[]byte(`"max_completion_tokens"`),
	[]byte(`"seed"`),
	[]byte(`"service_tier"`),
	// A raw member preserved verbatim from a client body may spell a key with
	// \u escapes ("\u0073eed"), which the plain markers above miss; any JSON
	// unicode escape contains this byte pair, so such bodies take the full
	// parse. False positives only cost the decode.
	[]byte(`\u`),
}

// wireBodyNeedsPatch is the cheap pre-check sparing typical chat bodies (none
// of the markers present) the full decode/re-encode round trip.
func wireBodyNeedsPatch(body []byte) bool {
	for _, marker := range wirePatchMarkers {
		if bytes.Contains(body, marker) {
			return true
		}
	}
	return false
}

// adaptMistralChatBody patches one marshaled chat completion body:
//
//   - A replayed assistant turn keeps its reasoning: Mistral's guide says to
//     always replay the full assistant message including its ThinkChunk, and
//     stripping the trace "significantly degrades" output. reasoning_content
//     becomes that thinking chunk first in content, and the member itself is
//     removed, because Mistral rejects it with a 422. The mistral_content_chunks
//     vendor member is dropped: those chunks carry no reasoning and (unlike
//     text parts) may not be valid on a request.
//   - Mistral's schema sets additionalProperties: false, so members OpenAI
//     clients send that Mistral does not know are removed or renamed instead
//     of 422ing: stream_options (usage arrives in the final chunk upstream),
//     max_completion_tokens → max_tokens, seed → random_seed, and
//     service_tier normalized to Mistral's auto/standard_only.
//
// It returns nil when nothing needed patching, leaving the original typed
// marshal on the wire.
func adaptMistralChatBody(body []byte) []byte {
	if !wireBodyNeedsPatch(body) {
		return nil
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		return nil
	}
	messagesRaw, ok := top["messages"]
	if !ok {
		return nil
	}
	var messages []map[string]json.RawMessage
	if err := json.Unmarshal(messagesRaw, &messages); err != nil {
		return nil
	}
	changed := false
	for _, message := range messages {
		if adaptReplayedAssistantMessage(message) {
			changed = true
		}
	}
	changed = adaptTopLevelMembers(top) || changed
	if !changed {
		return nil
	}
	encodedMessages, err := json.Marshal(messages)
	if err != nil {
		return nil
	}
	top["messages"] = encodedMessages
	patched, err := json.Marshal(top)
	if err != nil {
		return nil
	}
	return patched
}

// adaptReplayedAssistantMessage rewrites one message in place and reports
// whether it changed: a non-assistant message, or an assistant message with
// nothing to adapt, is left untouched. reasoning_content is removed whenever
// it is present, even empty or null — Mistral's schema forbids the member —
// but the thinking chunk is only injected for a non-empty trace.
func adaptReplayedAssistantMessage(message map[string]json.RawMessage) bool {
	if rawString(message["role"]) != "assistant" {
		return false
	}
	reasoning := rawString(message[reasoningContentKey])
	_, hasReasoning := message[reasoningContentKey]
	_, hasVendor := message[unknownChunksKey]
	if !hasReasoning && !hasVendor {
		return false
	}
	delete(message, reasoningContentKey)
	delete(message, unknownChunksKey)
	if reasoning != "" {
		message["content"] = appendThinkingChunk(message["content"], reasoning)
	}
	return true
}

// appendThinkingChunk builds the replayed content: the thinking chunk first,
// then the original content (a string becomes a text chunk, an array stays as
// it is — its text parts are Mistral TextChunks as well).
func appendThinkingChunk(content json.RawMessage, reasoning string) json.RawMessage {
	var chunks []json.RawMessage
	trimmed := bytes.TrimSpace(content)
	switch {
	case len(trimmed) == 0 || string(trimmed) == "null":
		// nothing else to replay
	case trimmed[0] == '[':
		if err := json.Unmarshal(trimmed, &chunks); err != nil {
			chunks = nil
		}
	default:
		if text := rawString(trimmed); text != "" {
			if textChunk, err := json.Marshal(map[string]string{"type": chunkTypeText, "text": text}); err == nil {
				chunks = append(chunks, textChunk)
			}
		}
	}
	thinking, err := json.Marshal(map[string]any{
		"type":     chunkTypeThinking,
		"thinking": []map[string]string{{"type": chunkTypeText, "text": reasoning}},
	})
	if err != nil {
		return content
	}
	out := make([]json.RawMessage, 0, len(chunks)+1)
	out = append(out, thinking)
	out = append(out, chunks...)
	encoded, err := json.Marshal(out)
	if err != nil {
		return content
	}
	return encoded
}

// adaptTopLevelMembers removes or renames request members outside the
// messages array, reporting whether anything changed.
func adaptTopLevelMembers(top map[string]json.RawMessage) bool {
	changed := false
	if _, ok := top["stream_options"]; ok {
		delete(top, "stream_options")
		changed = true
	}
	if changeKey(top, "max_completion_tokens", "max_tokens") {
		changed = true
	}
	if changeKey(top, "seed", "random_seed") {
		changed = true
	}
	if adaptServiceTier(top) {
		changed = true
	}
	return changed
}

// changeKey renames from into to when from is present and to is not.
func changeKey(top map[string]json.RawMessage, from, to string) bool {
	if _, ok := top[from]; !ok {
		return false
	}
	if _, ok := top[to]; ok {
		delete(top, from)
		return true
	}
	top[to] = top[from]
	delete(top, from)
	return true
}

// adaptServiceTier normalizes service_tier to Mistral's enum: auto and
// standard_only stay, OpenAI's default maps to standard_only (auto may select
// priority capacity at a 75% surcharge, while default asks for standard
// behavior), and any other value is dropped rather than 422ing.
func adaptServiceTier(top map[string]json.RawMessage) bool {
	raw, ok := top["service_tier"]
	if !ok {
		return false
	}
	switch tier := rawString(raw); tier {
	case "auto", "standard_only":
		return false
	case "default":
		top["service_tier"] = json.RawMessage(`"standard_only"`)
	default:
		delete(top, "service_tier")
	}
	return true
}
