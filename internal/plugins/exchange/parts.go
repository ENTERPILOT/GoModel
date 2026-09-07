package exchange

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/pluginapi"
)

// partsFromChatContent maps chat message content (string, null, or content
// parts) to unified parts.
func partsFromChatContent(content any) ([]pluginapi.Part, error) {
	switch c := content.(type) {
	case nil:
		return nil, nil
	case string:
		if c == "" {
			return nil, nil
		}
		return []pluginapi.Part{{Kind: pluginapi.PartText, Text: c}}, nil
	case []core.ContentPart:
		return partsFromContentParts(c), nil
	}
	if parts, ok := core.NormalizeContentParts(content); ok {
		return partsFromContentParts(parts), nil
	}
	items, ok := content.([]any)
	if !ok {
		return nil, fmt.Errorf("exchange: unsupported chat content type %T", content)
	}
	out := make([]pluginapi.Part, 0, len(items))
	for _, item := range items {
		m, isMap := item.(map[string]any)
		if isMap && isChatTextType(m["type"]) {
			text, _ := m["text"].(string)
			out = append(out, pluginapi.Part{Kind: pluginapi.PartText, Text: text})
			continue
		}
		out = append(out, opaquePart(item))
	}
	return out, nil
}

func isChatTextType(v any) bool {
	t, _ := v.(string)
	return t == "text" || t == "input_text"
}

func partsFromContentParts(parts []core.ContentPart) []pluginapi.Part {
	out := make([]pluginapi.Part, len(parts))
	for i, part := range parts {
		out[i] = partFromContentPart(part)
	}
	return out
}

func partFromContentPart(cp core.ContentPart) pluginapi.Part {
	switch cp.Type {
	case "text", "input_text":
		return pluginapi.Part{Kind: pluginapi.PartText, Text: cp.Text}
	case "image_url", "input_image":
		part := pluginapi.Part{Kind: pluginapi.PartImage}
		if cp.ImageURL != nil {
			part.URL = cp.ImageURL.URL
			part.MediaType = cp.ImageURL.MediaType
			if part.MediaType == "" {
				part.MediaType = dataURIMediaType(cp.ImageURL.URL)
			}
		}
		return part
	case "input_audio":
		part := pluginapi.Part{Kind: pluginapi.PartAudio}
		if cp.InputAudio != nil {
			part.Data = []byte(cp.InputAudio.Data)
			if cp.InputAudio.Format != "" {
				part.MediaType = "audio/" + cp.InputAudio.Format
			} else {
				part.MediaType = dataURIMediaType(cp.InputAudio.Data)
			}
		}
		return part
	default:
		return opaquePart(cp)
	}
}

func opaquePart(v any) pluginapi.Part {
	raw, err := json.Marshal(v)
	if err != nil {
		raw = nil
	}
	return pluginapi.Part{Kind: pluginapi.PartOpaque, Raw: raw}
}

// contentPartFromPart encodes a unified part as a chat content part.
func contentPartFromPart(p pluginapi.Part) (core.ContentPart, error) {
	switch p.Kind {
	case pluginapi.PartText:
		return core.ContentPart{Type: "text", Text: p.Text}, nil
	case pluginapi.PartImage:
		url := p.URL
		if url == "" && len(p.Data) > 0 {
			url = "data:" + p.MediaType + ";base64," + base64.StdEncoding.EncodeToString(p.Data)
		}
		if url == "" {
			return core.ContentPart{}, fmt.Errorf("exchange: image part has neither URL nor data")
		}
		return core.ContentPart{Type: "image_url", ImageURL: &core.ImageURLContent{URL: url}}, nil
	case pluginapi.PartAudio:
		format := strings.TrimPrefix(p.MediaType, "audio/")
		if strings.HasPrefix(string(p.Data), "data:") {
			format = ""
		}
		return core.ContentPart{Type: "input_audio", InputAudio: &core.InputAudioContent{Data: string(p.Data), Format: format}}, nil
	case pluginapi.PartOpaque:
		var cp core.ContentPart
		if err := json.Unmarshal(p.Raw, &cp); err != nil {
			return core.ContentPart{}, fmt.Errorf("exchange: opaque part is not a chat content part: %w", err)
		}
		return cp, nil
	default:
		return core.ContentPart{}, fmt.Errorf("exchange: part kind %q is not supported in chat content", p.Kind)
	}
}

// chatContentFromParts encodes unified parts as chat content: a string for a
// single text part, an array otherwise, "" for no parts.
func chatContentFromParts(parts []pluginapi.Part) (any, error) {
	switch {
	case len(parts) == 0:
		return "", nil
	case len(parts) == 1 && parts[0].Kind == pluginapi.PartText:
		return parts[0].Text, nil
	}
	out := make([]core.ContentPart, len(parts))
	for i, part := range parts {
		cp, err := contentPartFromPart(part)
		if err != nil {
			return nil, err
		}
		out[i] = cp
	}
	return out, nil
}

// rewriteChatContent rewrites the text parts of original chat content from
// the unified parts, keeping the original structure when the part layout is
// unchanged and re-encoding from the unified form otherwise.
func rewriteChatContent(original any, originalNull bool, parts []pluginapi.Part) (content any, null bool, err error) {
	switch orig := original.(type) {
	case nil:
		if len(parts) == 0 {
			return nil, originalNull, nil
		}
		content, err = chatContentFromParts(parts)
		return content, false, err
	case string:
		content, err = chatContentFromParts(parts)
		return content, false, err
	case []core.ContentPart:
		return rewriteContentParts(orig, parts)
	}
	if typed, ok := core.NormalizeContentParts(original); ok {
		return rewriteContentParts(typed, parts)
	}
	items, ok := original.([]any)
	if !ok {
		return nil, false, fmt.Errorf("exchange: unsupported chat content type %T", original)
	}
	if len(items) != len(parts) {
		content, err = chatContentFromParts(parts)
		return content, false, err
	}
	out := cloneAnySlice(items)
	for i, item := range out {
		if parts[i].Kind != pluginapi.PartText {
			continue
		}
		m, isMap := item.(map[string]any)
		if !isMap || !isChatTextType(m["type"]) {
			return nil, false, fmt.Errorf("exchange: content part %d is not a text part", i)
		}
		m["text"] = parts[i].Text
	}
	return out, false, nil
}

func rewriteContentParts(orig []core.ContentPart, parts []pluginapi.Part) (any, bool, error) {
	if len(orig) != len(parts) {
		content, err := chatContentFromParts(parts)
		return content, false, err
	}
	out := cloneContentParts(orig)
	for i, part := range parts {
		if part.Kind != pluginapi.PartText {
			continue
		}
		if out[i].Type != "text" && out[i].Type != "input_text" {
			return nil, false, fmt.Errorf("exchange: content part %d is not a text part", i)
		}
		out[i].Text = part.Text
	}
	return out, false, nil
}

// argumentsFromString exposes a tool-call argument string as JSON: parsed
// when it is a JSON object or array, otherwise as a JSON string.
func argumentsFromString(s string) json.RawMessage {
	trimmed := strings.TrimSpace(s)
	if (strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[")) && json.Valid([]byte(trimmed)) {
		return json.RawMessage(trimmed)
	}
	raw, _ := json.Marshal(s)
	return raw
}

// argumentsToString is the inverse of argumentsFromString.
func argumentsToString(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return ""
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err == nil {
			return s
		}
	}
	return string(trimmed)
}

func dataURIMediaType(s string) string {
	rest, ok := strings.CutPrefix(s, "data:")
	if !ok {
		return ""
	}
	meta, _, ok := strings.Cut(rest, ",")
	if !ok {
		return ""
	}
	mediaType, _, _ := strings.Cut(meta, ";")
	if !strings.Contains(mediaType, "/") {
		return ""
	}
	return mediaType
}

// splitParts separates a message's content parts from its tool calls.
func splitParts(m pluginapi.Message) (content []pluginapi.Part, calls []pluginapi.ToolCall) {
	for _, part := range m.Parts {
		switch part.Kind {
		case pluginapi.PartToolCall:
			if part.ToolCall != nil {
				calls = append(calls, *part.ToolCall)
			}
		case pluginapi.PartToolResult:
			if part.ToolResult != nil {
				content = append(content, part.ToolResult.Parts...)
			}
		default:
			content = append(content, part)
		}
	}
	return content, calls
}

func textParts(parts []pluginapi.Part) []pluginapi.Part {
	var out []pluginapi.Part
	for _, part := range parts {
		if part.Kind == pluginapi.PartText {
			out = append(out, part)
		}
	}
	return out
}

func joinText(parts []pluginapi.Part) string {
	var b strings.Builder
	for _, part := range parts {
		if part.Kind == pluginapi.PartText {
			b.WriteString(part.Text)
		}
	}
	return b.String()
}

func lookupString(fields core.UnknownJSONFields, key string) string {
	raw := fields.Lookup(key)
	if raw == nil {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}
