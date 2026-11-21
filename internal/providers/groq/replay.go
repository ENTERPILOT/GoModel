package groq

import (
	"log/slog"
	"slices"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
)

// rejectedAssistantMembers are members OpenAI returns on an assistant message
// that Groq's message schema does not define. Replaying such a message fails
// with 400 "property 'refusal' is unsupported" (verified November 2026, on
// every model: the schema is checked before the model is looked up).
var rejectedAssistantMembers = []string{"refusal", "annotations", "audio"}

// adaptReplayedMessages makes an assistant turn that came out of a chat
// completion fit to send back to Groq:
//
//   - refusal, annotations and audio are dropped.
//   - function_call is dropped when it is null. Groq defines the member, so a
//     legacy function call keeps it, but it answers "Value is not nullable"
//     to null.
//   - reasoning_content, the name the gateway returns Groq's own reasoning
//     under, goes back as reasoning (see replayReasoning).
func adaptReplayedMessages(req *core.ChatRequest) (*core.ChatRequest, error) {
	adapted := providers.DropMessageMembers(req, "assistant", rejectedAssistantMembers...)
	adapted = providers.DropNullMessageMembers(adapted, "assistant", "function_call")
	return replayReasoning(adapted)
}

// replayReasoning undoes the response rename on replayed assistant messages.
// Groq rejects reasoning_content ("property 'reasoning_content' is
// unsupported"), which is what a client holds after reading a Groq response
// through the gateway and what the Responses translation attaches to a
// tool-call turn. On the reasoning families the trace is sent as reasoning,
// unless the client already sent that member; other models answer 400
// "reasoning is not supported with this model", so there it is dropped.
func replayReasoning(req *core.ChatRequest) (*core.ChatRequest, error) {
	rename := supportsReasoningFormat(req.Model)
	var adapted *core.ChatRequest
	for i := range req.Messages {
		message := &req.Messages[i]
		if message.Role != "assistant" {
			continue
		}
		trace := message.ExtraFields.Lookup(canonicalKey)
		if trace == nil {
			continue
		}
		extra := message.ExtraFields.Without(canonicalKey)
		if rename && extra.Lookup(groqReasoningKey) == nil {
			merged, err := core.MergeUnknownJSONFields(extra, map[string]json.RawMessage{groqReasoningKey: trace})
			if err != nil {
				return nil, core.NewInvalidRequestError("failed to adapt replayed reasoning for Groq: "+err.Error(), err)
			}
			extra = merged
		}
		if adapted == nil {
			copied := *req
			copied.Messages = slices.Clone(req.Messages)
			adapted = &copied
		}
		adapted.Messages[i].ExtraFields = extra
	}
	if adapted == nil {
		return req, nil
	}
	slog.Debug("adapted replayed reasoning_content for Groq", "model", req.Model, "sent_as_reasoning", rename)
	return adapted, nil
}
