package exchange

import (
	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/pluginapi"
)

var ephemeralCacheControl = json.RawMessage(`{"type":"ephemeral"}`)

// patchChatMessage rewrites the edited parts of m inside the original chat
// message, keeping its content structure, extra fields, and tool-call
// envelopes.
func patchChatMessage(original core.Message, m pluginapi.Message) (core.Message, error) {
	out := cloneChatMessage(original)
	out.Role = string(m.Role)
	out.ToolCallID = m.ToolCallID
	if m.Role == pluginapi.RoleTool && out.ToolCallID == "" {
		out.ToolCallID = toolResultCallID(m)
	}

	contentParts, calls := splitParts(m)
	content, null, err := rewriteChatContent(original.Content, original.ContentNull, contentParts)
	if err != nil {
		return core.Message{}, err
	}
	out.Content, out.ContentNull = content, null
	out.ToolCalls = patchToolCalls(original.ToolCalls, calls)

	fields, err := patchMessageExtras(original.ExtraFields, m)
	if err != nil {
		return core.Message{}, err
	}
	out.ExtraFields = fields
	return out, nil
}

func patchToolCalls(originals []core.ToolCall, calls []pluginapi.ToolCall) []core.ToolCall {
	if len(calls) == 0 {
		return nil
	}
	byID := make(map[string]core.ToolCall, len(originals))
	for _, tc := range originals {
		byID[tc.ID] = tc
	}
	out := make([]core.ToolCall, 0, len(calls))
	for _, call := range calls {
		tc, ok := byID[call.ID]
		if !ok {
			out = append(out, newToolCall(call))
			continue
		}
		tc = cloneToolCall(tc)
		tc.Function.Name = call.Name
		tc.Function.Arguments = argumentsToString(call.Arguments)
		out = append(out, tc)
	}
	return out
}

func newToolCall(call pluginapi.ToolCall) core.ToolCall {
	return core.ToolCall{
		ID:   call.ID,
		Type: "function",
		Function: core.FunctionCall{
			Name:      call.Name,
			Arguments: argumentsToString(call.Arguments),
		},
	}
}

// patchMessageExtras updates the name and cache_control members when the
// unified message changed them, leaving every other extra field alone.
func patchMessageExtras(original core.UnknownJSONFields, m pluginapi.Message) (core.UnknownJSONFields, error) {
	fields := core.CloneUnknownJSONFields(original)
	additions := map[string]json.RawMessage{}
	if name := lookupString(original, "name"); name != m.Name {
		if m.Name == "" {
			fields = fields.Without("name")
		} else {
			encoded, _ := json.Marshal(m.Name)
			additions["name"] = encoded
		}
	}
	hadBreakpoint := original.Lookup("cache_control") != nil
	switch {
	case m.CacheBreakpoint && !hadBreakpoint:
		additions["cache_control"] = ephemeralCacheControl
	case !m.CacheBreakpoint && hadBreakpoint:
		fields = fields.Without("cache_control")
	}
	return core.MergeUnknownJSONFields(fields, additions)
}

// encodeChatMessage encodes a message a plugin inserted.
func encodeChatMessage(m pluginapi.Message) (core.Message, error) {
	out := core.Message{Role: string(m.Role), ToolCallID: m.ToolCallID}
	contentParts, calls := splitParts(m)
	if m.Role == pluginapi.RoleTool && out.ToolCallID == "" {
		out.ToolCallID = toolResultCallID(m)
	}
	content, err := chatContentFromParts(contentParts)
	if err != nil {
		return core.Message{}, err
	}
	out.Content = content
	if len(contentParts) == 0 && len(calls) > 0 {
		out.Content, out.ContentNull = nil, true
	}
	for _, call := range calls {
		out.ToolCalls = append(out.ToolCalls, newToolCall(call))
	}
	fields, err := patchMessageExtras(core.UnknownJSONFields{}, m)
	if err != nil {
		return core.Message{}, err
	}
	out.ExtraFields = fields
	return out, nil
}

func toolResultCallID(m pluginapi.Message) string {
	for _, part := range m.Parts {
		if part.Kind == pluginapi.PartToolResult && part.ToolResult != nil {
			return part.ToolResult.CallID
		}
	}
	return ""
}
