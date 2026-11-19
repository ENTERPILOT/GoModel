package m365copilot

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

// chatBody is the body of the chat and chatOverStream actions.
// https://learn.microsoft.com/microsoft-365-copilot/extensibility/api/ai-services/chat/copilotconversation-chat
type chatBody struct {
	Message             requestMessage   `json:"message"`
	AdditionalContext   []contextMessage `json:"additionalContext,omitempty"`
	LocationHint        json.RawMessage  `json:"locationHint"`
	ContextualResources json.RawMessage  `json:"contextualResources,omitempty"`
}

type requestMessage struct {
	Text string `json:"text"`
}

// contextMessage is extra grounding for one turn; Copilot reads it but does
// not treat it as an earlier turn of the conversation.
type contextMessage struct {
	Description string `json:"description,omitempty"`
	Text        string `json:"text"`
}

// Chat API parameters a caller may set as extra request fields, forwarded
// unchanged: locationHint replaces the gateway's time zone, and
// contextualResources names OneDrive and SharePoint files or turns web
// grounding off for the turn.
const (
	locationHintField        = "locationHint"
	contextualResourcesField = "contextualResources"
)

// newChatBody maps an OpenAI chat request onto one Copilot turn. The Chat API
// takes a single text prompt, so the last message, which must be the user's,
// becomes the prompt, and every earlier message becomes additional context.
func newChatBody(req *core.ChatRequest, timeZone string) (*chatBody, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("chat request is required", nil)
	}
	if err := rejectUnsupported(req); err != nil {
		return nil, err
	}
	if len(req.Messages) == 0 {
		return nil, core.NewInvalidRequestError("messages must not be empty", nil).WithParam("messages")
	}

	body := &chatBody{}
	last := len(req.Messages) - 1
	for i, message := range req.Messages {
		role, text, err := messageText(message)
		if err != nil {
			return nil, core.NewInvalidRequestError(fmt.Sprintf("messages[%d]: %v", i, err), err).WithParam("messages")
		}
		if i == last {
			if role != "user" || strings.TrimSpace(text) == "" {
				return nil, core.NewInvalidRequestError(
					"the last message must be a user message with text; Microsoft 365 Copilot answers one user prompt per request",
					nil).WithParam("messages")
			}
			body.Message.Text = text
			continue
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		body.AdditionalContext = append(body.AdditionalContext, contextMessage{
			Description: contextDescriptions[role],
			Text:        text,
		})
	}

	var err error
	if body.LocationHint, err = extraObject(req, locationHintField); err != nil {
		return nil, err
	}
	if body.LocationHint == nil {
		if body.LocationHint, err = json.Marshal(map[string]string{"timeZone": timeZone}); err != nil {
			return nil, core.NewInvalidRequestError("failed to encode the location hint", err)
		}
	}
	if body.ContextualResources, err = extraObject(req, contextualResourcesField); err != nil {
		return nil, err
	}
	return body, nil
}

// contextDescriptions labels an earlier message by the role that wrote it.
var contextDescriptions = map[string]string{
	"system":    "System instructions",
	"user":      "Earlier user message",
	"assistant": "Earlier assistant reply",
}

// messageText returns a message's normalized role and its text, or an error
// for anything the Chat API has no place for.
func messageText(message core.Message) (role, text string, err error) {
	role = strings.ToLower(strings.TrimSpace(message.Role))
	switch role {
	case "developer":
		role = "system"
	case "system", "user":
	case "assistant":
		if len(message.ToolCalls) > 0 {
			return "", "", fmt.Errorf("tool calls are not supported by Microsoft 365 Copilot")
		}
	case "tool", "function":
		return "", "", fmt.Errorf("tool results are not supported by Microsoft 365 Copilot")
	default:
		return "", "", fmt.Errorf("role %q is not supported", message.Role)
	}

	content, err := core.NormalizeMessageContent(message.Content)
	if err != nil {
		return "", "", fmt.Errorf("content %w", err)
	}
	parts, ok := content.([]core.ContentPart)
	if !ok {
		text, _ = content.(string)
		return role, text, nil
	}
	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		if part.Type != "text" && part.Type != "input_text" {
			return "", "", fmt.Errorf("%s content is not supported; Microsoft 365 Copilot reads text only", part.Type)
		}
		texts = append(texts, part.Text)
	}
	return role, strings.Join(texts, "\n"), nil
}

// rejectUnsupported fails a request whose result would differ from what the
// caller asked for. Sampling and length settings (temperature, top_p,
// max_tokens, stop, seed, ...) are dropped instead: Copilot exposes none of
// them, and ignoring them still answers the prompt.
func rejectUnsupported(req *core.ChatRequest) error {
	if (len(req.Tools) > 0 && req.ToolChoice != "none") || req.ExtraFields.HasAny("functions") {
		return core.NewInvalidRequestError(
			"tools are not supported: Microsoft 365 Copilot's Chat API has no function calling", nil).WithParam("tools")
	}
	var n int
	if raw := req.ExtraFields.Lookup("n"); len(raw) > 0 && json.Unmarshal(raw, &n) == nil && n > 1 {
		return core.NewInvalidRequestError(
			"n > 1 is not supported: Microsoft 365 Copilot returns one answer per prompt", nil).WithParam("n")
	}
	var modalities []string
	if raw := req.ExtraFields.Lookup("modalities"); len(raw) > 0 && json.Unmarshal(raw, &modalities) == nil {
		for _, modality := range modalities {
			if modality != "text" {
				return core.NewInvalidRequestError(
					"modality "+modality+" is not supported: Microsoft 365 Copilot returns text only", nil).WithParam("modalities")
			}
		}
	}
	var format struct {
		Type string `json:"type"`
	}
	if raw := req.ExtraFields.Lookup("response_format"); len(raw) > 0 && json.Unmarshal(raw, &format) == nil {
		if format.Type != "" && format.Type != "text" {
			return core.NewInvalidRequestError(
				"response_format "+format.Type+" is not supported: Microsoft 365 Copilot returns plain text", nil).WithParam("response_format")
		}
	}
	return nil
}

// extraObject returns the caller's extra request field name as a JSON object,
// or nil when it is absent or null.
func extraObject(req *core.ChatRequest, name string) (json.RawMessage, error) {
	raw := bytes.TrimSpace(req.ExtraFields.Lookup(name))
	if len(raw) == 0 || core.IsJSONNull(raw) {
		return nil, nil
	}
	if raw[0] != '{' {
		return nil, core.NewInvalidRequestError(name+" must be a JSON object", nil).WithParam(name)
	}
	return raw, nil
}
