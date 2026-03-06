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
