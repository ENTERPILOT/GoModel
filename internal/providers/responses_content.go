package providers

import (
	"strings"

	"github.com/goccy/go-json"

	"gomodel/internal/core"
)

// ConvertResponsesContentToChatContent maps Responses input content to Chat content.
// Text-only arrays are flattened to strings for broader provider compatibility.
// Any non-text part preserves the array form so multimodal payloads survive routing.
func ConvertResponsesContentToChatContent(content any) (any, bool) {
	switch c := content.(type) {
	case string:
		return c, true
	case []map[string]any:
		items := make([]any, 0, len(c))
		for _, item := range c {
			items = append(items, item)
		}
		return convertResponsesContentParts(items)
	case []any:
		return convertResponsesContentParts(c)
	case []core.ContentPart:
		parts := make([]core.ContentPart, 0, len(c))
		for _, part := range c {
			normalized, ok := normalizeTypedResponsesContentPart(part)
			if !ok {
				return nil, false
			}
			parts = append(parts, normalized)
		}
		return finalizeResponsesChatContent(parts)
	case core.ContentPart:
		normalized, ok := normalizeTypedResponsesContentPart(c)
		if !ok {
			return nil, false
		}
		return finalizeResponsesChatContent([]core.ContentPart{normalized})
	default:
		return nil, false
	}
}

func convertResponsesContentParts(parts []any) (any, bool) {
	typedParts := make([]core.ContentPart, 0, len(parts))

	for _, part := range parts {
		partMap, ok := part.(map[string]any)
		if !ok {
			return nil, false
		}

		partType, _ := partMap["type"].(string)
		switch partType {
		case "text", "input_text", "output_text":
			text, ok := partMap["text"].(string)
			if !ok || text == "" {
				return nil, false
			}
			typedParts = append(typedParts, core.ContentPart{
				Type:        "text",
				Text:        text,
				ExtraFields: core.UnknownJSONFieldsFromMap(rawJSONMapFromUnknownKeys(partMap, "type", "text")),
			})
		case "image_url", "input_image":
			imageURL, ok := normalizeResponsesImageURLForChat(partMap["image_url"])
			if !ok {
				return nil, false
			}
			typedParts = append(typedParts, core.ContentPart{
				Type:        "image_url",
				ImageURL:    imageURL,
				ExtraFields: core.UnknownJSONFieldsFromMap(rawJSONMapFromUnknownKeys(partMap, "type", "image_url")),
			})
		case "input_audio":
			inputAudio, ok := normalizeResponsesInputAudioForChat(partMap["input_audio"])
			if !ok {
				return nil, false
			}
			typedParts = append(typedParts, core.ContentPart{
				Type:        "input_audio",
				InputAudio:  inputAudio,
				ExtraFields: core.UnknownJSONFieldsFromMap(rawJSONMapFromUnknownKeys(partMap, "type", "input_audio")),
			})
		default:
			if nested, ok := partMap["content"]; ok {
				text := ExtractContentFromInput(nested)
				if text == "" {
					return nil, false
				}
				typedParts = append(typedParts, core.ContentPart{Type: "text", Text: text})
				continue
			}
			return nil, false
		}
	}

	if len(typedParts) == 0 {
		return nil, false
	}
	return finalizeResponsesChatContent(typedParts)
}

func normalizeTypedResponsesContentPart(part core.ContentPart) (core.ContentPart, bool) {
	switch part.Type {
	case "text", "input_text", "output_text":
		if part.Text == "" {
			return core.ContentPart{}, false
		}
		return core.ContentPart{
			Type:        "text",
			Text:        part.Text,
			ExtraFields: core.CloneUnknownJSONFields(part.ExtraFields),
		}, true
	case "image_url", "input_image":
		if part.ImageURL == nil {
			return core.ContentPart{}, false
		}
		url := strings.TrimSpace(part.ImageURL.URL)
		if url == "" {
			return core.ContentPart{}, false
		}
		return core.ContentPart{
			Type: "image_url",
			ImageURL: &core.ImageURLContent{
				URL:         url,
				Detail:      strings.TrimSpace(part.ImageURL.Detail),
				MediaType:   strings.TrimSpace(part.ImageURL.MediaType),
				ExtraFields: core.CloneUnknownJSONFields(part.ImageURL.ExtraFields),
			},
			ExtraFields: core.CloneUnknownJSONFields(part.ExtraFields),
		}, true
	case "input_audio":
		if part.InputAudio == nil {
			return core.ContentPart{}, false
		}
		data := strings.TrimSpace(part.InputAudio.Data)
		format := strings.TrimSpace(part.InputAudio.Format)
		if !core.ValidInputAudioPayload(data, format) {
			return core.ContentPart{}, false
		}
		return core.ContentPart{
			Type: "input_audio",
			InputAudio: &core.InputAudioContent{
				Data:        data,
				Format:      format,
				ExtraFields: core.CloneUnknownJSONFields(part.InputAudio.ExtraFields),
			},
			ExtraFields: core.CloneUnknownJSONFields(part.ExtraFields),
		}, true
	default:
		return core.ContentPart{}, false
	}
}

func finalizeResponsesChatContent(parts []core.ContentPart) (any, bool) {
	if len(parts) == 0 {
		return nil, false
	}

	if !canFlattenResponsesPartsToText(parts) {
		return parts, true
	}

	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		texts = append(texts, part.Text)
	}
	return strings.Join(texts, " "), true
}

func canFlattenResponsesPartsToText(parts []core.ContentPart) bool {
	for _, part := range parts {
		if part.Type != "text" {
			return false
		}
		if !part.ExtraFields.IsEmpty() {
			return false
		}
	}
	return true
}

func normalizeResponsesImageURLForChat(value any) (*core.ImageURLContent, bool) {
	switch v := value.(type) {
	case string:
		url := strings.TrimSpace(v)
		if url == "" {
			return nil, false
		}
		return &core.ImageURLContent{URL: url}, true
	case map[string]string:
		url := strings.TrimSpace(v["url"])
		if url == "" {
			return nil, false
		}
		return &core.ImageURLContent{
			URL:         url,
			Detail:      strings.TrimSpace(v["detail"]),
			MediaType:   strings.TrimSpace(v["media_type"]),
			ExtraFields: core.UnknownJSONFieldsFromMap(rawJSONMapFromUnknownStringKeys(v, "url", "detail", "media_type")),
		}, true
	case map[string]any:
		url, _ := v["url"].(string)
		url = strings.TrimSpace(url)
		if url == "" {
			return nil, false
		}
		detail, _ := v["detail"].(string)
		mediaType, _ := v["media_type"].(string)
		return &core.ImageURLContent{
			URL:         url,
			Detail:      strings.TrimSpace(detail),
			MediaType:   strings.TrimSpace(mediaType),
			ExtraFields: core.UnknownJSONFieldsFromMap(rawJSONMapFromUnknownKeys(v, "url", "detail", "media_type")),
		}, true
	default:
		return nil, false
	}
}

func normalizeResponsesInputAudioForChat(value any) (*core.InputAudioContent, bool) {
	switch v := value.(type) {
	case map[string]string:
		data := strings.TrimSpace(v["data"])
		format := strings.TrimSpace(v["format"])
		if !core.ValidInputAudioPayload(data, format) {
			return nil, false
		}
		return &core.InputAudioContent{
			Data:        data,
			Format:      format,
			ExtraFields: core.UnknownJSONFieldsFromMap(rawJSONMapFromUnknownStringKeys(v, "data", "format")),
		}, true
	case map[string]any:
		data, _ := v["data"].(string)
		format, _ := v["format"].(string)
		data = strings.TrimSpace(data)
		format = strings.TrimSpace(format)
		if !core.ValidInputAudioPayload(data, format) {
			return nil, false
		}
		return &core.InputAudioContent{
			Data:        data,
			Format:      format,
			ExtraFields: core.UnknownJSONFieldsFromMap(rawJSONMapFromUnknownKeys(v, "data", "format")),
		}, true
	default:
		return nil, false
	}
}

func cloneResponsesToolCall(call core.ToolCall) core.ToolCall {
	cloned := call
	cloned.ExtraFields = core.CloneUnknownJSONFields(call.ExtraFields)
	cloned.Function.ExtraFields = core.CloneUnknownJSONFields(call.Function.ExtraFields)
	return cloned
}

func cloneResponsesContentPart(part core.ContentPart) core.ContentPart {
	cloned := part
	cloned.ExtraFields = core.CloneUnknownJSONFields(part.ExtraFields)
	if part.ImageURL != nil {
		image := *part.ImageURL
		image.ExtraFields = core.CloneUnknownJSONFields(part.ImageURL.ExtraFields)
		cloned.ImageURL = &image
	}
	if part.InputAudio != nil {
		audio := *part.InputAudio
		audio.ExtraFields = core.CloneUnknownJSONFields(part.InputAudio.ExtraFields)
		cloned.InputAudio = &audio
	}
	return cloned
}

func rawJSONMapFromUnknownKeys(src map[string]any, knownKeys ...string) map[string]json.RawMessage {
	if len(src) == 0 {
		return nil
	}
	known := make(map[string]struct{}, len(knownKeys))
	for _, key := range knownKeys {
		known[key] = struct{}{}
	}

	var extras map[string]json.RawMessage
	for key, value := range src {
		if _, ok := known[key]; ok {
			continue
		}
		raw, err := json.Marshal(value)
		if err != nil {
			continue
		}
		if extras == nil {
			extras = make(map[string]json.RawMessage)
		}
		extras[key] = raw
	}
	return extras
}

func rawJSONMapFromUnknownStringKeys(src map[string]string, knownKeys ...string) map[string]json.RawMessage {
	if len(src) == 0 {
		return nil
	}

	converted := make(map[string]any, len(src))
	for key, value := range src {
		converted[key] = value
	}
	return rawJSONMapFromUnknownKeys(converted, knownKeys...)
}

// ExtractContentFromInput extracts text content from responses input.
func ExtractContentFromInput(content any) string {
	switch c := content.(type) {
	case string:
		return c
	case []core.ContentPart:
		texts := make([]string, 0, len(c))
		for _, part := range c {
			if part.Text != "" {
				texts = append(texts, part.Text)
			}
		}
		return strings.Join(texts, " ")
	case []map[string]any:
		return extractTextFromMapSlice(c)
	case []any:
		texts := make([]string, 0, len(c))
		for _, part := range c {
			if partMap, ok := part.(map[string]any); ok {
				if text := extractTextFromInputMap(partMap); text != "" {
					texts = append(texts, text)
				}
			}
		}
		return strings.Join(texts, " ")
	default:
		return ""
	}
}

func extractTextFromMapSlice(parts []map[string]any) string {
	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		if text := extractTextFromInputMap(part); text != "" {
			texts = append(texts, text)
		}
	}
	return strings.Join(texts, " ")
}

func extractTextFromInputMap(part map[string]any) string {
	texts := make([]string, 0, 2)
	if text, ok := part["text"].(string); ok && text != "" {
		texts = append(texts, text)
	}
	if nested, ok := part["content"]; ok {
		if text := ExtractContentFromInput(nested); text != "" {
			texts = append(texts, text)
		}
	}
	return strings.Join(texts, " ")
}
