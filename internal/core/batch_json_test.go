package core

import (
	"encoding/json"
	"testing"
)

func TestBatchRequestJSON_PreservesUnknownFields(t *testing.T) {
	var req BatchRequest
	body := []byte(`{
		"input_file_id":"file-123",
		"endpoint":"/v1/chat/completions",
		"completion_window":"24h",
		"metadata":{"provider":"openai"},
		"requests":[{
			"custom_id":"chat-1",
			"method":"POST",
			"url":"/v1/chat/completions",
			"body":{"model":"gpt-5-mini","messages":[{"role":"user","content":"hi"}]},
			"x_item_flag":{"enabled":true,"label":"batch-item"}
		}],
		"x_top":{"trace":"batch-1","mode":"strict"}
	}`)
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if req.ExtraFields.Lookup("x_top") == nil {
		t.Fatalf("x_top missing from ExtraFields: %+v", req.ExtraFields)
	}
	var topExtra map[string]any
	if err := json.Unmarshal(req.ExtraFields.Lookup("x_top"), &topExtra); err != nil {
		t.Fatalf("failed to decode x_top: %v", err)
	}
	if topExtra["trace"] != "batch-1" || topExtra["mode"] != "strict" {
		t.Fatalf("x_top = %#v, want trace=batch-1 mode=strict", topExtra)
	}
	if len(req.Requests) != 1 {
		t.Fatalf("len(Requests) = %d, want 1", len(req.Requests))
	}
	if req.Requests[0].ExtraFields.Lookup("x_item_flag") == nil {
		t.Fatalf("x_item_flag missing from Requests[0].ExtraFields: %+v", req.Requests[0].ExtraFields)
	}
	var itemExtra map[string]any
	if err := json.Unmarshal(req.Requests[0].ExtraFields.Lookup("x_item_flag"), &itemExtra); err != nil {
		t.Fatalf("failed to decode x_item_flag: %v", err)
	}
	if itemExtra["enabled"] != true || itemExtra["label"] != "batch-item" {
		t.Fatalf("x_item_flag = %#v, want enabled=true label=batch-item", itemExtra)
	}

	roundTrip, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(roundTrip, &decoded); err != nil {
		t.Fatalf("json.Unmarshal(roundTrip) error = %v", err)
	}
	top, ok := decoded["x_top"].(map[string]any)
	if !ok {
		t.Fatalf("x_top = %#v, want object", decoded["x_top"])
	}
	if top["trace"] != "batch-1" || top["mode"] != "strict" {
		t.Fatalf("x_top = %#v, want trace=batch-1 mode=strict", top)
	}

	requests := decoded["requests"].([]any)
	first := requests[0].(map[string]any)
	item, ok := first["x_item_flag"].(map[string]any)
	if !ok {
		t.Fatalf("x_item_flag = %#v, want object", first["x_item_flag"])
	}
	if item["enabled"] != true || item["label"] != "batch-item" {
		t.Fatalf("x_item_flag = %#v, want enabled=true label=batch-item", item)
	}
}
