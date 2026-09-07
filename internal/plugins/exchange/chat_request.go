package exchange

import (
	"fmt"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/pluginapi"
)

// chatModelledParams are the body keys Params exposes as typed fields.
var chatModelledParams = []string{"model", "messages", "tools", "tool_choice", "max_tokens", "temperature", "top_p", "stream", "provider"}

// FromChatRequest builds the unified prompt for a chat completion request.
// Message IDs are "m<index>".
func FromChatRequest(req *core.ChatRequest) (*pluginapi.Prompt, error) {
	if req == nil {
		return nil, fmt.Errorf("exchange: nil chat request")
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("exchange: encode chat request: %w", err)
	}
	p := &pluginapi.Prompt{Raw: raw, Tools: toolsFromMaps(req.Tools)}
	p.Messages = make([]pluginapi.Message, 0, len(req.Messages))
	for i, m := range req.Messages {
		msg, err := messageFromChat(originalID(i), m)
		if err != nil {
			return nil, fmt.Errorf("exchange: message %d: %w", i, err)
		}
		p.Messages = append(p.Messages, msg)
	}
	p.Params = pluginapi.Params{
		Model:       req.Model,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		Stream:      req.Stream,
		ToolChoice:  req.ToolChoice,
		Extra:       extraParams(raw, chatModelledParams...),
	}
	p.Reset()
	return p, nil
}

func messageFromChat(id string, m core.Message) (pluginapi.Message, error) {
	msg := pluginapi.Message{
		ID:              id,
		Role:            pluginapi.Role(m.Role),
		ToolCallID:      m.ToolCallID,
		Name:            lookupString(m.ExtraFields, "name"),
		CacheBreakpoint: m.ExtraFields.Lookup("cache_control") != nil,
	}
	parts, err := partsFromChatContent(m.Content)
	if err != nil {
		return pluginapi.Message{}, err
	}
	if m.Role == string(pluginapi.RoleTool) {
		msg.Parts = []pluginapi.Part{{
			Kind:       pluginapi.PartToolResult,
			ToolResult: &pluginapi.ToolResult{CallID: m.ToolCallID, Parts: parts},
		}}
		return msg, nil
	}
	msg.Parts = parts
	for _, tc := range m.ToolCalls {
		msg.Parts = append(msg.Parts, pluginapi.Part{Kind: pluginapi.PartToolCall, ToolCall: &pluginapi.ToolCall{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: argumentsFromString(tc.Function.Arguments),
		}})
	}
	return msg, nil
}

// ApplyToChatRequest returns a copy of original with the prompt's edits
// applied. Untouched messages are copied verbatim; edited ones keep their
// envelope with only the touched parts rewritten; inserted ones are encoded
// from the unified form. Changing "model" or "stream" is an error because
// routing has already happened.
func ApplyToChatRequest(original *core.ChatRequest, p *pluginapi.Prompt) (*core.ChatRequest, error) {
	if original == nil || p == nil {
		return nil, fmt.Errorf("exchange: nil chat request or prompt")
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	changes := p.Changes()
	result := *original
	result.Messages = make([]core.Message, 0, len(p.Messages))
	for _, m := range p.Messages {
		encoded, err := applyChatMessage(original.Messages, m, changes.Messages[m.ID])
		if err != nil {
			return nil, err
		}
		result.Messages = append(result.Messages, encoded)
	}
	if err := applyChatParams(&result, changes.Params); err != nil {
		return nil, err
	}
	return &result, nil
}

func applyChatMessage(originals []core.Message, m pluginapi.Message, kind pluginapi.ChangeKind) (core.Message, error) {
	if kind == pluginapi.ChangeInserted {
		return encodeChatMessage(m)
	}
	idx, err := originalIndex(m.ID, len(originals))
	if err != nil {
		return core.Message{}, err
	}
	if kind == pluginapi.ChangeEdited {
		return patchChatMessage(originals[idx], m)
	}
	return cloneChatMessage(originals[idx]), nil
}

func applyChatParams(result *core.ChatRequest, params map[string]any) error {
	if len(params) == 0 {
		return nil
	}
	applier := newParamApplier(core.ChatRequest{}, "model", "stream", "messages", "tools", "provider")
	for _, key := range sortedKeys(params) {
		value := params[key]
		var err error
		switch key {
		case "max_tokens":
			result.MaxTokens, err = maxTokensParam(value)
		case "temperature":
			result.Temperature, err = floatParam(key, value)
		case "top_p":
			result.TopP, err = floatParam(key, value)
		case "tool_choice":
			result.ToolChoice = value
		case "user":
			result.User, err = paramString(key, value)
		case "service_tier":
			result.ServiceTier, err = paramString(key, value)
		case "parallel_tool_calls":
			var b bool
			if b, err = paramBool(key, value); err == nil {
				result.ParallelToolCalls = &b
			}
		default:
			err = applier.route(key, value)
		}
		if err != nil {
			return err
		}
	}
	if len(applier.jsonMerge) > 0 {
		messages, plan := result.Messages, result.PromptCachePlan
		if err := mergeJSONParams(result, applier.jsonMerge); err != nil {
			return fmt.Errorf("exchange: apply parameters: %w", err)
		}
		result.Messages, result.PromptCachePlan = messages, plan
	}
	extra, err := applier.extraFields(result.ExtraFields)
	if err != nil {
		return fmt.Errorf("exchange: apply parameters: %w", err)
	}
	result.ExtraFields = extra
	return nil
}
