package exchange

import (
	"fmt"
	"maps"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/pluginapi"
)

// coerceInputElements decodes the Responses input array, whatever container
// the request holds it in, into typed elements. Unknown item types keep
// their JSON in Raw.
func coerceInputElements(input any) ([]core.ResponsesInputElement, error) {
	switch typed := input.(type) {
	case []core.ResponsesInputElement:
		out := make([]core.ResponsesInputElement, len(typed))
		copy(out, typed)
		return out, nil
	case []map[string]any:
		out := make([]core.ResponsesInputElement, len(typed))
		for i, item := range typed {
			if err := decodeInputElement(item, &out[i]); err != nil {
				return nil, fmt.Errorf("exchange: input item %d: %w", i, err)
			}
		}
		return out, nil
	case []any:
		out := make([]core.ResponsesInputElement, len(typed))
		for i, item := range typed {
			if err := decodeInputElement(item, &out[i]); err != nil {
				return nil, fmt.Errorf("exchange: input item %d: %w", i, err)
			}
		}
		return out, nil
	default:
		return nil, fmt.Errorf("exchange: unsupported responses input type %T", input)
	}
}

func decodeInputElement(item any, into *core.ResponsesInputElement) error {
	raw, err := json.Marshal(item)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, into)
}

func messageFromInputElement(id string, el core.ResponsesInputElement) (pluginapi.Message, error) {
	switch el.Type {
	case "function_call":
		return pluginapi.Message{ID: id, Role: pluginapi.RoleAssistant, Parts: []pluginapi.Part{{
			Kind:     pluginapi.PartToolCall,
			ToolCall: &pluginapi.ToolCall{ID: el.CallID, Name: el.Name, Arguments: argumentsFromString(el.Arguments)},
		}}}, nil
	case "function_call_output":
		result := &pluginapi.ToolResult{CallID: el.CallID}
		if el.Output != "" {
			result.Parts = []pluginapi.Part{{Kind: pluginapi.PartText, Text: el.Output}}
		}
		return pluginapi.Message{ID: id, Role: pluginapi.RoleTool, ToolCallID: el.CallID, Parts: []pluginapi.Part{{
			Kind: pluginapi.PartToolResult, ToolResult: result,
		}}}, nil
	case "", "message":
		parts, err := partsFromResponsesContent(el.Content)
		if err != nil {
			return pluginapi.Message{}, err
		}
		return pluginapi.Message{ID: id, Role: pluginapi.Role(el.Role), Parts: parts}, nil
	default:
		raw := el.Raw
		if len(raw) == 0 {
			encoded, err := json.Marshal(el)
			if err != nil {
				return pluginapi.Message{}, err
			}
			raw = encoded
		}
		return pluginapi.Message{ID: id, Role: pluginapi.RoleAssistant, Parts: []pluginapi.Part{{Kind: pluginapi.PartOpaque, Raw: raw}}}, nil
	}
}

// partsFromResponsesContent maps Responses message content (string or a list
// of typed blocks) to unified parts. Non-text blocks keep their JSON in Raw.
func partsFromResponsesContent(content any) ([]pluginapi.Part, error) {
	switch c := content.(type) {
	case nil:
		return nil, nil
	case string:
		if c == "" {
			return nil, nil
		}
		return []pluginapi.Part{{Kind: pluginapi.PartText, Text: c}}, nil
	case []core.ContentPart:
		blocks := core.ResponsesBlocksFromContentParts(c)
		if blocks == nil {
			return nil, fmt.Errorf("exchange: unsupported responses content part")
		}
		return partsFromBlocks(blocks), nil
	case []any:
		return partsFromBlocks(c), nil
	case []map[string]any:
		items := make([]any, len(c))
		for i, m := range c {
			items[i] = m
		}
		return partsFromBlocks(items), nil
	default:
		return nil, fmt.Errorf("exchange: unsupported responses content type %T", content)
	}
}

func partsFromBlocks(blocks []any) []pluginapi.Part {
	out := make([]pluginapi.Part, 0, len(blocks))
	for _, block := range blocks {
		m, ok := block.(map[string]any)
		if !ok {
			out = append(out, opaquePart(block))
			continue
		}
		out = append(out, partFromBlock(m))
	}
	return out
}

func partFromBlock(m map[string]any) pluginapi.Part {
	blockType, _ := m["type"].(string)
	switch blockType {
	case "input_text", "output_text", "text":
		text, _ := m["text"].(string)
		return pluginapi.Part{Kind: pluginapi.PartText, Text: text}
	case "refusal":
		text, _ := m["refusal"].(string)
		return pluginapi.Part{Kind: pluginapi.PartRefusal, Text: text}
	case "input_image":
		part := opaquePart(m)
		part.Kind = pluginapi.PartImage
		switch img := m["image_url"].(type) {
		case string:
			part.URL = img
		case map[string]any:
			part.URL, _ = img["url"].(string)
		}
		part.MediaType = dataURIMediaType(part.URL)
		return part
	case "input_file":
		part := opaquePart(m)
		part.Kind = pluginapi.PartFile
		part.URL, _ = m["file_url"].(string)
		if data, ok := m["file_data"].(string); ok {
			part.Data = []byte(data)
		}
		return part
	default:
		return opaquePart(m)
	}
}

// inputItem is one entry of the applied input list: the element to emit and,
// when it comes from the original request, its index and whether it was
// edited.
type inputItem struct {
	elem    core.ResponsesInputElement
	origIdx int
	edited  bool
}

func applyResponsesInput(original any, msgs []pluginapi.Message, changes map[string]pluginapi.ChangeKind) (any, error) {
	switch orig := original.(type) {
	case nil:
		if len(msgs) == 0 {
			return nil, nil
		}
		items, err := buildInputItems(nil, msgs, changes)
		if err != nil {
			return nil, err
		}
		return elementsOf(items), nil
	case string:
		return applyStringInput(orig, msgs, changes)
	}
	elements, err := coerceInputElements(original)
	if err != nil {
		return nil, err
	}
	items, err := buildInputItems(elements, msgs, changes)
	if err != nil {
		return nil, err
	}
	return patchInputEnvelope(original, items)
}

func applyStringInput(orig string, msgs []pluginapi.Message, changes map[string]pluginapi.ChangeKind) (any, error) {
	if len(msgs) == 0 {
		return "", nil
	}
	if len(msgs) == 1 && msgs[0].ID == originalID(0) {
		content, _ := splitParts(msgs[0])
		switch changes[msgs[0].ID] {
		case "":
			return orig, nil
		case pluginapi.ChangeEdited:
			if len(content) == 0 {
				return "", nil
			}
			if len(content) == 1 && content[0].Kind == pluginapi.PartText {
				return content[0].Text, nil
			}
		}
	}
	stringElement := core.ResponsesInputElement{Type: "message", Role: "user", Content: orig}
	items, err := buildInputItems([]core.ResponsesInputElement{stringElement}, msgs, changes)
	if err != nil {
		return nil, err
	}
	return elementsOf(items), nil
}

func buildInputItems(originals []core.ResponsesInputElement, msgs []pluginapi.Message, changes map[string]pluginapi.ChangeKind) ([]inputItem, error) {
	items := make([]inputItem, 0, len(msgs))
	for _, m := range msgs {
		kind := changes[m.ID]
		if kind == pluginapi.ChangeInserted || m.ID == InstructionsMessageID {
			for _, el := range encodeResponsesElements(m) {
				items = append(items, inputItem{elem: el, origIdx: -1})
			}
			continue
		}
		idx, err := originalIndex(m.ID, len(originals))
		if err != nil {
			return nil, err
		}
		if kind != pluginapi.ChangeEdited {
			items = append(items, inputItem{elem: originals[idx], origIdx: idx})
			continue
		}
		patched, err := patchResponsesElement(originals[idx], m)
		if err != nil {
			return nil, fmt.Errorf("exchange: input item %d: %w", idx, err)
		}
		items = append(items, inputItem{elem: patched, origIdx: idx, edited: true})
	}
	return items, nil
}

func elementsOf(items []inputItem) []core.ResponsesInputElement {
	out := make([]core.ResponsesInputElement, len(items))
	for i, item := range items {
		out[i] = item.elem
	}
	return out
}

// patchInputEnvelope re-emits the input in the container shape the request
// arrived with, copying untouched items verbatim.
func patchInputEnvelope(original any, items []inputItem) (any, error) {
	switch orig := original.(type) {
	case []map[string]any:
		out := make([]map[string]any, len(items))
		for i, item := range items {
			var origMap map[string]any
			if item.origIdx >= 0 {
				origMap = orig[item.origIdx]
			}
			patched, err := patchInputMap(origMap, item)
			if err != nil {
				return nil, err
			}
			out[i] = patched
		}
		return out, nil
	case []any:
		out := make([]any, len(items))
		for i, item := range items {
			var origMap map[string]any
			if item.origIdx >= 0 {
				if m, ok := orig[item.origIdx].(map[string]any); ok {
					origMap = m
				} else if !item.edited {
					out[i] = orig[item.origIdx]
					continue
				}
			}
			patched, err := patchInputMap(origMap, item)
			if err != nil {
				return nil, err
			}
			out[i] = patched
		}
		return out, nil
	default:
		return elementsOf(items), nil
	}
}

var inputElementKeys = []string{"type", "role", "status", "content", "call_id", "id", "name", "arguments", "output"}

func patchInputMap(origMap map[string]any, item inputItem) (map[string]any, error) {
	if origMap != nil && !item.edited {
		return cloneAnyMap(origMap), nil
	}
	updated, err := elementAsMap(item.elem)
	if err != nil {
		return nil, err
	}
	if origMap == nil {
		return updated, nil
	}
	out := cloneAnyMap(origMap)
	for _, key := range inputElementKeys {
		delete(out, key)
	}
	maps.Copy(out, updated)
	if item.elem.Type == "function_call_output" {
		if _, wasString := origMap["output"].(string); !wasString {
			var decoded any
			if err := json.Unmarshal([]byte(item.elem.Output), &decoded); err == nil {
				out["output"] = decoded
			}
		}
	}
	return out, nil
}

func elementAsMap(el core.ResponsesInputElement) (map[string]any, error) {
	raw, err := json.Marshal(el)
	if err != nil {
		return nil, fmt.Errorf("exchange: encode input item: %w", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("exchange: encode input item: %w", err)
	}
	return m, nil
}
