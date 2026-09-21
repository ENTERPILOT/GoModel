package minimax

import (
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

// MiniMax's reasoning models keep the answer content clean only when the
// request asks them to split the chain of thought out of it: with
// reasoning_split set, the reasoning arrives in reasoning_details and
// content carries just the answer.
const (
	reasoningSplitKey     = "reasoning_split"
	reasoningDetailsKey   = "reasoning_details"
	canonicalReasoningKey = "reasoning_content"
)

// isReasoningModel reports whether the model is one of MiniMax's reasoning
// models, matched case-insensitively against an exact allowlist. The other
// MiniMax models reject reasoning_split as an unknown member, so the
// adaptation must not fire for them.
func isReasoningModel(model string) bool {
	switch strings.ToLower(model) {
	case
		"minimax-m2",
		"minimax-m2.5",
		"minimax-m2.5-highspeed",
		"minimax-m2.7",
		"minimax-m2.7-highspeed",
		"minimax-m3":
		return true
	default:
		return false
	}
}

// adaptChatRequest merges MiniMax's reasoning_split flag into the request's
// unknown members on the reasoning models. A caller that set the flag keeps
// its own value; any other request is returned unchanged.
func adaptChatRequest(req *core.ChatRequest) *core.ChatRequest {
	if req == nil || !isReasoningModel(req.Model) || req.ExtraFields.Lookup(reasoningSplitKey) != nil {
		return req
	}
	extra, err := core.MergeUnknownJSONFields(req.ExtraFields, map[string]json.RawMessage{
		reasoningSplitKey: json.RawMessage(`true`),
	})
	if err != nil {
		return req
	}
	adapted := *req
	adapted.ExtraFields = extra
	return &adapted
}

// concatenateReasoningDetails joins the text members of a reasoning_details
// value. It reports false when the value is not the expected array of
// objects, leaving such responses to pass through untouched.
func concatenateReasoningDetails(raw json.RawMessage) (string, bool) {
	var details []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &details); err != nil {
		return "", false
	}
	var joined strings.Builder
	for _, detail := range details {
		joined.WriteString(detail.Text)
	}
	return joined.String(), true
}

// normalizeChatResponse moves the reasoning of every choice onto the
// canonical reasoning_content member, where the Responses and Messages
// translation layers and the dashboard expect it. Messages without
// reasoning_details, and messages that already carry reasoning_content, are
// left alone; content is never touched.
func normalizeChatResponse(resp *core.ChatResponse) {
	if resp == nil {
		return
	}
	for i := range resp.Choices {
		normalizeReasoningMessage(&resp.Choices[i].Message)
	}
}

// normalizeReasoningMessage concatenates a message's reasoning_details texts
// into reasoning_content. A message that already carries reasoning_content
// wins, and a reasoning_details value of the wrong shape leaves the message
// untouched.
func normalizeReasoningMessage(msg *core.ResponseMessage) {
	raw := msg.ExtraFields.Lookup(reasoningDetailsKey)
	if raw == nil || msg.ExtraFields.Lookup(canonicalReasoningKey) != nil {
		return
	}
	reasoning, ok := concatenateReasoningDetails(raw)
	if !ok || reasoning == "" {
		return
	}
	merged, err := core.MergeUnknownJSONFields(msg.ExtraFields, map[string]json.RawMessage{
		canonicalReasoningKey: marshalRaw(reasoning),
	})
	if err != nil {
		return
	}
	msg.ExtraFields = merged
}

// marshalRaw re-encodes v, a value assembled only from strings and raw JSON
// members. json.Marshal on such a value cannot fail, so the error is dropped.
func marshalRaw(v any) json.RawMessage {
	encoded, _ := json.Marshal(v)
	return encoded
}
