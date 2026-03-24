package core

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMessageUnmarshalJSON_StringContent(t *testing.T) {
	var msg Message
	if err := json.Unmarshal([]byte(`{"role":"user","content":"hello"}`), &msg); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if msg.Role != "user" {
		t.Fatalf("Role = %q, want user", msg.Role)
	}
	if msg.Content != "hello" {
		t.Fatalf("Content = %#v, want hello", msg.Content)
	}
}

func TestMessageUnmarshalJSON_MultimodalContent(t *testing.T) {
	var msg Message
	err := json.Unmarshal([]byte(`{"role":"user","content":[{"type":"text","text":"Describe this image"},{"type":"image_url","image_url":{"url":"https://example.com/image.png","detail":"high","media_type":"image/png"}}]}`), &msg)
	if err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	parts, ok := msg.Content.([]ContentPart)
	if !ok {
		t.Fatalf("Content type = %T, want []ContentPart", msg.Content)
	}
	if len(parts) != 2 {
		t.Fatalf("len(parts) = %d, want 2", len(parts))
	}
	if parts[0].Type != "text" || parts[0].Text != "Describe this image" {
		t.Fatalf("unexpected first part: %+v", parts[0])
	}
	if parts[1].Type != "image_url" || parts[1].ImageURL == nil || parts[1].ImageURL.URL != "https://example.com/image.png" {
		t.Fatalf("unexpected second part: %+v", parts[1])
	}
	if parts[1].ImageURL.MediaType != "image/png" {
		t.Fatalf("second part media type = %q, want image/png", parts[1].ImageURL.MediaType)
	}
}

func TestMessageUnmarshalJSON_NullContentPreservedAsNil(t *testing.T) {
	var msg Message
	if err := json.Unmarshal([]byte(`{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]}`), &msg); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if msg.Content != nil {
		t.Fatalf("Content = %#v, want nil", msg.Content)
	}
}

func TestMessageUnmarshalJSON_RejectsUnsupportedContentTypes(t *testing.T) {
	tests := []string{
		`{"role":"user","content":123}`,
		`{"role":"user","content":{"foo":"bar"}}`,
		`{"role":"user","content":[{"type":"unknown"}]}`,
	}

	for _, payload := range tests {
		t.Run(payload, func(t *testing.T) {
			var msg Message
			err := json.Unmarshal([]byte(payload), &msg)
			if err == nil {
				t.Fatal("json.Unmarshal() succeeded, want error")
			}
			if !strings.Contains(err.Error(), "content") && !strings.Contains(err.Error(), "must be a string or array of content parts") {
				t.Fatalf("error = %v, want content validation error", err)
			}
		})
	}
}

func TestMessageMarshalJSON_RejectsUnsupportedContentType(t *testing.T) {
	_, err := json.Marshal(Message{Role: "user", Content: 123})
	if err == nil {
		t.Fatal("json.Marshal() succeeded, want error")
	}
	if !strings.Contains(err.Error(), "must be a string or array of content parts") {
		t.Fatalf("error = %v, want content validation error", err)
	}
}

func TestMessageMarshalJSON_PreservesNullContentForToolCalls(t *testing.T) {
	body, err := json.Marshal(Message{
		Role:    "assistant",
		Content: nil,
		ToolCalls: []ToolCall{
			{
				ID:   "call_123",
				Type: "function",
				Function: FunctionCall{
					Name:      "lookup",
					Arguments: "{}",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if !strings.Contains(string(body), `"content":null`) {
		t.Fatalf("expected content:null, got %s", string(body))
	}
}

func TestMessageMarshalJSON_PreservesNullContentForToolCallsWhenContentIsEmptyString(t *testing.T) {
	body, err := json.Marshal(Message{
		Role:    "assistant",
		Content: "",
		ToolCalls: []ToolCall{
			{
				ID:   "call_123",
				Type: "function",
				Function: FunctionCall{
					Name:      "lookup",
					Arguments: "{}",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if !strings.Contains(string(body), `"content":null`) {
		t.Fatalf("expected content:null, got %s", string(body))
	}
}

func TestResponseMessageMarshalJSON_PreservesNullContentForToolCalls(t *testing.T) {
	body, err := json.Marshal(ResponseMessage{
		Role:    "assistant",
		Content: nil,
		ToolCalls: []ToolCall{
			{
				ID:   "call_123",
				Type: "function",
				Function: FunctionCall{
					Name:      "lookup",
					Arguments: "{}",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if !strings.Contains(string(body), `"content":null`) {
		t.Fatalf("expected content:null, got %s", string(body))
	}
}

func TestResponseMessageUnmarshalJSON_PreservesNullContentForToolCalls(t *testing.T) {
	var msg ResponseMessage
	if err := json.Unmarshal([]byte(`{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]}`), &msg); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if msg.Content != nil {
		t.Fatalf("Content = %#v, want nil", msg.Content)
	}
}

func TestNormalizeMessageContent_RejectsEmptyTypedTextPart(t *testing.T) {
	_, err := NormalizeMessageContent([]ContentPart{{Type: "text", Text: ""}})
	if err == nil {
		t.Fatal("NormalizeMessageContent() succeeded, want error")
	}
	if !strings.Contains(err.Error(), "text part is missing text") {
		t.Fatalf("error = %v, want text validation error", err)
	}
}

func TestMessageUnmarshalJSON_RejectsEmptyJSONTextPart(t *testing.T) {
	var msg Message
	err := json.Unmarshal([]byte(`{"role":"user","content":[{"type":"text","text":""}]}`), &msg)
	if err == nil {
		t.Fatal("json.Unmarshal() succeeded, want error")
	}
	if !strings.Contains(err.Error(), "text part is missing text") {
		t.Fatalf("error = %v, want text validation error", err)
	}
}

func TestNormalizeMessageContent_RejectsEmptyMapTextPart(t *testing.T) {
	_, err := NormalizeMessageContent([]any{
		map[string]any{
			"type": "text",
			"text": "",
		},
	})
	if err == nil {
		t.Fatal("NormalizeMessageContent() succeeded, want error")
	}
	if !strings.Contains(err.Error(), "text part is missing text") {
		t.Fatalf("error = %v, want text validation error", err)
	}
}

func TestMessageUnmarshalJSON_InputAudioContent(t *testing.T) {
	var msg Message
	err := json.Unmarshal([]byte(`{"role":"user","content":[{"type":"input_audio","input_audio":{"data":"base64data","format":"wav"}}]}`), &msg)
	if err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	parts, ok := msg.Content.([]ContentPart)
	if !ok {
		t.Fatalf("Content type = %T, want []ContentPart", msg.Content)
	}
	if len(parts) != 1 {
		t.Fatalf("len(parts) = %d, want 1", len(parts))
	}
	if parts[0].Type != "input_audio" {
		t.Fatalf("Type = %q, want input_audio", parts[0].Type)
	}
	if parts[0].InputAudio == nil {
		t.Fatal("InputAudio is nil")
	}
	if parts[0].InputAudio.Data != "base64data" {
		t.Fatalf("Data = %q, want base64data", parts[0].InputAudio.Data)
	}
	if parts[0].InputAudio.Format != "wav" {
		t.Fatalf("Format = %q, want wav", parts[0].InputAudio.Format)
	}
}

func TestMessageUnmarshalJSON_RejectsInputAudioMissingData(t *testing.T) {
	var msg Message
	err := json.Unmarshal([]byte(`{"role":"user","content":[{"type":"input_audio","input_audio":{"data":"","format":"wav"}}]}`), &msg)
	if err == nil {
		t.Fatal("json.Unmarshal() succeeded, want error")
	}
	if !strings.Contains(err.Error(), "input_audio part is missing data or format") {
		t.Fatalf("error = %v, want input_audio validation error", err)
	}
}

func TestMessageUnmarshalJSON_RejectsInputAudioMissingFormat(t *testing.T) {
	var msg Message
	err := json.Unmarshal([]byte(`{"role":"user","content":[{"type":"input_audio","input_audio":{"data":"abc","format":""}}]}`), &msg)
	if err == nil {
		t.Fatal("json.Unmarshal() succeeded, want error")
	}
	if !strings.Contains(err.Error(), "input_audio part is missing data or format") {
		t.Fatalf("error = %v, want input_audio validation error", err)
	}
}

func TestMessageUnmarshalJSON_RejectsInputAudioNull(t *testing.T) {
	var msg Message
	err := json.Unmarshal([]byte(`{"role":"user","content":[{"type":"input_audio","input_audio":null}]}`), &msg)
	if err == nil {
		t.Fatal("json.Unmarshal() succeeded, want error")
	}
	if !strings.Contains(err.Error(), "input_audio part is missing data or format") {
		t.Fatalf("error = %v, want input_audio validation error", err)
	}
}

func TestMessageUnmarshalJSON_RejectsInputAudioNotObject(t *testing.T) {
	var msg Message
	err := json.Unmarshal([]byte(`{"role":"user","content":[{"type":"input_audio","input_audio":"string"}]}`), &msg)
	if err == nil {
		t.Fatal("json.Unmarshal() succeeded, want error")
	}
	if !strings.Contains(err.Error(), "input_audio must be an object") {
		t.Fatalf("error = %v, want input_audio type error", err)
	}
}

func TestNormalizeMessageContent_InputAudioTypedPart(t *testing.T) {
	result, err := NormalizeMessageContent([]ContentPart{{
		Type:       "input_audio",
		InputAudio: &InputAudioContent{Data: "abc", Format: "wav"},
	}})
	if err != nil {
		t.Fatalf("NormalizeMessageContent() error = %v", err)
	}

	parts, ok := result.([]ContentPart)
	if !ok {
		t.Fatalf("result type = %T, want []ContentPart", result)
	}
	if len(parts) != 1 {
		t.Fatalf("len(parts) = %d, want 1", len(parts))
	}
	if parts[0].Type != "input_audio" || parts[0].InputAudio == nil {
		t.Fatalf("unexpected part: %+v", parts[0])
	}
	if parts[0].InputAudio.Data != "abc" || parts[0].InputAudio.Format != "wav" {
		t.Fatalf("InputAudio = %+v, want {abc wav}", parts[0].InputAudio)
	}
}

func TestNormalizeMessageContent_RejectsNilInputAudio(t *testing.T) {
	_, err := NormalizeMessageContent([]ContentPart{{Type: "input_audio", InputAudio: nil}})
	if err == nil {
		t.Fatal("NormalizeMessageContent() succeeded, want error")
	}
	if !strings.Contains(err.Error(), "input_audio part is missing data or format") {
		t.Fatalf("error = %v, want input_audio validation error", err)
	}
}

func TestNormalizeMessageContent_InputAudioFromMap(t *testing.T) {
	result, err := NormalizeMessageContent([]any{
		map[string]any{
			"type":        "input_audio",
			"input_audio": map[string]any{"data": "abc", "format": "wav"},
		},
	})
	if err != nil {
		t.Fatalf("NormalizeMessageContent() error = %v", err)
	}

	parts, ok := result.([]ContentPart)
	if !ok {
		t.Fatalf("result type = %T, want []ContentPart", result)
	}
	if len(parts) != 1 {
		t.Fatalf("len(parts) = %d, want 1", len(parts))
	}
	if parts[0].Type != "input_audio" || parts[0].InputAudio == nil {
		t.Fatalf("unexpected part: %+v", parts[0])
	}
	if parts[0].InputAudio.Data != "abc" || parts[0].InputAudio.Format != "wav" {
		t.Fatalf("InputAudio = %+v, want {abc wav}", parts[0].InputAudio)
	}
}

func TestNormalizeMessageContent_RejectsInputAudioFromMapMissingFields(t *testing.T) {
	_, err := NormalizeMessageContent([]any{
		map[string]any{
			"type":        "input_audio",
			"input_audio": map[string]any{"data": "", "format": "wav"},
		},
	})
	if err == nil {
		t.Fatal("NormalizeMessageContent() succeeded, want error")
	}
	if !strings.Contains(err.Error(), "input_audio part is missing data or format") {
		t.Fatalf("error = %v, want input_audio validation error", err)
	}
}

func TestMessageUnmarshalJSON_MixedTextImageAudio(t *testing.T) {
	var msg Message
	err := json.Unmarshal([]byte(`{"role":"user","content":[{"type":"text","text":"Describe"},{"type":"image_url","image_url":{"url":"https://example.com/img.png"}},{"type":"input_audio","input_audio":{"data":"abc","format":"mp3"}}]}`), &msg)
	if err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	parts, ok := msg.Content.([]ContentPart)
	if !ok {
		t.Fatalf("Content type = %T, want []ContentPart", msg.Content)
	}
	if len(parts) != 3 {
		t.Fatalf("len(parts) = %d, want 3", len(parts))
	}
	if parts[0].Type != "text" || parts[0].Text != "Describe" {
		t.Fatalf("unexpected part 0: %+v", parts[0])
	}
	if parts[1].Type != "image_url" || parts[1].ImageURL == nil || parts[1].ImageURL.URL != "https://example.com/img.png" {
		t.Fatalf("unexpected part 1: %+v", parts[1])
	}
	if parts[2].Type != "input_audio" || parts[2].InputAudio == nil || parts[2].InputAudio.Data != "abc" || parts[2].InputAudio.Format != "mp3" {
		t.Fatalf("unexpected part 2: %+v", parts[2])
	}
}

func TestHasNonTextContent_InputAudio(t *testing.T) {
	result := HasNonTextContent([]ContentPart{{
		Type:       "input_audio",
		InputAudio: &InputAudioContent{Data: "abc", Format: "wav"},
	}})
	if !result {
		t.Fatal("HasNonTextContent() = false, want true")
	}
}
