package exchange

import (
	"encoding/base64"
	"fmt"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/pluginapi"
)

// patchResponsesElement rewrites the edited parts of m inside the original
// input element, keeping its extra fields and content container.
func patchResponsesElement(original core.ResponsesInputElement, m pluginapi.Message) (core.ResponsesInputElement, error) {
	out := original
	out.ExtraFields = core.CloneUnknownJSONFields(original.ExtraFields)
	content, calls := splitParts(m)
	switch original.Type {
	case "function_call":
		if len(calls) > 0 {
			out.Name = calls[0].Name
			out.Arguments = argumentsToString(calls[0].Arguments)
		}
	case "function_call_output":
		out.Output = joinText(content)
	case "", "message":
		rewritten, err := rewriteResponsesContent(original.Content, m.Role, content)
		if err != nil {
			return core.ResponsesInputElement{}, err
		}
		out.Content = rewritten
	}
	return out, nil
}

// rewriteResponsesContent rewrites the text blocks of original message content
// in place when the block layout is unchanged, and re-encodes from the
// unified parts otherwise. A string stays a string while the message is a
// single text part.
func rewriteResponsesContent(original any, role pluginapi.Role, parts []pluginapi.Part) (any, error) {
	switch orig := original.(type) {
	case nil, string:
		if len(parts) == 0 {
			return "", nil
		}
		if len(parts) == 1 && parts[0].Kind == pluginapi.PartText {
			return parts[0].Text, nil
		}
		return blocksFromParts(role, parts)
	case []core.ContentPart:
		blocks := core.ResponsesBlocksFromContentParts(orig)
		if blocks == nil {
			return nil, fmt.Errorf("exchange: unsupported responses content part")
		}
		return rewriteBlocks(blocks, role, parts)
	case []any:
		return rewriteBlocks(orig, role, parts)
	case []map[string]any:
		items := make([]any, len(orig))
		for i, m := range orig {
			items[i] = m
		}
		rewritten, err := rewriteBlocks(items, role, parts)
		if err != nil {
			return nil, err
		}
		out := make([]map[string]any, len(rewritten))
		for i, block := range rewritten {
			m, ok := block.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("exchange: content block %d is not an object", i)
			}
			out[i] = m
		}
		return out, nil
	default:
		return nil, fmt.Errorf("exchange: unsupported responses content type %T", original)
	}
}

func rewriteBlocks(blocks []any, role pluginapi.Role, parts []pluginapi.Part) ([]any, error) {
	if len(blocks) != len(parts) {
		return blocksFromParts(role, parts)
	}
	out := cloneAnySlice(blocks)
	for i, part := range parts {
		if part.Kind != pluginapi.PartText {
			continue
		}
		m, ok := out[i].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("exchange: content block %d is not a text block", i)
		}
		switch m["type"] {
		case "input_text", "output_text", "text":
			m["text"] = part.Text
		default:
			return nil, fmt.Errorf("exchange: content block %d is not a text block", i)
		}
	}
	return out, nil
}

// blocksFromParts encodes unified parts as Responses content blocks. Parts
// that carry their original JSON (Raw) are emitted from it.
func blocksFromParts(role pluginapi.Role, parts []pluginapi.Part) ([]any, error) {
	textType := "input_text"
	if role == pluginapi.RoleAssistant {
		textType = "output_text"
	}
	out := make([]any, 0, len(parts))
	for i, part := range parts {
		if len(part.Raw) > 0 && part.Kind != pluginapi.PartText {
			var block any
			if err := json.Unmarshal(part.Raw, &block); err != nil {
				return nil, fmt.Errorf("exchange: part %d has invalid raw JSON: %w", i, err)
			}
			out = append(out, block)
			continue
		}
		switch part.Kind {
		case pluginapi.PartText:
			out = append(out, map[string]any{"type": textType, "text": part.Text})
		case pluginapi.PartRefusal:
			out = append(out, map[string]any{"type": "refusal", "refusal": part.Text})
		case pluginapi.PartImage:
			url := part.URL
			if url == "" && len(part.Data) > 0 {
				url = "data:" + part.MediaType + ";base64," + base64.StdEncoding.EncodeToString(part.Data)
			}
			out = append(out, map[string]any{"type": "input_image", "image_url": url})
		case pluginapi.PartFile:
			block := map[string]any{"type": "input_file"}
			if part.URL != "" {
				block["file_url"] = part.URL
			}
			if len(part.Data) > 0 {
				block["file_data"] = string(part.Data)
			}
			out = append(out, block)
		default:
			return nil, fmt.Errorf("exchange: part kind %q is not supported in responses content", part.Kind)
		}
	}
	return out, nil
}

// encodeResponsesElements encodes a message a plugin inserted. A message
// with tool-call parts becomes a message item followed by one function_call
// item per call; a tool message becomes a function_call_output item.
func encodeResponsesElements(m pluginapi.Message) []core.ResponsesInputElement {
	content, calls := splitParts(m)
	if m.Role == pluginapi.RoleTool {
		callID := m.ToolCallID
		if callID == "" {
			callID = toolResultCallID(m)
		}
		return []core.ResponsesInputElement{{Type: "function_call_output", CallID: callID, Output: joinText(content)}}
	}
	var out []core.ResponsesInputElement
	if len(content) > 0 || len(calls) == 0 {
		el := core.ResponsesInputElement{Type: "message", Role: string(m.Role)}
		if len(content) == 1 && content[0].Kind == pluginapi.PartText {
			el.Content = content[0].Text
		} else if len(content) == 0 {
			el.Content = ""
		} else if blocks, err := blocksFromParts(m.Role, content); err == nil {
			el.Content = blocks
		} else {
			el.Content = joinText(content)
		}
		out = append(out, el)
	}
	for _, call := range calls {
		out = append(out, core.ResponsesInputElement{
			Type:      "function_call",
			CallID:    call.ID,
			Name:      call.Name,
			Arguments: argumentsToString(call.Arguments),
		})
	}
	return out
}
