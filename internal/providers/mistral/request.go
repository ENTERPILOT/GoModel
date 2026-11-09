package mistral

import (
	"bytes"
	"slices"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
)

// Mistral's chat schema sets additionalProperties: false on the request and on
// every message, so members OpenAI clients commonly send are adapted on the
// typed request instead of failing the call with a 422.

// adaptedRequestFields are the unknown top-level members adaptTopLevelExtras
// renames or removes: max_completion_tokens and seed have Mistral spellings,
// the rest have no Mistral counterpart.
var adaptedRequestFields = []string{
	"max_completion_tokens",
	"seed",
	"logit_bias",
	"store",
	"verbosity",
	"safety_identifier",
}

// replayedReasoningKey is the vendor spelling of a replayed reasoning trace
// some clients send instead of reasoning_content (OpenRouter, vLLM).
const replayedReasoningKey = "reasoning"

// adaptChatRequest applies Mistral's request dialect to the typed request:
//
//   - The nested reasoning object becomes the flat reasoning_effort string,
//     lowercased and otherwise passed through: which levels a model accepts
//     is model-specific (Mistral Large 4 takes low/medium/high, hosted GLM
//     models take low/high/max). An effort-less object (summary only) is
//     dropped, because Mistral has no nested reasoning parameter.
//   - stream_options and user are dropped. Mistral reports usage in the
//     final stream chunk on its own.
//   - service_tier maps to Mistral's auto/standard_only.
//   - max_completion_tokens becomes max_tokens and seed becomes random_seed;
//     OpenAI-only members such as logit_bias and store are dropped.
//   - Each message keeps only the members Mistral's schema allows for its
//     role (see allowedMessageMembers).
//
// It returns req itself when nothing changes. A replayed reasoning trace is
// turned into a Mistral thinking chunk later, by adaptChatWire, because the
// typed content cannot carry that chunk type.
func adaptChatRequest(req *core.ChatRequest) (*core.ChatRequest, error) {
	if req == nil {
		return nil, nil
	}
	out := req
	if req.Reasoning != nil {
		effort := strings.ToLower(strings.TrimSpace(req.Reasoning.Effort))
		if effort == "" {
			out = providers.DropReasoning(req)
		} else {
			var err error
			if out, err = providers.AdaptReasoningEffortRequest(req, effort); err != nil {
				return nil, err
			}
		}
	}
	tier := mistralServiceTier(out.ServiceTier)
	extra, maxTokens, extraChanged, err := adaptTopLevelExtras(out.ExtraFields, out.MaxTokens)
	if err != nil {
		return nil, err
	}
	messages := adaptMessages(out.Messages)
	if out.StreamOptions == nil && out.User == "" && tier == out.ServiceTier && !extraChanged && messages == nil {
		return out, nil
	}
	adapted := *out
	adapted.StreamOptions = nil
	adapted.User = ""
	adapted.ServiceTier = tier
	adapted.ExtraFields = extra
	adapted.MaxTokens = maxTokens
	if messages != nil {
		adapted.Messages = messages
	}
	return &adapted, nil
}

// mistralServiceTier maps service_tier to Mistral's enum. auto and
// standard_only stay; OpenAI's default maps to standard_only, because
// Mistral's auto may use priority capacity at a surcharge while default asks
// for standard behavior; any other value is dropped.
func mistralServiceTier(tier string) string {
	switch tier {
	case "", "auto", "standard_only":
		return tier
	case "default":
		return "standard_only"
	default:
		return ""
	}
}

// adaptTopLevelExtras renames or removes the adaptedRequestFields members. A
// max_completion_tokens value fills max_tokens when that is unset (null means
// no limit; a non-integer is rejected rather than silently lost), and seed
// becomes random_seed when the client did not send the Mistral spelling too.
func adaptTopLevelExtras(fields core.UnknownJSONFields, maxTokens *int) (core.UnknownJSONFields, *int, bool, error) {
	if !fields.HasAny(adaptedRequestFields...) {
		return fields, maxTokens, false, nil
	}
	if raw := fields.Lookup("max_completion_tokens"); raw != nil && maxTokens == nil {
		// A pointer keeps null as "no limit" instead of decoding it to 0.
		var limit *int
		if err := json.Unmarshal(raw, &limit); err != nil {
			return fields, maxTokens, false, core.NewInvalidRequestError("max_completion_tokens must be an integer", err)
		}
		maxTokens = limit
	}
	seed := fields.Lookup("seed")
	keepRandomSeed := fields.Lookup("random_seed") != nil
	fields = fields.Without(adaptedRequestFields...)
	if seed != nil && !keepRandomSeed {
		merged, err := core.MergeUnknownJSONFields(fields, map[string]json.RawMessage{"random_seed": seed})
		if err != nil {
			return fields, maxTokens, false, core.NewInvalidRequestError("failed to adapt seed for Mistral: "+err.Error(), err)
		}
		fields = merged
	}
	return fields, maxTokens, true, nil
}

// allowedMessageMembers lists the untyped members Mistral's schema accepts on
// a message of the given role. role, content, tool_calls and tool_call_id are
// typed core fields. reasoning_content is not a Mistral member: it is kept on
// assistant messages only until adaptChatWire turns it into a thinking chunk.
func allowedMessageMembers(role string) []string {
	switch role {
	case "assistant":
		return []string{"prefix", reasoningContentKey}
	case "tool":
		return []string{"name"}
	default:
		return nil
	}
}

// adaptMessages restricts every message to the members Mistral accepts. It
// returns nil when no message carries untyped members, the common case.
func adaptMessages(messages []core.Message) []core.Message {
	var adapted []core.Message
	for i := range messages {
		fields := messages[i].ExtraFields
		if fields.IsEmpty() {
			continue
		}
		if messages[i].Role == "assistant" {
			fields = canonicalReplayedReasoning(fields)
		}
		if adapted == nil {
			adapted = slices.Clone(messages)
		}
		adapted[i].ExtraFields = fields.Only(allowedMessageMembers(messages[i].Role)...)
	}
	return adapted
}

// canonicalReplayedReasoning leaves a replayed assistant turn with a
// non-empty reasoning_content member or none. A non-empty reasoning_content
// wins; otherwise an empty or null one is removed and the vendor reasoning
// spelling is adopted when it carries text.
func canonicalReplayedReasoning(fields core.UnknownJSONFields) core.UnknownJSONFields {
	if !fields.HasAny(reasoningContentKey, replayedReasoningKey) {
		return fields
	}
	if rawString(fields.Lookup(reasoningContentKey)) != "" {
		return fields
	}
	fields = fields.Without(reasoningContentKey)
	alt := fields.Lookup(replayedReasoningKey)
	if rawString(alt) == "" {
		return fields
	}
	merged, err := core.MergeUnknownJSONFields(fields, map[string]json.RawMessage{reasoningContentKey: alt})
	if err != nil {
		return fields
	}
	return merged
}

// adaptChatWire is the client RequestMutator that replays reasoning the way
// Mistral's guide asks: a prior assistant turn's reasoning_content becomes a
// thinking chunk first in its content ("do not strip ThinkChunk from
// assistant messages before replaying them"). The typed request cannot hold
// that chunk type (chat_content.go rejects it), so the marshaled body is
// patched and swapped in as a json.RawMessage, which round-trips verbatim on
// retries. Requests without replayed reasoning are left untouched and are
// not marshaled here.
func adaptChatWire(r *llmclient.Request) {
	req, ok := r.Body.(*core.ChatRequest)
	if !ok || !hasReplayedReasoning(req.Messages) {
		return
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return // the client's own marshal would fail with the same error
	}
	if patched := replayReasoningAsThinking(raw); patched != nil {
		r.Body = json.RawMessage(patched)
	}
}

// hasReplayedReasoning reports whether any assistant message still carries a
// reasoning_content member after adaptChatRequest.
func hasReplayedReasoning(messages []core.Message) bool {
	for i := range messages {
		if messages[i].Role == "assistant" && messages[i].ExtraFields.HasAny(reasoningContentKey) {
			return true
		}
	}
	return false
}

// replayReasoningAsThinking moves each assistant message's reasoning_content
// into a leading thinking chunk and removes the member, which Mistral rejects
// with a 422. It returns nil when nothing changed.
func replayReasoningAsThinking(body []byte) []byte {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		return nil
	}
	var messages []map[string]json.RawMessage
	if err := json.Unmarshal(top["messages"], &messages); err != nil {
		return nil
	}
	changed := false
	for _, message := range messages {
		raw, ok := message[reasoningContentKey]
		if !ok || rawString(message["role"]) != "assistant" {
			continue
		}
		delete(message, reasoningContentKey)
		if reasoning := rawString(raw); reasoning != "" {
			message["content"] = appendThinkingChunk(message["content"], reasoning)
		}
		changed = true
	}
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
