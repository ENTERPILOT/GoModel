package core

import (
	"encoding/json"
	"testing"
)

func TestResponsesRequestUnmarshalJSON_StringInput(t *testing.T) {
	var req ResponsesRequest
	if err := json.Unmarshal([]byte(`{"model":"gpt-4o-mini","input":"hello"}`), &req); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if req.Model != "gpt-4o-mini" {
		t.Fatalf("Model = %q, want gpt-4o-mini", req.Model)
	}
	input, ok := req.Input.(string)
	if !ok || input != "hello" {
		t.Fatalf("Input = %#v, want string hello", req.Input)
	}
}

func TestResponsesRequestUnmarshalJSON_ArrayInput(t *testing.T) {
	var req ResponsesRequest
	if err := json.Unmarshal([]byte(`{"model":"gpt-4o-mini","input":[{"role":"user","content":[{"type":"input_text","text":"hello"}]}]}`), &req); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	input, ok := req.Input.([]interface{})
	if !ok || len(input) != 1 {
		t.Fatalf("Input = %#v, want []interface{} len=1", req.Input)
	}
}

func TestResponsesRequestMarshalJSON_PreservesInput(t *testing.T) {
	body, err := json.Marshal(ResponsesRequest{
		Model: "gpt-4o-mini",
		Input: []interface{}{
			map[string]interface{}{
				"role": "user",
				"content": []interface{}{
					map[string]interface{}{
						"type": "input_text",
						"text": "hello",
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if _, ok := decoded["input"]; !ok {
		t.Fatalf("marshal output missing input: %s", string(body))
	}
}
