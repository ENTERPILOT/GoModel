package core

import (
	"encoding/json"
	"testing"
)

func TestCloneBatchRequestDeepCopiesNestedFields(t *testing.T) {
	original := &BatchRequest{
		InputFileID:      "file_source",
		Endpoint:         "/v1/chat/completions",
		CompletionWindow: "24h",
		Metadata: map[string]string{
			"provider": "openai",
		},
		Requests: []BatchRequestItem{
			{
				CustomID: "chat-1",
				Method:   "POST",
				URL:      "/v1/chat/completions",
				Body:     json.RawMessage(`{"model":"smart","messages":[{"role":"user","content":"hi"}]}`),
				ExtraFields: UnknownJSONFieldsFromMap(map[string]json.RawMessage{
					"x_item": json.RawMessage(`{"trace":true}`),
				}),
			},
		},
		ExtraFields: UnknownJSONFieldsFromMap(map[string]json.RawMessage{
			"x_top": json.RawMessage(`{"debug":true}`),
		}),
	}

	cloned := cloneBatchRequest(original)
	if cloned == nil {
		t.Fatal("cloneBatchRequest() returned nil")
	}

	cloned.Metadata["provider"] = "anthropic"
	cloned.Requests[0].CustomID = "chat-2"
	cloned.Requests[0].Body[10] = 'X'
	itemExtra := cloned.Requests[0].ExtraFields.Lookup("x_item")
	if len(itemExtra) <= 9 {
		t.Fatalf("cloned item extra too short: %q", itemExtra)
	}
	itemExtra[9] = 'f'
	topExtra := cloned.ExtraFields.Lookup("x_top")
	if len(topExtra) <= 9 {
		t.Fatalf("cloned top extra too short: %q", topExtra)
	}
	topExtra[9] = 'f'

	if got := original.Metadata["provider"]; got != "openai" {
		t.Fatalf("original metadata mutated to %q", got)
	}
	if got := original.Requests[0].CustomID; got != "chat-1" {
		t.Fatalf("original custom_id mutated to %q", got)
	}
	if got := string(original.Requests[0].Body); got != `{"model":"smart","messages":[{"role":"user","content":"hi"}]}` {
		t.Fatalf("original body mutated to %s", got)
	}
	if got := string(original.Requests[0].ExtraFields.Lookup("x_item")); got != `{"trace":true}` {
		t.Fatalf("original item extra mutated to %s", got)
	}
	if got := string(original.ExtraFields.Lookup("x_top")); got != `{"debug":true}` {
		t.Fatalf("original top extra mutated to %s", got)
	}
}
