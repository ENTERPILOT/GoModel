package exchange

import (
	"fmt"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/pluginapi"
)

var responsesModelledParams = []string{"model", "input", "instructions", "tools", "tool_choice", "max_output_tokens", "temperature", "top_p", "stream", "provider"}

// FromResponsesRequest builds the unified prompt for a Responses request. A
// non-empty instructions field becomes a leading system message with ID
// "instructions"; input items get IDs "m<index>" (a string input is "m0").
// Items the mapper does not model (reasoning, item references, hosted tool
// items) become assistant messages with one opaque part.
func FromResponsesRequest(req *core.ResponsesRequest) (*pluginapi.Prompt, error) {
	if req == nil {
		return nil, fmt.Errorf("exchange: nil responses request")
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("exchange: encode responses request: %w", err)
	}
	p := &pluginapi.Prompt{Raw: raw, Tools: toolsFromMaps(req.Tools)}
	if req.Instructions != "" {
		msg := pluginapi.TextMessage(pluginapi.RoleSystem, req.Instructions)
		msg.ID = InstructionsMessageID
		p.Messages = append(p.Messages, msg)
	}
	switch input := req.Input.(type) {
	case nil:
	case string:
		msg := pluginapi.Message{ID: originalID(0), Role: pluginapi.RoleUser}
		if input != "" {
			msg.Parts = []pluginapi.Part{{Kind: pluginapi.PartText, Text: input}}
		}
		p.Messages = append(p.Messages, msg)
	default:
		elements, err := coerceInputElements(input)
		if err != nil {
			return nil, err
		}
		for i, el := range elements {
			msg, err := messageFromInputElement(originalID(i), el)
			if err != nil {
				return nil, fmt.Errorf("exchange: input item %d: %w", i, err)
			}
			p.Messages = append(p.Messages, msg)
		}
	}
	p.Params = pluginapi.Params{
		Model:       req.Model,
		MaxTokens:   req.MaxOutputTokens,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		Stream:      req.Stream,
		ToolChoice:  req.ToolChoice,
		Extra:       extraParams(raw, responsesModelledParams...),
	}
	p.Reset()
	return p, nil
}

// ApplyToResponsesRequest returns a copy of original with the prompt's edits
// applied. The instructions message writes back to Instructions (a removed
// one clears it; a system message inserted at position 0 when there was no
// instructions message sets it). The input keeps its original container
// shape and a string input stays a string while it is a single text
// message. Changing "model" or "stream" is an error.
func ApplyToResponsesRequest(original *core.ResponsesRequest, p *pluginapi.Prompt) (*core.ResponsesRequest, error) {
	if original == nil || p == nil {
		return nil, fmt.Errorf("exchange: nil responses request or prompt")
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	changes := p.Changes()
	result := *original

	msgs := p.Messages
	result.Instructions = ""
	if len(msgs) > 0 {
		first := msgs[0]
		switch {
		case first.ID == InstructionsMessageID:
			result.Instructions = first.Text()
			msgs = msgs[1:]
		case original.Instructions == "" && changes.Messages[first.ID] == pluginapi.ChangeInserted && isPlainSystem(first):
			result.Instructions = first.Text()
			msgs = msgs[1:]
		}
	}

	input, err := applyResponsesInput(original.Input, msgs, changes.Messages)
	if err != nil {
		return nil, err
	}
	result.Input = input
	if err := applyResponsesParams(&result, changes.Params); err != nil {
		return nil, err
	}
	return &result, nil
}

func isPlainSystem(m pluginapi.Message) bool {
	if m.Role != pluginapi.RoleSystem {
		return false
	}
	for _, part := range m.Parts {
		if part.Kind != pluginapi.PartText {
			return false
		}
	}
	return true
}

func applyResponsesParams(result *core.ResponsesRequest, params map[string]any) error {
	if len(params) == 0 {
		return nil
	}
	applier := newParamApplier(core.ResponsesRequest{}, "model", "stream", "input", "instructions", "tools", "provider")
	for _, key := range sortedKeys(params) {
		value := params[key]
		var err error
		switch key {
		case "max_tokens", "max_output_tokens":
			result.MaxOutputTokens, err = maxTokensParam(value)
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
		input := result.Input
		if err := mergeJSONParams(result, applier.jsonMerge); err != nil {
			return fmt.Errorf("exchange: apply parameters: %w", err)
		}
		result.Input = input
	}
	extra, err := applier.extraFields(result.ExtraFields)
	if err != nil {
		return fmt.Errorf("exchange: apply parameters: %w", err)
	}
	result.ExtraFields = extra
	return nil
}
