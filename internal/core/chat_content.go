package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// ContentPart represents a single OpenAI-compatible multimodal chat content part.
type ContentPart struct {
	Type       string             `json:"type"`
	Text       string             `json:"text,omitempty"`
	ImageURL   *ImageURLContent   `json:"image_url,omitempty"`
	InputAudio *InputAudioContent `json:"input_audio,omitempty"`
}

// ImageURLContent contains an image reference for image_url parts.
type ImageURLContent struct {
	URL       string `json:"url"`
	Detail    string `json:"detail,omitempty"`
	MediaType string `json:"media_type,omitempty"`
}

// InputAudioContent contains inline audio payload metadata.
type InputAudioContent struct {
	Data   string `json:"data"`
	Format string `json:"format"`
}

// UnmarshalMessageContent decodes supported chat message content payloads.
// Chat content accepts plain strings, null, or arrays of supported content parts.
func UnmarshalMessageContent(data []byte) (any, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}

	switch trimmed[0] {
	case '"':
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return nil, err
		}
		return text, nil
	case '[':
		var rawParts []json.RawMessage
		if err := json.Unmarshal(trimmed, &rawParts); err != nil {
			return nil, err
		}

		parts := make([]ContentPart, len(rawParts))
		for i, rawPart := range rawParts {
			part, err := unmarshalContentPart(rawPart)
			if err != nil {
				return nil, fmt.Errorf("part %d: %w", i, err)
			}
			parts[i] = part
		}
		return parts, nil
	default:
		return nil, fmt.Errorf("must be a string or array of content parts")
	}
}

// NormalizeMessageContent validates dynamic content and returns its canonical form.
func NormalizeMessageContent(content any) (any, error) {
	switch c := content.(type) {
	case nil:
		return "", nil
	case string:
		return c, nil
	case []ContentPart:
		parts := make([]ContentPart, len(c))
		for i, part := range c {
			normalized, err := normalizeTypedContentPart(part)
			if err != nil {
				return nil, fmt.Errorf("part %d: %w", i, err)
			}
			parts[i] = normalized
		}
		return parts, nil
	case []interface{}:
		parts := make([]ContentPart, len(c))
		for i, part := range c {
			normalized, err := normalizeContentPartValue(part)
			if err != nil {
				return nil, fmt.Errorf("part %d: %w", i, err)
			}
			parts[i] = normalized
		}
		return parts, nil
	default:
		return nil, fmt.Errorf("must be a string or array of content parts")
	}
}

// ExtractTextContent returns the textual portion of request content.
// Structured content parts are reduced to their text components only.
func ExtractTextContent(content any) string {
	switch c := content.(type) {
	case string:
		return c
	case []ContentPart:
		return joinTextParts(partsText(c))
	case []interface{}:
		return joinTextParts(interfacePartsText(c))
	default:
		return ""
	}
}

// HasStructuredContent reports whether the content uses the array form.
func HasStructuredContent(content any) bool {
	switch c := content.(type) {
	case []ContentPart:
		return len(c) > 0
	case []interface{}:
		return len(c) > 0
	default:
		return false
	}
}

// HasNonTextContent reports whether the content contains image/audio parts.
func HasNonTextContent(content any) bool {
	parts, ok := NormalizeContentParts(content)
	if !ok {
		return false
	}
	for _, part := range parts {
		if part.Type != "text" {
			return true
		}
	}
	return false
}

// NormalizeContentParts converts dynamic JSON-decoded content into typed parts.
func NormalizeContentParts(content any) ([]ContentPart, bool) {
	normalized, err := NormalizeMessageContent(content)
	if err != nil {
		return nil, false
	}
	parts, ok := normalized.([]ContentPart)
	if !ok || len(parts) == 0 {
		return nil, false
	}
	return parts, true
}

func joinTextParts(texts []string) string {
	if len(texts) == 0 {
		return ""
	}
	return strings.Join(texts, " ")
}

func partsText(parts []ContentPart) []string {
	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		switch part.Type {
		case "text", "input_text":
			if part.Text != "" {
				texts = append(texts, part.Text)
			}
		}
	}
	return texts
}

func interfacePartsText(parts []interface{}) []string {
	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		partMap, ok := part.(map[string]interface{})
		if !ok {
			continue
		}
		partType, _ := partMap["type"].(string)
		switch partType {
		case "text", "input_text":
			if text, ok := partMap["text"].(string); ok && text != "" {
				texts = append(texts, text)
			}
		}
	}
	return texts
}

func unmarshalContentPart(data []byte) (ContentPart, error) {
	var raw struct {
		Type       string          `json:"type"`
		Text       *string         `json:"text,omitempty"`
		ImageURL   json.RawMessage `json:"image_url,omitempty"`
		InputAudio json.RawMessage `json:"input_audio,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return ContentPart{}, err
	}

	switch raw.Type {
	case "text", "input_text":
		if raw.Text == nil || *raw.Text == "" {
			return ContentPart{}, fmt.Errorf("text part is missing text")
		}
		return ContentPart{Type: "text", Text: *raw.Text}, nil
	case "image_url", "input_image":
		imageURL, err := unmarshalImageURLContent(raw.ImageURL)
		if err != nil {
			return ContentPart{}, err
		}
		return ContentPart{Type: "image_url", ImageURL: imageURL}, nil
	case "input_audio":
		audio, err := unmarshalInputAudioContent(raw.InputAudio)
		if err != nil {
			return ContentPart{}, err
		}
		return ContentPart{Type: "input_audio", InputAudio: audio}, nil
	default:
		return ContentPart{}, fmt.Errorf("unsupported content part type %q", raw.Type)
	}
}

func normalizeTypedContentPart(part ContentPart) (ContentPart, error) {
	switch part.Type {
	case "text", "input_text":
		if part.Text == "" {
			return ContentPart{}, fmt.Errorf("text part is missing text")
		}
		return ContentPart{Type: "text", Text: part.Text}, nil
	case "image_url", "input_image":
		if part.ImageURL == nil || part.ImageURL.URL == "" {
			return ContentPart{}, fmt.Errorf("image_url part is missing image_url.url")
		}
		return ContentPart{
			Type: "image_url",
			ImageURL: &ImageURLContent{
				URL:       part.ImageURL.URL,
				Detail:    part.ImageURL.Detail,
				MediaType: part.ImageURL.MediaType,
			},
		}, nil
	case "input_audio":
		if part.InputAudio == nil || part.InputAudio.Data == "" || part.InputAudio.Format == "" {
			return ContentPart{}, fmt.Errorf("input_audio part is missing data or format")
		}
		return ContentPart{
			Type: "input_audio",
			InputAudio: &InputAudioContent{
				Data:   part.InputAudio.Data,
				Format: part.InputAudio.Format,
			},
		}, nil
	default:
		return ContentPart{}, fmt.Errorf("unsupported content part type %q", part.Type)
	}
}

func normalizeContentPartValue(part any) (ContentPart, error) {
	switch v := part.(type) {
	case ContentPart:
		return normalizeTypedContentPart(v)
	case map[string]interface{}:
		return normalizeContentPartMap(v)
	default:
		return ContentPart{}, fmt.Errorf("content part must be an object")
	}
}

func normalizeContentPartMap(partMap map[string]interface{}) (ContentPart, error) {
	partType, _ := partMap["type"].(string)
	switch partType {
	case "text", "input_text":
		text, ok := partMap["text"].(string)
		if !ok || text == "" {
			return ContentPart{}, fmt.Errorf("text part is missing text")
		}
		return ContentPart{Type: "text", Text: text}, nil
	case "image_url", "input_image":
		imageURL, ok := parseImageURLValue(partMap["image_url"])
		if !ok {
			return ContentPart{}, fmt.Errorf("image_url part is missing image_url.url")
		}
		return ContentPart{
			Type: "image_url",
			ImageURL: &ImageURLContent{
				URL:       imageURL.URL,
				Detail:    imageURL.Detail,
				MediaType: imageURL.MediaType,
			},
		}, nil
	case "input_audio":
		audio, ok := parseInputAudioValue(partMap["input_audio"])
		if !ok {
			return ContentPart{}, fmt.Errorf("input_audio part is missing data or format")
		}
		return ContentPart{
			Type: "input_audio",
			InputAudio: &InputAudioContent{
				Data:   audio.Data,
				Format: audio.Format,
			},
		}, nil
	default:
		return ContentPart{}, fmt.Errorf("unsupported content part type %q", partType)
	}
}

func unmarshalImageURLContent(data []byte) (*ImageURLContent, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, fmt.Errorf("image_url part is missing image_url.url")
	}

	switch trimmed[0] {
	case '"':
		var url string
		if err := json.Unmarshal(trimmed, &url); err != nil {
			return nil, err
		}
		if url == "" {
			return nil, fmt.Errorf("image_url part is missing image_url.url")
		}
		return &ImageURLContent{URL: url}, nil
	case '{':
		var imageURL ImageURLContent
		if err := json.Unmarshal(trimmed, &imageURL); err != nil {
			return nil, err
		}
		if imageURL.URL == "" {
			return nil, fmt.Errorf("image_url part is missing image_url.url")
		}
		return &imageURL, nil
	default:
		return nil, fmt.Errorf("image_url must be a string or object")
	}
}

func unmarshalInputAudioContent(data []byte) (*InputAudioContent, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, fmt.Errorf("input_audio part is missing data or format")
	}

	if trimmed[0] != '{' {
		return nil, fmt.Errorf("input_audio must be an object")
	}

	var audio InputAudioContent
	if err := json.Unmarshal(trimmed, &audio); err != nil {
		return nil, err
	}
	if audio.Data == "" || audio.Format == "" {
		return nil, fmt.Errorf("input_audio part is missing data or format")
	}
	return &audio, nil
}

func parseImageURLValue(value any) (*ImageURLContent, bool) {
	switch v := value.(type) {
	case string:
		if v == "" {
			return nil, false
		}
		return &ImageURLContent{URL: v}, true
	case map[string]string:
		if v["url"] == "" {
			return nil, false
		}
		return &ImageURLContent{
			URL:       v["url"],
			Detail:    v["detail"],
			MediaType: v["media_type"],
		}, true
	case map[string]interface{}:
		url, _ := v["url"].(string)
		if url == "" {
			return nil, false
		}
		detail, _ := v["detail"].(string)
		mediaType, _ := v["media_type"].(string)
		return &ImageURLContent{
			URL:       url,
			Detail:    detail,
			MediaType: mediaType,
		}, true
	default:
		return nil, false
	}
}

func parseInputAudioValue(value any) (*InputAudioContent, bool) {
	switch v := value.(type) {
	case map[string]string:
		if v["data"] == "" || v["format"] == "" {
			return nil, false
		}
		return &InputAudioContent{
			Data:   v["data"],
			Format: v["format"],
		}, true
	case map[string]interface{}:
		data, _ := v["data"].(string)
		format, _ := v["format"].(string)
		if data == "" || format == "" {
			return nil, false
		}
		return &InputAudioContent{
			Data:   data,
			Format: format,
		}, true
	default:
		return nil, false
	}
}
